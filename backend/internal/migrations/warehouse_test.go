package migrations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

const syntheticWarehouse = "DB034 Synthetic TEST warehouse"

func TestWarehouseNames(t *testing.T) {
	for _, name := range []string{"", " ", " padded", "padded ", "line\nbreak", strings.Repeat("x", 201), string([]byte{0xff})} {
		if warehouseName(name) == nil {
			t.Fatal("invalid name accepted")
		}
	}
	if warehouseName(syntheticWarehouse) != nil || warehouseName("ТЕСТ Ёжик") != nil {
		t.Fatal("valid name rejected")
	}
	if _, err := loadWarehouseContract(); err != nil {
		t.Fatal(err)
	}
}

func warehouseFixture(t *testing.T) (*Runner, *pgx.Conn) {
	t.Helper()
	target, conn := fixture(t)
	r := testRunner(t, target)
	if _, err := r.Run(context.Background(), "apply"); err != nil {
		t.Fatal(err)
	}
	return r, conn
}

func TestWarehouseIntegrationLifecycleAndReadOnly(t *testing.T) {
	r, conn := warehouseFixture(t)
	ctx := context.Background()
	var ledgerBefore, ledgerAfter string
	if conn.QueryRow(ctx, `SELECT json_agg(row_to_json(x) ORDER BY ordinal)::text FROM schema_migrations x`).Scan(&ledgerBefore) != nil {
		t.Fatal("ledger read")
	}
	ready, err := r.ValidateWarehouse(ctx, syntheticWarehouse)
	if err != nil || ready.State != "schema-ready" {
		t.Fatalf("schema: %+v %v", ready, err)
	}
	first, err := r.BootstrapWarehouse(ctx, syntheticWarehouse)
	if err != nil || !first.Created || first.WarehouseID <= 0 || first.State != "bootstrap-ready" {
		t.Fatalf("first: %+v %v", first, err)
	}
	var recordBefore, recordAfter string
	conn.QueryRow(ctx, `SELECT row_to_json(w)::text FROM warehouses w`).Scan(&recordBefore)
	for i := 0; i < 2; i++ {
		again, e := r.BootstrapWarehouse(ctx, syntheticWarehouse)
		if e != nil || again.Created || again.WarehouseID != first.WarehouseID {
			t.Fatalf("repeat: %+v %v", again, e)
		}
	}
	if _, err = r.BootstrapWarehouse(ctx, "DB034 Other TEST warehouse"); !errors.Is(err, ErrWarehouseConflict) {
		t.Fatal(err)
	}
	if _, err = r.ValidateWarehouse(ctx, "DB034 Other TEST warehouse"); !errors.Is(err, ErrWarehouseConflict) {
		t.Fatal(err)
	}
	ready, err = r.ValidateWarehouse(ctx, syntheticWarehouse)
	if err != nil || ready.State != "bootstrap-ready" || ready.Created {
		t.Fatalf("validate: %+v %v", ready, err)
	}
	conn.QueryRow(ctx, `SELECT row_to_json(w)::text FROM warehouses w`).Scan(&recordAfter)
	conn.QueryRow(ctx, `SELECT json_agg(row_to_json(x) ORDER BY ordinal)::text FROM schema_migrations x`).Scan(&ledgerAfter)
	if recordBefore != recordAfter || ledgerBefore != ledgerAfter {
		t.Fatal("repeat/validator changed records")
	}
	// The validator itself proves every other business table remains empty.
	var warehouses int
	conn.QueryRow(ctx, `SELECT count(*) FROM warehouses`).Scan(&warehouses)
	if warehouses != 1 {
		t.Fatal(warehouses)
	}
}

func TestWarehouseIntegrationConcurrentAndRollback(t *testing.T) {
	t.Run("concurrent", func(t *testing.T) {
		r, conn := warehouseFixture(t)
		ctx := context.Background()
		start := make(chan struct{})
		done := make(chan error, 2)
		created := make(chan bool, 2)
		for i := 0; i < 2; i++ {
			go func() {
				<-start
				s, e := r.BootstrapWarehouse(ctx, syntheticWarehouse)
				created <- s.Created
				done <- e
			}()
		}
		close(start)
		for i := 0; i < 2; i++ {
			if e := <-done; e != nil {
				t.Fatal(e)
			}
		}
		count := 0
		for i := 0; i < 2; i++ {
			if <-created {
				count++
			}
		}
		var actual int
		conn.QueryRow(ctx, `SELECT count(*) FROM warehouses`).Scan(&actual)
		if count != 1 || actual != 1 {
			t.Fatalf("created=%d rows=%d", count, actual)
		}
	})
	t.Run("rollback", func(t *testing.T) {
		r, conn := warehouseFixture(t)
		ctx := context.Background()
		_, err := r.bootstrapWarehouse(ctx, syntheticWarehouse, func(tx pgx.Tx) error { _, e := tx.Exec(ctx, `SELECT 1/0`); return e })
		if err == nil {
			t.Fatal("fault succeeded")
		}
		var count int
		conn.QueryRow(ctx, `SELECT count(*) FROM warehouses`).Scan(&count)
		if count != 0 {
			t.Fatal("partial bootstrap")
		}
		if s, e := r.ValidateWarehouse(ctx, syntheticWarehouse); e != nil || s.State != "schema-ready" {
			t.Fatalf("rollback: %+v %v", s, e)
		}
		if _, e := r.BootstrapWarehouse(ctx, syntheticWarehouse); e != nil {
			t.Fatal(e)
		}
	})
}

