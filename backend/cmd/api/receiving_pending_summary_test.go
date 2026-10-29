package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReceivingPendingSummary(t *testing.T) {
	db := receivingTestDB(t)
	ctx := context.Background()
	check := func(expected int64) {
		t.Helper()
		w := httptest.NewRecorder()
		receivingPendingSummaryHandler(db, w, httptest.NewRequest(http.MethodGet, "/api/receiving/pending-summary", nil))
		requireReceivingStatus(t, w, http.StatusOK)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("unexpected headers: %v", w.Header())
		}
		var response map[string]int64
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		quantity, exists := response["pendingQuantity"]
		if !exists || len(response) != 1 || quantity != expected {
			t.Fatalf("response = %s, want only pendingQuantity=%d", w.Body.String(), expected)
		}
	}
	check(0)
	first, second := receivingSession(t, db), receivingSession(t, db)
	empty := receivingSession(t, db)
	check(0)
	receivingAdd(t, db, first, 5)
	receivingAdd(t, db, second, 7)
	check(12) // Count units across sessions, not sessions or SKU rows.
	// Defensive fixture: even if a completed session retains an item row,
	// its quantity must never enter the active-session summary.
	completed := receivingSession(t, db)
	receivingAdd(t, db, completed, 99)
	if _, err := db.Exec(ctx, `UPDATE receiving_sessions SET status='completed', completed_at=now() WHERE id=$1`, completed); err != nil {
		t.Fatal(err)
	}
	check(12)
	placement := receivingPlace(t, db, first, 5)
	check(7)
	w := receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", placement)
	requireReceivingStatus(t, w, http.StatusOK)
	check(12)
	// A valid aggregate can exceed a 32-bit quantity while each item fits.
	receivingAdd(t, db, empty, 1)
	if _, err := db.Exec(ctx, `UPDATE receiving_session_items SET quantity=1000000000 WHERE session_id IN ($1,$2,$3)`, first, second, empty); err != nil {
		t.Fatal(err)
	}
	check(3000000000)
}
