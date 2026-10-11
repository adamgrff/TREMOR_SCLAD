package main

import (
	"context"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/assemblyorders"
	"github.com/jackc/pgx/v5/pgxpool"
)

func mappingTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := warehouseTestDB(t)
	sql, err := os.ReadFile("../../migrations/016_assembly_sku_mappings.sql")
	if err != nil {
		t.Fatal(err)
	}
	stockTestExec(t, db, string(sql))
	stockTestExec(t, db, `INSERT INTO cell_stock VALUES(2,1,5)`)
	return db
}
func mappingAction(db *pgxpool.Pool, n, version int, pid, sku string, product int64, confirmed bool) *httptest.ResponseRecorder {
	body := encodeValue(map[string]any{"requestId": warehouseID(n), "version": version, "positionSku": sku, "productId": product, "confirmed": confirmed})
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	r.SetPathValue("problemID", pid)
	w := httptest.NewRecorder()
	assemblyMappingHandler(db, w, r)
	return w
}
func unknownOrder(number string, quantity int) assemblyorders.Order {
	return assemblyorders.Order{Source: "ozon", Number: number, CreatedAt: "2026-10-12T10:00:00Z", Items: []assemblyorders.Item{{Name: "External alpha", SKU: "EXT-A", OzonSKU: "123", Quantity: quantity}}}
}

func TestMappingShortageSeparateRecheckReturnAndRealPick(t *testing.T) {
	db := mappingTestDB(t)
	order := unknownOrder("MAP-1", 7)
	importWarehouseTest(t, db, 1, order)
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=2 WHERE product_id=1`)
	state := responseTestWorkspace(t, warehouseProblem(db, 2, 0, "queue", order.Number, assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "EXT-A"}))
	pid := problemIDOf(testIssues(state)[0])
	original := contextOf(testIssues(state)[0])
	mapped := mappingAction(db, 3, 1, pid, "EXT-A", 1, true)
	state = warehouseOK(t, mapped)
	if !sameWarehouseResponse(mapped, mappingAction(db, 3, 1, pid, "EXT-A", 1, true)) {
		t.Fatal("mapping replay changed")
	}
	assertStorageCount(t, db, "assembly_sku_mappings", 1)
	assertStorageCount(t, db, "assembly_stock_checks", 1)
	assertStorageCount(t, db, "assembly_order_reservations", 0)
	issues := testIssues(state)
	if len(issues) != 2 || testProblem(issues[0])["resolutionStatus"] != "resolved" || testProblem(issues[1])["type"] != "insufficient_stock" || testProblem(issues[1])["resolutionStatus"] != "awaiting" || state["activeOrder"] != nil {
		t.Fatal("mapping skipped shortage gate", string(encodeValue(state)))
	}
	if !reflect.DeepEqual(original, contextOf(issues[0])) {
		t.Fatal("creation context rewritten")
	}
	if w := stockTestAction(db, warehouseID(4), 2, pid, "return"); w.Code != 409 {
		t.Fatal("unresolved shortage allowed return", w.Body.String())
	}
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5 WHERE product_id=1`)
	shortageID := problemIDOf(issues[1])
	warehouseOK(t, stockTestAction(db, warehouseID(5), 2, shortageID, "check-stock"))
	// Stock changed after resolution: explicit return must recheck again.
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=1 WHERE product_id=1`)
	stockTestApplied(t, stockTestAction(db, warehouseID(6), 3, pid, "return"), false)
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5 WHERE product_id=1`)
	warehouseOK(t, stockTestAction(db, warehouseID(7), 4, shortageID, "check-stock"))
	state = warehouseOK(t, stockTestAction(db, warehouseID(8), 5, pid, "return"))
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 7)
	cell := nextWarehouseCell(valueMap(state["activeOrder"]))
	warehouseOK(t, warehouseAction(db, 9, 6, "scan", "", textValue(cell["scanCode"])))
	state = warehouseOK(t, warehouseAction(db, 10, 7, "scan", "", "A"))
	pick := valueMap(valueArray(valueMap(state["activeState"])["picks"])[0])
	if pick["sku"] != "EXT-A" {
		t.Fatal("external identity lost")
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 1)
	stockTestQuantity(t, db, "cell_stock", 1, 9)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 6)
	// New imported order uses the same durable Ozon identity despite a changed offer label.
	next := unknownOrder("MAP-2", 1)
	next.Items[0].SKU = "NEW-OFFER"
	importWarehouseTest(t, db, 11, next)
	var target int64
	if err := db.QueryRow(context.Background(), `SELECT p.warehouse_product_id FROM assembly_order_positions p JOIN assembly_order_heads h ON h.version_id=p.version_id JOIN assembly_order_records o ON o.id=h.order_id WHERE o.order_number='MAP-2'`).Scan(&target); err != nil || target != 1 {
		t.Fatal("mapping not reused", err, target)
	}
}

