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

	"github.com/jackc/pgx/v5/pgxpool"
)

func applyProblemContextMigration(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	migration, err := os.ReadFile("../../migrations/013_assembly_problem_context.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(context.Background(), string(migration)); err != nil {
		t.Fatal(err)
	}
}
func createTestProblem(db *pgxpool.Pool, id string, version int, source string, order map[string]any, details assemblyProblemDetails) *httptest.ResponseRecorder {
	request := map[string]any{"requestId": id, "version": version, "source": source, "orderNumber": order["number"], "details": details}
	if source == "queue" {
		request["order"] = order
	}
	body, _ := json.Marshal(request)
	w := httptest.NewRecorder()
	createAssemblyProblemHandler(db, w, httptest.NewRequest("POST", "/api/assembly/problems", strings.NewReader(string(body))))
	return w
}
func creationTestOrder() map[string]any {
	return map[string]any{"number": "CONTEXT-ORDER", "items": []any{
		map[string]any{"sku": "A", "name": "Alpha", "quantity": 3, "stock": 1, "cells": "A1 ×1", "cellStocks": map[string]any{"A1": 1}},
		map[string]any{"sku": "B", "name": "Beta", "quantity": 4, "stock": 2, "cells": "B3 ×2", "cellStocks": map[string]any{"B3": 2}},
	}}
}
func contextOf(entry any) map[string]any {
	return testProblem(entry)["creationContext"].(map[string]any)
}

