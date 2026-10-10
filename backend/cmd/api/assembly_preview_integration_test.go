package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAssemblyDraftPersistence(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("assembly_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migration, err := os.ReadFile("../../migrations/011_assembly_preview_drafts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	state := `{"schemaVersion":1,"activeOrder":{"number":"024831-0001","items":[{"sku":"TR-BLACK","quantity":2}]},"activeState":{"picks":[{"sku":"TR-BLACK","cell":"A1"}],"confirmedCell":"A1","shipmentConfirmed":false,"lastScan":"TR-BLACK","log":"saved","error":false},"problemOrders":[],"completedOrders":[]}`
	put := func(id string, version int, payload string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"requestId":%q,"version":%d,"state":%s}`, id, version, payload)
		w := httptest.NewRecorder()
		saveAssemblyDraftHandler(db, w, httptest.NewRequest("PUT", "/", strings.NewReader(body)))
		return w
	}
	id := "00000000-0000-4000-8000-000000000001"
	for i := 0; i < 2; i++ {
		w := put(id, 0, state)
		if w.Code != 200 {
			t.Fatalf("save/replay: %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	getAssemblyDraftHandler(db, w, httptest.NewRequest("GET", "/", nil))
	var restored assemblyDraftResponse
	if err = json.Unmarshal(w.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	var actual, expected any
	json.Unmarshal(restored.State, &actual)
	json.Unmarshal([]byte(state), &expected)
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(expected)
	if restored.Version != 1 || string(a) != string(b) {
		t.Fatalf("state did not survive reload: %s", w.Body.String())
	}
	if w = put("00000000-0000-4000-8000-000000000002", 0, state); w.Code != 409 {
		t.Fatalf("stale save: %d", w.Code)
	}
	invalid := strings.Replace(state, `"quantity":2`, `"quantity":0`, 1)
	if w = put("00000000-0000-4000-8000-000000000003", 1, invalid); w.Code != 400 {
		t.Fatalf("invalid state: %d", w.Code)
	}
	shipment := strings.Replace(state, `"shipmentConfirmed":false`, `"shipmentConfirmed":true`, 1)
	if w = put("00000000-0000-4000-8000-000000000004", 1, shipment); w.Code != 400 {
		t.Fatalf("premature shipment: %d", w.Code)
	}
	changed := strings.Replace(state, `"log":"saved"`, `"log":"different"`, 1)
	if w = put(id, 0, changed); w.Code != 409 {
		t.Fatalf("reused ID: %d", w.Code)
	}
	if w = put("00000000-0000-4000-8000-000000000005", 1, changed); w.Code != 200 {
		t.Fatalf("next save: %d %s", w.Code, w.Body.String())
	}
}