func TestMappingAllUnknownAliasesShareStockAndReserve(t *testing.T) {
	db := mappingTestDB(t)
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=4 WHERE product_id=1`)
	order := unknownOrder("ALIASES", 5)
	order.Items = append(order.Items, assemblyorders.Item{Name: "External alias", SKU: "EXT-B", OzonSKU: "456", Quantity: 5})
	importWarehouseTest(t, db, 1, order)
	state := responseTestWorkspace(t, warehouseProblem(db, 2, 0, "queue", order.Number, assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "EXT-A"}))
	pid := problemIDOf(testIssues(state)[0])
	state = warehouseOK(t, mappingAction(db, 3, 1, pid, "EXT-A", 1, true))
	if testProblem(testIssues(state)[0])["resolutionStatus"] != "awaiting" {
		t.Fatal("partial mapping resolved all unknowns")
	}
	state = warehouseOK(t, mappingAction(db, 4, 2, pid, "EXT-B", 1, true))
	if len(testIssues(state)) != 2 {
		t.Fatal("shared stock double counted")
	}
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=6 WHERE product_id=1`)
	warehouseOK(t, stockTestAction(db, warehouseID(5), 3, problemIDOf(testIssues(state)[1]), "check-stock"))
	state = warehouseOK(t, stockTestAction(db, warehouseID(6), 4, pid, "return"))
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 10)
	allocated := map[int64]int{}
	for _, p := range warehousePlans(valueMap(state["activeOrder"])) {
		allocated[int64(intValue(p["cellId"]))] += intValue(p["required"])
	}
	for _, n := range allocated {
		if n > 6 {
			t.Fatal("cell allocated twice")
		}
	}
	// Both aliases can be collected through the actual warehouse barcode; each pick retains its order position.
	version := 5
	n := 7
	for len(valueArray(valueMap(state["activeState"])["picks"])) < 10 {
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
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 0)
	stockTestQuantity(t, db, "cell_stock", 1, 2)
}

func TestMappingOtherProblemsReservationsRejectionsAndAtomicity(t *testing.T) {
	db := mappingTestDB(t)
	// Another real order owns six units; mapping must not borrow them.
	reserved := realTestOrder("RESERVED")
	reserved.Items = reserved.Items[:1]
	reserved.Items[0].Quantity = 6
	importWarehouseTest(t, db, 1, reserved)
	warehouseOK(t, warehouseAction(db, 2, 0, "start", reserved.Number, ""))
	responseTestWorkspace(t, warehouseProblem(db, 3, 1, "assembly", reserved.Number, assemblyProblemDetails{Type: "other", Comment: "Pause for test"}))
	order := unknownOrder("BLOCKED", 5)
	importWarehouseTest(t, db, 4, order)
	state := responseTestWorkspace(t, warehouseProblem(db, 5, 2, "queue", order.Number, assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "EXT-A"}))
	pid := problemIDOf(testIssues(state)[1])
	// Seed a second cause through the trusted domain builder; public queue creation is intentionally single-use.
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SELECT set_config('assembly.server_action','true',true)`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildAssemblyProblem(encodeValue(state), assemblyProblemCreationRequest{Source: "queue", OrderNumber: order.Number, Order: encodeValue(valueMap(testIssues(state)[1])["order"]), AllowExistingQueue: true, Details: assemblyProblemDetails{Type: "other", Comment: "Separate unresolved reason"}}, "2026-10-12T11:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `UPDATE assembly_preview_drafts SET state=assembly_store_workspace($1::jsonb),version=version+1 WHERE id=true`, []byte(second))
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	state = warehouseState(t, db)
	for _, r := range []*httptest.ResponseRecorder{mappingAction(db, 7, 4, pid, "EXT-A", 1, false), mappingAction(db, 8, 4, pid, "NOT-AN-ITEM", 1, true), mappingAction(db, 9, 4, pid, "EXT-A", 999, true)} {
		if r.Code != 400 && r.Code != 409 {
			t.Fatal("invalid mapping accepted", r.Body.String())
		}
	}
	stockTestExec(t, db, `UPDATE products SET archived=true WHERE id=1`)
	if r := mappingAction(db, 10, 4, pid, "EXT-A", 1, true); r.Code != 409 {
		t.Fatal("archived mapping accepted")
	}
	stockTestExec(t, db, `UPDATE products SET archived=false WHERE id=1`)
	// Fail after INSERT to prove mapping, check, status and history roll back together.
	stockTestExec(t, db, `CREATE FUNCTION reject_mapping_check() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated failure'; END $$`)
	stockTestExec(t, db, `CREATE TRIGGER reject_mapping_check BEFORE INSERT ON assembly_stock_checks FOR EACH ROW EXECUTE FUNCTION reject_mapping_check()`)
	before := warehouseState(t, db)
	if r := mappingAction(db, 11, 4, pid, "EXT-A", 1, true); r.Code != 500 {
		t.Fatal("failure not injected", r.Body.String())
	}
	assertStorageCount(t, db, "assembly_sku_mappings", 0)
	if !reflect.DeepEqual(before, warehouseState(t, db)) {
		t.Fatal("partial mapping state survived rollback")
	}
	stockTestExec(t, db, `DROP TRIGGER reject_mapping_check ON assembly_stock_checks`)
	state = warehouseOK(t, mappingAction(db, 11, 4, pid, "EXT-A", 1, true))
	issues := testIssues(state)
	if len(issues) != 4 || testProblem(issues[2])["resolutionStatus"] != "awaiting" || testProblem(issues[3])["type"] != "insufficient_stock" {
		t.Fatal("mapping bypassed foreign reserve or other problem")
	}
	if r := stockTestAction(db, warehouseID(12), 5, pid, "return"); r.Code != 409 {
		t.Fatal("other problem allowed return")
	}
	// Stale-tab action writes nothing; a mapping cannot be reassigned in another order.
	if r := mappingAction(db, 13, 4, pid, "EXT-A", 1, true); r.Code != 409 {
		t.Fatal("stale version accepted")
	}
	another := unknownOrder("CONFLICT", 1)
	importWarehouseTest(t, db, 14, another)
	state = responseTestWorkspace(t, warehouseProblem(db, 15, 5, "queue", another.Number, assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "EXT-A"}))
	pid2 := problemIDOf(testIssues(state)[4])
	if r := mappingAction(db, 16, 6, pid2, "EXT-A", 2, true); r.Code != 409 {
		t.Fatal("existing mapping retargeted", r.Body.String())
	}
	assertStorageCount(t, db, "assembly_sku_mappings", 1)
}

