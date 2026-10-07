package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReceivingHistoryDeletionPreservesStock(t *testing.T) {
	db := receivingTestDB(t)
	complete := func(quantity int) int64 {
		t.Helper()
		session := receivingSession(t, db)
		receivingAdd(t, db, session, quantity)
		placement := receivingPlace(t, db, session, quantity)
		w := receivingRequest(db, finishReceivingHandler, fmt.Sprintf(`{"sessionId":%d,"placementIds":[%d]}`, session, placement), "", 0)
		requireReceivingStatus(t, w, http.StatusOK)
		return session
	}
	first := complete(1)
	second := complete(2)
	active := receivingSession(t, db)
	receivingAdd(t, db, active, 4)
	w := receivingRequest(db, deleteReceivingHistoryHandler, "", "sessionID", second)
	requireReceivingStatus(t, w, http.StatusNoContent)
	var deletedAt time.Time
	if err := db.QueryRow(context.Background(), `SELECT deleted_at FROM receiving_sessions WHERE id = $1`, second).Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	w = receivingRequest(db, deleteReceivingHistoryHandler, "", "sessionID", second)
	requireReceivingStatus(t, w, http.StatusNoContent)
	var retryDeletedAt time.Time
	if err := db.QueryRow(context.Background(), `SELECT deleted_at FROM receiving_sessions WHERE id = $1`, second).Scan(&retryDeletedAt); err != nil {
		t.Fatal(err)
	}
	if !deletedAt.Equal(retryDeletedAt) || receivingStock(t, db) != 3 {
		t.Fatal("deletion/retry changed stock or original deletion timestamp")
	}
	var placementCount, placementQuantity int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*), COALESCE(SUM(items.quantity), 0)::int
		FROM receiving_placements AS placements
		JOIN receiving_placement_items AS items ON items.placement_id = placements.id
		WHERE placements.receiving_session_id = $1 AND placements.status = 'active'`, second).Scan(&placementCount, &placementQuantity); err != nil {
		t.Fatal(err)
	}
	if placementCount != 1 || placementQuantity != 2 {
		t.Fatal("history deletion removed placement audit records")
	}
	w = receivingRequest(db, completedReceivingHistoryHandler, "", "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	var history completedReceivingHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Sessions) != 1 || history.Sessions[0].ID != first {
		t.Fatalf("deleted receiving is still visible: %+v", history)
	}
	w = receivingRequest(db, latestReceivingHandler, "", "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	var latest latestReceivingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if latest.ID != first {
		t.Fatalf("latest handler restored a deleted receiving: %+v", latest)
	}
	w = receivingRequest(db, getReceivingSessionHandler, "", "sessionID", second)
	requireReceivingStatus(t, w, http.StatusNotFound)
	w = receivingRequest(db, deleteReceivingHistoryHandler, "", "sessionID", active)
	requireReceivingStatus(t, w, http.StatusConflict)
	details := receivingDetails(t, db, active)
	if details.Status != "active" || len(details.PendingItems) != 1 || details.PendingItems[0].Quantity != 4 {
		t.Fatalf("active session changed: %+v", details)
	}
	w = receivingRequest(db, deleteReceivingHistoryHandler, "", "sessionID", first)
	requireReceivingStatus(t, w, http.StatusNoContent)
	w = receivingRequest(db, latestReceivingHandler, "", "", 0)
	requireReceivingStatus(t, w, http.StatusNotFound)
	w = receivingRequest(db, completedReceivingHistoryHandler, "", "", 0)
	requireReceivingStatus(t, w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Sessions) != 0 || history.NextCursor != nil || receivingStock(t, db) != 3 {
		t.Fatalf("empty history after deletion: %+v", history)
	}
	w = receivingRequest(db, deleteReceivingHistoryHandler, "", "sessionID", 999999)
	requireReceivingStatus(t, w, http.StatusNotFound)
}

func TestReceivingHistoryDeletionRejectsInvalidID(t *testing.T) {
	for _, id := range []string{"0", "-1", "abc", "9223372036854775808"} {
		r := httptest.NewRequest(http.MethodDelete, "/", nil)
		r.SetPathValue("sessionID", id)
		w := httptest.NewRecorder()
		deleteReceivingHistoryHandler(nil, w, r)
		requireReceivingStatus(t, w, http.StatusBadRequest)
	}
}
