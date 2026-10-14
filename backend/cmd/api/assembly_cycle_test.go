package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/assemblyorders"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run the production handlers over actual HTTP, using only the fixture's isolated schema.
func cycleServer(t *testing.T, db *pgxpool.Pool, drop *atomic.Bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /draft", func(w http.ResponseWriter, r *http.Request) { getAssemblyDraftHandler(db, w, r) })
	mux.HandleFunc("GET /archive", func(w http.ResponseWriter, r *http.Request) { assemblyProblemArchiveHandler(db, w, r) })
	mux.HandleFunc("GET /history/{problemID}", func(w http.ResponseWriter, r *http.Request) { assemblyProblemHistoryHandler(db, w, r) })
	mux.HandleFunc("POST /problems", func(w http.ResponseWriter, r *http.Request) { createAssemblyProblemHandler(db, w, r) })
	mux.HandleFunc("POST /mapping/{problemID}", func(w http.ResponseWriter, r *http.Request) { assemblyMappingHandler(db, w, r) })
	for _, action := range []string{"start", "scan", "undo", "finish"} {
		mux.HandleFunc("POST /assembly/"+action, func(w http.ResponseWriter, r *http.Request) { warehouseAssemblyHandler(db, w, r, action) })
	}
	for _, action := range []string{"check-stock", "return", "retry-ozon", "transfer-disassembly"} {
		mux.HandleFunc("POST /problem/{problemID}/"+action, func(w http.ResponseWriter, r *http.Request) { assemblyProblemOperationHandler(db, w, r, action) })
	}
	for _, action := range []string{"start", "scan", "finish", "pause"} {
		mux.HandleFunc("POST /disassembly/{sessionID}/"+action, func(w http.ResponseWriter, r *http.Request) { disassemblyHandler(db, w, r, "disassembly-"+action) })
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && drop.Swap(false) {
			// Commit normally, then close the socket without transmitting the committed response.
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, r)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAssemblyCycleReceivingToProblemToPickOnProductionSchema(t *testing.T) {
	// Use the actual warehouse migrations, not the lightweight stock-table fixture.
	db := receivingTestDB(t)
	for _, name := range []string{"010_rack_cell_codes.sql", "011_assembly_preview_drafts.sql", "012_assembly_problem_storage.sql", "013_assembly_problem_context.sql", "014_assembly_stock_checks.sql", "015_assembly_warehouse_operations.sql", "016_assembly_sku_mappings.sql", "017_assembly_ozon_operations.sql", "018_assembly_disassembly.sql"} {
		data, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		stockTestExec(t, db, string(data))
	}
	var drop atomic.Bool
	server := cycleServer(t, db, &drop)
	c := cycleClient{t: t, db: db, server: server, client: &http.Client{Timeout: 5 * time.Second}, next: 1001}
	order := assemblyorders.Order{Source: "ozon", Number: "RECEIVING-INTEGRATION", CreatedAt: "2026-10-14T10:00:00Z", Items: []assemblyorders.Item{{Name: "Unknown warehouse product", SKU: "EXT-RECEIVING", OzonSKU: "777", Quantity: 3}}}
	importWarehouseTest(t, db, 1000, order)
	c.command("/problems", map[string]any{"source": "queue", "orderNumber": order.Number, "details": assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "EXT-RECEIVING"}}, true, false, &drop)
	pid := problemIDOf(testIssues(c.state)[0])
	c.command("/mapping/"+pid, map[string]any{"positionSku": "EXT-RECEIVING", "productId": 1, "confirmed": true}, true, false, &drop)
	issues := testIssues(c.state)
	if len(issues) != 2 || testProblem(issues[1])["resolutionStatus"] != "awaiting" {
		t.Fatal("mapping failed to create real shortage")
	}
	stockID := problemIDOf(issues[1])
	session := receivingSession(t, db)
	receivingAdd(t, db, session, 3)
	placement := receivingPlace(t, db, session, 3)
	// Goods placed by Receiving resolve the shortage, but may change again before return.
	c.command("/problem/"+stockID+"/check-stock", nil, true, false, &drop)
	w := receivingRequest(db, undoReceivingPlacementHandler, "", "placementID", placement)
	requireReceivingStatus(t, w, http.StatusOK)
	c.command("/problem/"+pid+"/return", nil, false, false, &drop)
	placement = receivingPlace(t, db, session, 3)
	body := string(encodeValue(map[string]any{"sessionId": session, "placementIds": []int64{placement}}))
	requireReceivingStatus(t, receivingRequest(db, finishReceivingHandler, body, "", 0), http.StatusOK)
	c.command("/problem/"+stockID+"/check-stock", nil, true, false, &drop)
	c.command("/problem/"+pid+"/return", nil, true, false, &drop)
	c.command("/assembly/scan", map[string]any{"code": "CELL:1"}, true, false, &drop)
	c.command("/assembly/scan", map[string]any{"code": "TEST-SKU"}, true, true, &drop)
	stockTestQuantity(t, db, "cell_stock", 1, 2)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 2)
	pick := valueMap(valueArray(valueMap(c.state["activeState"])["picks"])[0])
	if pick["sku"] != "EXT-RECEIVING" || intValue(pick["cellId"]) != 1 {
		t.Fatal("warehouse barcode did not retain order identity")
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 1)
}

