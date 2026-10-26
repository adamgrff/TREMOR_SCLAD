package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func applyDisassemblyReversalsMigration(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	data, err := os.ReadFile("../../migrations/019_assembly_disassembly_return_reversals.sql")
	if err != nil {
		t.Fatal(err)
	}
	stockTestExec(t, db, string(data))
}

func requireModelSQLState(t *testing.T, err error, state string) {
	t.Helper()
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != state {
		t.Fatalf("want SQLSTATE %s, got %v", state, err)
	}
}

func modelReceipt(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `INSERT INTO assembly_preview_requests(request_id,payload_hash,response) VALUES($1::uuid,'R4.2A-test-model','{"applied":true}'::jsonb)`, id)
	return err
}

// Test fixture only, not a production cancellation operation. Keep physical stock
// balanced in the isolated schema while directly preparing the reversal ledger.
func modelReverseReturn(t *testing.T, f *disassemblyTestFixture, request, original string) (string, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err = modelReceipt(ctx, tx, request); err != nil {
		return "", err
	}
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO assembly_disassembly_return_reversals(request_id,return_id) VALUES($1::uuid,$2::uuid) RETURNING id::text`, request, original).Scan(&id); err != nil {
		return "", err
	}
	result, err := tx.Exec(ctx, `UPDATE cell_stock c SET quantity=c.quantity-1 FROM assembly_disassembly_returns r WHERE r.id=$1::uuid AND c.cell_id=r.cell_id AND c.product_id=r.product_id AND c.quantity>0`, original)
	if err != nil {
		return "", err
	}
	if result.RowsAffected() != 1 {
		return "", fmt.Errorf("test compensation could not debit its original cell")
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

func modelItem(t *testing.T, f *disassemblyTestFixture, id string) map[string]any {
	t.Helper()
	for _, value := range valueArray(disassemblyTestSession(warehouseState(t, f.db))["items"]) {
		item := valueMap(value)
		if textValue(item["id"]) == id {
			return item
		}
	}
	t.Fatalf("missing source item %s", id)
	return nil
}

func modelReturned(t *testing.T, f *disassemblyTestFixture) int {
	t.Helper()
	count := 0
	for _, value := range valueArray(disassemblyTestSession(warehouseState(t, f.db))["items"]) {
		if valueMap(value)["returned"] == true {
			count++
		}
	}
	return count
}

func modelRow(t *testing.T, db *pgxpool.Pool, relation, id string) string {
	t.Helper()
	var raw string
	if err := db.QueryRow(context.Background(), `SELECT to_jsonb(r)::text FROM `+relation+` r WHERE id=$1::uuid`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDisassemblyReturnModelUpgradePreservesExistingReturnsAndReadState(t *testing.T) {
	f := disassemblyLegacyFixture(t, false)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	f.action(t, "scan", "CELL:1", true)
	before := warehouseState(t, f.db)
	events := archiveEventSnapshot(t, f.db)
	var ledger string
	if err := f.db.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(r) ORDER BY id)::text FROM assembly_disassembly_returns r`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	applyDisassemblyReversalsMigration(t, f.db)
	if !reflect.DeepEqual(before, warehouseState(t, f.db)) || events != archiveEventSnapshot(t, f.db) {
		t.Fatal("019 changed existing progress, IDs, timestamps or history")
	}
	item := valueMap(valueArray(disassemblyTestSession(before)["items"])[0])
	var afterLedger string
	if err := f.db.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(r) ORDER BY id)::text FROM assembly_disassembly_returns r`).Scan(&afterLedger); err != nil {
		t.Fatal(err)
	}
	if modelRow(t, f.db, "assembly_disassembly_returns", textValue(item["returnId"])) == "" || ledger != afterLedger {
		t.Fatal("original return missing")
	}
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 1)
	assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 0)
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
	f.returnAll(t)
	f.action(t, "finish", "", true)
}

func TestDisassemblyReturnModelReversalAndRepeatPhysicalReturnPreserveHistory(t *testing.T) {
	f := disassemblyFixture(t, true)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	f.action(t, "scan", "CELL:1", true)
	first := valueMap(valueArray(disassemblyTestSession(f.state)["items"])[0])
	itemID, firstID := textValue(first["id"]), textValue(first["returnId"])
	original := modelRow(t, f.db, "assembly_disassembly_returns", firstID)
	events := archiveEventSnapshot(t, f.db)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 1)
	reversal, err := modelReverseReturn(t, f, warehouseID(9000), firstID)
	if err != nil {
		t.Fatal(err)
	}
	if modelRow(t, f.db, "assembly_disassembly_returns", firstID) != original || archiveEventSnapshot(t, f.db) != events {
		t.Fatal("reversal rewrote original physical return/history")
	}
	reversed := modelItem(t, f, itemID)
	if reversed["returned"] != false || reversed["returnId"] != nil || reversed["returnedAt"] != nil || modelReturned(t, f) != 0 {
		t.Fatal("read model still includes reversed return", reversed)
	}
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 1)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 0)
	assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 1)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	tx, err := f.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var oid string
	if err = tx.QueryRow(context.Background(), `SELECT order_id::text FROM assembly_disassembly_sessions WHERE id=$1::uuid`, f.sid).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	// All seven original picks must again reconcile, rather than excluding the
	// historical-but-compensated physical return.
	picks := valueArray(valueMap(valueMap(testIssues(f.state)[1])["assembly"])["picks"])
	if err = verifyWarehousePicks(context.Background(), tx, oid, picks); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(context.Background())
	f.state = warehouseState(t, f.db)
	f.action(t, "scan", "A", true)
	if disassemblyTestSession(f.state)["pendingItemId"] != itemID {
		t.Fatal("next scan skipped compensated source unit")
	}
	f.action(t, "scan", "CELL:1", true)
	second := modelItem(t, f, itemID)
	secondID := textValue(second["returnId"])
	if second["returned"] != true || secondID == "" || secondID == firstID || second["returnedAt"] == nil || modelReturned(t, f) != 1 {
		t.Fatal("repeat return was not the sole effective return", second)
	}
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 2)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 1)
	if modelRow(t, f.db, "assembly_disassembly_returns", firstID) != original || modelRow(t, f.db, "assembly_disassembly_return_reversals", reversal) == "" {
		t.Fatal("return -> reversal -> return chain was not preserved")
	}
	detail := archiveTestDetail(t, f.db, f.pid)
	journals := valueMap(detail["journals"])
	historical := valueArray(journals["disassembly"])
	if len(historical) != 1 || len(valueArray(valueMap(historical[0])["returns"])) != 2 {
		t.Fatal("historical journal hid compensated physical return", detail)
	}
	stockTestQuantity(t, f.db, "cell_stock", 1, 4)
}

func TestDisassemblyReturnModelFinishAndPickReconciliationUseEffectiveReturns(t *testing.T) {
	f := disassemblyFixture(t, false)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.returnAll(t)
	var lastID, itemID string
	if err := f.db.QueryRow(context.Background(), `SELECT r.id::text,r.item_id::text FROM assembly_disassembly_effective_returns r ORDER BY r.occurred_at DESC,r.id DESC LIMIT 1`).Scan(&lastID, &itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := modelReverseReturn(t, f, warehouseID(9001), lastID); err != nil {
		t.Fatal(err)
	}
	if modelReturned(t, f) != 6 {
		t.Fatal("global/SKU count ignored reversal")
	}
	assertDisassemblyFinishPreconditions(t, f, [4]int{1, 0, 1, 0})
	assertDisassemblyFinishRejectedUnchanged(t, f, "Сначала физически верните все исходные отборы.")
	tx, err := f.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var oid, op string
	if err = tx.QueryRow(context.Background(), `SELECT s.order_id::text,i.original_operation_id::text FROM assembly_disassembly_items i JOIN assembly_disassembly_sessions s ON s.id=i.session_id WHERE i.id=$1::uuid`, itemID).Scan(&oid, &op); err != nil {
		t.Fatal(err)
	}
	if err = verifyWarehousePicks(context.Background(), tx, oid, []any{}); err == nil {
		t.Fatal("reconciliation hid compensated pick")
	}
	if err = verifyWarehousePicks(context.Background(), tx, oid, []any{map[string]any{"operationId": op}}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(context.Background())
	f.state = warehouseState(t, f.db)
	f.returnAll(t)
	f.action(t, "finish", "", true)
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 8)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 7)
	stockTestQuantity(t, f.db, "cell_stock", 1, 10)
}

func TestDisassemblyReturnModelReversalConstraintsAndImmutability(t *testing.T) {
	f := disassemblyFixture(t, false)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	f.action(t, "scan", "CELL:1", true)
	original := valueMap(valueArray(disassemblyTestSession(f.state)["items"])[0])
	id := textValue(original["returnId"])
	reversal, err := modelReverseReturn(t, f, warehouseID(9002), id)
	if err != nil {
		t.Fatal(err)
	}
	beforeReturn := modelRow(t, f.db, "assembly_disassembly_returns", id)
	beforeReversal := modelRow(t, f.db, "assembly_disassembly_return_reversals", reversal)
	_, err = modelReverseReturn(t, f, warehouseID(9003), id)
	requireModelSQLState(t, err, "23505")
	_, err = modelReverseReturn(t, f, warehouseID(9004), warehouseID(99999))
	requireModelSQLState(t, err, "23503")
	for _, relation := range []string{"assembly_disassembly_returns", "assembly_disassembly_return_reversals"} {
		_, err = f.db.Exec(context.Background(), `UPDATE `+relation+` SET occurred_at=clock_timestamp()`)
		requireModelSQLState(t, err, "23514")
		_, err = f.db.Exec(context.Background(), `DELETE FROM `+relation)
		requireModelSQLState(t, err, "23514")
	}
	if modelRow(t, f.db, "assembly_disassembly_returns", id) != beforeReturn || modelRow(t, f.db, "assembly_disassembly_return_reversals", reversal) != beforeReversal {
		t.Fatal("append-only records changed")
	}
	assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 1)
	stockTestQuantity(t, f.db, "cell_stock", 1, 3)
	// The deferred receipt FK is enforced at COMMIT, exactly as for returns.
	tx, err := f.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.state = warehouseState(t, f.db)
	f.action(t, "scan", "A", true)
	f.action(t, "scan", "CELL:1", true)
	latest := modelItem(t, f, textValue(original["id"]))
	_, err = tx.Exec(context.Background(), `INSERT INTO assembly_disassembly_return_reversals(request_id,return_id) VALUES($1::uuid,$2::uuid)`, warehouseID(9005), textValue(latest["returnId"]))
	if err != nil {
		t.Fatal(err)
	}
	requireModelSQLState(t, tx.Commit(context.Background()), "23503")
	assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 1)
}

func modelInsertReturn(ctx context.Context, tx pgx.Tx, request, item string) error {
	if err := modelReceipt(ctx, tx, request); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO assembly_disassembly_returns(request_id,item_id,product_id,cell_id,quantity)
 SELECT $1::uuid,i.id,op.product_id,op.cell_id,1 FROM assembly_disassembly_items i JOIN assembly_warehouse_operations op ON op.id=i.original_operation_id WHERE i.id=$2::uuid`, request, item)
	return err
}

