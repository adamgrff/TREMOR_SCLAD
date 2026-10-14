package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type disassemblyTestFixture struct {
	db            *pgxpool.Pool
	state         map[string]any
	pid, sid      string
	version, next int
}

func disassemblyTestSession(state map[string]any) map[string]any {
	sessions := valueArray(state["disassemblySessions"])
	if len(sessions) == 0 {
		return nil
	}
	return valueMap(sessions[0])
}
func disassemblyTestAction(db *pgxpool.Pool, n, version int, sid, action, code string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(encodeValue(map[string]any{"requestId": warehouseID(n), "version": version, "code": code}))))
	r.SetPathValue("sessionID", sid)
	w := httptest.NewRecorder()
	disassemblyHandler(db, w, r, "disassembly-"+action)
	return w
}
func (f *disassemblyTestFixture) action(t *testing.T, action, code string, want bool) *httptest.ResponseRecorder {
	t.Helper()
	w := disassemblyTestAction(f.db, f.next, f.version, f.sid, action, code)
	f.next++
	stockTestApplied(t, w, want)
	f.state = responseTestWorkspace(t, w)
	f.version++
	return w
}
func (f *disassemblyTestFixture) transfer(t *testing.T) {
	t.Helper()
	w := stockTestAction(f.db, warehouseID(f.next), f.version, f.pid, "transfer-disassembly")
	f.next++
	f.state = warehouseOK(t, w)
	f.version++
	f.sid = textValue(disassemblyTestSession(f.state)["id"])
}

func disassemblyFixture(t *testing.T, duplicate bool) *disassemblyTestFixture {
	t.Helper()
	db := ozonTestDB(t)
	migration, err := os.ReadFile("../../migrations/018_assembly_disassembly.sql")
	if err != nil {
		t.Fatal(err)
	}
	stockTestExec(t, db, string(migration))
	if duplicate {
		stockTestExec(t, db, `UPDATE cells SET code='A1' WHERE id=2`)
	}
	order := realTestOrder("DISASSEMBLE")
	order.Items = order.Items[:1]
	order.Items[0].Quantity = 7
	importWarehouseTest(t, db, 1, order)
	state := warehouseOK(t, warehouseAction(db, 2, 0, "start", order.Number, ""))
	n, version := 3, 1
	for len(valueArray(valueMap(state["activeState"])["picks"])) < 7 {
		if valueMap(state["activeState"])["confirmedCell"] == nil {
			cell := nextWarehouseCell(valueMap(state["activeOrder"]))
			state = warehouseOK(t, warehouseAction(db, n, version, "scan", "", textValue(cell["scanCode"])))
			n++
			version++
		}
		state = warehouseOK(t, warehouseAction(db, n, version, "scan", "", "A"))
		n++
		version++
	}
	id, _ := seedOzonRefresh(t, db, order.Number, true)
	version++
	r := receiptFor(t, db, id, "cancel-for-disassembly", "cancelled", 1)
	if err = ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
		t.Fatal(err)
	}
	version++
	state = warehouseState(t, db)
	pid := problemIDOf(testIssues(state)[1])
	return &disassemblyTestFixture{db: db, state: state, pid: pid, version: version, next: 100}
}

