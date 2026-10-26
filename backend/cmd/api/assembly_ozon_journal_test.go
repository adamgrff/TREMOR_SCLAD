package main

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func ozonTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := mappingTestDB(t)
	migration, err := os.ReadFile("../../migrations/017_assembly_ozon_operations.sql")
	if err != nil {
		t.Fatal(err)
	}
	stockTestExec(t, db, string(migration))
	return db
}
func receiptFor(t *testing.T, db *pgxpool.Pool, id, evidence, outcome string, n int) ozonDomainReceipt {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	op, err := loadOzonOperation(ctx, tx, id)
	if err != nil {
		t.Fatal(err)
	}
	return ozonDomainReceipt{OperationID: id, EvidenceID: evidence, OrderNumber: textValue(valueMap(valueMapFromRaw(op.Payload)["order"])["number"]), PayloadHash: op.Hash, Outcome: outcome, ObservedAt: time.Date(2026, 10, 11, 10, 0, n, 0, time.UTC).Format(time.RFC3339Nano)}
}
func finishOzonTestOrder(t *testing.T, db *pgxpool.Pool) (map[string]any, string, int) {
	t.Helper()
	order := realTestOrder("CONFIRM")
	importWarehouseTest(t, db, 1, order)
	state := warehouseOK(t, warehouseAction(db, 2, 0, "start", order.Number, ""))
	n, version := 3, 1
	for len(valueArray(valueMap(state["activeState"])["picks"])) < 3 {
		if valueMap(state["activeState"])["confirmedCell"] == nil {
			cell := nextWarehouseCell(valueMap(state["activeOrder"]))
			state = warehouseOK(t, warehouseAction(db, n, version, "scan", "", textValue(cell["scanCode"])))
			n++
			version++
		}
		cellID := intValue(valueMap(state["activeState"])["confirmedCellId"])
		sku := ""
		for _, p := range warehousePlans(valueMap(state["activeOrder"])) {
			if intValue(p["cellId"]) == cellID && intValue(p["required"]) > intValue(p["picked"]) {
				sku = warehouseScanSKU(p)
				break
			}
		}
		state = warehouseOK(t, warehouseAction(db, n, version, "scan", "", sku))
		n++
		version++
	}
	warehouseOK(t, warehouseAction(db, n, version, "scan", "", order.Number))
	n++
	version++
	finished := warehouseAction(db, n, version, "finish", "", "")
	stockTestApplied(t, finished, false)
	state = responseTestWorkspace(t, finished)
	version++
	if state["activeOrder"] != nil || len(valueArray(state["completedOrders"])) != 0 || len(testIssues(state)) != 1 {
		t.Fatal("unverified finish falsely completed or lost problem")
	}
	p := testProblem(testIssues(state)[0])
	if p["type"] != "ozon_error" || p["ozonPurpose"] != "confirm_shipment" {
		t.Fatal("original operation not bound")
	}
	assertStorageCount(t, db, "assembly_ozon_operations", 1)
	assertStorageCount(t, db, "assembly_warehouse_operations", 3)
	return state, textValue(p["ozonOperationId"]), version
}

func TestOzonReconciliationDoesNotRepeatBusinessOperation(t *testing.T) {
	db := ozonTestDB(t)
	state, id, version := finishOzonTestOrder(t, db)
	pid := problemIDOf(testIssues(state)[0])
	before := contextOf(testIssues(state)[0])
	picks := valueMap(testIssues(state)[0])["assembly"]
	first := stockTestAction(db, warehouseID(100), version, pid, "retry-ozon")
	stockTestApplied(t, first, false)
	if !sameWarehouseResponse(first, stockTestAction(db, warehouseID(100), version, pid, "retry-ozon")) {
		t.Fatal("lost HTTP response changed replay")
	}
	version++
	state = responseTestWorkspace(t, stockTestAction(db, warehouseID(101), version, pid, "retry-ozon"))
	version++
	assertStorageCount(t, db, "assembly_ozon_operations", 1)
	assertStorageCount(t, db, "assembly_warehouse_operations", 3)
	assertStorageCount(t, db, "assembly_ozon_reconciliations", 3)
	if !reflect.DeepEqual(before, contextOf(testIssues(state)[0])) || !reflect.DeepEqual(picks, valueMap(testIssues(state)[0])["assembly"]) {
		t.Fatal("reconciliation changed picks or context")
	}
	for i, outcome := range []string{"unknown", "not_applied", "rejected"} {
		r := receiptFor(t, db, id, fmt.Sprintf("proof-%d", i), outcome, i)
		if err := ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
			t.Fatal(err)
		}
		state = warehouseState(t, db)
		if testProblem(testIssues(state)[0])["resolutionStatus"] != "awaiting" || len(valueArray(state["completedOrders"])) != 0 {
			t.Fatal("negative/unknown response counted as success")
		}
	}
	// Models a previously executed operation whose response was lost: only its verified result is applied.
	r := receiptFor(t, db, id, "lost-response-now-verified", "applied", 4)
	if err := ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
		t.Fatal(err)
	}
	state = warehouseState(t, db)
	if len(valueArray(state["completedOrders"])) != 1 || testProblem(testIssues(state)[0])["closedOutcome"] != "completed" {
		t.Fatal("confirmed finish not applied")
	}
	if err := ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, warehouseState(t, db)) {
		t.Fatal("duplicate external result changed state")
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 3)
	stockTestQuantity(t, db, "cell_stock", 1, 8)
	stockTestQuantity(t, db, "cell_stock", 2, 7)
	if err := ingestOzonDomainReceipt(context.Background(), db, receiptFor(t, db, id, "regressive", "unknown", 5)); err == nil {
		t.Fatal("success replaced by unknown")
	}
	r.EvidenceID = "wrong-order"
	r.OrderNumber = "OTHER"
	if err := ingestOzonDomainReceipt(context.Background(), db, r); err == nil {
		t.Fatal("unrelated receipt accepted")
	}
}