func cycleRequest(client *http.Client, base, path string, body map[string]any) (int, map[string]any, error) {
	method, reader := "GET", strings.NewReader("")
	if body != nil {
		method, reader = "POST", strings.NewReader(string(encodeValue(body)))
	}
	r, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := client.Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, nil, err
	}
	var data map[string]any
	err = json.Unmarshal(raw, &data)
	return response.StatusCode, data, err
}

type cycleClient struct {
	t             *testing.T
	db            *pgxpool.Pool
	server        *httptest.Server
	client        *http.Client
	version, next int
	state         map[string]any
}

func (c *cycleClient) get(path string) map[string]any {
	c.t.Helper()
	status, data, err := cycleRequest(c.client, c.server.URL, path, nil)
	if err != nil || status != 200 {
		c.t.Fatalf("GET %s status=%d err=%v data=%v", path, status, err, data)
	}
	return data
}
func (c *cycleClient) command(path string, params map[string]any, applied bool, lose bool, drop *atomic.Bool) {
	c.t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["requestId"], params["version"] = warehouseID(c.next), c.version
	c.next++
	if lose {
		drop.Store(true)
		if _, _, err := cycleRequest(c.client, c.server.URL, path, params); err == nil {
			c.t.Fatal("lost-response simulation did not lose response")
		}
	}
	status, data, err := cycleRequest(c.client, c.server.URL, path, params)
	if err != nil || status != 200 || (data["applied"] == false) == applied {
		c.t.Fatalf("%s status=%d err=%v data=%s", path, status, err, encodeValue(data))
	}
	c.version, c.state = intValue(data["version"]), valueMap(data["state"])
	// A second click with the SAME intent must replay exactly, without another event or stock mutation.
	before := archiveEventSnapshot(c.t, c.db)
	status, replay, err := cycleRequest(c.client, c.server.URL, path, params)
	if err != nil || status != 200 || !reflect.DeepEqual(data, replay) || archiveEventSnapshot(c.t, c.db) != before {
		c.t.Fatal("HTTP replay altered response/history", path)
	}
	loaded := c.get("/draft")
	if intValue(loaded["version"]) != c.version || !reflect.DeepEqual(valueMap(loaded["state"]), c.state) {
		c.t.Fatal("reload lost state", path)
	}
}

func cycleDB(t *testing.T) *pgxpool.Pool {
	db := ozonTestDB(t)
	data, err := os.ReadFile("../../migrations/018_assembly_disassembly.sql")
	if err != nil {
		t.Fatal(err)
	}
	stockTestExec(t, db, string(data))
	return db
}

