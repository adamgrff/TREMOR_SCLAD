package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func stockTestDB(t *testing.T, legacy string) *pgxpool.Pool {
	t.Helper()
	db := assemblyStorageTestDB(t, legacy)
	applyProblemContextMigration(t, db)
	for _, sql := range []string{
		`CREATE TABLE warehouses(id BIGINT PRIMARY KEY,name TEXT NOT NULL)`,
		`CREATE TABLE cells(id BIGINT PRIMARY KEY,warehouse_id BIGINT REFERENCES warehouses(id),code TEXT NOT NULL)`,
		`CREATE TABLE products(id BIGINT PRIMARY KEY,sku TEXT UNIQUE,name TEXT,archived BOOLEAN NOT NULL DEFAULT false)`,
		`CREATE TABLE cell_stock(cell_id BIGINT REFERENCES cells(id),product_id BIGINT REFERENCES products(id),quantity INTEGER NOT NULL CHECK(quantity>=0),PRIMARY KEY(cell_id,product_id))`,
		`INSERT INTO warehouses VALUES(1,'Test warehouse'),(2,'Second warehouse')`,
		`INSERT INTO cells VALUES(1,1,'A1'),(2,1,'B3')`,
		`INSERT INTO products(id,sku,name) VALUES(1,'A','Alpha'),(2,'B','Beta')`,
		`INSERT INTO cell_stock VALUES(1,1,1),(2,2,2)`,
	} {
		if _, err := db.Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile("../../migrations/014_assembly_stock_checks.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(context.Background(), string(data)); err != nil {
		t.Fatal(err)
	}
	return db
}
func stockTestAction(db *pgxpool.Pool, id string, version int, problemID, action string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"requestId":%q,"version":%d}`, id, version)
	request := httptest.NewRequest("POST", "/api/assembly/problems/"+problemID+"/"+action, strings.NewReader(body))
	request.SetPathValue("problemID", problemID)
	response := httptest.NewRecorder()
	assemblyProblemOperationHandler(db, response, request, action)
	return response
}
func stockTestVersion(t *testing.T, w *httptest.ResponseRecorder) int {
	t.Helper()
	var response assemblyDraftResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return int(response.Version)
}
func stockTestApplied(t *testing.T, w *httptest.ResponseRecorder, want bool) {
	t.Helper()
	var response assemblyDraftResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Applied == nil || *response.Applied != want {
		t.Fatalf("applied=%v: HTTP %d %s", want, w.Code, w.Body.String())
	}
}
func problemIDOf(entry any) string { return textValue(valueMap(entry)["problemId"]) }
func stockTestExec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func stockTestQuantity(t *testing.T, db *pgxpool.Pool, table string, product int64, want int) {
	t.Helper()
	var value int
	if err := db.QueryRow(context.Background(), "SELECT COALESCE(sum(quantity),0)::int FROM "+table+" WHERE product_id=$1", product).Scan(&value); err != nil || value != want {
		t.Fatalf("%s product %d quantity=%d want=%d err=%v", table, product, value, want, err)
	}
}

func TestAssemblyServerStockRecheckAndReturn(t *testing.T) {
	db := stockTestDB(t, "")
	order := creationTestOrder()
	id := func(n int) string { return fmt.Sprintf("40000000-0000-4000-8000-%012d", n) }
	initial := responseTestWorkspace(t, createTestProblem(db, id(1), 0, "queue", order, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"}))
	pid := problemIDOf(testIssues(initial)[0])
	contextBefore := contextOf(testIssues(initial)[0])
	check := stockTestAction(db, id(2), 1, pid, "check-stock")
	stockTestApplied(t, check, false)
	state := responseTestWorkspace(t, check)
	if testProblem(testIssues(state)[0])["resolutionStatus"] != "awaiting" || len(testProblem(testIssues(state)[0])["shortages"].([]any)) != 2 {
		t.Fatal("server shortage not persisted")
	}
	assertStorageCount(t, db, "assembly_order_reservations", 0)
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5`)
	check = stockTestAction(db, id(3), 2, pid, "check-stock")
	stockTestApplied(t, check, true)
	state = responseTestWorkspace(t, check)
	if testProblem(testIssues(state)[0])["resolutionStatus"] != "resolved" {
		t.Fatal("stock problem was not resolved")
	}
	// Simulate stock changing after a successful check.
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=0 WHERE product_id=1`)
	returned := stockTestAction(db, id(4), 3, pid, "return")
	stockTestApplied(t, returned, false)
	state = responseTestWorkspace(t, returned)
	if state["activeOrder"] != nil || testProblem(testIssues(state)[0])["resolutionStatus"] != "awaiting" {
		t.Fatal("stale availability allowed return")
	}
	assertStorageCount(t, db, "assembly_order_reservations", 0)
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5 WHERE product_id=1`)
	check = stockTestAction(db, id(5), 4, pid, "check-stock")
	stockTestApplied(t, check, true)
	returned = stockTestAction(db, id(6), 5, pid, "return")
	stockTestApplied(t, returned, true)
	state = responseTestWorkspace(t, returned)
	if state["activeOrder"] == nil || testProblem(testIssues(state)[0])["resolutionStatus"] != "returned" || valueMap(state["activeState"])["confirmedCell"] != nil {
		t.Fatal("checked return failed or old cell confirmation reused")
	}
	if !reflect.DeepEqual(contextOf(testIssues(state)[0]), contextBefore) {
		t.Fatal("stock check rewrote creation context")
	}
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 3)
	stockTestQuantity(t, db, "assembly_order_reservations", 2, 4)
	replay := stockTestAction(db, id(6), 5, pid, "return")
	stockTestApplied(t, replay, true)
	if !reflect.DeepEqual(responseTestWorkspace(t, replay), state) || stockTestVersion(t, replay) != 6 {
		t.Fatal("return replay changed state/version")
	}
	assertStorageCount(t, db, "assembly_stock_checks", 5)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 3)
	tampered, _ := decodeWorkspace(encodeValue(state))
	testProblem(testIssues(tampered)[0])["resolutionStatus"] = "resolved"
	if w := storeTestWorkspace(db, id(7), 6, tampered); w.Code != 409 {
		t.Fatal("frontend could set solved/returned state")
	}
	// One preview pick consumes remaining reservation but remains separately held, not physically deducted.
	valueMap(state["activeState"])["picks"] = []any{map[string]any{"sku": "A", "cell": "A1"}}
	state = responseTestWorkspace(t, storeTestWorkspace(db, id(8), 6, state))
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 2)
	stockTestQuantity(t, db, "assembly_preview_stock_holds", 1, 1)
	stockTestQuantity(t, db, "cell_stock", 1, 5)
	state = responseTestWorkspace(t, createTestProblem(db, id(9), 7, "assembly", order, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "B"}))
	nextID := problemIDOf(testIssues(state)[1])
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 2)
	check = stockTestAction(db, id(10), 8, nextID, "check-stock")
	stockTestApplied(t, check, true)
	returned = stockTestAction(db, id(11), 9, nextID, "return")
	stockTestApplied(t, returned, true)
	state = responseTestWorkspace(t, returned)
	if len(valueArray(valueMap(state["activeState"])["picks"])) != 1 {
		t.Fatal("partial progress not restored")
	}
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 2)
	stockTestQuantity(t, db, "assembly_preview_stock_holds", 1, 1)
	assertStorageCount(t, db, "assembly_order_reservations", 2)
	if _, err := db.Exec(context.Background(), `UPDATE assembly_stock_checks SET result='{}'`); err == nil {
		t.Fatal("stock evidence overwritten")
	}
}

