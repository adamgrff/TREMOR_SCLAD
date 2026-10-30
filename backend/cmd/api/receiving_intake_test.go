package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func requireIntakeTotals(t *testing.T, db *pgxpool.Pool, count, quantity int64) {
	t.Helper()
	var gotCount, gotQuantity int64
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*),COALESCE(SUM(quantity),0)::bigint FROM receiving_intake_events`).Scan(&gotCount, &gotQuantity); err != nil {
		t.Fatal(err)
	}
	if gotCount != count || gotQuantity != quantity {
		t.Fatalf("intake count/quantity=%d/%d, want %d/%d", gotCount, gotQuantity, count, quantity)
	}
}

func TestReceivingIntakeJournalLifecycle(t *testing.T) {
	db := homeStatisticsTestDB(t)
	ctx := context.Background()
	session, other := receivingSession(t, db), receivingSession(t, db)
	receivingAdd(t, db, session, 5)
	receivingAdd(t, db, session, 3)
	requireIntakeTotals(t, db, 2, 8)
	if _, err := db.Exec(ctx, `INSERT INTO products(category_id,sku,name) VALUES(1,'SECOND','Second SKU')`); err != nil {
		t.Fatal(err)
	}
	w := receivingRequest(db, addReceivingSessionItemHandler, `{"sku":"SECOND","quantity":4,"requestId":"20000000-0000-4000-8000-000000000001"}`, "sessionID", session)
	requireReceivingStatus(t, w, http.StatusOK)
	receivingAdd(t, db, other, 7)
	requireIntakeTotals(t, db, 4, 19)
	checkStatistics := func() {
		t.Helper()
		requestedAt := time.Now()
		w := httptest.NewRecorder()
		homeStatisticsHandler(db, w, httptest.NewRequest(http.MethodGet, "/api/home/statistics?period=today", nil))
		requireReceivingStatus(t, w, http.StatusOK)
		var result homeStatisticsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Receiving.ReceivedQuantity != 19 {
			var databaseNow time.Time
			var events string
			_ = db.QueryRow(ctx, `SELECT clock_timestamp(),COALESCE(jsonb_agg(jsonb_build_object('quantity',quantity,'occurredAt',occurred_at))::text,'[]') FROM receiving_intake_events`).Scan(&databaseNow, &events)
			t.Fatalf("received quantity=%d; Go request=%s; Go now=%s; PostgreSQL now=%s; events=%s", result.Receiving.ReceivedQuantity, requestedAt.Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano), databaseNow.Format(time.RFC3339Nano), events)
		}
	}
	checkStatistics()
	w = receivingRequest(db, createReceivingPlacementHandler, fmt.Sprintf(`{"sessionId":%d,"cellCode":"TEST-1","items":[{"sku":"TEST-SKU","quantity":8},{"sku":"SECOND","quantity":4}]}`, session), "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	var placement receivingPlacementResponse
	if err := json.Unmarshal(w.Body.Bytes(), &placement); err != nil {
		t.Fatal(err)
	}
	requireIntakeTotals(t, db, 4, 19)
	checkStatistics()
	w = receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", placement.ID)
	requireReceivingStatus(t, w, http.StatusOK)
	requireIntakeTotals(t, db, 4, 19)
	// Deleting a current pending SKU is not a reversal of its intake history.
	r := httptest.NewRequest(http.MethodDelete, "/", strings.NewReader(fmt.Sprintf(`{"requestId":%q,"expectedRevision":%q}`, newReceivingScanID(t), receivingDetails(t, db, session).PendingRevision)))
	r.SetPathValue("sessionID", fmt.Sprint(session))
	r.SetPathValue("sku", "TEST-SKU")
	w = httptest.NewRecorder()
	deleteReceivingSessionItemHandler(db, w, r)
	requireReceivingStatus(t, w, http.StatusOK)
	requireIntakeTotals(t, db, 4, 19)
	checkStatistics()
	for _, sql := range []string{
		`UPDATE receiving_intake_events SET quantity=100`,
		`UPDATE receiving_intake_events SET occurred_at=now()-interval '1 day'`,
		`DELETE FROM receiving_intake_events`, `TRUNCATE receiving_intake_events`,
	} {
		_, err := db.Exec(ctx, sql)
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "23514" {
			t.Fatalf("journal modification must be rejected: %s: %v", sql, err)
		}
	}
	requireIntakeTotals(t, db, 4, 19)
}

func TestReceivingIntakePeriodBoundaries(t *testing.T) {
	db := homeStatisticsTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.FixedZone("Asia/Krasnoyarsk", 7*60*60))
	start, _ := homeStatisticsPeriod("today", now)
	session := receivingSession(t, db)
	for _, event := range []struct {
		at       time.Time
		quantity int64
	}{
		{start.AddDate(0, 0, -31), 1000}, {start.AddDate(0, 0, -20), 30},
		{start.AddDate(0, 0, -6), 7}, {start.Add(-time.Microsecond), 3},
		{start, 5}, {now.Add(-time.Microsecond), 2}, {now, 500}, {now.Add(time.Hour), 900},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO receiving_intake_events(session_id,product_id,quantity,occurred_at) VALUES($1,1,$2,$3)`, session, event.quantity, event.at); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		period   string
		quantity int64
	}{{"today", 7}, {"7d", 17}, {"30d", 47}} {
		from, _ := homeStatisticsPeriod(test.period, now)
		result, err := readHomeStatistics(ctx, db, from, now)
		if err != nil {
			t.Fatal(err)
		}
		if result.Receiving.ReceivedQuantity != test.quantity {
			t.Fatalf("period %s received=%d, want %d", test.period, result.Receiving.ReceivedQuantity, test.quantity)
		}
	}
}