func TestWarehouseIntegrationRefusesUnsafeState(t *testing.T) {
	for _, kind := range []string{"unknown", "incomplete", "multiple", "data", "trigger-disabled", "trigger-rewritten", "function-rewritten", "constraint-removed", "columns", "extra-trigger", "ledger-checksum"} {
		t.Run(kind, func(t *testing.T) {
			target, conn := fixture(t)
			r := testRunner(t, target)
			ctx := context.Background()
			if kind != "unknown" {
				if _, e := r.Run(ctx, "apply"); e != nil {
					t.Fatal(e)
				}
			}
			statements := map[string]string{
				"unknown":            `CREATE TABLE unrelated(id int)`,
				"incomplete":         `DELETE FROM schema_migrations WHERE version=23`,
				"multiple":           `INSERT INTO warehouses(name) VALUES('DB034 TEST one'),('DB034 TEST two')`,
				"data":               `INSERT INTO categories(code,name) VALUES('synthetic-test','DB034 TEST category')`,
				"trigger-disabled":   `ALTER TABLE receiving_operation_requests DISABLE TRIGGER receiving_operation_requests_no_rewrite`,
				"trigger-rewritten":  `DROP TRIGGER receiving_operation_requests_no_rewrite ON receiving_operation_requests`,
				"function-rewritten": `CREATE OR REPLACE FUNCTION receiving_receipt_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`,
				"constraint-removed": `ALTER TABLE cell_stock DROP CONSTRAINT cell_stock_quantity_non_negative`,
				"columns":            `ALTER TABLE warehouses ALTER COLUMN name DROP NOT NULL`,
				"extra-trigger":      `CREATE TRIGGER unexpected BEFORE INSERT ON warehouses FOR EACH ROW EXECUTE FUNCTION receiving_receipt_immutable()`,
				"ledger-checksum":    `UPDATE schema_migrations SET checksum=repeat('a',64) WHERE version=23`,
			}
			writeTestSQL(t, conn, statements[kind])
			if _, e := r.BootstrapWarehouse(ctx, syntheticWarehouse); e == nil {
				t.Fatal("bootstrap accepted unsafe state")
			}
			if _, e := r.ValidateWarehouse(ctx, syntheticWarehouse); e == nil {
				t.Fatal("validator accepted unsafe state")
			}
			if kind != "unknown" {
				var n int
				conn.QueryRow(ctx, `SELECT count(*) FROM warehouses`).Scan(&n)
				expected := 0
				if kind == "multiple" {
					expected = 2
				}
				if n != expected {
					t.Fatal("changed warehouses")
				}
			}
		})
	}
}

func TestWarehouseIntegrationRejectsIdentityBeforeWrite(t *testing.T) {
	r, _ := warehouseFixture(t)
	ctx := context.Background()
	r.target.ServerID = "1"
	if _, err := r.BootstrapWarehouse(ctx, syntheticWarehouse); err == nil {
		t.Fatal("wrong identity accepted")
	}
	if _, err := r.ValidateWarehouse(ctx, syntheticWarehouse); err == nil {
		t.Fatal("wrong identity accepted")
	}
	r.target.Database = "TrEmOr_SkLaD"
	if _, err := r.BootstrapWarehouse(ctx, syntheticWarehouse); err == nil {
		t.Fatal("forbidden database accepted")
	}
}

// Executes the actual warehouse lookup and first-rack INSERTs from
// cmd/api/rack_create.go against a clean migrated/bootstrap fixture. No API
// process is started. The synthetic rack/cell are rolled back, not persisted.
func TestWarehouseIntegrationFirstRackSQLCompatibility(t *testing.T) {
	r, conn := warehouseFixture(t)
	ctx := context.Background()
	first, err := r.BootstrapWarehouse(ctx, syntheticWarehouse)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	var count int
	var warehouseID, categoryID int64
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(min(id),0) FROM warehouses`).Scan(&count, &warehouseID); err != nil || count != 1 || warehouseID != first.WarehouseID {
		t.Fatalf("lookup: %d %v", count, err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO categories(code,name) VALUES($1,$2) RETURNING id`, "rack-db034-synthetic", "DB034 Synthetic TEST rack").Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO cells(warehouse_id,category_id,code,row_number,position_number) VALUES($1,$2,$3,$4,$5)`, warehouseID, categoryID, "A1", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if ready, e := r.ValidateWarehouse(ctx, syntheticWarehouse); e != nil || ready.State != "bootstrap-ready" {
		t.Fatalf("rack rollback: %+v %v", ready, e)
	}
}