func TestDisassemblyReturnModelConcurrentInsertAllowsOnlyOneEffectiveReturn(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.Serializable} {
		for _, again := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_after_reversal_%t", isolation, again), func(t *testing.T) {
				f := disassemblyFixture(t, false)
				f.transfer(t)
				f.action(t, "start", "", true)
				if again {
					f.action(t, "scan", "A", true)
					f.action(t, "scan", "CELL:1", true)
					item := valueMap(valueArray(disassemblyTestSession(f.state)["items"])[0])
					if _, err := modelReverseReturn(t, f, warehouseID(9020), textValue(item["returnId"])); err != nil {
						t.Fatal(err)
					}
				}
				f.action(t, "scan", "A", true)
				itemID := textValue(disassemblyTestSession(f.state)["pendingItemId"])
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				first, err := f.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
				if err != nil {
					t.Fatal(err)
				}
				defer first.Rollback(ctx)
				if err = modelInsertReturn(ctx, first, warehouseID(9021), itemID); err != nil {
					t.Fatal(err)
				}
				result := make(chan error, 1)
				go func() {
					tx, e := f.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
					if e != nil {
						result <- e
						return
					}
					defer tx.Rollback(ctx)
					e = modelInsertReturn(ctx, tx, warehouseID(9022), itemID)
					if e == nil {
						e = tx.Commit(ctx)
					}
					result <- e
				}()
				select {
				case e := <-result:
					t.Fatalf("competing return did not wait for source lock: %v", e)
				case <-time.After(100 * time.Millisecond):
				}
				if err = first.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				secondErr := <-result
				var pgerr *pgconn.PgError
				if !errors.As(secondErr, &pgerr) || (pgerr.Code != "23505" && !(isolation == pgx.Serializable && pgerr.Code == "40001")) {
					t.Fatalf("competing effective return was not rejected: %v", secondErr)
				}
				assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 1)
				want := 1
				if again {
					want = 2
				}
				assertStorageCount(t, f.db, "assembly_disassembly_returns", want)
			})
		}
	}

}

