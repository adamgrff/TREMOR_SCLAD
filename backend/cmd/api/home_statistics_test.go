package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func homeStatisticsTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := receivingTestDB(t)
	for _, name := range []string{"010_rack_cell_codes.sql", "011_assembly_preview_drafts.sql", "012_assembly_problem_storage.sql", "013_assembly_problem_context.sql", "014_assembly_stock_checks.sql", "015_assembly_warehouse_operations.sql", "016_assembly_sku_mappings.sql", "017_assembly_ozon_operations.sql", "018_assembly_disassembly.sql", "019_assembly_disassembly_return_reversals.sql", "021_assembly_lifecycle_events.sql"} {
		migration, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(context.Background(), string(migration)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	return db
}

func TestHomeStatisticsPeriods(t *testing.T) {
	now := time.Date(2026, 10, 28, 18, 30, 0, 0, time.UTC) // 29 October 01:30 in Krasnoyarsk.
	for _, test := range []struct{ period, expected string }{
		{"today", "2026-10-29T00:00:00+07:00"}, {"", "2026-10-29T00:00:00+07:00"},
		{"7d", "2026-10-23T00:00:00+07:00"}, {"30d", "2026-09-30T00:00:00+07:00"},
	} {
		start, ok := homeStatisticsPeriod(test.period, now)
		if !ok || start.Format(time.RFC3339) != test.expected {
			t.Fatalf("period %q: %v, %v", test.period, start, ok)
		}
	}
	for _, query := range []string{"period=week", "period=TODAY", "period=today&period=7d", "period=%207d"} {
		w := httptest.NewRecorder()
		homeStatisticsHandler(nil, w, httptest.NewRequest(http.MethodGet, "/api/home/statistics?"+query, nil))
		requireReceivingStatus(t, w, http.StatusBadRequest)
	}
}

func TestHomeStatisticsEmptyAndUnavailable(t *testing.T) {
	db := homeStatisticsTestDB(t)
	w := httptest.NewRecorder()
	homeStatisticsHandler(db, w, httptest.NewRequest(http.MethodGet, "/api/home/statistics?period=today", nil))
	requireReceivingStatus(t, w, http.StatusOK)
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatal(w.Header())
	}
	var result homeStatisticsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Stock.TotalCells != 1 || result.Stock.TotalQuantity != 0 || result.Stock.OccupiedCells != 0 || result.Stock.SKUCount != 0 || result.Stock.LowStockCount != 0 || result.Stock.LowStockItems == nil || len(result.Stock.LowStockItems) != 0 {
		t.Fatalf("empty stock: %+v", result.Stock)
	}
	if result.Receiving.Sessions != 0 || result.Receiving.ReceivedQuantity != 0 || result.Receiving.PlacedQuantity != 0 || result.Receiving.PendingQuantity != 0 || result.Assembly.QueuedOrders != 0 || result.Assembly.ActiveOrders != 0 || result.Assembly.ProblemOrders != 0 {
		t.Fatal(result)
	}
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"completedOrders", "averageAssemblySeconds"} {
		if string(raw["assembly"][field]) != "0" {
			t.Fatalf("empty %s must be numeric zero: %s", field, w.Body.String())
		}
	}
	db.Close()
	w = httptest.NewRecorder()
	homeStatisticsHandler(db, w, httptest.NewRequest(http.MethodGet, "/api/home/statistics", nil))
	requireReceivingStatus(t, w, http.StatusInternalServerError)
}