func seedOzonRefresh(t *testing.T, db *pgxpool.Pool, number string, fromActive bool) (string, map[string]any) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT assembly_load_workspace(state) FROM assembly_preview_drafts WHERE id=true FOR UPDATE`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	state, _ := decodeWorkspace(raw)
	var order, active map[string]any
	if fromActive {
		order = valueMap(state["activeOrder"])
		active = valueMap(state["activeState"])
	} else {
		order, err = loadQueuedWarehouseOrder(ctx, tx, number)
		if err != nil {
			t.Fatal(err)
		}
		active = valueMap(previousEmptyState())
		result, e := readAssemblyStock(ctx, tx, order, nil)
		if e != nil {
			t.Fatal(e)
		}
		updateAssemblyPlan(order, result, nil)
	}
	op, err := registerOzonOperation(ctx, tx, order, active, "refresh_order")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := active
	if !fromActive {
		snapshot = nil
	}
	next, entry, err := appendOzonCause(ctx, tx, state, order, snapshot, "ozon_error", "Проверка внешнего состояния ожидает официального клиента.", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	p := valueMap(entry["problem"])
	p["ozonOperationId"], p["ozonPurpose"], p["ozonOutcome"] = op.ID, op.Purpose, "unknown"
	if _, err = tx.Exec(ctx, `SELECT set_config('assembly.server_action','true',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE assembly_preview_drafts SET state=assembly_store_workspace($1::jsonb),version=version+1 WHERE id=true`, []byte(encodeValue(next))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return op.ID, warehouseState(t, db)
}

func TestOzonCancellationPreservesPicksAndReleasesOnlyReserve(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			db := ozonTestDB(t)
			order := realTestOrder("CANCEL")
			importWarehouseTest(t, db, 1, order)
			if partial {
				state := warehouseOK(t, warehouseAction(db, 2, 0, "start", order.Number, ""))
				cell := nextWarehouseCell(valueMap(state["activeOrder"]))
				warehouseOK(t, warehouseAction(db, 3, 1, "scan", "", textValue(cell["scanCode"])))
				warehouseOK(t, warehouseAction(db, 4, 2, "scan", "", "A"))
			}
			id, _ := seedOzonRefresh(t, db, order.Number, partial)
			r := receiptFor(t, db, id, "cancellation", "cancelled", 1)
			if err := ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
				t.Fatal(err)
			}
			state := warehouseState(t, db)
			issues := testIssues(state)
			if len(issues) != 2 || testProblem(issues[1])["type"] != "cancelled_order" {
				t.Fatal("cancellation not a separate cause")
			}
			stockTestQuantity(t, db, "assembly_order_reservations", 1, 0)
			stockTestQuantity(t, db, "assembly_order_reservations", 2, 0)
			qty, ops := 10, 0
			if partial {
				qty, ops = 9, 1
			}
			stockTestQuantity(t, db, "cell_stock", 1, qty)
			assertStorageCount(t, db, "assembly_warehouse_operations", ops)
			if err := ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
				t.Fatal(err)
			}
			assertStorageCount(t, db, "assembly_problem_records", 2)
			version := 2
			if partial {
				version = 5
			}
			pid := problemIDOf(issues[1])
			closed := stockTestAction(db, warehouseID(30), version, pid, "close-cancelled")
			if partial {
				if closed.Code != 409 || testProblem(issues[1])["requiresDisassembly"] != true || len(valueArray(valueMap(valueMap(issues[1])["assembly"])["picks"])) != 1 {
					t.Fatal("picked cancellation closed or auto-returned")
				}
			} else {
				stockTestApplied(t, closed, true)
				state = responseTestWorkspace(t, closed)
				for _, e := range testIssues(state) {
					if testProblem(e)["closedOutcome"] != "cancelled" {
						t.Fatal("terminal reasons remained in working queue")
					}
				}
				var status string
				if err := db.QueryRow(context.Background(), `SELECT status FROM assembly_order_records WHERE source='ozon' AND order_number=$1`, order.Number).Scan(&status); err != nil || status != "cancelled" {
					t.Fatal("cancelled order status wrong", err, status)
				}
			}
		})
	}
}

func TestOzonRefreshChecksCompositionStockAndOtherCauses(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			db := ozonTestDB(t)
			order := realTestOrder("REFRESH")
			importWarehouseTest(t, db, 1, order)
			id, _ := seedOzonRefresh(t, db, order.Number, false)
			stockTestExec(t, db, `UPDATE cell_stock SET quantity=0 WHERE product_id=1`)
			r := receiptFor(t, db, id, "read-result", "applied", 1)
			updated := order
			if changed {
				updated.Items[0].Quantity = 1
			}
			r.Order = encodeValue(updated)
			if err := ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
				t.Fatal(err)
			}
			state := warehouseState(t, db)
			if state["activeOrder"] != nil || len(valueArray(state["completedOrders"])) != 0 || len(testIssues(state)) != 2 {
				t.Fatal("refresh resumed/completed order or missed new cause")
			}
			reason := "insufficient_stock"
			if changed {
				reason = "invalid_order"
			}
			if testProblem(testIssues(state)[1])["type"] != reason || testProblem(testIssues(state)[1])["resolutionStatus"] != "awaiting" {
				t.Fatal("refresh bypassed next problem")
			}
		})
	}
}

func TestOzonReceiptRollbackAndConcurrentDuplicate(t *testing.T) {
	db := ozonTestDB(t)
	_, id, _ := finishOzonTestOrder(t, db)
	before := warehouseState(t, db)
	r := receiptFor(t, db, id, "single-proof", "applied", 1)
	stockTestExec(t, db, `CREATE FUNCTION reject_receipt_publish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated publish failure'; END $$`)
	stockTestExec(t, db, `CREATE TRIGGER reject_receipt_publish BEFORE UPDATE ON assembly_preview_drafts FOR EACH ROW EXECUTE FUNCTION reject_receipt_publish()`)
	if err := ingestOzonDomainReceipt(context.Background(), db, r); err == nil {
		t.Fatal("failure not injected")
	}
	assertStorageCount(t, db, "assembly_ozon_observations", 0)
	if !reflect.DeepEqual(before, warehouseState(t, db)) {
		t.Fatal("receipt survived partial rollback")
	}
	stockTestExec(t, db, `DROP TRIGGER reject_receipt_publish ON assembly_preview_drafts`)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- ingestOzonDomainReceipt(context.Background(), db, r) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertStorageCount(t, db, "assembly_ozon_observations", 1)
	assertStorageCount(t, db, "assembly_warehouse_operations", 3)
	state := warehouseState(t, db)
	if len(valueArray(state["completedOrders"])) != 1 {
		t.Fatal("duplicate receipt completed twice")
	}
	r.Outcome = "unknown"
	if err := ingestOzonDomainReceipt(context.Background(), db, r); err == nil {
		t.Fatal("same evidence identity accepted changed content")
	}
}