func TestReceivingIntakeMigrationPreservesLegacyData(t *testing.T) {
	db := receivingTestDBWithIntake(t, false)
	ctx := context.Background()
	session := receivingSession(t, db)
	if _, err := db.Exec(ctx, `INSERT INTO receiving_session_items(session_id,product_id,quantity) VALUES($1,1,9)`, session); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO cell_stock(cell_id,product_id,quantity) VALUES(1,1,11)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/020_receiving_intake_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	requireIntakeTotals(t, db, 0, 0)
	if receivingStock(t, db) != 11 || receivingDetails(t, db, session).PendingItems[0].Quantity != 9 {
		t.Fatal("migration changed legacy data")
	}
	receivingAdd(t, db, session, 5)
	requireIntakeTotals(t, db, 1, 5)
}

func TestReceivingIntakeWriteFailureRollsBack(t *testing.T) {
	db := receivingTestDB(t)
	ctx := context.Background()
	session := receivingSession(t, db)
	if _, err := db.Exec(ctx, `CREATE FUNCTION fail_intake() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'intake failure'; END $$;
		CREATE TRIGGER fail_intake BEFORE INSERT ON receiving_intake_events FOR EACH ROW EXECUTE FUNCTION fail_intake()`); err != nil {
		t.Fatal(err)
	}
	w := receivingRequest(db, addReceivingSessionItemHandler, `{"sku":"TEST-SKU","quantity":5,"requestId":"20000000-0000-4000-8000-000000000001"}`, "sessionID", session)
	requireReceivingStatus(t, w, http.StatusInternalServerError)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"quantity":3,"requestId":"00000000-0000-4000-8000-000000000099"}`))
	r.SetPathValue("sessionID", fmt.Sprint(session))
	r.SetPathValue("productID", "1")
	w = httptest.NewRecorder()
	receivingQuantityHandler(db, w, r)
	requireReceivingStatus(t, w, http.StatusConflict)
	w = receivingRequest(db, saveCatalogProductHandler, fmt.Sprintf(`{"name":"Rollback","sku":"ROLLBACK","categoryId":1,"receivingQuantity":2,"sessionId":%d,"requestId":"00000000-0000-4000-8000-000000000098"}`, session), "", 0)
	requireReceivingStatus(t, w, http.StatusInternalServerError)
	requireIntakeTotals(t, db, 0, 0)
	if len(receivingDetails(t, db, session).PendingItems) != 0 {
		t.Fatal("failed journal insert changed current pending quantity")
	}
	var artifacts int
	if err := db.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM receiving_quantity_requests)+(SELECT COUNT(*) FROM products WHERE sku='ROLLBACK')`).Scan(&artifacts); err != nil || artifacts != 0 {
		t.Fatalf("failed intake left retry receipt or product: %d, %v", artifacts, err)
	}
}