func TestAssemblyOtherReservationAndPreviewPicks(t *testing.T) {
	db := stockTestDB(t, "")
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5 WHERE product_id=1`)
	id := func(n int) string { return fmt.Sprintf("41000000-0000-4000-8000-%012d", n) }
	first := map[string]any{"number": "FIRST", "items": []any{map[string]any{"sku": "A", "name": "Alpha", "quantity": 4, "stock": 5, "cells": "A1 ×4", "cellStocks": map[string]int{"A1": 5}}}}
	state := map[string]any{"schemaVersion": 1, "activeOrder": first, "activeState": emptyTestActiveState(), "problemOrders": []any{}, "completedOrders": []any{}}
	state = responseTestWorkspace(t, storeTestWorkspace(db, id(1), 0, state))
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 4)
	other := map[string]any{"number": "SECOND", "items": []any{map[string]any{"sku": "A", "name": "Alpha", "quantity": 2, "stock": 0, "cells": "A1 ×2", "cellStocks": map[string]int{"A1": 0}}}}
	state = responseTestWorkspace(t, createTestProblem(db, id(2), 1, "queue", other, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"}))
	pid := problemIDOf(testIssues(state)[0])
	check := stockTestAction(db, id(3), 2, pid, "check-stock")
	stockTestApplied(t, check, false)
	state = responseTestWorkspace(t, check)
	shortage := testProblem(testIssues(state)[0])["shortages"].([]any)[0].(map[string]any)
	if shortage["available"] != float64(1) || shortage["missing"] != float64(1) {
		t.Fatal("other order's reserve counted as available")
	}
	valueMap(state["activeState"])["picks"] = []any{map[string]any{"sku": "A", "cell": "A1"}}
	state = responseTestWorkspace(t, storeTestWorkspace(db, id(4), 3, state))
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 3)
	stockTestQuantity(t, db, "assembly_preview_stock_holds", 1, 1)
	check = stockTestAction(db, id(5), 4, pid, "check-stock")
	stockTestApplied(t, check, false)
	state = responseTestWorkspace(t, check)
	shortage = testProblem(testIssues(state)[0])["shortages"].([]any)[0].(map[string]any)
	if shortage["available"] != float64(1) {
		t.Fatal("preview pick released the same physical unit for another order")
	}
	// FIRST is stopped too: its own remaining reserve must not be subtracted from its availability.
	state = responseTestWorkspace(t, createTestProblem(db, id(6), 5, "assembly", first, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"}))
	ownID := problemIDOf(testIssues(state)[1])
	check = stockTestAction(db, id(7), 6, ownID, "check-stock")
	stockTestApplied(t, check, true)
	var raw json.RawMessage
	if err := db.QueryRow(context.Background(), `SELECT result FROM assembly_stock_checks WHERE request_id=$1::uuid`, id(7)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var result assemblyStockResult
	json.Unmarshal(raw, &result)
	if result.Positions[0].OwnReserved != 3 || result.Positions[0].Available != 4 || result.Positions[0].Required != 3 {
		t.Fatal("own reservation was added twice or subtracted as foreign")
	}
	stockTestQuantity(t, db, "cell_stock", 1, 5)
}

func TestAssemblyServerStockErrorsAndAtomicity(t *testing.T) {
	for _, test := range []string{"unknown", "archived", "duplicate cell code", "database failure"} {
		t.Run(test, func(t *testing.T) {
			db := stockTestDB(t, "")
			order := creationTestOrder()
			id := func(n int) string { return fmt.Sprintf("42000000-0000-4000-8000-%012d", n) }
			state := responseTestWorkspace(t, createTestProblem(db, id(1), 0, "queue", order, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"}))
			pid := problemIDOf(testIssues(state)[0])
			switch test {
			case "unknown":
				stockTestExec(t, db, `DELETE FROM cell_stock WHERE product_id=1`)
				stockTestExec(t, db, `DELETE FROM products WHERE id=1`)
			case "archived":
				stockTestExec(t, db, `UPDATE products SET archived=true WHERE id=1`)
			case "duplicate cell code":
				stockTestExec(t, db, `INSERT INTO cells VALUES(3,2,'A1')`)
				stockTestExec(t, db, `INSERT INTO cell_stock VALUES(3,1,10)`)
			case "database failure":
				stockTestExec(t, db, `ALTER TABLE cell_stock RENAME TO unavailable_stock`)
			}
			check := stockTestAction(db, id(2), 1, pid, "check-stock")
			if test == "database failure" {
				if check.Code != 500 {
					t.Fatal("database failure not reported")
				}
				assertStorageCount(t, db, "assembly_stock_checks", 0)
				assertStorageCount(t, db, "assembly_problem_events", 2)
				return
			}
			stockTestApplied(t, check, false)
			state = responseTestWorkspace(t, check)
			problem := testProblem(testIssues(state)[0])
			if problem["resolutionStatus"] != "awaiting" {
				t.Fatal("unverified stock resolved problem")
			}
			events := problem["history"].([]any)
			last := valueMap(events[len(events)-1])
			if last["outcome"] != "error" || last["checkId"] == nil {
				t.Fatal("error history/evidence missing")
			}
		})
	}
}

func TestAssemblyStockRowLock(t *testing.T) {
	db := stockTestDB(t, "")
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	result, err := readAssemblyStock(ctx, tx, valueMap(creationTestOrder()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Positions[0].Physical != 1 {
		t.Fatal("wrong stock")
	}
	finished := make(chan error, 1)
	go func() {
		_, updateErr := db.Exec(ctx, `UPDATE cell_stock SET quantity=0 WHERE product_id=1`)
		finished <- updateErr
	}()
	select {
	case err := <-finished:
		t.Fatalf("stock changed while checked transaction held lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stock update did not resume after transaction")
	}
}

func TestAssemblyMultipleProblemsAndServerEvidence(t *testing.T) {
	order := creationTestOrder()
	first := testStorageIssue(order, "insufficient_stock", "A", "2026-10-11T10:00:00Z")
	delete(first, "assembly")
	second := testStorageIssue(order, "invalid_order", "A", "2026-10-11T10:00:01Z")
	delete(second, "assembly")
	legacy := map[string]any{"schemaVersion": 1, "activeOrder": nil, "activeState": emptyTestActiveState(), "problemOrders": []any{first, second}, "completedOrders": []any{}}
	db := stockTestDB(t, string(encodeValue(legacy)))
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5`)
	get := httptest.NewRecorder()
	getAssemblyDraftHandler(db, get, httptest.NewRequest("GET", "/", nil))
	state := responseTestWorkspace(t, get)
	pid := problemIDOf(testIssues(state)[0])
	otherID := problemIDOf(testIssues(state)[1])
	checked := stockTestAction(db, "43000000-0000-4000-8000-000000000001", 7, pid, "check-stock")
	stockTestApplied(t, checked, true)
	state = responseTestWorkspace(t, checked)
	if testProblem(testIssues(state)[1])["resolutionStatus"] != "awaiting" {
		t.Fatal("stock check resolved unrelated reason")
	}
	if result := stockTestAction(db, "43000000-0000-4000-8000-000000000002", 8, pid, "return"); result.Code != 409 {
		t.Fatal("one solved issue allowed return with another unresolved")
	}
	// A prior 'resolved' label alone is not a backend proof.
	stockTestExec(t, db, `UPDATE assembly_problem_records SET resolution_status='resolved',problem_metadata=jsonb_set(problem_metadata,'{resolutionStatus}','"resolved"') WHERE id=$1::uuid`, otherID)
	if result := stockTestAction(db, "43000000-0000-4000-8000-000000000003", 8, pid, "return"); result.Code != 409 {
		t.Fatal("unverified legacy resolved label allowed return")
	}
	stockTestExec(t, db, `INSERT INTO assembly_problem_clearances(problem_id,order_version_id,kind,evidence) SELECT id,order_version_id,'order_verified','{"test":"trusted backend verification"}' FROM assembly_problem_records WHERE id=$1::uuid`, otherID)
	returned := stockTestAction(db, "43000000-0000-4000-8000-000000000004", 8, pid, "return")
	stockTestApplied(t, returned, true)
	state = responseTestWorkspace(t, returned)
	for _, entry := range testIssues(state) {
		if testProblem(entry)["resolutionStatus"] != "returned" {
			t.Fatal("all problems not transitioned together")
		}
	}
}

