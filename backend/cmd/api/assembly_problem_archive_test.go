package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func archiveTestGet(t *testing.T, db *pgxpool.Pool, query string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	assemblyProblemArchiveHandler(db, w, httptest.NewRequest("GET", "/archive?"+query, nil))
	if w.Code != 200 {
		t.Fatalf("archive HTTP %d: %s", w.Code, w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}
func archiveTestDetail(t *testing.T, db *pgxpool.Pool, id string) map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/history", nil)
	r.SetPathValue("problemID", id)
	w := httptest.NewRecorder()
	assemblyProblemHistoryHandler(db, w, r)
	if w.Code != 200 {
		t.Fatalf("history HTTP %d: %s", w.Code, w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}
func archiveEventSnapshot(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var raw string
	if err := db.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',id,'problem',problem_id,'ordinal',ordinal,'occurred',occurred_at,'recorded',recorded_at,'payload',payload) ORDER BY problem_id,ordinal),'[]'::jsonb)::text FROM assembly_problem_events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestProblemArchiveAllOutcomesFiltersAndLegacyUnknownTimes(t *testing.T) {
	entries := []any{}
	for n := 0; n < 8; n++ {
		order := testStoredOrder()
		order["number"] = fmt.Sprintf("ARCHIVE-%d", n)
		if n == 2 {
			valueMap(valueArray(order["items"])[0])["name"] = "Чёрный товар"
		}
		entry := testStorageIssue(order, "insufficient_stock", "A", fmt.Sprintf("2026-10-14T10:00:%02dZ", n))
		p := testProblem(entry)
		if n < 6 {
			p["resolutionStatus"] = "resolved"
			if n%4 == 0 {
				p["resolutionStatus"] = "returned"
			} else {
				p["closedOutcome"] = []string{"returned", "completed", "cancelled", "disassembled"}[n%4]
			}
		}
		entries = append(entries, entry)
	}
	db := assemblyStorageTestDB(t, string(encodeValue(map[string]any{"schemaVersion": 1, "activeOrder": nil, "activeState": emptyTestActiveState(), "problemOrders": entries, "completedOrders": []any{}})))
	beforeEvents := archiveEventSnapshot(t, db)
	before := warehouseState(t, db)
	working := httptest.NewRecorder()
	assemblyProblemsHandler(db, working, httptest.NewRequest("GET", "/", nil))
	var list map[string]any
	json.Unmarshal(working.Body.Bytes(), &list)
	if working.Code != 200 || len(valueArray(list["problems"])) != 2 {
		t.Fatal("closed outcomes leaked into working list", working.Body.String())
	}
	first := archiveTestGet(t, db, "")
	if intValue(first["total"]) != 6 || len(valueArray(first["problems"])) != 5 || intValue(first["pageCount"]) != 2 {
		t.Fatal("pagination incorrect", string(encodeValue(first)))
	}
	second := archiveTestGet(t, db, "page=2")
	if len(valueArray(second["problems"])) != 1 {
		t.Fatal("second page lost records")
	}
	seen := map[string]bool{}
	for _, page := range []map[string]any{first, second} {
		for _, v := range valueArray(page["problems"]) {
			id := textValue(valueMap(v)["problemId"])
			if seen[id] {
				t.Fatal("duplicate page row")
			}
			seen[id] = true
			if valueMap(v)["closedAt"] != nil {
				t.Fatal("legacy closure time fabricated")
			}
		}
	}
	for _, query := range []string{"outcome=returned", "outcome=completed", "outcome=cancelled", "outcome=disassembled", "source=preview", "q=" + url.QueryEscape("чёрный"), "q=ARCHIVE-0", "q=A"} {
		if intValue(archiveTestGet(t, db, query)["total"]) == 0 {
			t.Fatal("filter lost closed record", query)
		}
	}
	if intValue(archiveTestGet(t, db, "q=%25")["total"]) != 0 {
		t.Fatal("literal percent treated as wildcard")
	}
	if intValue(archiveTestGet(t, db, "page=999")["page"]) != 2 {
		t.Fatal("out of bounds page not clamped")
	}
	for _, query := range []string{"page=-1", "pageSize=1000", "outcome=fake", "type=invalid", "source=unknown", "page=abc"} {
		w := httptest.NewRecorder()
		assemblyProblemArchiveHandler(db, w, httptest.NewRequest("GET", "/?"+query, nil))
		if w.Code != 400 {
			t.Fatal("invalid filter accepted", query, w.Code)
		}
	}
	id := textValue(valueMap(valueArray(first["problems"])[0])["problemId"])
	detail := archiveTestDetail(t, db, id)
	if len(valueArray(detail["historyRecords"])) != 1 || valueMap(valueArray(detail["historyRecords"])[0])["id"] == nil {
		t.Fatal("event identity missing")
	}
	pick := valueMap(valueArray(valueMap(valueMap(detail["entry"])["assembly"])["picks"])[0])
	if pick["occurredAt"] != nil {
		t.Fatal("old scan time invented")
	}
	if archiveEventSnapshot(t, db) != beforeEvents || !reflect.DeepEqual(before, warehouseState(t, db)) {
		t.Fatal("archive read mutated history/workspace")
	}
}

func TestProblemArchivePreservesEventsFromMappingToPhysicalReturn(t *testing.T) {
	db := ozonTestDB(t)
	migration, err := os.ReadFile("../../migrations/018_assembly_disassembly.sql")
	if err != nil {
		t.Fatal(err)
	}
	stockTestExec(t, db, string(migration))
	order := unknownOrder("FULL-HISTORY", 11)
	importWarehouseTest(t, db, 1, order)
	state := responseTestWorkspace(t, warehouseProblem(db, 2, 0, "queue", order.Number, assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "EXT-A"}))
	mappingID := problemIDOf(testIssues(state)[0])
	originalContext := contextOf(testIssues(state)[0])
	state = warehouseOK(t, mappingAction(db, 3, 1, mappingID, "EXT-A", 1, true))
	stockID := problemIDOf(testIssues(state)[1])
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=6 WHERE product_id=1`)
	warehouseOK(t, stockTestAction(db, warehouseID(4), 2, stockID, "check-stock"))
	state = warehouseOK(t, stockTestAction(db, warehouseID(5), 3, mappingID, "return"))
	archivedBefore := archiveTestDetail(t, db, mappingID)
	prefix := valueArray(archivedBefore["historyRecords"])
	cell := nextWarehouseCell(valueMap(state["activeOrder"]))
	warehouseOK(t, warehouseAction(db, 6, 4, "scan", "", textValue(cell["scanCode"])))
	warehouseOK(t, warehouseAction(db, 7, 5, "scan", "", "A"))
	oid, _ := seedOzonRefresh(t, db, order.Number, true)
	if err = ingestOzonDomainReceipt(context.Background(), db, receiptFor(t, db, oid, "archive-cancel", "cancelled", 1)); err != nil {
		t.Fatal(err)
	}
	state = warehouseState(t, db)
	cancelID := problemIDOf(testIssues(state)[3])
	f := disassemblyTestFixture{db: db, state: state, pid: cancelID, version: 8, next: 100}
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	f.action(t, "scan", textValue(cell["scanCode"]), true)
	f.action(t, "finish", "", true)
	summary := archiveTestGet(t, db, "")
	if intValue(summary["total"]) != 4 {
		t.Fatal("closed problems lost", string(encodeValue(summary)))
	}
	if intValue(archiveTestGet(t, db, "source=ozon")["total"]) != 4 || intValue(archiveTestGet(t, db, "source=preview")["total"]) != 0 {
		t.Fatal("canonical source filter incorrect")
	}
	kinds := map[string]bool{}
	beforeEvents := archiveEventSnapshot(t, db)
	for _, v := range valueArray(summary["problems"]) {
		detail := archiveTestDetail(t, db, textValue(valueMap(v)["problemId"]))
		records := valueArray(detail["historyRecords"])
		for n, e := range records {
			record := valueMap(e)
			if intValue(record["ordinal"]) != n || record["id"] == nil || record["recordedAt"] == nil {
				t.Fatal("event order/identity lost")
			}
			kinds[textValue(record["kind"])] = true
		}
	}
	for _, kind := range []string{"creation", "transfer", "mapping", "check", "result", "resolution", "return", "pick", "ozon_result", "ozon_cancel", "disassembly_transfer", "disassembly_start", "disassembly_return", "disassembly_finish"} {
		if !kinds[kind] {
			t.Fatal("event missing in archived histories", kind)
		}
	}
	mapped := archiveTestDetail(t, db, mappingID)
	if !reflect.DeepEqual(prefix, valueArray(mapped["historyRecords"])) || !reflect.DeepEqual(originalContext, contextOf(valueMap(mapped["entry"]))) {
		t.Fatal("closed mapping history/context rewritten by later stages")
	}
	cancelled := archiveTestDetail(t, db, cancelID)
	if len(valueArray(cancelled["relatedProblems"])) != 4 {
		t.Fatal("related causes missing")
	}
	journals := valueMap(cancelled["journals"])
	if len(valueArray(journals["ozon"])) != 1 || len(valueArray(journals["disassembly"])) != 1 {
		t.Fatal("linked operation journals missing")
	}
	if len(valueArray(valueMap(valueArray(journals["disassembly"])[0])["returns"])) != 1 {
		t.Fatal("physical return journal missing")
	}
	if archiveEventSnapshot(t, db) != beforeEvents {
		t.Fatal("read rewrote archive")
	}
	stockTestQuantity(t, db, "cell_stock", 1, 12)
	if _, err = db.Exec(context.Background(), `UPDATE assembly_problem_events SET payload='{}'::jsonb`); err == nil {
		t.Fatal("event overwrite allowed")
	}
	if _, err = db.Exec(context.Background(), `DELETE FROM assembly_problem_events`); err == nil {
		t.Fatal("event deletion allowed")
	}
}

func TestProblemHistoryAddsEventsWithoutChangingStoredPrefix(t *testing.T) {
	db := stockTestDB(t, "")
	state := responseTestWorkspace(t, createTestProblem(db, warehouseID(600), 0, "queue", creationTestOrder(), assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "A"}))
	id := problemIDOf(testIssues(state)[0])
	before := archiveTestDetail(t, db, id)
	prefix := valueArray(before["historyRecords"])
	checked := stockTestAction(db, warehouseID(601), 1, id, "check-stock")
	stockTestApplied(t, checked, false)
	after := archiveTestDetail(t, db, id)
	all := valueArray(after["historyRecords"])
	if len(all) <= len(prefix) || !reflect.DeepEqual(prefix, all[:len(prefix)]) {
		t.Fatal("new events replaced old prefix")
	}
	replay := stockTestAction(db, warehouseID(601), 1, id, "check-stock")
	if !sameWarehouseResponse(checked, replay) {
		t.Fatal("replay changed action")
	}
	if !reflect.DeepEqual(all, valueArray(archiveTestDetail(t, db, id)["historyRecords"])) {
		t.Fatal("read or replay duplicated/rewrote events")
	}
	var oldStock int
	if err := db.QueryRow(context.Background(), `SELECT sum(quantity) FROM cell_stock`).Scan(&oldStock); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"not-a-uuid", warehouseID(999)} {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("problemID", id)
		w := httptest.NewRecorder()
		assemblyProblemHistoryHandler(db, w, r)
		if w.Code != 400 && w.Code != 404 {
			t.Fatal("bad identity not rejected", w.Code)
		}
	}
	working := httptest.NewRecorder()
	assemblyProblemsHandler(db, working, httptest.NewRequest("GET", "/?includeClosed=true", strings.NewReader("")))
	if working.Code != 200 {
		t.Fatal("legacy includeClosed compatibility broken")
	}
}