func TestDisassemblyTransferStartNeverCreditAndReturnToOriginalCells(t *testing.T) {
	f := disassemblyFixture(t, true)
	original := contextOf(testIssues(f.state)[1])
	picks := valueMap(testIssues(f.state)[1])["assembly"]
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	f.transfer(t)
	assertStorageCount(t, f.db, "assembly_disassembly_sessions", 1)
	assertStorageCount(t, f.db, "assembly_disassembly_items", 7)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 0)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	// Different button requests must reuse the same transfer, not create more return units.
	f.transfer(t)
	assertStorageCount(t, f.db, "assembly_disassembly_sessions", 1)
	assertStorageCount(t, f.db, "assembly_disassembly_items", 7)
	if w := stockTestAction(f.db, warehouseID(200), f.version, f.pid, "return"); w.Code != 409 {
		t.Fatal("transferred order resumed assembly")
	}
	f.action(t, "start", "", true)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	if w := disassemblyTestAction(f.db, 201, f.version, f.sid, "finish", ""); w.Code != 409 {
		t.Fatal("incomplete disassembly finished")
	}
	f.action(t, "scan", "WRONG", false)
	f.action(t, "scan", "A", true)
	session := disassemblyTestSession(f.state)
	pending := textValue(session["pendingItemId"])
	if pending == "" {
		t.Fatal("product confirmation lost")
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	reloaded := warehouseState(t, f.db)
	if textValue(disassemblyTestSession(reloaded)["pendingItemId"]) != pending {
		t.Fatal("reload lost pending source unit")
	}
	f.action(t, "pause", "", true)
	f.action(t, "start", "", true)
	if textValue(disassemblyTestSession(f.state)["pendingItemId"]) != pending {
		t.Fatal("pause/resume lost pending item")
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	f.action(t, "scan", "A1", false)
	if textValue(disassemblyTestSession(f.state)["pendingItemId"]) != pending {
		t.Fatal("wrong cell cleared confirmed item")
	}
	f.action(t, "scan", "CELL:2", false)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	first := f.action(t, "scan", "CELL:1", true)
	replay := disassemblyTestAction(f.db, f.next-1, f.version-1, f.sid, "scan", "CELL:1")
	if !sameWarehouseResponse(first, replay) {
		t.Fatal("lost response replay changed")
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 1)
	// A new request with the same cell code cannot credit another unit without a new product scan.
	f.action(t, "scan", "CELL:1", false)
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
	for returned := 1; returned < 7; returned++ {
		f.action(t, "scan", "A", true)
		s := disassemblyTestSession(f.state)
		code := ""
		for _, v := range valueArray(s["items"]) {
			item := valueMap(v)
			if item["id"] == s["pendingItemId"] {
				code = fmt.Sprintf("CELL:%d", intValue(item["cellId"]))
				break
			}
		}
		f.action(t, "scan", code, true)
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 10)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 7)
	assertStorageCount(t, f.db, "assembly_warehouse_operations", 7)
	var a, b int
	f.db.QueryRow(context.Background(), `SELECT quantity FROM cell_stock WHERE cell_id=1 AND product_id=1`).Scan(&a)
	f.db.QueryRow(context.Background(), `SELECT quantity FROM cell_stock WHERE cell_id=2 AND product_id=1`).Scan(&b)
	if a != 5 || b != 5 {
		t.Fatal("goods returned to wrong source", a, b)
	}
	f.action(t, "scan", "A", false)
	f.action(t, "finish", "", true)
	if disassemblyTestSession(f.state)["status"] != "completed" || testProblem(testIssues(f.state)[1])["closedOutcome"] != "disassembled" {
		t.Fatal("terminal disassembly missing")
	}
	if !reflect.DeepEqual(original, contextOf(testIssues(f.state)[1])) || !reflect.DeepEqual(picks, valueMap(testIssues(f.state)[1])["assembly"]) {
		t.Fatal("original snapshot/picks rewritten")
	}
	stockTestQuantity(t, f.db, "assembly_order_reservations", 1, 0)
	var status string
	f.db.QueryRow(context.Background(), `SELECT status FROM assembly_order_records WHERE order_number='DISASSEMBLE'`).Scan(&status)
	if status != "disassembled" {
		t.Fatal("order state wrong", status)
	}
	if w := disassemblyTestAction(f.db, 202, f.version, f.sid, "start", ""); w.Code != 409 {
		t.Fatal("completed disassembly restarted")
	}
}

func TestDisassemblyStockFailureRollsBackPendingAndHistory(t *testing.T) {
	f := disassemblyFixture(t, false)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	before := warehouseState(t, f.db)
	stockTestExec(t, f.db, `CREATE FUNCTION reject_return_credit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated stock write failure'; END $$`)
	stockTestExec(t, f.db, `CREATE TRIGGER reject_return_credit BEFORE INSERT OR UPDATE ON cell_stock FOR EACH ROW EXECUTE FUNCTION reject_return_credit()`)
	w := disassemblyTestAction(f.db, f.next, f.version, f.sid, "scan", "CELL:1")
	if w.Code != 500 {
		t.Fatal("stock failure not reported", w.Body.String())
	}
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 0)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	if !reflect.DeepEqual(before, warehouseState(t, f.db)) {
		t.Fatal("partial return state/history survived")
	}
	stockTestExec(t, f.db, `DROP TRIGGER reject_return_credit ON cell_stock`)
	f.action(t, "scan", "CELL:1", true)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 1)
	if w := disassemblyTestAction(f.db, 300, f.version-1, f.sid, "scan", "A"); w.Code != 409 {
		t.Fatal("stale tab accepted")
	}
}