func TestAssemblyCycleHTTPRepeatedProblemsLostResponsesAndCompletion(t *testing.T) {
	db := cycleDB(t)
	var drop atomic.Bool
	server := cycleServer(t, db, &drop)
	c := cycleClient{t: t, db: db, server: server, client: &http.Client{Timeout: 5 * time.Second}, next: 800}
	order := unknownOrder("CYCLE-RETURN", 7)
	importWarehouseTest(t, db, 799, order)
	c.command("/problems", map[string]any{"source": "queue", "orderNumber": order.Number, "details": assemblyProblemDetails{Type: "unknown_sku", PositionSKU: "EXT-A"}}, true, true, &drop)
	pid := problemIDOf(testIssues(c.state)[0])
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=2 WHERE product_id=1`)
	c.command("/mapping/"+pid, map[string]any{"positionSku": "EXT-A", "productId": 1, "confirmed": true}, true, true, &drop)
	issues := testIssues(c.state)
	if len(issues) != 2 || testProblem(issues[1])["resolutionStatus"] != "awaiting" {
		t.Fatal("mapping bypassed actual stock")
	}
	status, _, err := cycleRequest(c.client, server.URL, "/problem/"+pid+"/return", map[string]any{"requestId": warehouseID(c.next), "version": c.version})
	c.next++
	if err != nil || status != 409 {
		t.Fatal("multiple causes did not block return", status, err)
	}
	stockID := problemIDOf(issues[1])
	c.command("/problem/"+stockID+"/check-stock", nil, false, false, &drop)
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5 WHERE product_id=1`)
	c.command("/problem/"+stockID+"/check-stock", nil, true, false, &drop)
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=1 WHERE product_id=1`)
	c.command("/problem/"+pid+"/return", nil, false, false, &drop)
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5 WHERE product_id=1`)
	c.command("/problem/"+stockID+"/check-stock", nil, true, false, &drop)
	c.command("/problem/"+pid+"/return", nil, true, true, &drop)
	closed := c.get("/history/" + pid)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 7)
	cell := nextWarehouseCell(valueMap(c.state["activeOrder"]))
	c.command("/assembly/scan", map[string]any{"code": cell["scanCode"]}, true, false, &drop)
	c.command("/assembly/scan", map[string]any{"code": "A"}, true, true, &drop)
	stockTestQuantity(t, db, "cell_stock", 1, 9)
	assertStorageCount(t, db, "assembly_warehouse_operations", 1)
	// Each new problem after returning is a NEW identity with the full partial-pick context.
	for cycle := 0; cycle < 2; cycle++ {
		count := len(testIssues(c.state))
		c.command("/problems", map[string]any{"source": "assembly", "orderNumber": order.Number, "details": assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: "EXT-A"}}, true, true, &drop)
		issues = testIssues(c.state)
		if len(issues) != count+1 {
			t.Fatal("new problem overwrote old record")
		}
		newID := problemIDOf(issues[count])
		if newID == stockID || newID == pid || len(valueArray(contextOf(issues[count])["picks"])) != 1 {
			t.Fatal("repeated problem identity/progress lost")
		}
		stockTestExec(t, db, `UPDATE cell_stock SET quantity=0 WHERE product_id=1`)
		c.command("/problem/"+newID+"/check-stock", nil, false, false, &drop)
		stockTestExec(t, db, `UPDATE cell_stock SET quantity=5 WHERE product_id=1`)
		c.command("/problem/"+newID+"/check-stock", nil, true, false, &drop)
		c.command("/problem/"+newID+"/return", nil, true, true, &drop)
		if len(valueArray(valueMap(c.state["activeState"])["picks"])) != 1 || valueMap(c.state["activeState"])["confirmedCell"] != nil {
			t.Fatal("return discarded picks/reused cell confirmation")
		}
		stockTestQuantity(t, db, "assembly_order_reservations", 1, 6)
		if !reflect.DeepEqual(closed["historyRecords"], c.get("/history/" + pid)["historyRecords"]) {
			t.Fatal("new problem changed closed history")
		}
	}
	for len(valueArray(valueMap(c.state["activeState"])["picks"])) < 7 {
		if valueMap(c.state["activeState"])["confirmedCell"] == nil {
			cell = nextWarehouseCell(valueMap(c.state["activeOrder"]))
			c.command("/assembly/scan", map[string]any{"code": cell["scanCode"]}, true, false, &drop)
		}
		c.command("/assembly/scan", map[string]any{"code": "A"}, true, false, &drop)
	}
	c.command("/assembly/scan", map[string]any{"code": "wrong-label"}, false, false, &drop)
	c.command("/assembly/scan", map[string]any{"code": order.Number}, true, false, &drop)
	c.command("/assembly/finish", nil, false, true, &drop)
	last := testIssues(c.state)[len(testIssues(c.state))-1]
	opID := textValue(testProblem(last)["ozonOperationId"])
	if opID == "" || c.state["activeOrder"] != nil {
		t.Fatal("confirmation operation lost")
	}
	r := receiptFor(t, db, opID, "verified-final", "applied", 1)
	if err = ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
		t.Fatal(err)
	}
	if err = ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
		t.Fatal(err)
	}
	loaded := c.get("/draft")
	if len(valueArray(valueMap(loaded["state"])["completedOrders"])) != 1 {
		t.Fatal("verified completion missing/duplicated")
	}
	if intValue(c.get("/archive")["total"]) != 5 {
		t.Fatal("closed repeated problems lost")
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 7)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 0)
	stockTestQuantity(t, db, "cell_stock", 1, 4)
}

