package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func disassemblyUndoFixture(t *testing.T, count int) *disassemblyTestFixture {
	t.Helper()
	f := disassemblyFixture(t, true)
	f.transfer(t)
	f.action(t, "start", "", true)
	for n := 0; n < count; n++ {
		disassemblyUndoReturnOne(t, f)
	}
	return f
}

func disassemblyUndoReturnOne(t *testing.T, f *disassemblyTestFixture) map[string]any {
	t.Helper()
	f.action(t, "scan", "A", true)
	itemID := textValue(disassemblyTestSession(f.state)["pendingItemId"])
	item := modelItem(t, f, itemID)
	f.action(t, "scan", fmt.Sprintf("CELL:%d", intValue(item["cellId"])), true)
	return modelItem(t, f, itemID)
}

func disassemblyUndoSnapshot(t *testing.T, f *disassemblyTestFixture) string {
	t.Helper()
	var snapshot string
	err := f.db.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'stock',(SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY cell_id,product_id),'[]') FROM cell_stock c),
 'sessions',(SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY id),'[]') FROM assembly_disassembly_sessions s),
 'returns',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id),'[]') FROM assembly_disassembly_returns r),
 'reversals',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id),'[]') FROM assembly_disassembly_return_reversals r),
 'operations',(SELECT COALESCE(jsonb_agg(to_jsonb(o) ORDER BY id),'[]') FROM assembly_warehouse_operations o),
 'reserves',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY order_id,product_id),'[]') FROM assembly_order_reservations r),
 'requests',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY request_id),'[]') FROM assembly_preview_requests r),
 'draft',(SELECT to_jsonb(d) FROM assembly_preview_drafts d WHERE id=true)
 )::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot + archiveEventSnapshot(t, f.db) + string(encodeValue(warehouseState(t, f.db)))
}

func disassemblyUndoRejected(t *testing.T, f *disassemblyTestFixture, status int) {
	t.Helper()
	before := disassemblyUndoSnapshot(t, f)
	w := disassemblyTestAction(f.db, f.next, f.version, f.sid, "undo-return", "")
	if w.Code != status {
		t.Fatalf("undo return: want HTTP %d, got %d: %s", status, w.Code, w.Body.String())
	}
	if before != disassemblyUndoSnapshot(t, f) {
		t.Fatal("rejected undo changed stock, ledger, history, reserve, session, version or receipt")
	}
}

