package main

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// Acceptance coverage only: production handlers over HTTP and real PostgreSQL
// in the existing isolated schema; each command also replays and reloads.
func TestDisassemblyFinalCycleHTTPUndoReloadAndCompletion(t *testing.T) {
	f := disassemblyFixture(t, true)
	originalContext := contextOf(testIssues(f.state)[1])
	f.transfer(t)
	var drop atomic.Bool
	server := cycleServer(t, f.db, &drop)
	c := cycleClient{t: t, db: f.db, server: server, client: &http.Client{Timeout: 5 * time.Second}, version: f.version, next: 9800, state: f.state}
	path := "/disassembly/" + f.sid + "/"
	c.command(path+"start", nil, true, false, &drop)
	c.command(path+"scan", map[string]any{"code": "WRONG"}, false, false, &drop)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 0)
	c.command(path+"scan", map[string]any{"code": "A"}, true, false, &drop)
	pending := disassemblyTestSession(c.state)["pendingItemId"]
	if pending == nil {
		t.Fatal("product scan did not select an original unit")
	}
	c.command(path+"pause", nil, true, false, &drop)
	if disassemblyTestSession(c.state)["status"] != "waiting" || disassemblyTestSession(c.state)["pendingItemId"] != pending {
		t.Fatal("pause/reload lost confirmed product")
	}
	c.command(path+"start", nil, true, false, &drop)
	if disassemblyTestSession(c.state)["pendingItemId"] != pending {
		t.Fatal("resume changed expected source unit/cell")
	}
	for _, wrongCell := range []string{"A1", "CELL:2"} {
		c.command(path+"scan", map[string]any{"code": wrongCell}, false, false, &drop)
		if disassemblyTestSession(c.state)["pendingItemId"] != pending {
			t.Fatal("wrong/ambiguous cell cleared pending unit")
		}
		stockTestQuantity(t, f.db, "cell_stock", 1, 3)
		assertStorageCount(t, f.db, "assembly_disassembly_returns", 0)
	}
	c.command(path+"scan", map[string]any{"code": "CELL:1"}, true, true, &drop)
	first := modelItem(t, f, textValue(pending))
	firstReturn := textValue(first["returnId"])
	firstRow := modelRow(t, f.db, "assembly_disassembly_returns", firstReturn)
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
	c.command(path+"undo-return", nil, true, true, &drop)
	if modelReturned(t, f) != 0 || disassemblyTestSession(c.state)["pendingItemId"] != nil {
		t.Fatal("undo/reload did not restore the product step")
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	beforeStale := disassemblyUndoSnapshot(t, f)
	status, _, err := cycleRequest(c.client, server.URL, path+"undo-return", map[string]any{"requestId": warehouseID(c.next), "version": c.version - 1})
	c.next++
	if err != nil || status != 409 || beforeStale != disassemblyUndoSnapshot(t, f) {
		t.Fatal("stale undo changed committed state", status, err)
	}
	// Repeat the same original unit, then finish all other source units, spanning
	// both cells despite their identical display code.
	for returned := 0; returned < 7; returned++ {
		c.command(path+"scan", map[string]any{"code": "A"}, true, false, &drop)
		itemID := textValue(disassemblyTestSession(c.state)["pendingItemId"])
		if returned == 0 && itemID != textValue(pending) {
			t.Fatal("undo -> scan selected a different original unit")
		}
		item := modelItem(t, f, itemID)
		c.command(path+"scan", map[string]any{"code": fmt.Sprintf("CELL:%d", intValue(item["cellId"]))}, true, returned == 0, &drop)
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 10)
	if modelReturned(t, f) != 7 {
		t.Fatal("full return progress is incorrect")
	}
	c.command(path+"undo-return", nil, true, false, &drop)
	if modelReturned(t, f) != 6 {
		t.Fatal("undo after full return did not reopen the session")
	}
	beforeFinish := disassemblyUndoSnapshot(t, f)
	status, _, err = cycleRequest(c.client, server.URL, path+"finish", map[string]any{"requestId": warehouseID(c.next), "version": c.version})
	c.next++
	if err != nil || status != 409 || beforeFinish != disassemblyUndoSnapshot(t, f) {
		t.Fatal("finish after undo was not rejected atomically", status, err)
	}
	c.command(path+"scan", map[string]any{"code": "A"}, true, false, &drop)
	item := modelItem(t, f, textValue(disassemblyTestSession(c.state)["pendingItemId"]))
	c.command(path+"scan", map[string]any{"code": fmt.Sprintf("CELL:%d", intValue(item["cellId"]))}, true, false, &drop)
	c.command(path+"finish", nil, true, true, &drop)
	if disassemblyTestSession(c.state)["status"] != "completed" || !reflect.DeepEqual(originalContext, contextOf(testIssues(c.state)[1])) {
		t.Fatal("completion lost terminal state or original context")
	}
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 9)
	assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 2)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 7)
	assertStorageCount(t, f.db, "assembly_warehouse_operations", 7)
	stockTestQuantity(t, f.db, "cell_stock", 1, 10)
	stockTestQuantity(t, f.db, "assembly_order_reservations", 1, 0)
	if modelRow(t, f.db, "assembly_disassembly_returns", firstReturn) != firstRow {
		t.Fatal("undo or completion rewrote the first physical return")
	}
	for kind, want := range map[string]int{"disassembly_return": 9, "disassembly_return_reversal": 2, "disassembly_finish": 1, "disassembly_pause": 1} {
		var got int
		if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM assembly_problem_events WHERE problem_id=$1::uuid AND kind=$2`, f.pid, kind).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("history %s: got %d, want %d", kind, got, want)
		}
	}
}
