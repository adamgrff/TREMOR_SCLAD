package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestReceivingPlacementUsesProductRack(t *testing.T) {
	db := receivingTestDB(t)
	_, err := db.Exec(context.Background(), `
 INSERT INTO categories(code,name) VALUES ('OTHER','Other rack');
 INSERT INTO cells(warehouse_id,category_id,code,row_number,position_number) VALUES(1,2,'OTHER-1',1,1);
 `)
	if err != nil {
		t.Fatal(err)
	}
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 3)
	wrong := receivingRequest(db, createReceivingPlacementHandler,
		`{"sessionId":`+strconv.FormatInt(session, 10)+`,"cellCode":"OTHER-1","items":[{"sku":"TEST-SKU","quantity":3}]}`, "", 0)
	requireReceivingStatus(t, wrong, http.StatusNotFound)
	if receivingStock(t, db) != 0 {
		t.Fatal("wrong rack changed stock")
	}
	receivingPlace(t, db, session, 3)
	request := httptest.NewRequest(http.MethodGet, "/api/cells/TEST-1?categoryId=1", nil)
	request.SetPathValue("cellName", "TEST-1")
	response := httptest.NewRecorder()
	cellHandler(db, response, request)
	requireReceivingStatus(t, response, http.StatusOK)
	var items []cellItem
	if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SKU != "TEST-SKU" || items[0].Quantity != 3 {
		t.Fatalf("cell contents: %+v", items)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/cells/TEST-1?categoryId=2", nil)
	request.SetPathValue("cellName", "TEST-1")
	response = httptest.NewRecorder()
	cellHandler(db, response, request)
	requireReceivingStatus(t, response, http.StatusNotFound)
	response = httptest.NewRecorder()
	racksHandler(db, response, httptest.NewRequest(http.MethodGet, "/api/racks", nil))
	requireReceivingStatus(t, response, http.StatusOK)
	var racks []rackResponse
	if err := json.Unmarshal(response.Body.Bytes(), &racks); err != nil {
		t.Fatal(err)
	}
	if len(racks) != 2 || len(racks[0].Rows) != 1 || racks[0].Rows[0][0] != "TEST-1" {
		t.Fatalf("racks: %+v", racks)
	}
}