func TestAssemblyReturnCreatesSeparateNewShortage(t *testing.T) {
	order := creationTestOrder()
	issue := testStorageIssue(order, "other", "A", "2026-10-11T10:00:00Z")
	delete(issue, "assembly")
	testProblem(issue)["resolutionStatus"] = "resolved"
	legacy := map[string]any{"schemaVersion": 1, "activeOrder": nil, "activeState": emptyTestActiveState(), "problemOrders": []any{issue}, "completedOrders": []any{}}
	db := stockTestDB(t, string(encodeValue(legacy)))
	stockTestExec(t, db, `INSERT INTO assembly_problem_clearances(problem_id,order_version_id,kind,evidence) SELECT id,order_version_id,'manual_resolution','{"test":"verified resolution"}' FROM assembly_problem_records`)
	get := httptest.NewRecorder()
	getAssemblyDraftHandler(db, get, httptest.NewRequest("GET", "/", nil))
	state := responseTestWorkspace(t, get)
	pid := problemIDOf(testIssues(state)[0])
	returned := stockTestAction(db, "44000000-0000-4000-8000-000000000001", 7, pid, "return")
	stockTestApplied(t, returned, false)
	state = responseTestWorkspace(t, returned)
	if len(testIssues(state)) != 2 || testProblem(testIssues(state)[0])["type"] != "other" || testProblem(testIssues(state)[0])["resolutionStatus"] != "resolved" || testProblem(testIssues(state)[1])["type"] != "insufficient_stock" || state["activeOrder"] != nil {
		t.Fatal("new shortage overwrote original reason or incorrectly returned order")
	}
	if contextOf(testIssues(state)[1])["source"] != "queue" || contextOf(testIssues(state)[1])["availabilitySource"] != "warehouse_check" {
		t.Fatal("derived problem invented assembly or missed real observation")
	}
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5`)
	checked := stockTestAction(db, "44000000-0000-4000-8000-000000000002", 8, problemIDOf(testIssues(state)[1]), "check-stock")
	stockTestApplied(t, checked, true)
	returned = stockTestAction(db, "44000000-0000-4000-8000-000000000003", 9, pid, "return")
	stockTestApplied(t, returned, true)
}
