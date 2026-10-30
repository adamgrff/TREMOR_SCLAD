package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func receivingParallel(t *testing.T, a, b func() *httptest.ResponseRecorder) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
	t.Helper()
	start := make(chan struct{})
	left := make(chan *httptest.ResponseRecorder, 1)
	right := make(chan *httptest.ResponseRecorder, 1)
	go func() { <-start; left <- a() }()
	go func() { <-start; right <- b() }()
	close(start)
	var x, y *httptest.ResponseRecorder
	select {
	case x = <-left:
	case <-time.After(10 * time.Second):
		t.Fatal("first concurrent request stalled")
	}
	select {
	case y = <-right:
	case <-time.After(10 * time.Second):
		t.Fatal("second concurrent request stalled")
	}
	for _, w := range []*httptest.ResponseRecorder{x, y} {
		if w.Code != 200 && w.Code != 201 && w.Code != 409 {
			t.Fatalf("unexpected concurrent status %d: %s", w.Code, w.Body.String())
		}
	}
	return x, y
}

// Reconcile each tested race against committed placements, pending quantities,
// intake events and both receipt tables. Foreign-key checks cover every table.
func receivingIntegrity(t *testing.T, db *pgxpool.Pool, pending, stock, events, receipts int) {
	t.Helper()
	ctx := context.Background()
	var p, s, e, r, negative, orphans int
	err := db.QueryRow(ctx, `SELECT
 (SELECT COALESCE(sum(quantity),0) FROM receiving_session_items),
 (SELECT COALESCE(sum(quantity),0) FROM cell_stock),
 (SELECT count(*) FROM receiving_intake_events),
 (SELECT count(*) FROM receiving_quantity_requests)+(SELECT count(*) FROM receiving_operation_requests),
 (SELECT count(*) FROM cell_stock WHERE quantity<0),
 (SELECT count(*) FROM receiving_placement_items i LEFT JOIN receiving_placements p ON p.id=i.placement_id WHERE p.id IS NULL)
 +(SELECT count(*) FROM receiving_session_items i LEFT JOIN receiving_sessions s ON s.id=i.session_id WHERE s.id IS NULL)`).Scan(&p, &s, &e, &r, &negative, &orphans)
	if err != nil {
		t.Fatal(err)
	}
	if p != pending || s != stock || e != events || r != receipts || negative != 0 || orphans != 0 {
		t.Fatalf("invariants pending/stock/events/receipts/negative/orphans=%d/%d/%d/%d/%d/%d wanted %d/%d/%d/%d/0/0", p, s, e, r, negative, orphans, pending, stock, events, receipts)
	}
	var placed int
	if err = db.QueryRow(ctx, `SELECT COALESCE(sum(i.quantity),0) FROM receiving_placement_items i JOIN receiving_placements p ON p.id=i.placement_id WHERE p.status='active'`).Scan(&placed); err != nil {
		t.Fatal(err)
	}
	if placed != stock {
		t.Fatalf("stock %d != effective receiving placements %d", stock, placed)
	}
}

