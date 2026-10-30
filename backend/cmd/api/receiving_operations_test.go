package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type receivingOperationCase struct {
	handler receivingTestHandler
	body    map[string]any
	pathKey string
	pathID  int64
	sku     string
	status  int
}

func (c receivingOperationCase) call(db *pgxpool.Pool, token string) *httptest.ResponseRecorder {
	fields := map[string]any{"requestId": token}
	for key, value := range c.body {
		fields[key] = value
	}
	raw, _ := json.Marshal(fields)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
	if c.pathKey != "" {
		r.SetPathValue(c.pathKey, fmt.Sprint(c.pathID))
	}
	if c.sku != "" {
		r.Method = http.MethodDelete
		r.SetPathValue("sku", c.sku)
	}
	w := httptest.NewRecorder()
	c.handler(db, w, r)
	return w
}

func receivingOperationFixture(t *testing.T, kind string) (*pgxpool.Pool, receivingOperationCase) {
	t.Helper()
	db := receivingTestDB(t)
	if kind == "create-session" {
		return db, receivingOperationCase{handler: createReceivingSessionHandler, status: 201}
	}
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 2)
	switch kind {
	case "place":
		return db, receivingOperationCase{handler: createReceivingPlacementHandler, body: map[string]any{"sessionId": session, "cellCode": "TEST-1", "items": []map[string]any{{"sku": "TEST-SKU", "quantity": 2}}}, status: 200}
	case "delete-item":
		return db, receivingOperationCase{handler: deleteReceivingSessionItemHandler, body: map[string]any{"expectedRevision": receivingDetails(t, db, session).PendingRevision}, pathKey: "sessionID", pathID: session, sku: "TEST-SKU", status: 200}
	}
	placement := receivingPlace(t, db, session, 2)
	if kind == "undo-placement" {
		return db, receivingOperationCase{handler: undoReceivingPlacementHandler, pathKey: "placementID", pathID: placement, status: 200}
	}
	return db, receivingOperationCase{handler: finishReceivingHandler, body: map[string]any{"sessionId": session, "placementIds": []int64{placement}}, status: 200}
}

func receivingOperationFingerprint(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var all []string
	for _, table := range []string{"receiving_sessions", "receiving_session_items", "receiving_placements", "receiving_placement_items", "cell_stock", "receiving_operation_requests", "receiving_quantity_requests", "receiving_intake_events"} {
		var hash string
		if err := db.QueryRow(context.Background(), `SELECT md5(COALESCE(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text)::text,'')) FROM `+table+` x`).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		all = append(all, hash)
	}
	return strings.Join(all, ":")
}

