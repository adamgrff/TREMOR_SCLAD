package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func lifecycleCount(t *testing.T, db *pgxpool.Pool, started, completed int) {
	t.Helper()
	var starts, completions int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FILTER(WHERE event_type='started'),COUNT(*) FILTER(WHERE event_type='completed') FROM assembly_lifecycle_events`).Scan(&starts, &completions); err != nil {
		t.Fatal(err)
	}
	if starts != started || completions != completed {
		t.Fatalf("events started/completed=%d/%d, want %d/%d", starts, completions, started, completed)
	}
}

func TestAssemblyLifecycleStartFinishAndReplay(t *testing.T) {
	db := homeStatisticsTestDB(t)
	ctx := context.Background()
	stockTestExec(t, db, `UPDATE products SET sku='A' WHERE id=1;
		INSERT INTO products(category_id,sku,name) VALUES(1,'B','Beta');
		INSERT INTO cell_stock(cell_id,product_id,quantity) VALUES(1,1,20),(1,2,20)`)
	_, operation, version := finishOzonTestOrder(t, db)
	lifecycleCount(t, db, 1, 0) // finish without verified Ozon success is NOT completion.
	startReplay := warehouseAction(db, 2, 0, "start", "CONFIRM", "")
	stockTestApplied(t, startReplay, true)
	var finishRequest string
	if err := db.QueryRow(ctx, `SELECT request_id::text FROM assembly_ozon_reconciliations WHERE operation_id=$1::uuid AND kind='unavailable'`, operation).Scan(&finishRequest); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	warehouseAssemblyHandler(db, w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"requestId":%q,"version":%d}`, finishRequest, version-1))), "finish")
	stockTestApplied(t, w, false)
	lifecycleCount(t, db, 1, 0)
	now := time.Now()
	from, _ := homeStatisticsPeriod("today", now)
	before, err := readHomeStatistics(ctx, db, from, now)
	if err != nil || before.Assembly.CompletedOrders != 0 || before.Assembly.AverageAssemblySeconds != 0 {
		t.Fatalf("unfinished order counted: %+v %v", before.Assembly, err)
	}
	proof := receiptFor(t, db, operation, "lifecycle-verified", "applied", 1)
	if err = ingestOzonDomainReceipt(ctx, db, proof); err != nil {
		t.Fatal(err)
	}
	lifecycleCount(t, db, 1, 1)
	if err = ingestOzonDomainReceipt(ctx, db, proof); err != nil {
		t.Fatal(err)
	}
	proof.EvidenceID = "lifecycle-same-operation-new-evidence"
	observed, err := time.Parse(time.RFC3339Nano, proof.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	proof.ObservedAt = observed.Add(time.Second).Format(time.RFC3339Nano)
	if err = ingestOzonDomainReceipt(ctx, db, proof); err != nil {
		t.Fatal(err)
	}
	lifecycleCount(t, db, 1, 1)
	var expected float64
	if err = db.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM(c.occurred_at-s.occurred_at))::double precision
		FROM assembly_lifecycle_events c JOIN assembly_lifecycle_events s ON s.order_id=c.order_id AND s.cycle_id=c.cycle_id
		WHERE c.event_type='completed' AND s.event_type='started'`).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	result, err := readHomeStatistics(ctx, db, from, time.Now())
	if err != nil || result.Assembly.CompletedOrders != 1 || math.Abs(result.Assembly.AverageAssemblySeconds-expected) > .000001 {
		t.Fatalf("completed statistics: %+v expected %f, %v", result.Assembly, expected, err)
	}
	for _, sql := range []string{`UPDATE assembly_lifecycle_events SET occurred_at=now()`, `DELETE FROM assembly_lifecycle_events`, `TRUNCATE assembly_lifecycle_events`} {
		_, err := db.Exec(ctx, sql)
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "23514" {
			t.Fatalf("journal mutation was not rejected: %v", err)
		}
	}
	lifecycleCount(t, db, 1, 1)
}

func TestAssemblyLifecyclePeriodsAndAverage(t *testing.T) {
	db := homeStatisticsTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.FixedZone("Asia/Krasnoyarsk", 7*60*60))
	start, _ := homeStatisticsPeriod("today", now)
	addOrder := func(number, status string) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, `INSERT INTO assembly_order_records(source,order_number,status) VALUES('ozon',$1,$2) RETURNING id::text`, number, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	addPair := func(number, status string, at time.Time, seconds int) (string, string) {
		t.Helper()
		id := addOrder(number, status)
		cycle := warehouseID(20000 + seconds)
		if _, err := db.Exec(ctx, `INSERT INTO assembly_lifecycle_events(order_id,cycle_id,event_type,request_id,occurred_at)
			VALUES($1::uuid,$2::uuid,'started',$2::uuid,$3)`, id, cycle, at.Add(-time.Duration(seconds)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO assembly_lifecycle_events(order_id,cycle_id,event_type,operation_id,occurred_at)
			VALUES($1::uuid,$2::uuid,'completed',gen_random_uuid(),$3)`, id, cycle, at); err != nil {
			t.Fatal(err)
		}
		return id, cycle
	}
	firstID, firstCycle := addPair("FIRST", "completed", start.Add(time.Hour), 60)
	// A second cycle's completion cannot inflate the unique-order counter.
	if _, err := db.Exec(ctx, `INSERT INTO assembly_lifecycle_events(order_id,cycle_id,event_type,operation_id,occurred_at)
		VALUES($1::uuid,$2::uuid,'completed',gen_random_uuid(),$3)`, firstID, warehouseID(30002), start.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	addPair("CROSSES-MIDNIGHT", "completed", start, 120)
	addPair("WEEK", "completed", start.AddDate(0, 0, -3), 180)
	addPair("MONTH", "completed", start.AddDate(0, 0, -20), 240)
	addPair("OLD", "completed", start.AddDate(0, 0, -31), 300)
	addPair("AT-END", "completed", now, 360)
	addPair("CANCELLED", "cancelled", start.Add(time.Hour), 420)
	addPair("PROBLEM", "problem", start.Add(time.Hour), 480)
	addPair("INVALID-PAIR", "completed", start.Add(time.Hour), -30)
	id := addOrder("UNFINISHED", "in_progress")
	if _, err := db.Exec(ctx, `INSERT INTO assembly_lifecycle_events(order_id,cycle_id,event_type,request_id) VALUES($1::uuid,$2::uuid,'started',$2::uuid)`, id, warehouseID(30000)); err != nil {
		t.Fatal(err)
	}
	// A new confirmed completion of a legacy active order has no proven start.
	id = addOrder("NO-KNOWN-START", "completed")
	if _, err := db.Exec(ctx, `INSERT INTO assembly_lifecycle_events(order_id,cycle_id,event_type,operation_id,occurred_at) VALUES($1::uuid,$2::uuid,'completed',$2::uuid,$3)`, id, warehouseID(30001), start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		period string
		count  int64
		mean   float64
	}{{"today", 4, 90}, {"7d", 5, 120}, {"30d", 6, 150}} {
		from, _ := homeStatisticsPeriod(test.period, now)
		result, err := readHomeStatistics(ctx, db, from, now)
		if err != nil || result.Assembly.CompletedOrders != test.count || math.Abs(result.Assembly.AverageAssemblySeconds-test.mean) > .000001 {
			t.Fatalf("%s: %+v, want count=%d mean=%f: %v", test.period, result.Assembly, test.count, test.mean, err)
		}
	}
	// Different request/operation IDs still cannot duplicate a cycle's events.
	for _, sql := range []string{
		`INSERT INTO assembly_lifecycle_events(order_id,cycle_id,event_type,request_id) VALUES($1::uuid,$2::uuid,'started',$2::uuid)`,
		`INSERT INTO assembly_lifecycle_events(order_id,cycle_id,event_type,operation_id) VALUES($1::uuid,$2::uuid,'completed',gen_random_uuid())`,
	} {
		_, err := db.Exec(ctx, sql, firstID, firstCycle)
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "23505" {
			t.Fatalf("duplicate cycle event not rejected: %v", err)
		}
	}
}

