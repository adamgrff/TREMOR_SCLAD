package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMutationBodiesRejectTrailingDataWithoutWrites(t *testing.T) {
	db := receivingTestDB(t)
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 1)
	placement := receivingPlace(t, db, session, 1)
	cases := []struct {
		name    string
		handler receivingTestHandler
		body    string
	}{
		{"catalog", saveCatalogProductHandler, `{"name":"Unexpected product","sku":"UNEXPECTED","categoryId":1}`},
		{"rack", createRackHandler, `{"name":"Unexpected rack","counts":[1,0,0]}`},
		{"placement", createReceivingPlacementHandler, receivingPlaceBody(session, 1)},
		{"finish", finishReceivingHandler, fmt.Sprintf(`{"sessionId":%d,"placementIds":[%d]}`, session, placement)},
		{"add-item", addReceivingSessionItemHandler, `{"sku":"TEST-SKU","quantity":1}`},
		{"quantity", receivingQuantityHandler, `{"quantity":1,"requestId":"00000000-0000-4000-8000-000000000001"}`},
	}
	for _, tc := range cases {
		for _, suffix := range []string{` {}`, ` invalid`, strings.Repeat(" ", 1<<20)} {
			t.Run(tc.name+fmt.Sprint(len(suffix)), func(t *testing.T) {
				r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body+suffix))
				r.SetPathValue("sessionID", fmt.Sprint(session))
				r.SetPathValue("productID", "1")
				w := httptest.NewRecorder()
				tc.handler(db, w, r)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("body suffix accepted: HTTP %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
	if receivingStock(t, db) != 1 {
		t.Fatal("invalid body changed stock")
	}
	var products, racks, placements, pending int
	err := db.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM products),(SELECT count(*) FROM categories),(SELECT count(*) FROM receiving_placements),(SELECT count(*) FROM receiving_session_items)`).Scan(&products, &racks, &placements, &pending)
	if err != nil || products != 1 || racks != 1 || placements != 1 || pending != 0 {
		t.Fatal("invalid body wrote warehouse data", err, products, racks, placements, pending)
	}
	if receivingDetails(t, db, session).Status != "active" {
		t.Fatal("invalid body completed receiving")
	}
}
