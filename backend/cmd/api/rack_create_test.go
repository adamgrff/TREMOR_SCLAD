package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
)

func TestCreateRackWithIndependentCellCodes(t *testing.T) {
	db := receivingTestDB(t)
	migration, err := os.ReadFile("../../migrations/010_rack_cell_codes.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(context.Background(), string(migration)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Rack one", "Rack two"} {
		response := receivingRequest(db, createRackHandler, fmt.Sprintf(`{"name":%q,"counts":[2,0,1]}`, name), "", 0)
		requireReceivingStatus(t, response, http.StatusCreated)
		var rack rackResponse
		if err = json.Unmarshal(response.Body.Bytes(), &rack); err != nil {
			t.Fatal(err)
		}
		if len(rack.Rows) != 2 || rack.Rows[0][1] != "A2" || rack.Rows[1][0] != "C1" {
			t.Fatalf("rack: %+v", rack)
		}
	}
	response := receivingRequest(db, createRackHandler, `{"name":"rack ONE","counts":[1,1,1]}`, "", 0)
	requireReceivingStatus(t, response, http.StatusConflict)
	response = receivingRequest(db, createRackHandler, `{"name":"Empty","counts":[0,0,0]}`, "", 0)
	requireReceivingStatus(t, response, http.StatusBadRequest)
	session := receivingSession(t, db)
	_, err = db.Exec(context.Background(), `INSERT INTO products(category_id,sku,name) VALUES(2,'RACK-SKU','Rack product')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(context.Background(), `INSERT INTO receiving_session_items(session_id,product_id,quantity) VALUES($1,2,4)`, session)
	if err != nil {
		t.Fatal(err)
	}
	response = receivingRequest(db, createReceivingPlacementHandler, fmt.Sprintf(`{"sessionId":%d,"cellCode":"A1","items":[{"sku":"RACK-SKU","quantity":4}]}`, session), "", 0)
	requireReceivingStatus(t, response, http.StatusOK)
	var categoryID int64
	if err = db.QueryRow(context.Background(), `SELECT cells.category_id FROM cell_stock JOIN cells ON cells.id=cell_stock.cell_id WHERE product_id=2`).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	if categoryID != 2 {
		t.Fatalf("wrong rack: %d", categoryID)
	}
}
