package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func scanRequest(db *pgxpool.Pool, session int64, token, sku string, quantity int) *httptest.ResponseRecorder {
	return receivingRequest(db, addReceivingSessionItemHandler,
		fmt.Sprintf(`{"requestId":%q,"sku":%q,"quantity":%d}`, token, sku, quantity), "sessionID", session)
}

func requireScanTotals(t *testing.T, db *pgxpool.Pool, quantity, events, receipts int64) {
	t.Helper()
	var pending, gotEvents, gotReceipts int64
	err := db.QueryRow(context.Background(), `SELECT
 (SELECT COALESCE(sum(quantity),0) FROM receiving_session_items),
 (SELECT count(*) FROM receiving_intake_events),
 (SELECT count(*) FROM receiving_quantity_requests)`).Scan(&pending, &gotEvents, &gotReceipts)
	if err != nil {
		t.Fatal(err)
	}
	if pending != quantity || gotEvents != events || gotReceipts != receipts {
		t.Fatalf("pending/events/receipts=%d/%d/%d, want %d/%d/%d", pending, gotEvents, gotReceipts, quantity, events, receipts)
	}
}

func TestReceivingScanRejectsOldAndInvalidContract(t *testing.T) {
	for _, body := range []string{
		`{"sku":"TEST-SKU","quantity":1}`,
		`{"sku":"TEST-SKU","quantity":1,"requestId":""}`,
		`{"sku":"TEST-SKU","quantity":1,"requestId":"xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"}`,
	} {
		// A nil pool proves validation rejects these requests before any DB access.
		w := receivingRequest(nil, addReceivingSessionItemHandler, body, "sessionID", 1)
		requireReceivingStatus(t, w, http.StatusBadRequest)
	}
}

func TestReceivingScanReplayAndIndependentIntent(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	token := newReceivingScanID(t)
	first := scanRequest(db, session, token, " test-sku ", 1)
	requireReceivingStatus(t, first, http.StatusOK)
	requireScanTotals(t, db, 1, 1, 1)
	// Simulate loss of the committed HTTP response: discard it and redeliver.
	replay := scanRequest(db, session, strings.ToUpper(token), "TEST-SKU", 1)
	requireReceivingStatus(t, replay, http.StatusOK)
	if first.Body.String() != replay.Body.String() {
		t.Fatal("replay changed the original response")
	}
	requireScanTotals(t, db, 1, 1, 1)
	requireReceivingStatus(t, scanRequest(db, session, newReceivingScanID(t), "TEST-SKU", 1), http.StatusOK)
	requireScanTotals(t, db, 2, 2, 2)
	requireIntakeTotals(t, db, 2, 2)
}

func TestReceivingScanConflictingPayloadAndOperation(t *testing.T) {
	db := receivingTestDB(t)
	session, other := receivingSession(t, db), receivingSession(t, db)
	token := newReceivingScanID(t)
	requireReceivingStatus(t, scanRequest(db, session, token, "TEST-SKU", 1), http.StatusOK)
	for _, test := range []struct {
		session  int64
		sku      string
		quantity int
	}{
		{session, "TEST-SKU", 2}, {session, "OTHER", 1}, {other, "TEST-SKU", 1},
	} {
		requireReceivingStatus(t, scanRequest(db, test.session, token, test.sku, test.quantity), http.StatusConflict)
		requireScanTotals(t, db, 1, 1, 1)
	}
	// The shared receipt cannot be reused by the quantity endpoint either.
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"requestId":%q,"quantity":1}`, token)))
	r.SetPathValue("sessionID", fmt.Sprint(session))
	r.SetPathValue("productID", "1")
	w := httptest.NewRecorder()
	receivingQuantityHandler(db, w, r)
	requireReceivingStatus(t, w, http.StatusConflict)
	requireScanTotals(t, db, 1, 1, 1)
}

func TestReceivingScanConcurrentReplay(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	token := newReceivingScanID(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; responses <- scanRequest(db, session, token, "TEST-SKU", 1) }()
	}
	close(start)
	wg.Wait()
	close(responses)
	for response := range responses {
		requireReceivingStatus(t, response, http.StatusOK)
	}
	requireScanTotals(t, db, 1, 1, 1)
}

func TestReceivingScanReplayAfterCompletion(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	token := newReceivingScanID(t)
	first := scanRequest(db, session, token, "TEST-SKU", 1)
	requireReceivingStatus(t, first, http.StatusOK)
	placement := receivingPlace(t, db, session, 1)
	w := receivingRequest(db, finishReceivingHandler, fmt.Sprintf(`{"sessionId":%d,"placementIds":[%d]}`, session, placement), "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	replay := scanRequest(db, session, token, "TEST-SKU", 1)
	requireReceivingStatus(t, replay, http.StatusOK)
	if first.Body.String() != replay.Body.String() {
		t.Fatal("completed-session replay changed response")
	}
	requireScanTotals(t, db, 0, 1, 1)
	requireReceivingStatus(t, scanRequest(db, session, newReceivingScanID(t), "TEST-SKU", 1), http.StatusConflict)
	requireScanTotals(t, db, 0, 1, 1)
}

func TestReceivingScanWriteFailureRollsBack(t *testing.T) {
	for _, table := range []string{"receiving_intake_events", "receiving_quantity_requests"} {
		t.Run(table, func(t *testing.T) {
			db := receivingTestDB(t)
			session := receivingSession(t, db)
			token := newReceivingScanID(t)
			_, err := db.Exec(context.Background(), `CREATE FUNCTION fail_scan_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected scan failure'; END $$`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(context.Background(), `CREATE TRIGGER fail_scan BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_scan_write()`); err != nil {
				t.Fatal(err)
			}
			requireReceivingStatus(t, scanRequest(db, session, token, "TEST-SKU", 1), http.StatusInternalServerError)
			requireScanTotals(t, db, 0, 0, 0)
			if _, err = db.Exec(context.Background(), `DROP TRIGGER fail_scan ON `+table); err != nil {
				t.Fatal(err)
			}
			requireReceivingStatus(t, scanRequest(db, session, token, "TEST-SKU", 1), http.StatusOK)
			requireScanTotals(t, db, 1, 1, 1)
		})
	}
}
