package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func fixtureDeployment(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `SELECT deployment_id::text FROM deployment_identity WHERE singleton`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestReceivingUIReadSnapshotsCarryDeployment(t *testing.T) {
	for i := 0; i < 2; i++ {
		db := receivingTestDB(t)
		identity := fixtureDeployment(t, db)
		session := receivingSession(t, db)
		receivingAdd(t, db, session, 1)
		placement := receivingPlace(t, db, session, 1)
		requireReceivingStatus(t, receivingRequest(db, finishReceivingHandler,
			fmt.Sprintf(`{"sessionId":%d,"placementIds":[%d]}`, session, placement), "", 0), 200)
		before := receivingOperationFingerprint(t, db)
		for _, handler := range []receivingTestHandler{productCatalogHandler, completedReceivingHistoryHandler} {
			w := httptest.NewRecorder()
			handler(db, w, httptest.NewRequest("GET", "/", nil))
			requireReceivingStatus(t, w, 200)
			var response struct {
				DeploymentID string                      `json:"deploymentId"`
				Products     []catalogProduct            `json:"products"`
				Sessions     []completedReceivingSummary `json:"sessions"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.DeploymentID != identity || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unbound/cached UI snapshot")
			}
			if len(response.Products)+len(response.Sessions) != 1 {
				t.Fatal("expected one colliding-ID UI record")
			}
		}
		if receivingOperationFingerprint(t, db) != before {
			t.Fatal("UI reads changed warehouse data/receipts")
		}
	}
}

func TestDeploymentIdentityAllReceivingMutations(t *testing.T) {
	db := receivingTestDB(t)
	other := receivingTestDB(t)
	id := fixtureDeployment(t, db)
	foreign := fixtureDeployment(t, other)
	if id == foreign {
		t.Fatal("independent fixtures share identity")
	}
	session := receivingSession(t, db)
	token := newReceivingScanID(t)
	requireReceivingStatus(t, scanRequest(db, session, token, "TEST-SKU", 1), 200)
	if details := receivingDetails(t, db, session); details.DeploymentID != id {
		t.Fatal("snapshot identity mismatch")
	}
	productsHash := func() string {
		var v string
		if err := db.QueryRow(context.Background(), `SELECT md5(coalesce(jsonb_agg(to_jsonb(p) ORDER BY id)::text,'')) FROM products p`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	productsBefore := productsHash()
	before := receivingOperationFingerprint(t, db)
	cases := []struct {
		name, method, body string
		handler            receivingTestHandler
		key                string
		value              int64
	}{
		{"create", "POST", fmt.Sprintf(`{"requestId":%q}`, token), createReceivingSessionHandler, "", 0},
		{"scan-receipt", "POST", fmt.Sprintf(`{"requestId":%q,"sku":"TEST-SKU","quantity":1}`, token), addReceivingSessionItemHandler, "sessionID", session},
		{"place", "POST", fmt.Sprintf(`{"requestId":%q,"sessionId":%d,"cellCode":"TEST-1","items":[{"sku":"TEST-SKU","quantity":1}]}`, token, session), createReceivingPlacementHandler, "", 0},
		{"undo", "POST", fmt.Sprintf(`{"requestId":%q}`, token), undoReceivingPlacementHandler, "placementID", 1},
		{"finish", "POST", fmt.Sprintf(`{"requestId":%q,"sessionId":%d,"placementIds":[1]}`, token, session), finishReceivingHandler, "", 0},
		{"delete-item", "DELETE", fmt.Sprintf(`{"requestId":%q,"expectedRevision":"1"}`, token), deleteReceivingSessionItemHandler, "sessionID", session},
		{"quantity-post", "POST", fmt.Sprintf(`{"requestId":%q,"quantity":2,"expectedQuantity":1}`, token), receivingQuantityHandler, "sessionID", session},
		{"quantity-put", "PUT", fmt.Sprintf(`{"requestId":%q,"quantity":2,"expectedQuantity":1}`, token), receivingQuantityHandler, "sessionID", session},
		{"catalog-post", "POST", `{"name":"Synthetic TEST","sku":"TEST-NEW","categoryId":1}`, saveCatalogProductHandler, "", 0},
		{"catalog-put", "PUT", `{"name":"Synthetic TEST","sku":"TEST-SKU","categoryId":1}`, saveCatalogProductHandler, "productID", 1},
		{"history", "DELETE", "", deleteReceivingHistoryHandler, "sessionID", session},
	}
	for _, tc := range cases {
		for _, header := range []string{"", "invalid", foreign} {
			t.Run(tc.name+header, func(t *testing.T) {
				r := httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
				if tc.key != "" {
					r.SetPathValue(tc.key, fmt.Sprint(tc.value))
				}
				r.SetPathValue("productID", "1")
				r.SetPathValue("sku", "TEST-SKU")
				r.Header.Set(deploymentHeader, header)
				w := httptest.NewRecorder()
				tc.handler(db, w, r)
				if header == foreign {
					requireReceivingStatus(t, w, 409)
				} else {
					requireReceivingStatus(t, w, 400)
				}
				if w.Header().Get("X-Receiving-Command-Rejected") != "" {
					t.Fatal("identity error marked business rejection")
				}
				var result map[string]string
				_ = json.Unmarshal(w.Body.Bytes(), &result)
				if !strings.HasPrefix(result["code"], "deployment_identity_") {
					t.Fatal(w.Body.String())
				}
				if receivingOperationFingerprint(t, db) != before || productsHash() != productsBefore {
					t.Fatal("identity rejection changed data or receipts")
				}
			})
		}
	}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		deploymentIdentityHandler(db, w, httptest.NewRequest("GET", "/api/deployment", nil))
		requireReceivingStatus(t, w, 200)
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || !strings.Contains(w.Body.String(), id) {
			t.Fatal("identity read changed or cached")
		}
	}
}
