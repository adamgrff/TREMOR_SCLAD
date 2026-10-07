package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductCatalogPersistenceAndArchive(t *testing.T) {
	db := receivingTestDB(t)
	save := func(method, id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/", strings.NewReader(body))
		r.SetPathValue("productID", id)
		w := httptest.NewRecorder()
		saveCatalogProductHandler(db, w, r)
		return w
	}
	body := `{"name":"New product","sku":" new-code ","categoryId":1}`
	requireReceivingStatus(t, save(http.MethodPost, "", body), http.StatusCreated)
	requireReceivingStatus(t, save(http.MethodPost, "", body), http.StatusConflict)
	requireReceivingStatus(t, save(http.MethodPut, "2", `{"name":"Edited","sku":"NEW-CODE","categoryId":1,"archived":true}`), http.StatusOK)
	w := httptest.NewRecorder()
	productCatalogHandler(db, w, httptest.NewRequest(http.MethodGet, "/", nil))
	requireReceivingStatus(t, w, http.StatusOK)
	var catalog struct {
		Products []catalogProduct `json:"products"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Products) != 2 || catalog.Products[0].Name != "Edited" || !catalog.Products[0].Archived {
		t.Fatalf("catalog did not persist: %+v", catalog)
	}
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 1)
	requireReceivingStatus(t, save(http.MethodPut, "1", `{"name":"Test product","sku":"TEST-SKU","categoryId":1,"archived":true}`), http.StatusConflict)
	receivingPlace(t, db, session, 1)
	requireReceivingStatus(t, save(http.MethodPut, "1", `{"name":"Test product","sku":"TEST-SKU","categoryId":1,"archived":true}`), http.StatusOK)
	if receivingStock(t, db) != 1 {
		t.Fatal("archiving changed stock")
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetPathValue("sku", "TEST-SKU")
	productStockHandler(db, w, r)
	requireReceivingStatus(t, w, http.StatusNotFound)
	requireReceivingStatus(t, save(http.MethodPut, "2", `{"name":"Edited","sku":"NEW-CODE","categoryId":1,"archived":false}`), http.StatusOK)
}