func TestDisassemblyReturnModelRepeatableReadWritesAreRejected(t *testing.T) {
	f := disassemblyFixture(t, false)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	err = modelInsertReturn(ctx, tx, warehouseID(9030), textValue(disassemblyTestSession(f.state)["pendingItemId"]))
	requireModelSQLState(t, err, "0A000")
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 0)
	tx.Rollback(ctx)
	f.action(t, "scan", "CELL:1", true)
	item := valueMap(valueArray(disassemblyTestSession(f.state)["items"])[0])
	reverseTx, err := f.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer reverseTx.Rollback(ctx)
	if err = modelReceipt(ctx, reverseTx, warehouseID(9031)); err != nil {
		t.Fatal(err)
	}
	_, err = reverseTx.Exec(ctx, `INSERT INTO assembly_disassembly_return_reversals(request_id,return_id) VALUES($1::uuid,$2::uuid)`, warehouseID(9031), textValue(item["returnId"]))
	requireModelSQLState(t, err, "0A000")
	assertStorageCount(t, f.db, "assembly_disassembly_return_reversals", 0)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 1)
}

func TestDisassemblyReturnModelUndoPickGuardUsesOnlyEffectiveReturns(t *testing.T) {
	f := disassemblyFixture(t, false)
	f.transfer(t)
	f.action(t, "start", "", true)
	f.action(t, "scan", "A", true)
	f.action(t, "scan", "CELL:1", true)
	item := valueMap(valueArray(disassemblyTestSession(f.state)["items"])[0])
	undo := `INSERT INTO assembly_warehouse_operations(request_id,order_id,order_position_id,product_id,cell_id,kind,quantity,reverses_operation_id)
 SELECT $1::uuid,op.order_id,op.order_position_id,op.product_id,op.cell_id,'undo_pick',1,op.id FROM assembly_warehouse_operations op WHERE op.id=$2::uuid`
	_, err := f.db.Exec(context.Background(), undo, warehouseID(9040), textValue(item["operationId"]))
	requireModelSQLState(t, err, "23514")
	if _, err = modelReverseReturn(t, f, warehouseID(9041), textValue(item["returnId"])); err != nil {
		t.Fatal(err)
	}
	// Model-only fixture: no assembly undo action is invoked or added here.
	stockTestExec(t, f.db, undo, warehouseID(9040), textValue(item["operationId"]))
	stockTestExec(t, f.db, `UPDATE assembly_disassembly_sessions SET pending_item_id=$1::uuid WHERE id=$2::uuid`, textValue(item["id"]), f.sid)
	tx, err := f.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	requireModelSQLState(t, modelInsertReturn(context.Background(), tx, warehouseID(9042), textValue(item["id"])), "23514")
	assertStorageCount(t, f.db, "assembly_disassembly_returns", 1)
	assertStorageCount(t, f.db, "assembly_disassembly_effective_returns", 0)
}