func TestReceivingOperationReceipts(t *testing.T) {
	for _, kind := range []string{"create-session", "place", "undo-placement", "finish", "delete-item"} {
		for _, mode := range []string{"lost-response-replay-conflict", "concurrent", "receipt-failure-rollback"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				db, command := receivingOperationFixture(t, kind)
				token := newReceivingScanID(t)
				if mode == "receipt-failure-rollback" {
					_, err := db.Exec(context.Background(), `CREATE FUNCTION fail_receiving_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected receipt failure'; END $$`)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec(context.Background(), `CREATE TRIGGER fail_receipt BEFORE INSERT ON receiving_operation_requests FOR EACH ROW EXECUTE FUNCTION fail_receiving_receipt()`); err != nil {
						t.Fatal(err)
					}
					before := receivingOperationFingerprint(t, db)
					requireReceivingStatus(t, command.call(db, token), 500)
					if receivingOperationFingerprint(t, db) != before {
						t.Fatal("failed receipt left business changes/revision behind")
					}
					if _, err = db.Exec(context.Background(), `DROP TRIGGER fail_receipt ON receiving_operation_requests`); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "concurrent" {
					var wg sync.WaitGroup
					start := make(chan struct{})
					responses := make(chan *httptest.ResponseRecorder, 2)
					for range 2 {
						wg.Add(1)
						go func() { defer wg.Done(); <-start; responses <- command.call(db, token) }()
					}
					close(start)
					wg.Wait()
					close(responses)
					var first string
					for response := range responses {
						requireReceivingStatus(t, response, command.status)
						if first != "" && first != response.Body.String() {
							t.Fatal("concurrent responses differ")
						}
						first = response.Body.String()
					}
				} else {
					first := command.call(db, token)
					requireReceivingStatus(t, first, command.status)
					// The first response can be lost after COMMIT; only the UUID is retried.
					before := receivingOperationFingerprint(t, db)
					replay := command.call(db, token)
					requireReceivingStatus(t, replay, command.status)
					if first.Body.String() != replay.Body.String() {
						t.Fatal("replay did not return the saved result")
					}
					if receivingOperationFingerprint(t, db) != before {
						t.Fatal("replay mutated business state or revision")
					}
					conflict := command
					conflict.body = map[string]any{}
					for key, value := range command.body {
						conflict.body[key] = value
					}
					switch kind {
					case "create-session":
						conflict = receivingOperationCase{handler: finishReceivingHandler, body: map[string]any{"sessionId": 1, "placementIds": []int64{1}}, status: 409}
					case "place":
						conflict.body["cellCode"] = "OTHER"
					case "undo-placement":
						conflict.pathID++
					case "finish":
						conflict.body["placementIds"] = []int64{99999}
					case "delete-item":
						conflict.body["expectedRevision"] = "99999"
					}
					requireReceivingStatus(t, conflict.call(db, token), 409)
					if receivingOperationFingerprint(t, db) != before {
						t.Fatal("payload conflict changed state")
					}
				}
				wantStock := map[string]int{"create-session": 0, "place": 2, "undo-placement": 0, "finish": 2, "delete-item": 0}[kind]
				if got := receivingStock(t, db); got != wantStock {
					t.Fatalf("stock=%d want %d", got, wantStock)
				}
				var pending int
				if err := db.QueryRow(context.Background(), `SELECT COALESCE(sum(quantity),0)::int FROM receiving_session_items`).Scan(&pending); err != nil {
					t.Fatal(err)
				}
				wantPending := 0
				if kind == "undo-placement" {
					wantPending = 2
				}
				if pending != wantPending {
					t.Fatalf("pending=%d want %d", pending, wantPending)
				}
				var count int
				if err := db.QueryRow(context.Background(), `SELECT count(*) FROM receiving_operation_requests WHERE request_id=$1::uuid`, token).Scan(&count); err != nil || count != 1 {
					t.Fatalf("receipt count=%d err=%v", count, err)
				}
				// New UUID is a distinct create intent; completed/consumed actions retain
				// their business guards instead of being silently treated as a replay.
				if kind == "create-session" {
					var sessions int
					db.QueryRow(context.Background(), `SELECT count(*) FROM receiving_sessions`).Scan(&sessions)
					if sessions != 1 {
						t.Fatalf("duplicate sessions: %d", sessions)
					}
					requireReceivingStatus(t, command.call(db, newReceivingScanID(t)), 201)
				}
			})
		}
	}
}

func TestReceivingDeleteRevisionABAAndReplay(t *testing.T) {
	db, command := receivingOperationFixture(t, "delete-item")
	session := command.pathID
	initial := command.body["expectedRevision"].(string)
	token := newReceivingScanID(t)
	requireReceivingStatus(t, command.call(db, token), 200)
	receivingAdd(t, db, session, 2) // Same SKU AND same quantity as before deletion.
	before := receivingOperationFingerprint(t, db)
	requireReceivingStatus(t, command.call(db, token), 200)
	requireReceivingStatus(t, command.call(db, newReceivingScanID(t)), 409)
	if before != receivingOperationFingerprint(t, db) {
		t.Fatal("delayed DELETE removed new goods")
	}
	details := receivingDetails(t, db, session)
	if len(details.PendingItems) != 1 || details.PendingItems[0].Quantity != 2 {
		t.Fatal("new pending goods lost")
	}
	old, _ := strconv.ParseInt(initial, 10, 64)
	current, _ := strconv.ParseInt(details.PendingRevision, 10, 64)
	if current <= old {
		t.Fatal("ABA did not advance revision")
	}
	command.body["expectedRevision"] = details.PendingRevision
	requireReceivingStatus(t, command.call(db, newReceivingScanID(t)), 200)
}