func TestMappingPreservesPartialPicksAndAnotherActiveOrder(t *testing.T) {
	db := mappingTestDB(t)
	order := realTestOrder("PARTIAL")
	importWarehouseTest(t, db, 1, order)
	state := warehouseOK(t, warehouseAction(db, 2, 0, "start", order.Number, ""))
	cell := nextWarehouseCell(valueMap(state["activeOrder"]))
	warehouseOK(t, warehouseAction(db, 3, 1, "scan", "", textValue(cell["scanCode"])))
	state = warehouseOK(t, warehouseAction(db, 4, 2, "scan", "", "A"))
	originalPicks := valueArray(valueMap(state["activeState"])["picks"])
	state = responseTestWorkspace(t, warehouseProblem(db, 5, 3, "assembly", order.Number, assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "B"}))
	pid := problemIDOf(testIssues(state)[0])
	originalContext := contextOf(testIssues(state)[0])
	another := realTestOrder("ANOTHER")
	another.Items = another.Items[:1]
	importWarehouseTest(t, db, 6, another)
	state = warehouseOK(t, warehouseAction(db, 7, 4, "start", another.Number, ""))
	originalActive := state["activeOrder"]
	originalState := state["activeState"]
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=0 WHERE product_id=2`)
	state = warehouseOK(t, mappingAction(db, 8, 5, pid, "B", 2, true))
	if !reflect.DeepEqual(state["activeOrder"], originalActive) || !reflect.DeepEqual(state["activeState"], originalState) {
		t.Fatal("mapping stopped another assembly")
	}
	issues := testIssues(state)
	if len(issues) != 2 {
		t.Fatal("missing independent shortage")
	}
	for _, issue := range issues {
		if !reflect.DeepEqual(valueArray(valueMap(valueMap(issue)["assembly"])["picks"]), originalPicks) {
			t.Fatal("partial picks lost")
		}
	}
	if !reflect.DeepEqual(originalContext, contextOf(issues[0])) {
		t.Fatal("original context changed")
	}
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 3)
	assertStorageCount(t, db, "assembly_warehouse_operations", 1)
}

func TestMappingArchivedAfterResolutionAndCatalog(t *testing.T) {
	db := mappingTestDB(t)
	order := unknownOrder("ARCHIVED", 1)
	importWarehouseTest(t, db, 1, order)
	state := responseTestWorkspace(t, warehouseProblem(db, 2, 0, "queue", order.Number, assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "EXT-A"}))
	pid := problemIDOf(testIssues(state)[0])
	state = warehouseOK(t, mappingAction(db, 3, 1, pid, "EXT-A", 1, true))
	if len(testIssues(state)) != 1 || state["activeOrder"] != nil {
		t.Fatal("mapping implicitly started order")
	}
	stockTestExec(t, db, `UPDATE products SET archived=true WHERE id=1`)
	stockTestApplied(t, stockTestAction(db, warehouseID(4), 2, pid, "return"), false)
	assertStorageCount(t, db, "assembly_order_reservations", 0)
	w := httptest.NewRecorder()
	assemblyMappingProductsHandler(db, w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), `"sku":"A"`) {
		t.Fatal("archived product listed", w.Body.String())
	}
}