func TestHomeStatisticsRealAggregates(t *testing.T) {
	db := homeStatisticsTestDB(t)
	ctx := context.Background()
	now := time.Now()
	start, _ := homeStatisticsPeriod("today", now)
	today := start.Add(now.Sub(start) / 2)
	week, month, old, future := start.AddDate(0, 0, -3).Add(12*time.Hour), start.AddDate(0, 0, -20).Add(12*time.Hour), start.AddDate(0, 0, -31), now.Add(24*time.Hour)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Real stock includes archived products. Low-stock classification is by
	// summed SKU, not individual cell rows, and occupied cells are distinct.
	exec(`INSERT INTO cells(warehouse_id,category_id,code,row_number,position_number) VALUES(1,1,'TEST-2',1,2),(1,1,'EMPTY',1,3)`)
	exec(`INSERT INTO products(category_id,sku,name,archived) VALUES
		(1,'SKU-A','A',false),(1,'SKU-B','B',false),(1,'SKU-C','C',false),
		(1,'ARCH','Archived',true),(1,'SKU-Z','Zero',false),(1,'SKU-D','D',false)`)
	exec(`INSERT INTO cell_stock(cell_id,product_id,quantity) SELECT 1,id,CASE sku WHEN 'SKU-A' THEN 1 WHEN 'SKU-B' THEN 2 WHEN 'SKU-C' THEN 3 WHEN 'ARCH' THEN 2 WHEN 'SKU-D' THEN 5 ELSE 0 END FROM products`)
	exec(`INSERT INTO cell_stock(cell_id,product_id,quantity) SELECT 2,id,3 FROM products WHERE sku IN ('SKU-B','SKU-C')`)
	var completed int64
	for _, at := range []time.Time{today, start, week, month, old, future} {
		if err := db.QueryRow(ctx, `INSERT INTO receiving_sessions(status,started_at,completed_at) VALUES('completed',$1,$1) RETURNING id`, at).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if at.Equal(today) {
			exec(`UPDATE receiving_sessions SET deleted_at=now() WHERE id=$1`, completed)
		}
	}
	first, second := receivingSession(t, db), receivingSession(t, db)
	receivingAdd(t, db, first, 5)
	receivingAdd(t, db, second, 7)
	// A leftover completed row must not inflate current pending stock.
	exec(`INSERT INTO receiving_session_items(session_id,product_id,quantity) VALUES($1,1,99)`, completed)
	for index, at := range []time.Time{start, week, month, old, future, today} {
		var placement int64
		status := "active"
		if index == 5 {
			status = "undone"
		}
		if err := db.QueryRow(ctx, `INSERT INTO receiving_placements(cell_id,receiving_session_id,status,created_at,undone_at)
			VALUES(1,$1,$2,$3,CASE WHEN $2='undone' THEN $3::timestamptz ELSE NULL END) RETURNING id`, first, status, at).Scan(&placement); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO receiving_placement_items(placement_id,product_id,quantity) VALUES($1,1,$2)`, placement, (index+1)*10)
	}
	// Imported order heads are required for the same queue as /assembly/orders.
	var realOrder, realVersion string
	for _, item := range []struct{ source, number, status string }{
		{"ozon", "QUEUED", "queued"}, {"ozon", "ACTIVE", "in_progress"}, {"ozon", "COMPLETED", "completed"},
		{"ozon", "PROBLEM", "problem"}, {"preview", "PREVIEW", "queued"},
	} {
		var version, id string
		if err := db.QueryRow(ctx, `SELECT assembly_store_order(jsonb_build_object('source',$1::text,'number',$2::text,'items','[]'::jsonb))`, item.source, item.number).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `UPDATE assembly_order_records SET status=$2 WHERE id=(SELECT order_id FROM assembly_order_versions WHERE id=$1::uuid) RETURNING id::text`, version, item.status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO assembly_order_heads(order_id,version_id) VALUES($1::uuid,$2::uuid)`, id, version)
		if item.number == "PROBLEM" {
			realOrder, realVersion = id, version
		}
	}
	addProblem := func(id, version string, at time.Time) {
		exec(`INSERT INTO assembly_problem_records(order_id,order_version_id,creation_order_version_id,legacy_key,problem_type,resolution_status,disposition,created_at,creation_problem_metadata,problem_metadata,entry_metadata)
			VALUES($1::uuid,$2::uuid,$2::uuid,gen_random_uuid()::text,'insufficient_stock','resolved','returned',$3,'{}','{}','{}')`, id, version, at)
	}
	addProblem(realOrder, realVersion, today)
	addProblem(realOrder, realVersion, today)
	for index, at := range []time.Time{week, month, old, future, today} {
		source := "ozon"
		if index == 4 {
			source = "preview"
		}
		var id, version string
		if err := db.QueryRow(ctx, `SELECT assembly_store_order(jsonb_build_object('source',$1::text,'number',$2::text,'items','[]'::jsonb))`, source, "DATED-"+at.Format(time.RFC3339Nano)).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT order_id::text FROM assembly_order_versions WHERE id=$1::uuid`, version).Scan(&id); err != nil {
			t.Fatal(err)
		}
		// No head: this fixture must not leak into the imported-order queue.
		addProblem(id, version, at)
	}
	expectedLow := []homeLowStockItem{{"SKU-A", 1}, {"ARCH", 2}, {"SKU-B", 5}, {"SKU-D", 5}}
	var stock homeStockStatistics
	for _, test := range []struct {
		period   string
		sessions int64
		placed   int64
		problems int64
	}{{"today", 2, 10, 1}, {"7d", 3, 30, 2}, {"30d", 4, 60, 3}} {
		from, _ := homeStatisticsPeriod(test.period, now)
		result, err := readHomeStatistics(ctx, db, from, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if result.Receiving.Sessions != test.sessions || result.Receiving.PlacedQuantity != test.placed || result.Receiving.PendingQuantity != 12 || result.Receiving.ReceivedQuantity != 12 {
			t.Fatalf("period %s receiving: %+v", test.period, result.Receiving)
		}
		if result.Stock.TotalQuantity != 19 || result.Stock.OccupiedCells != 2 || result.Stock.TotalCells != 3 || result.Stock.SKUCount != 5 || result.Stock.LowStockCount != 4 || !reflect.DeepEqual(result.Stock.LowStockItems, expectedLow) {
			t.Fatalf("period %s stock: %+v", test.period, result.Stock)
		}
		if result.Assembly.QueuedOrders != 1 || result.Assembly.ActiveOrders != 1 || result.Assembly.ProblemOrders != test.problems || result.Assembly.CompletedOrders != 0 || result.Assembly.AverageAssemblySeconds != 0 {
			t.Fatalf("period %s assembly: %+v", test.period, result.Assembly)
		}
		if test.period != "today" && !reflect.DeepEqual(stock, result.Stock) {
			t.Fatal("period changed current stock")
		}
		stock = result.Stock
	}
}
