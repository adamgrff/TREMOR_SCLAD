package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReceivingPendingDeletionIsScopedAndIdempotent(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 2)
	if _, err := db.Exec(context.Background(), `INSERT INTO products (category_id, sku, name) VALUES (1, 'OTHER', 'Other')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO receiving_session_items (session_id, product_id, quantity) VALUES ($1, 2, 3)`, session); err != nil {
		t.Fatal(err)
	}
	remove := func(sku string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "/", nil)
		r.SetPathValue("sessionID", fmt.Sprint(session))
		r.SetPathValue("sku", sku)
		w := httptest.NewRecorder()
		deleteReceivingSessionItemHandler(db, w, r)
		return w
	}
	for i := 0; i < 2; i++ {
		requireReceivingStatus(t, remove("TEST-SKU"), http.StatusOK)
	}
	details := receivingDetails(t, db, session)
	if len(details.PendingItems) != 1 || details.PendingItems[0].SKU != "OTHER" || details.PendingItems[0].Quantity != 3 || receivingStock(t, db) != 0 {
		t.Fatalf("unrelated pending items or stock changed: %+v", details)
	}
	requireReceivingStatus(t, remove("OTHER"), http.StatusOK)
	if _, err := db.Exec(context.Background(), `UPDATE receiving_sessions SET status = 'completed', completed_at = NOW() WHERE id = $1`, session); err != nil {
		t.Fatal(err)
	}
	requireReceivingStatus(t, remove("OTHER"), http.StatusConflict)
}