func TestOzonVerifiedRefreshAllowsOnlyExplicitRecheckedReturn(t *testing.T) {
	db := ozonTestDB(t)
	order := realTestOrder("READY-AFTER-READ")
	importWarehouseTest(t, db, 1, order)
	id, state := seedOzonRefresh(t, db, order.Number, false)
	pid := problemIDOf(testIssues(state)[0])
	// The UI label alone must never count as verified marketplace evidence.
	stockTestExec(t, db, `UPDATE assembly_problem_records SET resolution_status='resolved',problem_metadata=jsonb_set(problem_metadata,'{resolutionStatus}','"resolved"') WHERE id=$1::uuid`, pid)
	if w := stockTestAction(db, warehouseID(40), 1, pid, "return"); w.Code != 409 {
		t.Fatal("resolved label bypassed external proof")
	}
	r := receiptFor(t, db, id, "read-proof", "applied", 1)
	if err := ingestOzonDomainReceipt(context.Background(), db, r); err == nil {
		t.Fatal("read success accepted without contents")
	}
	r.Order = encodeValue(order)
	if err := ingestOzonDomainReceipt(context.Background(), db, r); err != nil {
		t.Fatal(err)
	}
	state = warehouseState(t, db)
	if state["activeOrder"] != nil || len(valueArray(state["completedOrders"])) != 0 {
		t.Fatal("read auto-resumed order")
	}
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=0 WHERE product_id=1`)
	w := stockTestAction(db, warehouseID(41), 2, pid, "return")
	stockTestApplied(t, w, false)
	state = responseTestWorkspace(t, w)
	if len(testIssues(state)) != 2 || testProblem(testIssues(state)[1])["type"] != "insufficient_stock" {
		t.Fatal("return skipped current stocks")
	}
	stockTestExec(t, db, `UPDATE cell_stock SET quantity=5 WHERE product_id=1`)
	warehouseOK(t, stockTestAction(db, warehouseID(42), 3, problemIDOf(testIssues(state)[1]), "check-stock"))
	state = warehouseOK(t, stockTestAction(db, warehouseID(43), 4, pid, "return"))
	if textValue(valueMap(state["activeOrder"])["number"]) != order.Number {
		t.Fatal("verified read could not explicitly return")
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 0)
}

func TestOzonPendingIdentityImmutableAndStaleReceiptsRejected(t *testing.T) {
	db := ozonTestDB(t)
	_, id, _ := finishOzonTestOrder(t, db)
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, err := loadOzonOperation(ctx, tx, id)
	if err != nil {
		t.Fatal(err)
	}
	payload := valueMapFromRaw(op.Payload)
	order := valueMap(payload["order"])
	active := valueMap(payload["assembly"])
	same, err := registerOzonOperation(ctx, tx, order, active, "confirm_shipment")
	if err != nil || same.BusinessKey != op.BusinessKey {
		t.Fatal("registration created another business key", err)
	}
	active["picks"] = []any{}
	if _, err = registerOzonOperation(ctx, tx, order, active, "confirm_shipment"); err == nil {
		t.Fatal("uncertain operation replaced by another intent")
	}
	tx.Rollback(ctx)
	r := receiptFor(t, db, id, "later-unknown", "unknown", 3)
	if err := ingestOzonDomainReceipt(ctx, db, r); err != nil {
		t.Fatal(err)
	}
	old := receiptFor(t, db, id, "old-applied", "applied", 2)
	if err := ingestOzonDomainReceipt(ctx, db, old); err == nil {
		t.Fatal("out-of-order result overwritten")
	}
	r.EvidenceID = "bad-hash"
	r.PayloadHash = "incorrect"
	if err := ingestOzonDomainReceipt(ctx, db, r); err == nil {
		t.Fatal("result bound to another payload accepted")
	}
	assertStorageCount(t, db, "assembly_ozon_operations", 1)
	assertStorageCount(t, db, "assembly_ozon_observations", 1)
	if _, err := db.Exec(ctx, `UPDATE assembly_ozon_operations SET business_key=gen_random_uuid() WHERE id=$1::uuid`, id); err == nil {
		t.Fatal("immutable business key updated")
	}
}

func TestOzonConfirmationCannotIgnoreAnotherProblem(t *testing.T) {
	db := ozonTestDB(t)
	state, id, _ := finishOzonTestOrder(t, db)
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	original := valueMap(testIssues(state)[0])
	order := valueMap(original["order"])
	saved := valueMap(original["assembly"])
	active := map[string]any{"picks": saved["picks"], "confirmedCell": saved["cell"], "shipmentConfirmed": saved["shipmentConfirmed"]}
	next, _, err := appendOzonCause(ctx, tx, state, order, active, "other", "Отдельная причина не устранена.", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('assembly.server_action','true',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE assembly_preview_drafts SET state=assembly_store_workspace($1::jsonb),version=version+1 WHERE id=true`, []byte(encodeValue(next))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := receiptFor(t, db, id, "confirmed-but-other-blocked", "applied", 1)
	if err = ingestOzonDomainReceipt(ctx, db, r); err != nil {
		t.Fatal(err)
	}
	state = warehouseState(t, db)
	if len(valueArray(state["completedOrders"])) != 0 || testProblem(testIssues(state)[0])["resolutionStatus"] != "resolved" || testProblem(testIssues(state)[1])["resolutionStatus"] != "awaiting" {
		t.Fatal("verified Ozon confirmation ignored another problem")
	}
	// Even a forged resolved label does not substitute for the stage 1-4 clearance.
	pid := problemIDOf(testIssues(state)[1])
	stockTestExec(t, db, `UPDATE assembly_problem_records SET resolution_status='resolved',problem_metadata=jsonb_set(problem_metadata,'{resolutionStatus}','"resolved"') WHERE id=$1::uuid`, pid)
	r = receiptFor(t, db, id, "confirmed-without-local-proof", "applied", 2)
	if err = ingestOzonDomainReceipt(ctx, db, r); err != nil {
		t.Fatal(err)
	}
	state = warehouseState(t, db)
	if len(valueArray(state["completedOrders"])) != 0 {
		t.Fatal("local proof bypassed")
	}
	assertStorageCount(t, db, "assembly_warehouse_operations", 3)
}