func TestReceivingIntegrityRaces(t *testing.T) {
	for _, kind := range []string{"same-scan", "different-scan", "scan-place", "scan-delete", "scan-finish", "place-undo", "independent-sessions", "shared-uuid"} {
		t.Run(kind, func(t *testing.T) {
			db := receivingTestDB(t)
			session := receivingSession(t, db)
			scan := receivingOperationCase{handler: addReceivingSessionItemHandler, pathKey: "sessionID", pathID: session, body: map[string]any{"sku": "TEST-SKU", "quantity": 1}}
			id1, id2 := newReceivingScanID(t), newReceivingScanID(t)
			switch kind {
			case "same-scan", "different-scan":
				if kind == "same-scan" {
					id2 = id1
				}
				receivingParallel(t, func() *httptest.ResponseRecorder { return scan.call(db, id1) }, func() *httptest.ResponseRecorder { return scan.call(db, id2) })
				n := 2
				if id1 == id2 {
					n = 1
				}
				receivingIntegrity(t, db, n, 0, n, 1+n)
			case "scan-place", "scan-delete":
				receivingAdd(t, db, session, 1)
				revision := receivingDetails(t, db, session).PendingRevision
				other := receivingOperationCase{handler: createReceivingPlacementHandler, body: map[string]any{"sessionId": session, "cellCode": "TEST-1", "items": []map[string]any{{"sku": "TEST-SKU", "quantity": 1}}}}
				if kind == "scan-delete" {
					other = receivingOperationCase{handler: deleteReceivingSessionItemHandler, pathKey: "sessionID", pathID: session, sku: "TEST-SKU", body: map[string]any{"expectedRevision": revision}}
				}
				a, b := receivingParallel(t, func() *httptest.ResponseRecorder { return scan.call(db, id1) }, func() *httptest.ResponseRecorder { return other.call(db, id2) })
				requireReceivingStatus(t, a, 200)
				pending, stock, receipts := 2, 0, 3
				if b.Code == 200 {
					pending = 1
					receipts++
					if kind == "scan-place" {
						stock = 1
					}
				}
				receivingIntegrity(t, db, pending, stock, 2, receipts)
				if receivingDetails(t, db, session).PendingRevision == revision {
					t.Fatal("revision unchanged")
				}
			case "scan-finish":
				receivingAdd(t, db, session, 1)
				placement := receivingPlace(t, db, session, 1)
				finish := receivingOperationCase{handler: finishReceivingHandler, body: map[string]any{"sessionId": session, "placementIds": []int64{placement}}}
				a, b := receivingParallel(t, func() *httptest.ResponseRecorder { return scan.call(db, id1) }, func() *httptest.ResponseRecorder { return finish.call(db, id2) })
				if b.Code == 200 {
					requireReceivingStatus(t, a, 409)
					receivingIntegrity(t, db, 0, 1, 1, 4)
				} else {
					requireReceivingStatus(t, a, 200)
					receivingIntegrity(t, db, 1, 1, 2, 4)
				}
			case "place-undo":
				receivingAdd(t, db, session, 1)
				placement := receivingPlace(t, db, session, 1)
				receivingAdd(t, db, session, 1)
				place := receivingOperationCase{handler: createReceivingPlacementHandler, body: map[string]any{"sessionId": session, "cellCode": "TEST-1", "items": []map[string]any{{"sku": "TEST-SKU", "quantity": 1}}}}
				undo := receivingOperationCase{handler: undoReceivingPlacementHandler, pathKey: "placementID", pathID: placement}
				a, b := receivingParallel(t, func() *httptest.ResponseRecorder { return place.call(db, id1) }, func() *httptest.ResponseRecorder { return undo.call(db, id2) })
				requireReceivingStatus(t, b, 200)
				if a.Code == 200 {
					receivingIntegrity(t, db, 1, 1, 2, 6)
				} else {
					receivingIntegrity(t, db, 2, 0, 2, 5)
				}
			case "independent-sessions":
				other := receivingSession(t, db)
				receivingAdd(t, db, session, 1)
				receivingAdd(t, db, other, 1)
				place := func(s int64, id string) *httptest.ResponseRecorder {
					c := receivingOperationCase{handler: createReceivingPlacementHandler, body: map[string]any{"sessionId": s, "cellCode": "TEST-1", "items": []map[string]any{{"sku": "TEST-SKU", "quantity": 1}}}}
					return c.call(db, id)
				}
				a, b := receivingParallel(t, func() *httptest.ResponseRecorder { return place(session, id1) }, func() *httptest.ResponseRecorder { return place(other, id2) })
				requireReceivingStatus(t, a, 200)
				requireReceivingStatus(t, b, 200)
				receivingIntegrity(t, db, 0, 2, 2, 6)
			case "shared-uuid":
				create := receivingOperationCase{handler: createReceivingSessionHandler}
				a, b := receivingParallel(t, func() *httptest.ResponseRecorder { return scan.call(db, id1) }, func() *httptest.ResponseRecorder { return create.call(db, id1) })
				if a.Code == 200 {
					requireReceivingStatus(t, b, 409)
					receivingIntegrity(t, db, 1, 0, 1, 2)
				} else {
					requireReceivingStatus(t, a, 409)
					requireReceivingStatus(t, b, 201)
					receivingIntegrity(t, db, 0, 0, 0, 2)
				}
			}
		})
	}
}

