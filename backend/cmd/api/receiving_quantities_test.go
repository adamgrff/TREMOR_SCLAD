package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReceivingQuantityRetryAndCorrection(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	call := func(method, token string, quantity, expected int) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/", strings.NewReader(fmt.Sprintf(`{"quantity":%d,"expectedQuantity":%d,"requestId":"%s"}`, quantity, expected, token)))
		r.SetPathValue("sessionID", fmt.Sprint(session))
		r.SetPathValue("productID", "1")
		w := httptest.NewRecorder()
		receivingQuantityHandler(db, w, r)
		return w
	}
	token := "00000000-0000-4000-8000-000000000001"
	for i := 0; i < 2; i++ {
		requireReceivingStatus(t, call(http.MethodPost, token, 10, 0), http.StatusNoContent)
	}
	details := receivingDetails(t, db, session)
	if len(details.PendingItems) != 1 || details.PendingItems[0].Quantity != 10 || receivingStock(t, db) != 0 {
		t.Fatalf("duplicate or changed stock: %+v", details)
	}
	requireReceivingStatus(t, call(http.MethodPost, token, 11, 0), http.StatusConflict)
	token = "00000000-0000-4000-8000-000000000002"
	for i := 0; i < 2; i++ {
		requireReceivingStatus(t, call(http.MethodPut, token, 6, 10), http.StatusNoContent)
	}
	requireReceivingStatus(t, call(http.MethodPut, "00000000-0000-4000-8000-000000000003", 5, 10), http.StatusConflict)
	details = receivingDetails(t, db, session)
	if details.PendingItems[0].Quantity != 6 || receivingStock(t, db) != 0 {
		t.Fatal("correction failed")
	}
	receivingPlace(t, db, session, 6)
	requireReceivingStatus(t, call(http.MethodPut, "00000000-0000-4000-8000-000000000004", 5, 6), http.StatusConflict)
}

func TestCatalogCreateAndReceivingQuantityAtomicRetry(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	body := fmt.Sprintf(`{"name":"New","sku":"NEW","categoryId":1,"receivingQuantity":10,"sessionId":%d,"requestId":"00000000-0000-4000-8000-000000000001"}`, session)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		saveCatalogProductHandler(db, w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
	}
	details := receivingDetails(t, db, session)
	if len(details.PendingItems) != 1 || details.PendingItems[0].Quantity != 10 || receivingStock(t, db) != 0 {
		t.Fatalf("retry doubled quantity: %+v", details)
	}
	body = `{"name":"Rollback","sku":"ROLLBACK","categoryId":1,"receivingQuantity":10,"sessionId":999999,"requestId":"00000000-0000-4000-8000-000000000002"}`
	w := httptest.NewRecorder()
	saveCatalogProductHandler(db, w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	requireReceivingStatus(t, w, http.StatusConflict)
}