func TestAssemblyProblemContextScanStates(t *testing.T) {
	for _, test := range []struct {
		name      string
		picked    int
		stock     int
		cellStock int
		step      string
		available int
	}{
		{"cell", 0, 5, 5, "cell", 5}, {"shipment", 2, 5, 5, "shipment", 3}, {"resumed plan", 1, 1, 2, "cell", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			order := map[string]any{"number": "SCAN", "items": []any{map[string]any{"sku": "A", "name": "Alpha", "quantity": 2, "stock": test.stock, "cells": "A1 ×2", "cellStocks": map[string]int{"A1": test.cellStock}}}}
			picks := []any{}
			for i := 0; i < test.picked; i++ {
				picks = append(picks, map[string]any{"sku": "A", "cell": "A1"})
			}
			active := map[string]any{"picks": picks, "confirmedCell": nil, "shipmentConfirmed": test.picked == 2, "lastScan": "last successful scan", "log": "last error", "error": true}
			state := map[string]any{"schemaVersion": 1, "activeOrder": order, "activeState": active, "problemOrders": []any{}, "completedOrders": []any{}}
			raw, _ := json.Marshal(state)
			result, err := buildAssemblyProblem(raw, assemblyProblemCreationRequest{Source: "assembly", OrderNumber: "SCAN", Details: assemblyProblemDetails{Type: "damaged_product", PositionSKU: "A"}}, "2026-10-11T12:00:00Z")
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]any
			json.Unmarshal(result, &stored)
			snapshot := contextOf(testIssues(stored)[0])
			if snapshot["scanStep"] != test.step || snapshot["shipmentConfirmed"] != (test.picked == 2) || snapshot["activeState"].(map[string]any)["log"] != "last error" {
				t.Fatal("scan state was recalculated incorrectly or log lost")
			}
			position := snapshot["positions"].([]any)[0].(map[string]any)
			if position["available"] != float64(test.available) {
				t.Fatal("available quantity double-subtracted picks after replan")
			}
		})
	}
}
func TestAssemblyProblemCreationQueue(t *testing.T) {
	db := assemblyStorageTestDB(t, "")
	applyProblemContextMigration(t, db)
	order := creationTestOrder()
	id := "30000000-0000-4000-8000-000000000001"
	response := createTestProblem(db, id, 0, "queue", order, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"})
	state := responseTestWorkspace(t, response)
	entry := testIssues(state)[0]
	snapshot := contextOf(entry)
	if snapshot["source"] != "queue" || snapshot["complete"] != true || snapshot["picked"] != float64(0) || len(snapshot["affectedPositions"].([]any)) != 2 {
		t.Fatal("incomplete queue context")
	}
	for _, p := range snapshot["positions"].([]any) {
		if p.(map[string]any)["orderPositionId"] == nil {
			t.Fatal("position ID missing")
		}
	}
	shortages := testProblem(entry)["shortages"].([]any)
	if len(shortages) != 2 || shortages[0].(map[string]any)["missing"] != float64(2) || shortages[1].(map[string]any)["missing"] != float64(2) {
		t.Fatal("queue shortage quantities lost")
	}
	if state["activeOrder"] != nil || entry.(map[string]any)["assembly"] != nil {
		t.Fatal("queue creation started assembly")
	}
	replay := createTestProblem(db, id, 0, "queue", order, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"})
	if replay.Code != 200 || !reflect.DeepEqual(responseTestWorkspace(t, replay), state) {
		t.Fatal("retry did not replay original result")
	}
	assertStorageCount(t, db, "assembly_problem_records", 1)
	assertStorageCount(t, db, "assembly_problem_events", 2)
	if w := createTestProblem(db, "30000000-0000-4000-8000-000000000002", 1, "queue", order, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"}); w.Code != 409 {
		t.Fatal("duplicate queue transfer accepted")
	}
	if w := createTestProblem(db, id, 0, "queue", order, assemblyProblemDetails{Type: "damaged_product", PositionSKU: "A"}); w.Code != 409 {
		t.Fatal("same request ID reused with another intent")
	}
	snapshot["picked"] = float64(99)
	if w := storeTestWorkspace(db, "30000000-0000-4000-8000-000000000003", 1, state); w.Code != 409 {
		t.Fatal("creation context overwritten")
	}
	if _, err := db.Exec(context.Background(), `DELETE FROM assembly_problem_contexts`); err == nil {
		t.Fatal("context delete accepted")
	}
}
func TestAssemblyProblemCreationActive(t *testing.T) {
	db := assemblyStorageTestDB(t, "")
	applyProblemContextMigration(t, db)
	ctx := context.Background()
	db.Exec(ctx, `CREATE TABLE cell_stock(quantity INTEGER)`)
	db.Exec(ctx, `INSERT INTO cell_stock VALUES(37)`)
	order := creationTestOrder()
	items := order["items"].([]any)
	items[0].(map[string]any)["stock"] = 5
	items[0].(map[string]any)["cells"] = "A1 ×3"
	items[0].(map[string]any)["cellStocks"] = map[string]any{"A1": 5}
	active := map[string]any{"picks": []any{map[string]any{"sku": "A", "cell": "A1"}}, "confirmedCell": "A1", "shipmentConfirmed": false, "lastScan": "A", "log": "One unit picked", "error": false}
	initial := map[string]any{"schemaVersion": 1, "activeOrder": order, "activeState": active, "problemOrders": []any{}, "completedOrders": []any{}}
	responseTestWorkspace(t, storeTestWorkspace(db, "31000000-0000-4000-8000-000000000001", 0, initial))
	// Selected B is short, but A's already confirmed pick must remain in the snapshot.
	response := createTestProblem(db, "31000000-0000-4000-8000-000000000002", 1, "assembly", order, assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "B"})
	state := responseTestWorkspace(t, response)
	entry := testIssues(state)[0]
	snapshot := contextOf(entry)
	if state["activeOrder"] != nil || snapshot["source"] != "assembly" || snapshot["scanStep"] != "product" || snapshot["confirmedCell"] != "A1" || snapshot["confirmedCellReferenceId"] == nil || snapshot["picked"] != float64(1) {
		t.Fatal("active context incomplete")
	}
	original, _ := json.Marshal(active)
	captured, _ := json.Marshal(snapshot["activeState"])
	if string(original) != string(captured) {
		t.Fatal("server active state not preserved")
	}
	picks := snapshot["picks"].([]any)
	pick := picks[0].(map[string]any)
	if pick["pickId"] == nil || pick["cellReferenceId"] == nil || pick["cell"] != "A1" || pick["occurredAt"] != nil {
		t.Fatal("pick identity/cell/timestamp incorrect")
	}
	positions := snapshot["positions"].([]any)
	a := positions[0].(map[string]any)
	b := positions[1].(map[string]any)
	if a["required"] != float64(3) || a["picked"] != float64(1) || a["remaining"] != float64(2) || a["available"] != float64(4) || b["missing"] != float64(2) {
		t.Fatal("remaining quantity confused with total or available stock")
	}
	if len(testProblem(entry)["shortages"].([]any)) != 1 || len(entry.(map[string]any)["assembly"].(map[string]any)["picks"].([]any)) != 1 {
		t.Fatal("shortage/picks lost")
	}
	get := httptest.NewRecorder()
	getAssemblyDraftHandler(db, get, httptest.NewRequest("GET", "/", nil))
	if !reflect.DeepEqual(responseTestWorkspace(t, get), state) {
		t.Fatal("context did not survive reload")
	}
	if w := createTestProblem(db, "31000000-0000-4000-8000-000000000003", 1, "assembly", order, assemblyProblemDetails{Type: "missing_in_cell", PositionSKU: "A"}); w.Code != 409 {
		t.Fatal("stale creation accepted")
	}
	assertStorageCount(t, db, "assembly_problem_records", 1)
	assertStorageCount(t, db, "assembly_problem_events", 2)
	var stock int
	db.QueryRow(ctx, `SELECT quantity FROM cell_stock`).Scan(&stock)
	if stock != 37 {
		t.Fatal("warehouse stock changed")
	}
}
func TestAssemblyProblemCreationValidation(t *testing.T) {
	db := assemblyStorageTestDB(t, "")
	applyProblemContextMigration(t, db)
	order := creationTestOrder()
	for i, details := range []assemblyProblemDetails{{Type: "missing_in_cell"}, {Type: "damaged_product", PositionSKU: "NOT-IN-ORDER"}} {
		if w := createTestProblem(db, fmt.Sprintf("32000000-0000-4000-8000-%012d", i), 0, "queue", order, details); w.Code != 409 {
			t.Fatalf("invalid position: HTTP %d", w.Code)
		}
	}
	if w := createTestProblem(db, "32000000-0000-4000-8000-000000000003", 0, "queue", order, assemblyProblemDetails{Type: "other", Comment: "  "}); w.Code != 400 {
		t.Fatal("blank other comment accepted")
	}
	if w := createTestProblem(db, "32000000-0000-4000-8000-000000000004", 0, "assembly", order, assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "A"}); w.Code != 409 {
		t.Fatal("missing active order accepted")
	}
	assertStorageCount(t, db, "assembly_problem_records", 0)
	state := map[string]any{"schemaVersion": 1, "activeOrder": nil, "activeState": emptyTestActiveState(), "completedOrders": []any{}, "problemOrders": []any{testStorageIssue(order, "other", "A", "2026-10-11T10:00:00Z")}}
	if w := storeTestWorkspace(db, "32000000-0000-4000-8000-000000000005", 0, state); w.Code != 409 {
		t.Fatal("whole-page PUT bypassed native creation")
	}
	// Without a shortage, position identity and quantities are still mandatory context.
	for _, item := range order["items"].([]any) {
		item.(map[string]any)["stock"] = 10
	}
	result := responseTestWorkspace(t, createTestProblem(db, "32000000-0000-4000-8000-000000000006", 0, "queue", order, assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "A"}))
	if len(contextOf(testIssues(result)[0])["affectedPositions"].([]any)) != 1 {
		t.Fatal("non-stock problem has no affected position")
	}
}
func TestAssemblyProblemContextLegacyMigration(t *testing.T) {
	issue := testStorageIssue(testStoredOrder(), "missing_in_cell", "B", "2026-10-11T10:00:00Z")
	state := map[string]any{"schemaVersion": 1, "activeOrder": nil, "activeState": emptyTestActiveState(), "completedOrders": []any{}, "problemOrders": []any{issue}}
	raw, _ := json.Marshal(state)
	db := assemblyStorageTestDB(t, string(raw))
	applyProblemContextMigration(t, db)
	get := httptest.NewRecorder()
	getAssemblyDraftHandler(db, get, httptest.NewRequest("GET", "/", nil))
	loaded := responseTestWorkspace(t, get)
	snapshot := contextOf(testIssues(loaded)[0])
	if snapshot["complete"] != false || snapshot["source"] != "assembly" || len(snapshot["affectedPositions"].([]any)) != 1 || len(snapshot["picks"].([]any)) != 1 {
		t.Fatal("legacy context migration lost known data or invented completeness")
	}
	responseTestWorkspace(t, storeTestWorkspace(db, "33000000-0000-4000-8000-000000000001", 7, loaded))
}