func TestReceivingReceiptMutationProtection(t *testing.T) {
	db := receivingTestDB(t)
	s := receivingSession(t, db)
	receivingAdd(t, db, s, 1)
	before := receivingOperationFingerprint(t, db)
	for _, table := range []string{"receiving_quantity_requests", "receiving_operation_requests"} {
		for _, query := range []string{"UPDATE " + table + " SET payload=payload", "DELETE FROM " + table, "TRUNCATE " + table} {
			_, err := db.Exec(context.Background(), query)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("receipt mutation allowed: %s: %v", query, err)
			}
			if before != receivingOperationFingerprint(t, db) {
				t.Fatal("protected receipt changed")
			}
		}
	}
}

func TestReceivingFreshScanRejectedAfterConcurrentCompletion(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	token := newReceivingScanID(t)
	requireReceivingStatus(t, scanRequest(db, session, token, "TEST-SKU", 1), 200)
	placement := receivingPlace(t, db, session, 1)
	finish := receivingOperationCase{handler: finishReceivingHandler, body: map[string]any{"sessionId": session, "placementIds": []int64{placement}}}
	requireReceivingStatus(t, finish.call(db, newReceivingScanID(t)), 200)
	before := receivingOperationFingerprint(t, db)
	rejected := scanRequest(db, session, newReceivingScanID(t), "TEST-SKU", 1)
	requireReceivingStatus(t, rejected, 409)
	if rejected.Header().Get("X-Receiving-Command-Rejected") != "true" {
		t.Fatal("fresh rejected scan cannot be safely resolved by the frontend")
	}
	for _, response := range []*httptest.ResponseRecorder{scanRequest(db, session, token, "TEST-SKU", 1), scanRequest(db, session, token, "TEST-SKU", 2)} {
		if response.Header().Get("X-Receiving-Command-Rejected") != "" {
			t.Fatal("replay/conflict incorrectly marked as fresh rejection")
		}
	}
	if before != receivingOperationFingerprint(t, db) {
		t.Fatal("rejected scan or retry changed warehouse data")
	}
}

// A test-only tracer exposes the lock order without changing application code.
type receivingStockTrace struct {
	first   chan struct{}
	release chan struct{}
	seen    bool
	needle  string
	second  chan struct{}
}
type receivingStockTraceKey struct{}

func (p *receivingStockTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	match := strings.Contains(d.SQL, p.needle)
	if match && p.seen && p.second != nil {
		select {
		case p.second <- struct{}{}:
		default:
		}
	}
	return context.WithValue(ctx, receivingStockTraceKey{}, match)
}
func (p *receivingStockTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	if ctx.Value(receivingStockTraceKey{}) == true && !p.seen && d.Err == nil {
		p.seen = true
		close(p.first)
		if p.release != nil {
			select {
			case <-p.release:
			case <-ctx.Done():
			}
		}
	}
}

