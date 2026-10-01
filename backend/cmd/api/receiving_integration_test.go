package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Tests use only their own schema. TEST_DATABASE_URL must point to a test database.
func receivingTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("receiving_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("clean test schema: %v", err)
		}
		admin.Close(ctx)
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 4
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	for _, name := range []string{"001_init.sql", "003_receiving_placements.sql", "004_receiving_sessions.sql", "005_receiving_drafts.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	_, err = db.Exec(ctx, `
		INSERT INTO warehouses (name) VALUES ('Test');
		INSERT INTO categories (code, name) VALUES ('TEST', 'Test');
		INSERT INTO cells (warehouse_id, category_id, code, row_number, position_number)
		VALUES (1, 1, 'TEST-1', 1, 1);
		INSERT INTO products (category_id, sku, name) VALUES (1, 'TEST-SKU', 'Test product');
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

type receivingTestHandler func(*pgxpool.Pool, http.ResponseWriter, *http.Request)

func receivingRequest(db *pgxpool.Pool, handler receivingTestHandler, body string, pathKey string, id int64) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if pathKey != "" {
		r.SetPathValue(pathKey, fmt.Sprint(id))
	}
	w := httptest.NewRecorder()
	handler(db, w, r)
	return w
}

func requireReceivingStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, status, w.Body.String())
	}
}

func receivingSession(t *testing.T, db *pgxpool.Pool) int64 {
	t.Helper()
	w := receivingRequest(db, createReceivingSessionHandler, "", "", 0)
	requireReceivingStatus(t, w, http.StatusCreated)
	var response createReceivingSessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.ID
}

func receivingAdd(t *testing.T, db *pgxpool.Pool, session int64, quantity int) {
	t.Helper()
	w := receivingRequest(db, addReceivingSessionItemHandler, fmt.Sprintf(`{"sku":"TEST-SKU","quantity":%d}`, quantity), "sessionID", session)
	requireReceivingStatus(t, w, http.StatusOK)
}

func receivingPlaceBody(session int64, quantity int) string {
	return fmt.Sprintf(`{"sessionId":%d,"cellCode":"TEST-1","items":[{"sku":"TEST-SKU","quantity":%d}]}`, session, quantity)
}