func TestDisassemblyDamagedGoodsNeverBecomeAvailable(t *testing.T) {
	f := disassemblyFixture(t, false)
	// Add a trusted damage cause using the existing immutable-context builder.
	ctx := context.Background()
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	original := valueMap(testIssues(f.state)[1])
	order := valueMap(original["order"])
	saved := valueMap(original["assembly"])
	active := map[string]any{"picks": saved["picks"], "confirmedCell": saved["cell"], "shipmentConfirmed": saved["shipmentConfirmed"]}
	base, _ := decodeWorkspace(encodeValue(f.state))
	base["activeOrder"] = order
	base["activeState"] = active
	raw, e := buildAssemblyProblem(encodeValue(base), assemblyProblemCreationRequest{Source: "assembly", OrderNumber: textValue(order["number"]), Details: assemblyProblemDetails{Type: "damaged_product", PositionSKU: "A"}}, "2026-10-14T10:00:00Z")
	if e != nil {
		t.Fatal(e)
	}
	state, _ := decodeWorkspace(raw)
	if _, err = tx.Exec(ctx, `SELECT set_config('assembly.server_action','true',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE assembly_preview_drafts SET state=assembly_store_workspace($1::jsonb),version=version+1 WHERE id=true`, []byte(encodeValue(state))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	f.version++
	f.state = warehouseState(t, f.db)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", false)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 0)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
}

func TestDisassemblyArchivedAndReclassifiedCellAreBlocked(t *testing.T) {
	f := disassemblyFixture(t, false)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	stockTestExec(t, f.db, `UPDATE products SET archived=true WHERE id=1`)
	f.action(t, "scan", "CELL:1", false)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 0)
	stockTestExec(t, f.db, `UPDATE products SET archived=false WHERE id=1`)
	stockTestExec(t, f.db, `ALTER TABLE products ADD COLUMN category_id INTEGER DEFAULT 1`)
	stockTestExec(t, f.db, `ALTER TABLE cells ADD COLUMN category_id INTEGER DEFAULT 1`)
	stockTestExec(t, f.db, `UPDATE cells SET category_id=2 WHERE id=1`)
	f.action(t, "scan", "CELL:1", false)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	if disassemblyTestSession(f.state)["pendingItemId"] == nil {
		t.Fatal("blocked return lost source")
	}
	stockTestExec(t, f.db, `UPDATE cells SET category_id=1 WHERE id=1`)
	f.action(t, "scan", "CELL:1", true)
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
	if _, err := f.db.Exec(context.Background(), `INSERT INTO assembly_warehouse_operations(request_id,order_id,order_position_id,product_id,cell_id,kind,quantity,reverses_operation_id) SELECT $1::uuid,op.order_id,op.order_position_id,op.product_id,op.cell_id,'undo_pick',1,op.id FROM assembly_disassembly_returns r JOIN assembly_disassembly_items i ON i.id=r.item_id JOIN assembly_warehouse_operations op ON op.id=i.original_operation_id LIMIT 1`, warehouseID(999)); err == nil {
		t.Fatal("returned item could be undone twice")
	}
}

func TestDisassemblyTransferWithoutPicksOrVerifiedCancellationIsRejected(t *testing.T) {
	db := ozonTestDB(t)
	migration, err := os.ReadFile("../../migrations/018_assembly_disassembly.sql")
	if err != nil {
		t.Fatal(err)
	}
	stockTestExec(t, db, string(migration))
	order := realTestOrder("NO-PICKS")
	importWarehouseTest(t, db, 1, order)
	id, _ := seedOzonRefresh(t, db, order.Number, false)
	if err = ingestOzonDomainReceipt(context.Background(), db, receiptFor(t, db, id, "no-pick-cancel", "cancelled", 1)); err != nil {
		t.Fatal(err)
	}
	state := warehouseState(t, db)
	pid := problemIDOf(testIssues(state)[1])
	if w := stockTestAction(db, warehouseID(500), 2, pid, "transfer-disassembly"); w.Code != 409 {
		t.Fatal("empty transfer created")
	}
	assertStorageCount(t, db, "assembly_disassembly_sessions", 0)
	stockTestQuantity(t, db, "cell_stock", 1, 10)
}

func TestDisassemblyConcurrentRepeatedCellScanCreditsOneUnit(t *testing.T) {
	f := disassemblyFixture(t, false)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- disassemblyTestAction(f.db, 800, f.version, f.sid, "scan", "CELL:1")
		}()
	}
	wg.Wait()
	close(results)
	var first *httptest.ResponseRecorder
	for w := range results {
		stockTestApplied(t, w, true)
		if first != nil && !sameWarehouseResponse(first, w) {
			t.Fatal("concurrent replay changed result")
		}
		first = w
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 1)
	state := warehouseState(t, f.db)
	id := textValue(testProblem(testIssues(state)[1])["ozonOperationId"])
	if err := ingestOzonDomainReceipt(context.Background(), f.db, receiptFor(t, f.db, id, "cancel-after-partial-return", "cancelled", 2)); err != nil {
		t.Fatal(err)
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
	assertStorageCount(t, f.db, "assembly_disassembly_sessions", 1)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 1)
}