func TestReceivingStockLockOrder(t *testing.T) {
	db := receivingTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// SKU order is deliberately opposite to primary-key order.
	stockTestExec(t, db, `INSERT INTO products(category_id,sku,name) VALUES(1,'AAA','Second')`)
	old, newSession := receivingSession(t, db), receivingSession(t, db)
	for _, s := range []int64{old, newSession} {
		receivingAdd(t, db, s, 1)
		requireReceivingStatus(t, receivingRequest(db, addReceivingSessionItemHandler, fmt.Sprintf(`{"sku":"AAA","quantity":1,"requestId":%q}`, newReceivingScanID(t)), "sessionID", s), 200)
	}
	body := map[string]any{"cellCode": "TEST-1", "items": []map[string]any{{"sku": "TEST-SKU", "quantity": 1}, {"sku": "AAA", "quantity": 1}}}
	body["sessionId"] = old
	c := receivingOperationCase{handler: createReceivingPlacementHandler, body: body}
	w := c.call(db, newReceivingScanID(t))
	requireReceivingStatus(t, w, 200)
	var placement receivingPlacementResponse
	json.Unmarshal(w.Body.Bytes(), &placement)
	pause := &receivingStockTrace{first: make(chan struct{}), release: make(chan struct{}), needle: "INSERT INTO cell_stock"}
	watch := &receivingStockTrace{first: make(chan struct{}), second: make(chan struct{}, 1), needle: "UPDATE cell_stock"}
	pool := func(tracer pgx.QueryTracer, app string) *pgxpool.Pool {
		cfg := db.Config()
		cfg.MaxConns = 1
		cfg.ConnConfig.Tracer = tracer
		cfg.ConnConfig.RuntimeParams["application_name"] = app
		cfg.ConnConfig.RuntimeParams["enable_seqscan"] = "off"
		cfg.ConnConfig.RuntimeParams["enable_bitmapscan"] = "off"
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	placeDB, undoDB := pool(pause, "receiving-place-order"), pool(watch, "receiving-undo-order")
	body["sessionId"] = newSession
	placeDone := make(chan *httptest.ResponseRecorder, 1)
	undoDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { placeDone <- c.call(placeDB, newReceivingScanID(t)) }()
	select {
	case <-pause.first:
	case <-ctx.Done():
		t.Fatal("placement did not lock stock")
	}
	undo := receivingOperationCase{handler: undoReceivingPlacementHandler, pathKey: "placementID", pathID: placement.ID}
	go func() { undoDone <- undo.call(undoDB, newReceivingScanID(t)) }()
	// Wait until undo actually waits on the placement. If it held the opposite
	// first row, releasing placement now reproduces PostgreSQL's deadlock.
	for {
		var blocked bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name='receiving-undo-order' AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
			close(pause.release)
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			close(pause.release)
			t.Fatal("undo did not reach stock lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(pause.release)
	var a, b *httptest.ResponseRecorder
	select {
	case a = <-placeDone:
	case <-ctx.Done():
		t.Fatal("place stalled")
	}
	select {
	case b = <-undoDone:
	case <-ctx.Done():
		t.Fatal("undo stalled")
	}
	requireReceivingStatus(t, a, 200)
	requireReceivingStatus(t, b, 200)
	select {
	case <-watch.first:
	default:
		t.Fatal("undo did not update stock")
	}
	receivingIntegrity(t, db, 2, 2, 4, 9)
}

func TestReceivingStockOrderAgainstAssembly(t *testing.T) {
	db := homeStatisticsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// SKU order is deliberately opposite to primary-key order.
	stockTestExec(t, db, `INSERT INTO products(category_id,sku,name) VALUES(1,'AAA','Second')`)
	old, newSession := receivingSession(t, db), receivingSession(t, db)
	for _, s := range []int64{old, newSession} {
		receivingAdd(t, db, s, 1)
		requireReceivingStatus(t, receivingRequest(db, addReceivingSessionItemHandler, fmt.Sprintf(`{"sku":"AAA","quantity":1,"requestId":%q}`, newReceivingScanID(t)), "sessionID", s), 200)
	}
	body := map[string]any{"cellCode": "TEST-1", "items": []map[string]any{{"sku": "TEST-SKU", "quantity": 1}, {"sku": "AAA", "quantity": 1}}}
	body["sessionId"] = old
	c := receivingOperationCase{handler: createReceivingPlacementHandler, body: body}
	w := c.call(db, newReceivingScanID(t))
	requireReceivingStatus(t, w, 200)
	var placement receivingPlacementResponse
	json.Unmarshal(w.Body.Bytes(), &placement)
	order := realTestOrder("RECEIVING-LOCK-ORDER")
	order.Items[0].SKU = "TEST-SKU"
	order.Items[0].Quantity = 1
	order.Items[1].SKU = "AAA"
	order.Items[1].Quantity = 1
	importWarehouseTest(t, db, 300, order)
	state := warehouseOK(t, warehouseAction(db, 301, 0, "start", order.Number, ""))
	cell := textValue(nextWarehouseCell(valueMap(state["activeOrder"]))["scanCode"])
	warehouseOK(t, warehouseAction(db, 302, 1, "scan", "", cell))
	pause := &receivingStockTrace{first: make(chan struct{}), release: make(chan struct{}), needle: "INSERT INTO cell_stock"}
	watch := &receivingStockTrace{first: make(chan struct{}), second: make(chan struct{}, 1), needle: "FROM cell_stock s JOIN"}
	pool := func(tracer pgx.QueryTracer, app string) *pgxpool.Pool {
		cfg := db.Config()
		cfg.MaxConns = 1
		cfg.ConnConfig.Tracer = tracer
		cfg.ConnConfig.RuntimeParams["application_name"] = app
		cfg.ConnConfig.RuntimeParams["enable_seqscan"] = "off"
		cfg.ConnConfig.RuntimeParams["enable_bitmapscan"] = "off"
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	placeDB, undoDB := pool(pause, "receiving-place-order"), pool(watch, "receiving-undo-order")
	body["sessionId"] = newSession
	placeDone := make(chan *httptest.ResponseRecorder, 1)
	undoDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { placeDone <- c.call(placeDB, newReceivingScanID(t)) }()
	select {
	case <-pause.first:
	case <-ctx.Done():
		t.Fatal("placement did not lock stock")
	}
	go func() { undoDone <- warehouseAction(undoDB, 303, 2, "scan", "", "AAA") }()
	// Wait until undo actually waits on the placement. If it held the opposite
	// first row, releasing placement now reproduces PostgreSQL's deadlock.
	for {
		var blocked bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name='receiving-undo-order' AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
			close(pause.release)
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			close(pause.release)
			t.Fatal("undo did not reach stock lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(pause.release)
	var a, b *httptest.ResponseRecorder
	select {
	case a = <-placeDone:
	case <-ctx.Done():
		t.Fatal("place stalled")
	}
	select {
	case b = <-undoDone:
	case <-ctx.Done():
		t.Fatal("undo stalled")
	}
	requireReceivingStatus(t, a, 200)
	requireReceivingStatus(t, b, 200)
	select {
	case <-watch.first:
	default:
		t.Fatal("undo did not update stock")
	}
	stockTestQuantity(t, db, "cell_stock", 1, 2)
	stockTestQuantity(t, db, "cell_stock", 2, 1)
	assertStorageCount(t, db, "assembly_warehouse_operations", 1)
	var pending int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM receiving_session_items`).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("pending group not fully placed")
	}
	assertStorageCount(t, db, "receiving_operation_requests", 4)
	assertStorageCount(t, db, "receiving_quantity_requests", 4)
	requireIntakeTotals(t, db, 4, 4)
}

func TestReceivingMigrationChain(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			url := os.Getenv("TEST_DATABASE_URL")
			if url == "" {
				t.Skip("TEST_DATABASE_URL required")
			}
			requireSafeReceivingTestDatabase(t, url)
			ctx := context.Background()
			admin, err := pgx.Connect(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close(ctx)
			schema := pgx.Identifier{fmt.Sprintf("receiving_chain_%d", time.Now().UnixNano())}.Sanitize()
			if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
				t.Fatal(err)
			}
			defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
			cfg, err := pgxpool.ParseConfig(url)
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.RuntimeParams["search_path"] = schema
			db, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			files, err := filepath.Glob("../../migrations/*.sql")
			if err != nil {
				t.Fatal(err)
			}
			sort.Strings(files)
			var stockBefore int
			var session int64
			var beforeProtection string
			for _, file := range files {
				if legacy && strings.HasPrefix(filepath.Base(file), "022_") {
					if err = db.QueryRow(ctx, `INSERT INTO receiving_sessions(status,completed_at) VALUES('active',NULL) RETURNING id`).Scan(&session); err != nil {
						t.Fatal(err)
					}
					stockTestExec(t, db, `INSERT INTO receiving_session_items(session_id,product_id,quantity) SELECT $1,id,7 FROM products ORDER BY id LIMIT 1`, session)
					if err = db.QueryRow(ctx, `SELECT COALESCE(sum(quantity),0) FROM cell_stock`).Scan(&stockBefore); err != nil {
						t.Fatal(err)
					}
				}
				if legacy && strings.HasPrefix(filepath.Base(file), "023_") {
					stockTestExec(t, db, `INSERT INTO receiving_quantity_requests(request_id,payload,product_id) SELECT '30000000-0000-4000-8000-000000000001','legacy',id FROM products ORDER BY id LIMIT 1`)
					stockTestExec(t, db, `INSERT INTO receiving_operation_requests(request_id,operation,payload,response,status_code) VALUES('30000000-0000-4000-8000-000000000002','create-session','{}','{"status":"active"}',201)`)
					beforeProtection = receivingOperationFingerprint(t, db)
				}
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				// Seed is also a psql script; client encoding is already UTF8.
				sql := strings.ReplaceAll(string(data), "\\encoding UTF8", "")
				if _, err = db.Exec(ctx, sql); err != nil {
					t.Fatalf("migration %s: %v", file, err)
				}
			}
			if legacy {
				if beforeProtection != receivingOperationFingerprint(t, db) {
					t.Fatal("receipt protection migration changed existing records")
				}
				details := receivingDetails(t, db, session)
				if details.PendingRevision != "0" || len(details.PendingItems) != 1 || details.PendingItems[0].Quantity != 7 || receivingStock(t, db) != stockBefore {
					t.Fatal("upgrade changed legacy warehouse data")
				}
			}
		})
	}
}

// Child processes run only the receiving handler against the parent's isolated
// test schema, never main() or the real API port/configuration.
func TestReceivingHTTPRestartAfterLostCommit(t *testing.T) {
	if os.Getenv("RECEIVING_RESTART_CHILD") == "1" {
		url := os.Getenv("TEST_DATABASE_URL")
		requireSafeReceivingTestDatabase(t, url)
		cfg, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = os.Getenv("RECEIVING_TEST_SCHEMA")
		db, err := pgxpool.NewWithConfig(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("RECEIVING_TEST_ADDR=" + listener.Addr().String())
		handler := func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("sessionID", os.Getenv("RECEIVING_TEST_SESSION"))
			if os.Getenv("RECEIVING_TEST_DROP") == "1" {
				result := httptest.NewRecorder()
				addReceivingSessionItemHandler(db, result, r)
				if result.Code != 200 {
					t.Errorf("initial status %d: %s", result.Code, result.Body.String())
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			addReceivingSessionItemHandler(db, w, r)
		}
		if err := http.Serve(listener, http.HandlerFunc(handler)); err != nil {
			t.Fatal(err)
		}
		return
	}
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	token := newReceivingScanID(t)
	body := fmt.Sprintf(`{"requestId":%q,"sku":"TEST-SKU","quantity":1}`, token)
	start := func(drop bool) (string, func()) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReceivingHTTPRestartAfterLostCommit$")
		flag := "0"
		if drop {
			flag = "1"
		}
		cmd.Env = append(os.Environ(), "RECEIVING_RESTART_CHILD=1", "RECEIVING_TEST_SCHEMA="+db.Config().ConnConfig.RuntimeParams["search_path"], "RECEIVING_TEST_SESSION="+strconv.FormatInt(session, 10), "RECEIVING_TEST_DROP="+flag)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		stop := func() { cancel(); _ = cmd.Wait() }
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "RECEIVING_TEST_ADDR=") {
				return "http://" + strings.TrimPrefix(line, "RECEIVING_TEST_ADDR="), stop
			}
		}
		stop()
		t.Fatal("isolated receiving child did not start")
		return "", func() {}
	}
	url, stop := start(true)
	_, err := http.Post(url, "application/json", strings.NewReader(body))
	stop()
	if err == nil {
		t.Fatal("expected lost response")
	}
	receivingIntegrity(t, db, 1, 0, 1, 2)
	placement := receivingPlace(t, db, session, 1)
	finish := receivingOperationCase{handler: finishReceivingHandler, body: map[string]any{"sessionId": session, "placementIds": []int64{placement}}}
	requireReceivingStatus(t, finish.call(db, newReceivingScanID(t)), 200)
	receivingIntegrity(t, db, 0, 1, 1, 4)
	url, stop = start(false)
	defer stop()
	response, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	receivingIntegrity(t, db, 0, 1, 1, 4)
}

func TestReceivingUndoAgainstAssemblyPick(t *testing.T) {
	db := homeStatisticsTestDB(t)
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 2)
	placement := receivingPlace(t, db, session, 2)
	order := realTestOrder("RECEIVING-RACE")
	order.Items = order.Items[:1]
	order.Items[0].SKU = "TEST-SKU"
	order.Items[0].Quantity = 1
	importWarehouseTest(t, db, 200, order)
	state := warehouseOK(t, warehouseAction(db, 201, 0, "start", order.Number, ""))
	cell := textValue(nextWarehouseCell(valueMap(state["activeOrder"]))["scanCode"])
	warehouseOK(t, warehouseAction(db, 202, 1, "scan", "", cell))
	undo := receivingOperationCase{handler: undoReceivingPlacementHandler, pathKey: "placementID", pathID: placement}
	a, b := receivingParallel(t, func() *httptest.ResponseRecorder { return undo.call(db, newReceivingScanID(t)) }, func() *httptest.ResponseRecorder { return warehouseAction(db, 203, 2, "scan", "", "TEST-SKU") })
	requireReceivingStatus(t, b, 200)
	var pick assemblyDraftResponse
	if err := json.Unmarshal(b.Body.Bytes(), &pick); err != nil {
		t.Fatal(err)
	}
	var picks, stock, pending, negatives int
	if err := db.QueryRow(context.Background(), `SELECT
 (SELECT count(*) FROM assembly_warehouse_operations WHERE kind='pick'),
 (SELECT COALESCE(sum(quantity),0) FROM cell_stock),
 (SELECT COALESCE(sum(quantity),0) FROM receiving_session_items),
 (SELECT count(*) FROM cell_stock WHERE quantity<0)`).Scan(&picks, &stock, &pending, &negatives); err != nil {
		t.Fatal(err)
	}
	if negatives != 0 {
		t.Fatal("negative stock")
	}
	if a.Code == 200 {
		if picks != 0 || stock != 0 || pending != 2 || pick.Applied == nil || *pick.Applied {
			t.Fatal("assembly consumed already undone stock")
		}
	} else {
		requireReceivingStatus(t, a, 409)
		if picks != 1 || stock != 1 || pending != 0 || pick.Applied == nil || !*pick.Applied {
			t.Fatal("undo did not roll back after assembly pick")
		}
	}
	fingerprint := receivingOperationFingerprint(t, db)
	replay := warehouseAction(db, 203, 2, "scan", "", "TEST-SKU")
	if !sameWarehouseResponse(b, replay) || fingerprint != receivingOperationFingerprint(t, db) {
		t.Fatal("assembly retry repeated stock effect")
	}
}

func TestReceivingOverflowRollback(t *testing.T) {
	db := receivingTestDB(t)
	s := receivingSession(t, db)
	receivingAdd(t, db, s, 1)
	stockTestExec(t, db, `INSERT INTO cell_stock(cell_id,product_id,quantity) VALUES(1,1,2147483647)`)
	before := receivingOperationFingerprint(t, db)
	c := receivingOperationCase{handler: createReceivingPlacementHandler, body: map[string]any{"sessionId": s, "cellCode": "TEST-1", "items": []map[string]any{{"sku": "TEST-SKU", "quantity": 1}}}}
	requireReceivingStatus(t, c.call(db, newReceivingScanID(t)), 500)
	if before != receivingOperationFingerprint(t, db) {
		t.Fatal("overflow left partial placement, revision or receipt")
	}
}

func TestReceivingDatabaseConstraints(t *testing.T) {
	db := receivingTestDB(t)
	s := receivingSession(t, db)
	receivingAdd(t, db, s, 1)
	receivingPlace(t, db, s, 1)
	before := receivingOperationFingerprint(t, db)
	for _, test := range []struct{ sql, code string }{
		{`UPDATE cell_stock SET quantity=-1`, "23514"},
		{`INSERT INTO receiving_session_items(session_id,product_id,quantity) VALUES(1,1,0)`, "23514"},
		{`INSERT INTO receiving_session_items(session_id,product_id,quantity) VALUES(99999,1,1)`, "23503"},
		{`INSERT INTO receiving_placement_items(placement_id,product_id,quantity) VALUES(99999,1,1)`, "23503"},
		{`DELETE FROM products WHERE id=1`, "23503"},
		{`DELETE FROM receiving_sessions WHERE id=1`, "23503"},
		{`UPDATE receiving_intake_events SET quantity=999`, "23514"},
		{`DELETE FROM receiving_intake_events`, "23514"},
		{`TRUNCATE receiving_intake_events`, "23514"},
	} {
		_, err := db.Exec(context.Background(), test.sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != test.code && !(test.code == "23503" && pgErr.Code == "23001")) {
			t.Fatalf("constraint %s: %v", test.sql, err)
		}
		if before != receivingOperationFingerprint(t, db) {
			t.Fatal("failed constraint left partial effects")
		}
	}
}

func TestReceivingOverlappingSessions(t *testing.T) {
	for _, sameCell := range []bool{true, false} {
		t.Run(fmt.Sprint("same-cell=", sameCell), func(t *testing.T) {
			db := receivingTestDB(t)
			stockTestExec(t, db, `INSERT INTO products(category_id,sku,name) VALUES(1,'AAA','Second'); INSERT INTO cells(warehouse_id,category_id,code,row_number,position_number) VALUES(1,1,'TEST-2',1,2)`)
			sessions := []int64{receivingSession(t, db), receivingSession(t, db)}
			commands := make([]receivingOperationCase, 2)
			for i, s := range sessions {
				receivingAdd(t, db, s, 1)
				requireReceivingStatus(t, receivingRequest(db, addReceivingSessionItemHandler, fmt.Sprintf(`{"sku":"AAA","quantity":1,"requestId":%q}`, newReceivingScanID(t)), "sessionID", s), 200)
				cell := "TEST-1"
				if i == 1 && !sameCell {
					cell = "TEST-2"
				}
				commands[i] = receivingOperationCase{handler: createReceivingPlacementHandler, body: map[string]any{"sessionId": s, "cellCode": cell, "items": []map[string]any{{"sku": "TEST-SKU", "quantity": 1}, {"sku": "AAA", "quantity": 1}}}}
			}
			a, b := receivingParallel(t, func() *httptest.ResponseRecorder { return commands[0].call(db, newReceivingScanID(t)) }, func() *httptest.ResponseRecorder { return commands[1].call(db, newReceivingScanID(t)) })
			requireReceivingStatus(t, a, 200)
			requireReceivingStatus(t, b, 200)
			receivingIntegrity(t, db, 0, 4, 4, 8)
			for i, w := range []*httptest.ResponseRecorder{a, b} {
				var p receivingPlacementResponse
				if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
					t.Fatal(err)
				}
				commands[i] = receivingOperationCase{handler: undoReceivingPlacementHandler, pathKey: "placementID", pathID: p.ID}
			}
			a, b = receivingParallel(t, func() *httptest.ResponseRecorder { return commands[0].call(db, newReceivingScanID(t)) }, func() *httptest.ResponseRecorder { return commands[1].call(db, newReceivingScanID(t)) })
			requireReceivingStatus(t, a, 200)
			requireReceivingStatus(t, b, 200)
			receivingIntegrity(t, db, 4, 0, 4, 10)
		})
	}
}