func TestAssemblyLifecycleStartFailureRollsBack(t *testing.T) {
	db := ozonTestDB(t)
	order := realTestOrder("ROLLBACK-START")
	importWarehouseTest(t, db, 700, order)
	stockTestExec(t, db, `CREATE FUNCTION fail_lifecycle() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'lifecycle failure'; END $$;
		CREATE TRIGGER fail_lifecycle BEFORE INSERT ON assembly_lifecycle_events FOR EACH ROW EXECUTE FUNCTION fail_lifecycle()`)
	w := warehouseAction(db, 701, 0, "start", order.Number, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatal(w.Code, w.Body.String())
	}
	lifecycleCount(t, db, 0, 0)
	assertStorageCount(t, db, "assembly_order_reservations", 0)
	assertStorageCount(t, db, "assembly_preview_requests", 0)
	if warehouseState(t, db)["activeOrder"] != nil {
		t.Fatal("failed event left an active order")
	}
	stockTestExec(t, db, `DROP TRIGGER fail_lifecycle ON assembly_lifecycle_events`)
	warehouseOK(t, warehouseAction(db, 701, 0, "start", order.Number, ""))
	lifecycleCount(t, db, 1, 0)
}

func TestAssemblyLifecycleFinishFailureRollsBack(t *testing.T) {
	db := ozonTestDB(t)
	ctx := context.Background()
	before, operation, _ := finishOzonTestOrder(t, db)
	stockTestExec(t, db, `CREATE FUNCTION fail_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'completion journal failure'; END $$;
		CREATE TRIGGER fail_completion BEFORE INSERT ON assembly_lifecycle_events FOR EACH ROW WHEN(NEW.event_type='completed') EXECUTE FUNCTION fail_completion()`)
	proof := receiptFor(t, db, operation, "finish-rollback", "applied", 1)
	if err := ingestOzonDomainReceipt(ctx, db, proof); err == nil {
		t.Fatal("completion journal failure was ignored")
	}
	lifecycleCount(t, db, 1, 0)
	assertStorageCount(t, db, "assembly_ozon_observations", 0)
	if !reflect.DeepEqual(before, warehouseState(t, db)) {
		t.Fatal("failed completion event changed the workspace")
	}
	stockTestExec(t, db, `DROP TRIGGER fail_completion ON assembly_lifecycle_events`)
	if err := ingestOzonDomainReceipt(ctx, db, proof); err != nil {
		t.Fatal(err)
	}
	lifecycleCount(t, db, 1, 1)
}
