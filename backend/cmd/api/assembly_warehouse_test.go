package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/assemblyorders"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func warehouseTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := stockTestDB(t, "")
	data, err := os.ReadFile("../../migrations/015_assembly_warehouse_operations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(context.Background(), string(data)); err != nil {
		t.Fatal(err)
	}
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5; INSERT INTO cell_stock VALUES(1,2,3)`)
	return db
}
func realTestOrder(number string) assemblyorders.Order {
	return assemblyorders.Order{Source: "ozon", Number: number, CreatedAt: "2026-10-11T10:00:00Z", Items: []assemblyorders.Item{{Name: "Alpha", SKU: "A", Quantity: 2}, {Name: "Beta", SKU: "B", Quantity: 1}}}
}
func warehouseID(n int) string { return fmt.Sprintf("50000000-0000-4000-8000-%012d", n) }
func sameWarehouseResponse(a, b *httptest.ResponseRecorder) bool {
	var left, right any
	json.Unmarshal(a.Body.Bytes(), &left)
	json.Unmarshal(b.Body.Bytes(), &right)
	return a.Code == b.Code && string(encodeValue(left)) == string(encodeValue(right))
}
func importWarehouseTest(t *testing.T, db *pgxpool.Pool, n int, order assemblyorders.Order) {
	t.Helper()
	if err := assemblyorders.ImportToDB(context.Background(), db, assemblyorders.Import{RequestID: warehouseID(n), Order: order}); err != nil {
		t.Fatal(err)
	}
}
func warehouseAction(db *pgxpool.Pool, n, version int, action, number, code string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"requestId": warehouseID(n), "version": version, "orderNumber": number, "code": code})
	w := httptest.NewRecorder()
	warehouseAssemblyHandler(db, w, httptest.NewRequest("POST", "/", strings.NewReader(string(body))), action)
	return w
}
func warehouseOK(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	stockTestApplied(t, w, true)
	return responseTestWorkspace(t, w)
}
func warehouseQueue(t *testing.T, db *pgxpool.Pool) []any {
	t.Helper()
	w := httptest.NewRecorder()
	assemblyOrdersHandler(db, w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var response map[string]any
	json.Unmarshal(w.Body.Bytes(), &response)
	return valueArray(response["orders"])
}
func warehouseState(t *testing.T, db *pgxpool.Pool) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	getAssemblyDraftHandler(db, w, httptest.NewRequest("GET", "/", nil))
	return responseTestWorkspace(t, w)
}
func warehouseProblem(db *pgxpool.Pool, n, version int, source, number string, details assemblyProblemDetails) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"requestId": warehouseID(n), "version": version, "source": source, "orderNumber": number, "details": details})
	w := httptest.NewRecorder()
	createAssemblyProblemHandler(db, w, httptest.NewRequest("POST", "/", strings.NewReader(string(body))))
	return w
}

func TestAssemblyWarehousePickUndoProblemReturn(t *testing.T) {
	db := warehouseTestDB(t)
	order := realTestOrder("REAL-1")
	importWarehouseTest(t, db, 1, order)
	if len(warehouseQueue(t, db)) != 1 {
		t.Fatal("real DB order missing")
	}
	state := warehouseOK(t, warehouseAction(db, 2, 0, "start", order.Number, ""))
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 2)
	stockTestQuantity(t, db, "assembly_preview_stock_holds", 1, 0)
	active := valueMap(state["activeOrder"])
	expected := nextWarehouseCell(active)
	cellCode := textValue(expected["scanCode"])
	state = warehouseOK(t, warehouseAction(db, 3, 1, "scan", "", cellCode))
	// This cell contains B and A. B may be scanned before A.
	first := warehouseAction(db, 4, 2, "scan", "", "B")
	state = warehouseOK(t, first)
	stockTestQuantity(t, db, "cell_stock", 2, 7)
	replay := warehouseAction(db, 4, 2, "scan", "", "B")
	if !sameWarehouseResponse(replay, first) {
		t.Fatal("retry changed pick response")
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 1)
	picks := valueArray(valueMap(state["activeState"])["picks"])
	if len(picks) != 1 || intValue(valueMap(picks[0])["cellId"]) != 1 || textValue(valueMap(picks[0])["operationId"]) == "" {
		t.Fatal("real pick identity absent")
	}
	loaded := warehouseState(t, db)
	if string(encodeValue(loaded)) != string(encodeValue(state)) {
		t.Fatal("reload changed scan state")
	}
	// Shipment and cell scans are not warehouse operations and cannot be undone.
	state = warehouseOK(t, warehouseAction(db, 5, 3, "undo", "", ""))
	stockTestQuantity(t, db, "cell_stock", 2, 8)
	assertStorageCount(t, db, "assembly_warehouse_operations", 2)
	state = warehouseOK(t, warehouseAction(db, 6, 4, "scan", "", "B"))
	state = warehouseOK(t, warehouseAction(db, 7, 5, "scan", "", "A"))
	stockTestQuantity(t, db, "cell_stock", 1, 4)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 1)
	state = responseTestWorkspace(t, warehouseProblem(db, 8, 6, "assembly", order.Number, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"}))
	issue := testIssues(state)[0]
	ctx := contextOf(issue)
	cp := valueArray(ctx["picks"])
	if len(cp) != 2 || textValue(valueMap(cp[0])["warehouseOperationId"]) == "" || intValue(valueMap(cp[0])["warehouseCellId"]) != 1 || textValue(valueMap(cp[0])["occurredAt"]) == "" {
		t.Fatal("problem lost warehouse origin")
	}
	if intValue(ctx["confirmedCellId"]) != 1 {
		t.Fatal("confirmed cell ID lost")
	}
	if len(valueArray(ctx["warehouseOperations"])) != 4 {
		t.Fatal("immutable context lost an undone operation")
	}
	history := valueArray(testProblem(issue)["history"])
	if valueMap(history[1])["kind"] != "undo_pick" || valueMap(history[len(history)-2])["kind"] != "creation" {
		t.Fatal("problem history lost chronological warehouse actions")
	}
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 1)
	pid := problemIDOf(issue)
	state = warehouseOK(t, stockTestAction(db, warehouseID(9), 7, pid, "check-stock"))
	state = warehouseOK(t, stockTestAction(db, warehouseID(10), 8, pid, "return"))
	if len(valueArray(valueMap(state["activeState"])["picks"])) != 2 || valueMap(state["activeState"])["confirmedCell"] != nil {
		t.Fatal("return did not restore picks/reset cell")
	}
	stockTestQuantity(t, db, "cell_stock", 1, 4)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 1)
	stockTestQuantity(t, db, "assembly_preview_stock_holds", 1, 0)
	state = warehouseOK(t, warehouseAction(db, 11, 9, "scan", "", "CELL:1"))
	state = warehouseOK(t, warehouseAction(db, 12, 10, "scan", "", "A"))
	wrong := warehouseAction(db, 13, 11, "scan", "", "WRONG")
	stockTestApplied(t, wrong, false)
	state = warehouseOK(t, warehouseAction(db, 14, 12, "scan", "", order.Number))
	// Undo after shipment confirmation rolls back the last product, never the shipment scan.
	state = warehouseOK(t, warehouseAction(db, 15, 13, "undo", "", ""))
	if valueMap(state["activeState"])["shipmentConfirmed"] != false {
		t.Fatal("undo retained shipment confirmation")
	}
	state = warehouseOK(t, warehouseAction(db, 16, 14, "scan", "", "A"))
	state = warehouseOK(t, warehouseAction(db, 17, 15, "scan", "", order.Number))
	finished := warehouseAction(db, 18, 16, "finish", "", "")
	stockTestApplied(t, finished, false)
	state = responseTestWorkspace(t, finished)
	if state["activeOrder"] == nil || len(valueArray(state["completedOrders"])) != 0 || valueMap(state["activeState"])["shipmentConfirmed"] != true || len(warehouseQueue(t, db)) != 0 {
		t.Fatal("order completed without verified Ozon confirmation")
	}
	stockTestQuantity(t, db, "cell_stock", 1, 3)
	stockTestQuantity(t, db, "cell_stock", 2, 7)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 0)
	if err := assemblyorders.ImportToDB(context.Background(), db, assemblyorders.Import{RequestID: warehouseID(19), Order: order}); err == nil {
		t.Fatal("assembled order overwritten while awaiting external confirmation")
	}
}
func TestAssemblyWarehouseDuplicateCodesAndForgery(t *testing.T) {
	db := warehouseTestDB(t)
	stockTestExec(t, db, `INSERT INTO cells VALUES(3,2,'A1'); INSERT INTO cell_stock VALUES(3,1,1)`)
	order := realTestOrder("DUPLICATE")
	order.Items = order.Items[:1]
	importWarehouseTest(t, db, 1, order)
	state := warehouseOK(t, warehouseAction(db, 2, 0, "start", order.Number, ""))
	stockTestApplied(t, warehouseAction(db, 3, 1, "scan", "", "A1"), false)
	state = warehouseOK(t, warehouseAction(db, 4, 2, "scan", "", "CELL:3"))
	state = warehouseOK(t, warehouseAction(db, 5, 3, "scan", "", "A"))
	var q int
	db.QueryRow(context.Background(), `SELECT quantity FROM cell_stock WHERE cell_id=3 AND product_id=1`).Scan(&q)
	if q != 0 {
		t.Fatal("wrong duplicate cell debited")
	}
	db.QueryRow(context.Background(), `SELECT quantity FROM cell_stock WHERE cell_id=1 AND product_id=1`).Scan(&q)
	if q != 5 {
		t.Fatal("other same-code cell was touched")
	}
	state = warehouseOK(t, warehouseAction(db, 6, 4, "scan", "", "CELL:1"))
	state = warehouseOK(t, warehouseAction(db, 7, 5, "scan", "", "A"))
	if len(valueArray(valueMap(state["activeState"])["picks"])) != 2 {
		t.Fatal("duplicate-code picks conflated")
	}
	valueMap(state["activeState"])["picks"] = []any{}
	forged := storeTestWorkspace(db, warehouseID(8), 6, state)
	if forged.Code != 409 {
		t.Fatal("client snapshot bypassed ledger", forged.Body.String())
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 2)
}
func TestAssemblyWarehouseFailureAtomicAndConcurrentRetry(t *testing.T) {
	db := warehouseTestDB(t)
	order := realTestOrder("ATOMIC")
	importWarehouseTest(t, db, 1, order)
	warehouseOK(t, warehouseAction(db, 2, 0, "start", order.Number, ""))
	warehouseOK(t, warehouseAction(db, 3, 1, "scan", "", "CELL:1"))
	// Fail after balance/reserve updates: no partial inventory mutation may survive.
	stockTestExec(t, db, `CREATE FUNCTION reject_warehouse_pick() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test operation failure'; END $$; CREATE TRIGGER reject_pick BEFORE INSERT ON assembly_warehouse_operations FOR EACH ROW EXECUTE FUNCTION reject_warehouse_pick()`)
	failure := warehouseAction(db, 4, 2, "scan", "", "A")
	if failure.Code != 500 {
		t.Fatal("expected DB failure", failure.Body.String())
	}
	stockTestQuantity(t, db, "cell_stock", 1, 5)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 2)
	assertStorageCount(t, db, "assembly_warehouse_operations", 0)
	stockTestExec(t, db, `DROP TRIGGER reject_pick ON assembly_warehouse_operations`)
	var wg sync.WaitGroup
	responses := make([]*httptest.ResponseRecorder, 2)
	for i := range responses {
		wg.Add(1)
		go func(i int) { defer wg.Done(); responses[i] = warehouseAction(db, 4, 2, "scan", "", "A") }(i)
	}
	wg.Wait()
	warehouseOK(t, responses[0])
	warehouseOK(t, responses[1])
	if !sameWarehouseResponse(responses[0], responses[1]) {
		t.Fatal("concurrent retry differed")
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 1)
	stockTestQuantity(t, db, "cell_stock", 1, 4)
	stale := warehouseAction(db, 5, 2, "scan", "", "A")
	if stale.Code != 409 {
		t.Fatal("stale tab changed stock")
	}
}
func TestAssemblyWarehouseQueueProblemAndImport(t *testing.T) {
	db := warehouseTestDB(t)
	order := realTestOrder("QUEUE")
	importWarehouseTest(t, db, 1, order)
	importWarehouseTest(t, db, 1, order)
	assertStorageCount(t, db, "assembly_order_imports", 1)
	changed := order
	changed.Items = append([]assemblyorders.Item{}, order.Items...)
	changed.Items[0].Quantity = 3
	if err := assemblyorders.ImportToDB(context.Background(), db, assemblyorders.Import{RequestID: warehouseID(1), Order: changed}); err == nil {
		t.Fatal("request identity allowed new payload")
	}
	importWarehouseTest(t, db, 2, changed)
	queue := warehouseQueue(t, db)
	if intValue(valueMap(valueArray(valueMap(queue[0])["items"])[0])["quantity"]) != 3 {
		t.Fatal("queued composition not updated")
	}
	// Queue creation contains no client order, stock quantities or picks.
	state := responseTestWorkspace(t, warehouseProblem(db, 3, 0, "queue", order.Number, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"}))
	issue := testIssues(state)[0]
	if valueMap(issue)["order"] == nil || len(valueArray(contextOf(issue)["affectedPositions"])) == 0 {
		t.Fatal("DB context missing")
	}
	if len(warehouseQueue(t, db)) != 0 {
		t.Fatal("problem order remained in queue")
	}
	if err := assemblyorders.ImportToDB(context.Background(), db, assemblyorders.Import{RequestID: warehouseID(4), Order: order}); err == nil {
		t.Fatal("problem composition overwritten")
	}
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 0)
}
func TestAssemblyWarehouseRealReserveAndUnknownSKU(t *testing.T) {
	db := warehouseTestDB(t)
	order := realTestOrder("FIRST-REAL")
	order.Items = order.Items[:1]
	order.Items[0].Quantity = 5
	importWarehouseTest(t, db, 1, order)
	second := realTestOrder("SECOND-REAL")
	second.Items = second.Items[:1]
	second.Items[0].Quantity = 1
	importWarehouseTest(t, db, 2, second)
	warehouseOK(t, warehouseAction(db, 3, 0, "start", order.Number, ""))
	warehouseOK(t, warehouseAction(db, 4, 1, "scan", "", "CELL:1"))
	warehouseOK(t, warehouseAction(db, 5, 2, "scan", "", "A"))
	queue := warehouseQueue(t, db)
	if len(queue) != 1 || intValue(valueMap(valueArray(valueMap(queue[0])["items"])[0])["stock"]) != 0 {
		t.Fatal("picked unit or remaining reserve became available to another order")
	}
	state := responseTestWorkspace(t, warehouseProblem(db, 6, 3, "assembly", order.Number, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"}))
	pid := problemIDOf(testIssues(state)[0])
	checked := stockTestAction(db, warehouseID(7), 4, pid, "check-stock")
	warehouseOK(t, checked)
	var raw json.RawMessage
	if err := db.QueryRow(context.Background(), `SELECT result FROM assembly_stock_checks WHERE request_id=$1::uuid`, warehouseID(7)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var result assemblyStockResult
	json.Unmarshal(raw, &result)
	if result.Positions[0].Available != 4 || result.Positions[0].Required != 4 || result.Positions[0].OwnReserved != 4 || result.Positions[0].PreviewHeld != 0 {
		t.Fatal("real pick double-counted", string(raw))
	}
	unknown := realTestOrder("UNKNOWN")
	unknown.Items = unknown.Items[:1]
	unknown.Items[0].SKU = "UNMAPPED"
	importWarehouseTest(t, db, 8, unknown)
	stockTestApplied(t, warehouseAction(db, 9, 5, "start", unknown.Number, ""), false)
	assertStorageCount(t, db, "assembly_warehouse_operations", 1)
}
