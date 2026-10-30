package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func assemblyStorageTestDB(t *testing.T, legacy string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	requireSafeReceivingTestDatabase(t, url)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("assembly_storage_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	for _, name := range []string{"011_assembly_preview_drafts.sql", "012_assembly_problem_storage.sql"} {
		data, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
		if name == "011_assembly_preview_drafts.sql" && legacy != "" {
			if _, err = db.Exec(ctx, `INSERT INTO assembly_preview_drafts(id,version,state) VALUES(TRUE,7,$1::jsonb)`, legacy); err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

func storeTestWorkspace(db *pgxpool.Pool, id string, version int, state map[string]any) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(state)
	body := fmt.Sprintf(`{"requestId":%q,"version":%d,"state":%s}`, id, version, payload)
	w := httptest.NewRecorder()
	saveAssemblyDraftHandler(db, w, httptest.NewRequest("PUT", "/", strings.NewReader(body)))
	return w
}
func responseTestWorkspace(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var response struct{ State map[string]any }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.State
}
func testIssues(state map[string]any) []any { return state["problemOrders"].([]any) }
func testProblem(entry any) map[string]any  { return entry.(map[string]any)["problem"].(map[string]any) }
func emptyTestActiveState() map[string]any {
	return map[string]any{"picks": []any{}, "confirmedCell": nil, "shipmentConfirmed": false, "lastScan": "—", "log": "", "error": false}
}
func testStorageIssue(order map[string]any, kind, sku, created string) map[string]any {
	return map[string]any{"order": order, "status": "problem", "assembly": map[string]any{"details": map[string]any{"type": kind, "comment": "", "positionSku": sku}, "picks": []any{map[string]any{"sku": "A", "cell": "A1"}}, "cell": "A1", "shipmentConfirmed": false}, "problem": map[string]any{"type": kind, "comment": "", "positionSku": sku, "createdAt": created, "resolutionStatus": "awaiting", "shortages": []any{}, "history": []any{map[string]any{"createdAt": created, "kind": "creation", "title": "Created", "message": sku, "outcome": "neutral"}}}}
}
func testStoredOrder() map[string]any {
	return map[string]any{"number": "TEST-ORDER", "items": []any{map[string]any{"sku": "A", "name": "Alpha", "quantity": 2}, map[string]any{"sku": "B", "name": "Beta", "quantity": 1}}}
}
func assertStorageCount(t *testing.T, db *pgxpool.Pool, table string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s count=%d want=%d", table, count, want)
	}
}

func TestAssemblyNormalizedProblems(t *testing.T) {
	db := assemblyStorageTestDB(t, "")
	ctx := context.Background()
	if _, err := db.Exec(ctx, `CREATE TABLE cell_stock(quantity INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO cell_stock VALUES(37)`); err != nil {
		t.Fatal(err)
	}
	order := testStoredOrder()
	state := map[string]any{"schemaVersion": 1, "activeOrder": nil, "activeState": emptyTestActiveState(), "completedOrders": []any{}, "problemOrders": []any{testStorageIssue(order, "insufficient_stock", "A", "2026-10-11T10:00:00Z"), testStorageIssue(order, "missing_in_cell", "B", "2026-10-11T10:00:01Z")}}
	requestID := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
	first := storeTestWorkspace(db, requestID(10), 0, state)
	state = responseTestWorkspace(t, first)
	assertStorageCount(t, db, "assembly_order_records", 1)
	assertStorageCount(t, db, "assembly_problem_records", 2)
	assertStorageCount(t, db, "assembly_problem_affected_positions", 2)
	assertStorageCount(t, db, "assembly_problem_events", 2)
	assertStorageCount(t, db, "assembly_pick_records", 1)
	assertStorageCount(t, db, "assembly_problem_pick_links", 2)
	for _, entry := range testIssues(state) {
		if entry.(map[string]any)["problemId"] == nil {
			t.Fatal("missing problem ID")
		}
	}
	var compact json.RawMessage
	if err := db.QueryRow(ctx, `SELECT state FROM assembly_preview_drafts WHERE id=true`).Scan(&compact); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(compact), `"problemOrders"`) || strings.Contains(string(compact), `"activeOrder"`) || strings.Contains(string(compact), `"picks"`) {
		t.Fatal("whole-page payload still stored as source of truth")
	}
	get := httptest.NewRecorder()
	getAssemblyDraftHandler(db, get, httptest.NewRequest("GET", "/", nil))
	loaded := responseTestWorkspace(t, get)
	a, _ := json.Marshal(state)
	b, _ := json.Marshal(loaded)
	if string(a) != string(b) {
		t.Fatal("normalized reload differs")
	}
	testProblem(testIssues(state)[0])["resolutionStatus"] = "resolved"
	testProblem(testIssues(state)[0])["comment"] = "Resolution note"
	state = responseTestWorkspace(t, storeTestWorkspace(db, requestID(11), 1, state))
	var originalComment string
	if err := db.QueryRow(ctx, `SELECT creation_problem_metadata->>'comment' FROM assembly_problem_records WHERE id=$1`, testIssues(state)[0].(map[string]any)["problemId"]).Scan(&originalComment); err != nil || originalComment != "" {
		t.Fatal("original problem comment was overwritten")
	}
	testProblem(testIssues(state)[0])["resolutionStatus"] = "returned"
	state["activeOrder"] = order
	state["activeState"] = map[string]any{"picks": []any{map[string]any{"sku": "A", "cell": "A1"}}, "confirmedCell": nil, "shipmentConfirmed": false}
	if w := storeTestWorkspace(db, requestID(12), 2, state); w.Code != 409 {
		t.Fatalf("return with unresolved second issue: %d %s", w.Code, w.Body.String())
	}
	get = httptest.NewRecorder()
	getAssemblyDraftHandler(db, get, httptest.NewRequest("GET", "/", nil))
	state = responseTestWorkspace(t, get)
	testProblem(testIssues(state)[1])["resolutionStatus"] = "resolved"
	state = responseTestWorkspace(t, storeTestWorkspace(db, requestID(13), 2, state))
	events := testProblem(testIssues(state)[0])["history"].([]any)
	events[0].(map[string]any)["message"] = "rewritten"
	if w := storeTestWorkspace(db, requestID(14), 3, state); w.Code != 409 {
		t.Fatalf("rewritten history accepted: %d", w.Code)
	}
	get = httptest.NewRecorder()
	getAssemblyDraftHandler(db, get, httptest.NewRequest("GET", "/", nil))
	state = responseTestWorkspace(t, get)
	savedIssues := testIssues(state)
	state["problemOrders"] = savedIssues[:1]
	if w := storeTestWorkspace(db, requestID(15), 3, state); w.Code != 409 {
		t.Fatalf("deleted problem accepted: %d", w.Code)
	}
	state["problemOrders"] = savedIssues
	picks := savedIssues[0].(map[string]any)["assembly"].(map[string]any)["picks"].([]any)
	picks[0].(map[string]any)["cell"] = "B2"
	if w := storeTestWorkspace(db, requestID(16), 3, state); w.Code != 409 {
		t.Fatalf("rewritten picking context accepted: %d", w.Code)
	}
	picks[0].(map[string]any)["cell"] = "A1"
	for _, entry := range savedIssues {
		testProblem(entry)["resolutionStatus"] = "returned"
	}
	state["activeOrder"] = order
	state["activeState"] = map[string]any{"picks": []any{map[string]any{"sku": "A", "cell": "A1"}}, "confirmedCell": nil, "shipmentConfirmed": false}
	state = responseTestWorkspace(t, storeTestWorkspace(db, requestID(17), 3, state))
	list := httptest.NewRecorder()
	assemblyProblemsHandler(db, list, httptest.NewRequest("GET", "/", nil))
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"problems":[]`) {
		t.Fatal("closed problems still in working list")
	}
	list = httptest.NewRecorder()
	assemblyProblemsHandler(db, list, httptest.NewRequest("GET", "/?includeClosed=true", nil))
	var listed struct{ Problems []any }
	json.Unmarshal(list.Body.Bytes(), &listed)
	if len(listed.Problems) != 2 {
		t.Fatal("closed history not retained")
	}
	state["activeOrder"] = nil
	state["activeState"] = emptyTestActiveState()
	state["problemOrders"] = append(testIssues(state), testStorageIssue(order, "insufficient_stock", "A", "2026-10-11T10:01:00Z"))
	original := state
	state = responseTestWorkspace(t, storeTestWorkspace(db, requestID(18), 4, state))
	// A retried request must return the same response without duplicate history or identities.
	responseTestWorkspace(t, storeTestWorkspace(db, requestID(18), 4, original))
	assertStorageCount(t, db, "assembly_problem_records", 3)
	assertStorageCount(t, db, "assembly_problem_events", 3)
	if _, err := db.Exec(ctx, `UPDATE assembly_problem_events SET kind='tampered'`); err == nil {
		t.Fatal("direct event UPDATE accepted")
	}
	var stock int
	if err := db.QueryRow(ctx, `SELECT quantity FROM cell_stock`).Scan(&stock); err != nil || stock != 37 {
		t.Fatal("warehouse sentinel changed")
	}
	// Concurrent writes share one workspace version; exactly one can win.
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, n := range []int{19, 20} {
		wg.Add(1)
		go func(n int) { defer wg.Done(); codes <- storeTestWorkspace(db, requestID(n), 5, state).Code }(n)
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatalf("concurrent result: %d", code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent writes were not serialized")
	}
}

func TestAssemblyLegacyBackfill(t *testing.T) {
	order := testStoredOrder()
	issue := testStorageIssue(order, "insufficient_stock", "A", "2026-10-11T10:00:00Z")
	issue["problem"].(map[string]any)["resolutionStatus"] = "resolved"
	legacy := map[string]any{"schemaVersion": 1, "activeOrder": nil, "activeState": emptyTestActiveState(), "problemOrders": []any{issue}, "completedOrders": []any{}}
	payload, _ := json.Marshal(legacy)
	db := assemblyStorageTestDB(t, string(payload))
	assertStorageCount(t, db, "assembly_workspace_migration_backup", 1)
	assertStorageCount(t, db, "assembly_problem_records", 1)
	var backup json.RawMessage
	var version int
	if err := db.QueryRow(context.Background(), `SELECT state,version FROM assembly_workspace_migration_backup`).Scan(&backup, &version); err != nil {
		t.Fatal(err)
	}
	var preserved any
	json.Unmarshal(backup, &preserved)
	a, _ := json.Marshal(preserved)
	b, _ := json.Marshal(legacy)
	if version != 7 || string(a) != string(b) {
		t.Fatal("legacy backup changed")
	}
	get := httptest.NewRecorder()
	getAssemblyDraftHandler(db, get, httptest.NewRequest("GET", "/", nil))
	state := responseTestWorkspace(t, get)
	entry := testIssues(state)[0].(map[string]any)
	if testProblem(entry)["resolutionStatus"] != "resolved" {
		t.Fatal("resolved status lost during import")
	}
	delete(entry, "problemId")
	a, _ = json.Marshal(state)
	if string(a) != string(b) {
		t.Fatal("legacy state not preserved")
	}
}