func disassemblyUndoHistoryCount(t *testing.T, f *disassemblyTestFixture) int {
	t.Helper()
	var count int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM assembly_problem_events WHERE problem_id=$1::uuid AND kind='disassembly_return_reversal'`, f.pid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestDisassemblyUndoReturnStockProgressHistoryReplayAndRescan(t *testing.T) {
	f := disassemblyUndoFixture(t, 1)
	original := valueMap(valueArray(disassemblyTestSession(f.state)["items"])[0])
	itemID, returnID := textValue(original["id"]), textValue(original["returnId"])
	originalRow := modelRow(t, f.db, "assembly_disassembly_returns", returnID)
	var reserves string
	if err := f.db.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(r) ORDER BY order_id,product_id)::text FROM assembly_order_reservations r`).Scan(&reserves); err != nil {
		t.Fatal(err)
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
	// Clear a previous diagnostic error, without pretending undo is a scan.
	f.action(t, "scan", "WRONG", false)
	lastScan := disassemblyTestSession(f.state)["lastScan"]
	first := f.action(t, "undo-return", "", true)
	if modelReturned(t, f) != 0 {
		t.Fatal("progress not decremented")
	}
	item := modelItem(t, f, itemID)
	if item["returned"] != false || item["returnId"] != nil || item["returnedAt"] != nil {
		t.Fatal("source unit still returned", item)
	}
	session := disassemblyTestSession(f.state)
	if session["pendingItemId"] != nil || session["error"] != false || session["status"] != "active" || session["lastScan"] != lastScan || !strings.Contains(textValue(session["log"]), "Сканируйте товар") {
		t.Fatal("undo did not restore the product step", session)
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 1)
	assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 1)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 0)
	assertStorageCount(t, f.db, "assembly_warehouse_operations", 7)
	if modelRow(t, f.db, "assembly_disassembly_returns", returnID) != originalRow || disassemblyUndoHistoryCount(t, f) != 1 {
		t.Fatal("original return lost or reversal event missing")
	}
	var reversed, request string
	if err := f.db.QueryRow(context.Background(), `SELECT return_id::text,request_id::text FROM assembly_disassembly_return_reversals`).Scan(&reversed, &request); err != nil {
		t.Fatal(err)
	}
	if reversed != returnID || request != warehouseID(f.next-1) {
		t.Fatal("reversal identity mismatch")
	}
	var currentReserves string
	if err := f.db.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(r) ORDER BY order_id,product_id)::text FROM assembly_order_reservations r`).Scan(&currentReserves); err != nil {
		t.Fatal(err)
	}
	if currentReserves != reserves {
		t.Fatal("undo rewrote reserves")
	}
	after := disassemblyUndoSnapshot(t, f)
	replay := disassemblyTestAction(f.db, f.next-1, f.version-1, f.sid, "undo-return", "")
	if !sameWarehouseResponse(first, replay) || after != disassemblyUndoSnapshot(t, f) {
		t.Fatal("replay debited stock or changed its original receipt")
	}
	// A new request cannot undo an already compensated sole return.
	disassemblyUndoRejected(t, f, http.StatusConflict)
	f.action(t, "scan", "A", true)
	if disassemblyTestSession(f.state)["pendingItemId"] != itemID {
		t.Fatal("rescan did not select the same original source item")
	}
	f.action(t, "scan", "CELL:1", true)
	replacement := modelItem(t, f, itemID)
	if replacement["returned"] != true || replacement["returnId"] == returnID || modelReturned(t, f) != 1 {
		t.Fatal("normal return after undo failed")
	}
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 2)
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
}

func TestDisassemblyUndoReturnSelectsLastReturnAndItsExactCell(t *testing.T) {
	f := disassemblyUndoFixture(t, 6)
	var lastID, itemID string
	var lastCell int
	if err := f.db.QueryRow(context.Background(), `SELECT r.id::text,r.item_id::text,r.cell_id FROM assembly_disassembly_effective_returns r JOIN assembly_disassembly_items i ON i.id=r.item_id WHERE i.session_id=$1::uuid ORDER BY r.occurred_at DESC,r.id DESC LIMIT 1`, f.sid).Scan(&lastID, &itemID, &lastCell); err != nil {
		t.Fatal(err)
	}
	if lastCell != 2 {
		t.Fatal("fixture must span both source cells", lastCell)
	}
	var first, second int
	f.db.QueryRow(context.Background(), `SELECT quantity FROM cell_stock WHERE cell_id=1 AND product_id=1`).Scan(&first)
	f.db.QueryRow(context.Background(), `SELECT quantity FROM cell_stock WHERE cell_id=2 AND product_id=1`).Scan(&second)
	f.action(t, "undo-return", "", true)
	var cancelled string
	if err := f.db.QueryRow(context.Background(), `SELECT return_id::text FROM assembly_disassembly_return_reversals`).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled != lastID || modelItem(t, f, itemID)["returned"] != false || modelReturned(t, f) != 5 {
		t.Fatal("undo cancelled a different source unit")
	}
	var a, b int
	f.db.QueryRow(context.Background(), `SELECT quantity FROM cell_stock WHERE cell_id=1 AND product_id=1`).Scan(&a)
	f.db.QueryRow(context.Background(), `SELECT quantity FROM cell_stock WHERE cell_id=2 AND product_id=1`).Scan(&b)
	if a != first || b != second-1 {
		t.Fatal("wrong source cell debited", a, b, first, second)
	}
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 6)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 5)
	// A distinct stale request must not cancel the next older return.
	before := disassemblyUndoSnapshot(t, f)
	stale := disassemblyTestAction(f.db, f.next, f.version-1, f.sid, "undo-return", "")
	if stale.Code != 409 || before != disassemblyUndoSnapshot(t, f) {
		t.Fatal("stale undo altered another return")
	}
}

func TestDisassemblyUndoReturnGuards(t *testing.T) {
	for _, scenario := range []string{"pending", "paused", "completed", "empty", "zero_stock", "missing_stock"} {
		t.Run(scenario, func(t *testing.T) {
			count := 1
			if scenario == "empty" {
				count = 0
			}
			f := disassemblyUndoFixture(t, count)
			switch scenario {
			case "pending":
				f.action(t, "scan", "A", true)
			case "paused":
				f.action(t, "pause", "", true)
			case "completed":
				f.returnAll(t)
				f.action(t, "finish", "", true)
			case "zero_stock":
				stockTestExec(t, f.db, `UPDATE cell_stock SET quantity=0 WHERE cell_id=1 AND product_id=1`)
			case "missing_stock":
				stockTestExec(t, f.db, `DELETE FROM cell_stock WHERE cell_id=1 AND product_id=1`)
			}
			disassemblyUndoRejected(t, f, http.StatusConflict)
			if scenario == "paused" {
				f.action(t, "start", "", true)
				f.action(t, "undo-return", "", true)
				if modelReturned(t, f) != 0 {
					t.Fatal("resume did not allow undo")
				}
			}
		})
	}
}

func TestDisassemblyUndoReturnRequiresMigration019(t *testing.T) {
	f := disassemblyLegacyFixture(t, false)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	f.action(t, "scan", "CELL:1", true)
	before, events := warehouseState(t, f.db), archiveEventSnapshot(t, f.db)
	w := disassemblyTestAction(f.db, f.next, f.version, f.sid, "undo-return", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "019") || !reflect.DeepEqual(before, warehouseState(t, f.db)) || events != archiveEventSnapshot(t, f.db) {
		t.Fatal("legacy schema undo was not rejected safely", w.Body.String())
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
}

func TestDisassemblyUndoReturnAfterFullReturnBlocksFinishAgain(t *testing.T) {
	f := disassemblyUndoFixture(t, 7)
	f.action(t, "undo-return", "", true)
	assertDisassemblyFinishPreconditions(t, f, [4]int{1, 0, 1, 0})
	assertDisassemblyFinishRejectedUnchanged(t, f, "Сначала физически верните все исходные отборы.")
	f.returnAll(t)
	f.action(t, "finish", "", true)
}

func TestDisassemblyUndoReturnConcurrentReplayCreatesOneReversal(t *testing.T) {
	f := disassemblyUndoFixture(t, 1)
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- disassemblyTestAction(f.db, 9500, f.version, f.sid, "undo-return", "")
		}()
	}
	wg.Wait()
	close(results)
	var first *httptest.ResponseRecorder
	for w := range results {
		stockTestApplied(t, w, true)
		if first != nil && !sameWarehouseResponse(first, w) {
			t.Fatal("concurrent replay returned different receipt")
		}
		first = w
	}
	assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 1)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 0)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	if disassemblyUndoHistoryCount(t, f) != 1 || modelReturned(t, f) != 0 {
		t.Fatal("concurrent undo duplicated history or progress")
	}
}

func TestDisassemblyUndoReturnFailuresRollbackStockReversalHistoryAndReceipt(t *testing.T) {
	for _, phase := range []string{"stock", "reversal", "history"} {
		t.Run(phase, func(t *testing.T) {
			f := disassemblyUndoFixture(t, 1)
			stockTestExec(t, f.db, `CREATE FUNCTION reject_disassembly_undo_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated undo return failure'; END $$`)
			switch phase {
			case "stock":
				stockTestExec(t, f.db, `CREATE TRIGGER reject_disassembly_undo BEFORE UPDATE ON cell_stock FOR EACH ROW EXECUTE FUNCTION reject_disassembly_undo_test()`)
			case "reversal":
				stockTestExec(t, f.db, `CREATE TRIGGER reject_disassembly_undo BEFORE INSERT ON assembly_disassembly_return_reversals FOR EACH ROW EXECUTE FUNCTION reject_disassembly_undo_test()`)
			case "history":
				stockTestExec(t, f.db, `CREATE TRIGGER reject_disassembly_undo BEFORE INSERT ON assembly_problem_events FOR EACH ROW WHEN (NEW.kind='disassembly_return_reversal') EXECUTE FUNCTION reject_disassembly_undo_test()`)
			}
			disassemblyUndoRejected(t, f, http.StatusInternalServerError)
			relation := map[string]string{"stock": "cell_stock", "reversal": "assembly_disassembly_return_reversals", "history": "assembly_problem_events"}[phase]
			stockTestExec(t, f.db, `DROP TRIGGER reject_disassembly_undo ON `+relation)
			// Retry the SAME requestId after rollback: no receipt or partial write survived.
			f.action(t, "undo-return", "", true)
			stockTestQuantity(t, f.db, "cell_stock", 1, 3)
			assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 1)
		})
	}
}

func TestDisassemblyUndoReturnHTTPRouteAndLostResponseReplay(t *testing.T) {
	f := disassemblyUndoFixture(t, 1)
	var drop atomic.Bool
	server := cycleServer(t, f.db, &drop)
	client := &http.Client{Timeout: 5 * time.Second}
	body := map[string]any{"requestId": warehouseID(9600), "version": f.version}
	path := "/disassembly/" + f.sid + "/undo-return"
	drop.Store(true)
	if _, _, err := cycleRequest(client, server.URL, path, body); err == nil {
		t.Fatal("fixture did not lose the committed response")
	}
	after := disassemblyUndoSnapshot(t, f)
	status, raw, err := cycleRequest(client, server.URL, path, body)
	if err != nil || status != 200 || after != disassemblyUndoSnapshot(t, f) {
		t.Fatal("HTTP undo replay changed committed warehouse state", status, err, raw)
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 1)
}