func TestAssemblyCycleHTTPIndependentTabsAndRestart(t *testing.T) {
	db := cycleDB(t)
	var drop atomic.Bool
	server := cycleServer(t, db, &drop)
	order := realTestOrder("TWO-TABS")
	order.Items = order.Items[:1]
	importWarehouseTest(t, db, 900, order)
	c := cycleClient{t: t, db: db, server: server, client: &http.Client{Timeout: 5 * time.Second}, next: 901}
	c.command("/assembly/start", map[string]any{"orderNumber": order.Number}, true, false, &drop)
	c.command("/assembly/scan", map[string]any{"code": "CELL:1"}, true, false, &drop)
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	errors := make([]error, 2)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client := &http.Client{Timeout: 5 * time.Second}
			statuses[i], _, errors[i] = cycleRequest(client, server.URL, "/assembly/scan", map[string]any{"requestId": warehouseID(910 + i), "version": c.version, "code": "A"})
		}(i)
	}
	wg.Wait()
	if errors[0] != nil || errors[1] != nil || !((statuses[0] == 200 && statuses[1] == 409) || (statuses[1] == 200 && statuses[0] == 409)) {
		t.Fatal("two tabs were not serialized", statuses, errors)
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 1)
	stockTestQuantity(t, db, "cell_stock", 1, 9)
	before := c.get("/draft")
	server.Close()
	// New server AND new PostgreSQL pool have no client refs or previous in-memory state.
	fresh, err := pgxpool.NewWithConfig(context.Background(), db.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	server = cycleServer(t, fresh, &drop)
	c.server = server
	if !reflect.DeepEqual(before, c.get("/draft")) {
		t.Fatal("restart lost authoritative state")
	}
	c.version = intValue(before["version"])
	// A new distinct request with the old tab's version must not duplicate the committed pick.
	status, _, err := cycleRequest(c.client, server.URL, "/assembly/scan", map[string]any{"requestId": warehouseID(920), "version": c.version - 1, "code": "A"})
	if err != nil || status != 409 {
		t.Fatal("stale tab accepted after restart")
	}
	c.command("/assembly/undo", nil, true, true, &drop)
	assertStorageCount(t, db, "assembly_warehouse_operations", 2)
	stockTestQuantity(t, db, "cell_stock", 1, 10)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 2)
	c.command("/assembly/scan", map[string]any{"code": "A"}, true, true, &drop)
	opID, _ := seedOzonRefresh(t, db, order.Number, true)
	receipt := receiptFor(t, db, opID, "cancel-partial-http", "cancelled", 1)
	if err = ingestOzonDomainReceipt(context.Background(), db, receipt); err != nil {
		t.Fatal(err)
	}
	loaded := c.get("/draft")
	c.version, c.state = intValue(loaded["version"]), valueMap(loaded["state"])
	issues := testIssues(c.state)
	pid := problemIDOf(issues[len(issues)-1])
	stockTestQuantity(t, db, "cell_stock", 1, 9)
	stockTestQuantity(t, db, "assembly_order_reservations", 1, 0)
	c.command("/problem/"+pid+"/transfer-disassembly", nil, true, true, &drop)
	stockTestQuantity(t, db, "cell_stock", 1, 9)
	sid := textValue(disassemblyTestSession(c.state)["id"])
	c.command("/disassembly/"+sid+"/start", nil, true, false, &drop)
	c.command("/disassembly/"+sid+"/scan", map[string]any{"code": "A"}, true, false, &drop)
	stockTestQuantity(t, db, "cell_stock", 1, 9)
	// Force a failure while crediting stock: pending product and all ledgers must remain intact.
	pending := c.get("/draft")
	events := archiveEventSnapshot(t, db)
	stockTestExec(t, db, `CREATE FUNCTION reject_cycle_return() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'cycle return failure'; END $$`)
	stockTestExec(t, db, `CREATE TRIGGER reject_cycle_return BEFORE INSERT OR UPDATE ON cell_stock FOR EACH ROW EXECUTE FUNCTION reject_cycle_return()`)
	body := map[string]any{"requestId": warehouseID(c.next), "version": c.version, "code": "CELL:1"}
	status, _, err = cycleRequest(c.client, server.URL, "/disassembly/"+sid+"/scan", body)
	if err != nil || status != 500 || !reflect.DeepEqual(pending, c.get("/draft")) || events != archiveEventSnapshot(t, db) {
		t.Fatal("failed return was not atomic", status, err)
	}
	stockTestQuantity(t, db, "cell_stock", 1, 9)
	assertStorageCount(t, db, "assembly_disassembly_returns", 0)
	stockTestExec(t, db, `DROP TRIGGER reject_cycle_return ON cell_stock`)
	// The failed request ID is reused, then the committed response is deliberately lost.
	c.command("/disassembly/"+sid+"/scan", map[string]any{"code": "CELL:1"}, true, true, &drop)
	assertStorageCount(t, db, "assembly_disassembly_returns", 1)
	stockTestQuantity(t, db, "cell_stock", 1, 10)
	c.command("/disassembly/"+sid+"/finish", nil, true, true, &drop)
	if intValue(c.get("/archive")["total"]) != 2 {
		t.Fatal("disassembled causes missing from archive")
	}
	if disassemblyTestSession(c.state)["status"] != "completed" {
		t.Fatal("disassembly did not complete")
	}
}