func TestReceivingPendingRevisionAllWriters(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	revision := func() int64 {
		value, err := strconv.ParseInt(receivingDetails(t, db, session).PendingRevision, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	prior := revision()
	check := func() {
		t.Helper()
		next := revision()
		if next <= prior {
			t.Fatalf("revision did not advance: %d -> %d", prior, next)
		}
		prior = next
	}
	receivingAdd(t, db, session, 2)
	check()
	r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(fmt.Sprintf(`{"requestId":%q,"quantity":3,"expectedQuantity":2}`, newReceivingScanID(t))))
	r.SetPathValue("sessionID", fmt.Sprint(session))
	r.SetPathValue("productID", "1")
	w := httptest.NewRecorder()
	receivingQuantityHandler(db, w, r)
	requireReceivingStatus(t, w, 204)
	check()
	placement := receivingPlace(t, db, session, 3)
	check()
	requireReceivingStatus(t, receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", placement), 200)
	check()
	command := receivingOperationCase{handler: deleteReceivingSessionItemHandler, body: map[string]any{"expectedRevision": fmt.Sprint(prior)}, pathKey: "sessionID", pathID: session, sku: "TEST-SKU", status: 200}
	requireReceivingStatus(t, command.call(db, newReceivingScanID(t)), 200)
	check()
}

func TestReceivingOperationIdentitySharedWithScan(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	token := newReceivingScanID(t)
	requireReceivingStatus(t, scanRequest(db, session, token, "TEST-SKU", 1), 200)
	create := receivingOperationCase{handler: createReceivingSessionHandler, status: 201}
	requireReceivingStatus(t, create.call(db, token), 409)
	token = newReceivingScanID(t)
	requireReceivingStatus(t, create.call(db, token), 201)
	requireReceivingStatus(t, scanRequest(db, session, token, "TEST-SKU", 1), 409)
	requireScanTotals(t, db, 1, 1, 1)
}

func TestReceivingOperationsRejectOldContract(t *testing.T) {
	for _, command := range []receivingOperationCase{
		{handler: createReceivingSessionHandler},
		{handler: createReceivingPlacementHandler, body: map[string]any{"sessionId": 1, "cellCode": "A1", "items": []map[string]any{{"sku": "SKU", "quantity": 1}}}},
		{handler: undoReceivingPlacementHandler, pathKey: "placementID", pathID: 1},
		{handler: finishReceivingHandler, body: map[string]any{"sessionId": 1, "placementIds": []int64{1}}},
		{handler: deleteReceivingSessionItemHandler, pathKey: "sessionID", pathID: 1, sku: "SKU", body: map[string]any{"expectedRevision": "0"}},
	} {
		requireReceivingStatus(t, command.call(nil, ""), 400)
	}
}

func TestReceivingOperationReplayAfterLaterChanges(t *testing.T) {
	db, place := receivingOperationFixture(t, "place")
	token := newReceivingScanID(t)
	first := place.call(db, token)
	requireReceivingStatus(t, first, 200)
	var result receivingPlacementResponse
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	requireReceivingStatus(t, receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", result.ID), 200)
	before := receivingOperationFingerprint(t, db)
	requireReceivingStatus(t, place.call(db, token), 200)
	if before != receivingOperationFingerprint(t, db) {
		t.Fatal("old placement replay placed a new pending group")
	}
	// Undo's receipt must survive a new placement and a completed session too.
	undo := receivingOperationCase{handler: undoReceivingPlacementHandler, pathKey: "placementID", pathID: result.ID, status: 200}
	// Locate the identity of the successful undo above rather than starting a new undo.
	var undoToken string
	if err := db.QueryRow(context.Background(), `SELECT request_id::text FROM receiving_operation_requests WHERE operation='undo-placement'`).Scan(&undoToken); err != nil {
		t.Fatal(err)
	}
	session := place.body["sessionId"].(int64)
	next := receivingPlace(t, db, session, 2)
	requireReceivingStatus(t, receivingRequest(db, finishReceivingHandler, fmt.Sprintf(`{"sessionId":%d,"placementIds":[%d]}`, session, next), "", 0), 200)
	before = receivingOperationFingerprint(t, db)
	requireReceivingStatus(t, undo.call(db, undoToken), 200)
	if before != receivingOperationFingerprint(t, db) {
		t.Fatal("old undo replay altered a completed session")
	}
}

func TestReceivingOperationMigrationUpgrade(t *testing.T) {
	db := receivingTestDBWithSchema(t, true, false)
	_, err := db.Exec(context.Background(), `INSERT INTO receiving_sessions(status,completed_at) VALUES('active',NULL); INSERT INTO receiving_session_items(session_id,product_id,quantity) VALUES(1,1,7); INSERT INTO cell_stock(cell_id,product_id,quantity) VALUES(1,1,4)`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/022_receiving_operation_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(context.Background(), string(migration)); err != nil {
		t.Fatal(err)
	}
	details := receivingDetails(t, db, 1)
	if details.PendingRevision != "0" || len(details.PendingItems) != 1 || details.PendingItems[0].Quantity != 7 || receivingStock(t, db) != 4 {
		t.Fatal("upgrade changed existing pending/stock")
	}
	receivingAdd(t, db, 1, 1)
	if receivingDetails(t, db, 1).PendingRevision == "0" {
		t.Fatal("upgrade did not activate revision trigger")
	}
}