func receivingPlace(t *testing.T, db *pgxpool.Pool, session int64, quantity int) int64 {
	t.Helper()
	w := receivingRequest(db, createReceivingPlacementHandler, receivingPlaceBody(session, quantity), "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	var response receivingPlacementResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.ID
}

func receivingDetails(t *testing.T, db *pgxpool.Pool, session int64) receivingSessionDetailsResponse {
	t.Helper()
	w := receivingRequest(db, getReceivingSessionHandler, "", "sessionID", session)
	requireReceivingStatus(t, w, http.StatusOK)
	var response receivingSessionDetailsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func receivingStock(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var quantity int
	if err := db.QueryRow(context.Background(), `SELECT COALESCE(SUM(quantity), 0)::int FROM cell_stock`).Scan(&quantity); err != nil {
		t.Fatal(err)
	}
	return quantity
}

func TestReceivingLifecycle(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 3)
	receivingAdd(t, db, session, 2)
	details := receivingDetails(t, db, session)
	if details.Status != "active" || len(details.PendingItems) != 1 || details.PendingItems[0].Quantity != 5 || len(details.PlacedItems) != 0 {
		t.Fatalf("restored draft: %+v", details)
	}
	// A stale tab cannot place a different group or change stock.
	w := receivingRequest(db, createReceivingPlacementHandler, receivingPlaceBody(session, 4), "", 0)
	requireReceivingStatus(t, w, http.StatusConflict)
	if receivingStock(t, db) != 0 {
		t.Fatal("rejected placement changed stock")
	}
	placement := receivingPlace(t, db, session, 5)
	details = receivingDetails(t, db, session)
	if len(details.PendingItems) != 0 || len(details.PlacedItems) != 1 || details.PlacedItems[0].Quantity != 5 || receivingStock(t, db) != 5 {
		t.Fatalf("restored placement: %+v", details)
	}
	w = receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", placement)
	requireReceivingStatus(t, w, http.StatusOK)
	w = receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", placement)
	requireReceivingStatus(t, w, http.StatusConflict)
	details = receivingDetails(t, db, session)
	if len(details.PendingItems) != 1 || details.PendingItems[0].Quantity != 5 || len(details.PlacedItems) != 0 || receivingStock(t, db) != 0 {
		t.Fatalf("undo did not restore exactly once: %+v", details)
	}
	w = receivingRequest(db, finishReceivingHandler, fmt.Sprintf(`{"sessionId":%d,"placementIds":[%d]}`, session, placement), "", 0)
	requireReceivingStatus(t, w, http.StatusConflict)
	placement = receivingPlace(t, db, session, 5)
	w = receivingRequest(db, finishReceivingHandler, fmt.Sprintf(`{"sessionId":%d,"placementIds":[%d]}`, session, placement+1), "", 0)
	requireReceivingStatus(t, w, http.StatusConflict)
	finishBody := fmt.Sprintf(`{"sessionId":%d,"placementIds":[%d]}`, session, placement)
	w = receivingRequest(db, finishReceivingHandler, finishBody, "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	w = receivingRequest(db, finishReceivingHandler, finishBody, "", 0)
	requireReceivingStatus(t, w, http.StatusConflict)
	w = receivingRequest(db, addReceivingSessionItemHandler, `{"sku":"TEST-SKU","quantity":1}`, "sessionID", session)
	requireReceivingStatus(t, w, http.StatusConflict)
	w = receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", placement)
	requireReceivingStatus(t, w, http.StatusConflict)
	details = receivingDetails(t, db, session)
	if details.Status != "completed" || details.CompletedAt == nil || receivingStock(t, db) != 5 {
		t.Fatalf("completed session changed: %+v", details)
	}
	// New active sessions must not hide the last completed receiving.
	active := receivingSession(t, db)
	receivingAdd(t, db, active, 1)
	receivingPlace(t, db, active, 1)
	w = receivingRequest(db, latestReceivingHandler, "", "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	var latest latestReceivingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if latest.ID != session || len(latest.Items) != 1 || latest.Items[0].PlacementID != placement || latest.Items[0].Quantity != 5 {
		t.Fatalf("latest completed receiving: %+v", latest)
	}
	w = receivingRequest(db, completedReceivingHistoryHandler, "", "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	var history completedReceivingHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Sessions) != 1 || history.NextCursor != nil {
		t.Fatalf("history included active session: %+v", history)
	}
	summary := history.Sessions[0]
	if summary.ID != session || summary.Quantity != 5 || summary.PlacementCount != 1 || summary.ProductCount != 1 || summary.CellCount != 1 {
		t.Fatalf("history included undone placement or incorrect totals: %+v", summary)
	}
}

func TestReceivingHistoryPagination(t *testing.T) {
	db := receivingTestDB(t)
	_, err := db.Exec(context.Background(), `
		INSERT INTO receiving_sessions (status, completed_at)
		SELECT 'completed', '2026-10-01T00:00:00Z'::timestamptz FROM generate_series(1, 22)
	`)
	if err != nil {
		t.Fatal(err)
	}
	load := func(url string) completedReceivingHistoryResponse {
		t.Helper()
		w := httptest.NewRecorder()
		completedReceivingHistoryHandler(db, w, httptest.NewRequest(http.MethodGet, url, nil))
		requireReceivingStatus(t, w, http.StatusOK)
		var page completedReceivingHistoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := load("/")
	if len(page.Sessions) != 20 || page.NextCursor == nil || *page.NextCursor != 3 {
		t.Fatalf("first page: %+v", page)
	}
	for index, session := range page.Sessions {
		if session.ID != int64(22-index) || session.Quantity != 0 {
			t.Fatalf("unstable order or empty-session totals: %+v", session)
		}
	}
	// A newly completed session must not shift or duplicate the older page.
	if _, err := db.Exec(context.Background(), `INSERT INTO receiving_sessions (status, completed_at) VALUES ('completed', NOW())`); err != nil {
		t.Fatal(err)
	}
	page = load("/?before=3")
	if len(page.Sessions) != 2 || page.Sessions[0].ID != 2 || page.Sessions[1].ID != 1 || page.NextCursor != nil {
		t.Fatalf("older page: %+v", page)
	}
}

func TestReceivingHistoryInvalidCursor(t *testing.T) {
	for _, value := range []string{"0", "-1", "abc", "9223372036854775808"} {
		t.Run(value, func(t *testing.T) {
			w := httptest.NewRecorder()
			completedReceivingHistoryHandler(nil, w, httptest.NewRequest(http.MethodGet, "/?before="+value, nil))
			requireReceivingStatus(t, w, http.StatusBadRequest)
		})
	}
}

func TestLatestReceivingWithoutCompletedSession(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 1)
	receivingPlace(t, db, session, 1)
	w := receivingRequest(db, latestReceivingHandler, "", "", 0)
	requireReceivingStatus(t, w, http.StatusNotFound)
}

func TestReceivingConcurrentPlacement(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 5)
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- receivingRequest(db, createReceivingPlacementHandler, receivingPlaceBody(session, 5), "", 0)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	counts := map[int]int{}
	for response := range results {
		counts[response.Code]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 || receivingStock(t, db) != 5 {
		t.Fatalf("concurrent placement: statuses=%v stock=%d", counts, receivingStock(t, db))
	}
	details := receivingDetails(t, db, session)
	if len(details.PlacedItems) != 1 || len(details.PendingItems) != 0 {
		t.Fatalf("concurrent placement duplicated group: %+v", details)
	}
}

func TestReceivingUndoInsufficientStock(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 5)
	placement := receivingPlace(t, db, session, 5)
	if _, err := db.Exec(context.Background(), `UPDATE cell_stock SET quantity = 4`); err != nil {
		t.Fatal(err)
	}
	w := receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", placement)
	requireReceivingStatus(t, w, http.StatusConflict)
	details := receivingDetails(t, db, session)
	if receivingStock(t, db) != 4 || len(details.PendingItems) != 0 || len(details.PlacedItems) != 1 {
		t.Fatalf("failed undo partially changed state: %+v", details)
	}
}
