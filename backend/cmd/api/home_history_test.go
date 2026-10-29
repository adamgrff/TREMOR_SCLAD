package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func historyRequest(t *testing.T, db *pgxpool.Pool, query string) homeHistoryResponse {
	t.Helper()
	w := httptest.NewRecorder()
	homeHistoryHandler(db, w, httptest.NewRequest(http.MethodGet, "/api/home/history"+query, nil))
	requireReceivingStatus(t, w, http.StatusOK)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Header())
	}
	var result homeHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHomeHistoryLimits(t *testing.T) {
	for _, query := range []string{"limit=0", "limit=-1", "limit=101", "limit=1.5", "limit=abc", "limit=", "limit=2&limit=3", "limit=99999999999999999999999"} {
		w := httptest.NewRecorder()
		homeHistoryHandler(nil, w, httptest.NewRequest(http.MethodGet, "/api/home/history?"+query, nil))
		requireReceivingStatus(t, w, http.StatusBadRequest)
	}
}

func TestHomeHistoryReceivingAndOrdering(t *testing.T) {
	db := homeStatisticsTestDB(t)
	if events := historyRequest(t, db, "").Events; events == nil || len(events) != 0 {
		t.Fatalf("empty history: %+v", events)
	}
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 5)
	receivingAdd(t, db, session, 3)
	w := receivingRequest(db, createReceivingPlacementHandler, fmt.Sprintf(`{"sessionId":%d,"cellCode":"TEST-1","items":[{"sku":"TEST-SKU","quantity":8}]}`, session), "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	var placement receivingPlacementResponse
	if err := json.Unmarshal(w.Body.Bytes(), &placement); err != nil {
		t.Fatal(err)
	}
	// Older real facts remain visible independently of the statistics period.
	stockTestExec(t, db, fmt.Sprintf(`INSERT INTO receiving_intake_events(session_id,product_id,quantity,occurred_at)
	 SELECT %d,1,1,'2020-01-01'::timestamptz + n * interval '1 second' FROM generate_series(1,105) n`, session))
	all := historyRequest(t, db, "?limit=100").Events
	if len(all) != 100 || all[0].Event != "Товар размещён" {
		t.Fatalf("events: %+v", all)
	}
	wantRefs := []homeHistoryReference{{"sku", "TEST-SKU"}, {"cell", "TEST-1"}}
	if !reflect.DeepEqual(all[0].References, wantRefs) || !strings.Contains(all[0].Description, "8 ед.") {
		t.Fatal(all[0])
	}
	seen := map[string]bool{}
	for i, event := range all {
		if seen[event.ID] || event.Process != "receiving" {
			t.Fatal(event)
		}
		seen[event.ID] = true
		if i > 0 && (event.OccurredAt.After(all[i-1].OccurredAt) || (event.OccurredAt.Equal(all[i-1].OccurredAt) && event.ID > all[i-1].ID)) {
			t.Fatal("incorrect chronological ordering")
		}
	}
	if len(historyRequest(t, db, "").Events) != 20 {
		t.Fatal("default limit")
	}
	if !reflect.DeepEqual(historyRequest(t, db, "?limit=2&period=today").Events, all[:2]) {
		t.Fatal("limit, stable IDs or period independence")
	}
	w = receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", placement.ID)
	requireReceivingStatus(t, w, http.StatusOK)
	undone := historyRequest(t, db, "?limit=1").Events[0]
	if undone.ID != all[0].ID || !strings.Contains(undone.Description, "отменено") {
		t.Fatal(undone)
	}
}

func TestHomeHistoryAssemblyAndOzonFacts(t *testing.T) {
	db := homeStatisticsTestDB(t)
	stockTestExec(t, db, `UPDATE products SET sku='A' WHERE id=1;
	 INSERT INTO products(category_id,sku,name) VALUES(1,'B','Beta');
	 INSERT INTO cell_stock(cell_id,product_id,quantity) VALUES(1,1,20),(1,2,20)`)
	_, operation, _ := finishOzonTestOrder(t, db)
	before := historyRequest(t, db, "").Events
	counts := map[string]int{}
	for _, event := range before {
		counts[event.Event]++
	}
	if len(before) != 2 || counts["Начата сборка"] != 1 || counts["Заказ переведён в проблемные"] != 1 {
		t.Fatalf("unfinished history (no scans or fake Ozon): %+v", before)
	}
	proof := receiptFor(t, db, operation, "history-applied", "applied", 1)
	if err := ingestOzonDomainReceipt(context.Background(), db, proof); err != nil {
		t.Fatal(err)
	}
	if err := ingestOzonDomainReceipt(context.Background(), db, proof); err != nil {
		t.Fatal(err)
	}
	after := historyRequest(t, db, "").Events
	counts = map[string]int{}
	for _, event := range after {
		counts[event.Event]++
		if !reflect.DeepEqual(event.References, []homeHistoryReference{{"order", "CONFIRM"}}) {
			t.Fatal(event)
		}
	}
	if len(after) != 4 || counts["Заказ собран"] != 1 || counts["Операция Ozon подтверждена"] != 1 {
		t.Fatalf("completion/replay: %+v", after)
	}
	// Preview-only problems are not real warehouse activity.
	stockTestExec(t, db, `INSERT INTO assembly_order_records(source,order_number) VALUES('preview','DEMO');
	 INSERT INTO assembly_order_versions(order_id,fingerprint,metadata) SELECT id,'test','{}' FROM assembly_order_records WHERE source='preview';
	 INSERT INTO assembly_problem_records(order_id,order_version_id,creation_order_version_id,legacy_key,problem_type,resolution_status,created_at,creation_problem_metadata,problem_metadata,entry_metadata)
	 SELECT o.id,v.id,v.id,'demo','other','awaiting',now(),'{}','{}','{}' FROM assembly_order_records o JOIN assembly_order_versions v ON v.order_id=o.id WHERE o.source='preview'`)
	if got := historyRequest(t, db, "").Events; !reflect.DeepEqual(got, after) {
		t.Fatal("preview event leaked")
	}
	stockTestExec(t, db, "DROP TABLE receiving_intake_events")
	w := httptest.NewRecorder()
	homeHistoryHandler(db, w, httptest.NewRequest(http.MethodGet, "/api/home/history", nil))
	requireReceivingStatus(t, w, http.StatusInternalServerError)
	if strings.Contains(w.Body.String(), `"events"`) {
		t.Fatal("database failure must not become empty success")
	}
}
