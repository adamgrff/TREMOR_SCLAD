package migrations

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// This is a bounded clean-v1 readiness contract, not a general schema diff.
// Captured from the reviewed DB-03.3 catalogue; migration hashes are checked too.
//
//go:embed warehouse_contract.json
var warehouseContractJSON []byte

type schemaObject struct {
	Name       string `json:"name"`
	Table      string `json:"table"`
	Type       string `json:"type"`
	Arguments  string `json:"arguments"`
	Definition string `json:"definition"`
}
type warehouseContract struct {
	Tables      []string       `json:"tables"`
	Constraints []schemaObject `json:"constraints"`
	Triggers    []schemaObject `json:"triggers"`
	Functions   []schemaObject `json:"functions"`
	Indexes     []schemaObject `json:"indexes"`
	Views       []schemaObject `json:"views"`
}

type Readiness struct {
	State         string `json:"state"`
	WarehouseID   int64  `json:"warehouseId,omitempty"`
	WarehouseName string `json:"warehouseName,omitempty"`
	Created       bool   `json:"created"`
}

var ErrWarehouseConflict = errors.New("warehouse state conflicts with explicit expected name")

func warehouseName(name string) error {
	if !utf8.ValidString(name) || name == "" || strings.TrimSpace(name) != name || strings.ContainsFunc(name, unicode.IsControl) || utf8.RuneCountInString(name) > 200 {
		return errors.New("explicit warehouse name of 1..200 characters without outer whitespace/control characters required")
	}
	return nil
}

// ValidateWarehouse uses exactly one read-only repeatable-read snapshot. It does
// not take a write lock, create receipts or call sequence functions.
func (r *Runner) ValidateWarehouse(ctx context.Context, expectedName string) (Readiness, error) {
	if err := warehouseName(expectedName); err != nil {
		return Readiness{}, err
	}
	conn, _, err := r.target.connect(ctx)
	if err != nil {
		return Readiness{}, err
	}
	defer closeConnection(conn)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Readiness{}, errors.New("cannot start read-only readiness snapshot")
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{r.target.Schema}.Sanitize()+",pg_catalog"); err != nil {
		return Readiness{}, errors.New("cannot select readiness schema")
	}
	state, err := r.inspectWarehouse(ctx, tx, expectedName)
	if err != nil {
		return Readiness{}, err
	}
	if tx.Commit(ctx) != nil {
		return Readiness{}, errors.New("cannot finish read-only readiness check")
	}
	return state, nil
}

// BootstrapWarehouse serializes with migration apply and locks all readiness
// tables before rechecking data. READ COMMITTED observes the preceding commit
// after waiting for the lock. No write is made until the entire contract passes.
func (r *Runner) BootstrapWarehouse(ctx context.Context, name string) (Readiness, error) {
	return r.bootstrapWarehouse(ctx, name, nil)
}

// afterInsert is a test-only transaction fault hook; no CLI can enable it.
func (r *Runner) bootstrapWarehouse(ctx context.Context, name string, afterInsert func(pgx.Tx) error) (Readiness, error) {
	if err := warehouseName(name); err != nil {
		return Readiness{}, err
	}
	conn, _, err := r.target.connect(ctx)
	if err != nil {
		return Readiness{}, err
	}
	defer closeConnection(conn)
	if _, err = conn.Exec(ctx, `SELECT pg_catalog.pg_advisory_lock($1)`, advisoryKey); err != nil {
		return Readiness{}, errors.New("cannot acquire deployment session lock")
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	if err != nil {
		return Readiness{}, errors.New("cannot start warehouse bootstrap")
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{r.target.Schema}.Sanitize()+",pg_catalog"); err != nil {
		return Readiness{}, errors.New("cannot select bootstrap schema")
	}
	// Verify ledger before issuing table locks against an unknown schema.
	state, err := r.readState(ctx, tx)
	if err != nil {
		return Readiness{}, err
	}
	if !state.Initialized || len(state.Pending) != 0 {
		return Readiness{}, ErrIncomplete
	}
	c, err := loadWarehouseContract()
	if err != nil {
		return Readiness{}, err
	}
	names := []string{pgx.Identifier{r.target.Schema, "schema_migrations"}.Sanitize()}
	if r.target.Profile == IdentityProfile {
		names = append(names, pgx.Identifier{r.target.Schema, "deployment_identity"}.Sanitize())
	}
	for _, t := range c.Tables {
		names = append(names, pgx.Identifier{r.target.Schema, t}.Sanitize())
	}
	if _, err = tx.Exec(ctx, "LOCK TABLE "+strings.Join(names, ",")+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return Readiness{}, errors.New("cannot lock bootstrap tables")
	}
	ready, err := r.inspectWarehouse(ctx, tx, name)
	if err != nil {
		return Readiness{}, err
	}
	if ready.State == "schema-ready" {
		var returnedName string
		err = tx.QueryRow(ctx, `INSERT INTO `+pgx.Identifier{r.target.Schema, "warehouses"}.Sanitize()+`(name) VALUES($1) RETURNING id,name`, name).Scan(&ready.WarehouseID, &returnedName)
		if err != nil || returnedName != name || ready.WarehouseID <= 0 {
			return Readiness{}, errors.New("warehouse insertion failed")
		}
		if afterInsert != nil {
			if err = afterInsert(tx); err != nil {
				return Readiness{}, errors.New("warehouse bootstrap transaction failed")
			}
		}
		ready, err = r.inspectWarehouse(ctx, tx, name)
		if err != nil || ready.State != "bootstrap-ready" {
			return Readiness{}, errors.New("warehouse postcondition failed")
		}
		ready.Created = true
	}
	if tx.Commit(ctx) != nil {
		// No blind INSERT retry. Repeating this explicit name safely determines
		// the result under the same session/table locks, even after a lost reply.
		return Readiness{}, errors.New("warehouse commit outcome uncertain; repeat bootstrap with the same name")
	}
	return ready, nil
}

func loadWarehouseContract() (warehouseContract, error) {
	var c warehouseContract
	if json.Unmarshal(warehouseContractJSON, &c) != nil || len(c.Tables) != 44 {
		return c, errors.New("invalid embedded clean-v1 readiness contract")
	}
	return c, nil
}

func (r *Runner) inspectWarehouse(ctx context.Context, tx pgx.Tx, name string) (Readiness, error) {
	state, err := r.readState(ctx, tx)
	if err != nil {
		return Readiness{}, err
	}
	if !state.Initialized || len(state.Pending) != 0 {
		return Readiness{}, ErrIncomplete
	}
	c, err := loadWarehouseContract()
	if err != nil {
		return Readiness{}, err
	}
	if err = r.checkWarehouseObjects(ctx, tx, c); err != nil {
		return Readiness{}, err
	}
	if err = r.checkDeployment(ctx, tx); err != nil {
		return Readiness{}, err
	}
	for _, t := range c.Tables {
		if t == "warehouses" {
			continue
		}
		var occupied bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+pgx.Identifier{r.target.Schema, t}.Sanitize()+` LIMIT 1)`).Scan(&occupied); err != nil || occupied {
			return Readiness{}, fmt.Errorf("%w: nonempty or unreadable business table %s", ErrUnsafe, t)
		}
	}
	rows, err := tx.Query(ctx, `SELECT id,name FROM `+pgx.Identifier{r.target.Schema, "warehouses"}.Sanitize()+` ORDER BY id LIMIT 2`)
	if err != nil {
		return Readiness{}, errors.New("cannot read warehouses")
	}
	defer rows.Close()
	ready := Readiness{State: "schema-ready"}
	count := 0
	for rows.Next() {
		count++
		if rows.Scan(&ready.WarehouseID, &ready.WarehouseName) != nil {
			return Readiness{}, errors.New("cannot decode warehouse")
		}
	}
	if rows.Err() != nil {
		return Readiness{}, errors.New("cannot finish warehouse read")
	}
	if count > 1 || (count == 1 && (ready.WarehouseName != name || ready.WarehouseID <= 0)) {
		return Readiness{}, ErrWarehouseConflict
	}
	if count == 1 {
		ready.State = "bootstrap-ready"
	}
	return ready, nil
}

func (r *Runner) checkWarehouseObjects(ctx context.Context, tx pgx.Tx, c warehouseContract) error {
	fail := func(detail ...string) error {
		return fmt.Errorf("%w: required clean-v1 objects or protections differ (%s)", ErrUnsafe, strings.Join(detail, " "))
	}
	// Only ordinary, non-RLS tables are accepted, including outside the target.
	rows, err := tx.Query(ctx, `SELECT n.nspname,c.relname,c.relkind::text,c.relrowsecurity,c.relforcerowsecurity FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema' AND c.relkind IN ('r','p','f','m')`)
	if err != nil {
		return fail()
	}
	want := map[string]bool{"schema_migrations": true}
	if r.target.Profile == IdentityProfile {
		want["deployment_identity"] = true
	}
	for _, t := range c.Tables {
		want[t] = true
	}
	for rows.Next() {
		var schema, t, kind string
		var rls, force bool
		if rows.Scan(&schema, &t, &kind, &rls, &force) != nil || schema != r.target.Schema || kind != "r" || rls || force || !want[t] {
			rows.Close()
			return fail()
		}
		delete(want, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(want) != 0 {
		return fail()
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indexrelid WHERE c.relnamespace=$1::regnamespace AND (NOT i.indisvalid OR NOT i.indisready)) AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint WHERE connamespace=$1::regnamespace AND NOT convalidated)`, r.target.Schema).Scan(&valid)
	if err != nil || !valid {
		return fail()
	}
	for _, o := range c.Constraints {
		var definition, typ string
		err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_constraintdef(c.oid),c.contype::text FROM pg_catalog.pg_constraint c JOIN pg_catalog.pg_class t ON t.oid=c.conrelid WHERE t.relnamespace=$1::regnamespace AND t.relname=$2 AND c.conname=$3`, r.target.Schema, o.Table, o.Name).Scan(&definition, &typ)
		if err != nil || definition != o.Definition || typ != o.Type {
			return fail("Constraints", o.Table, o.Name)
		}
	}
	for _, o := range c.Triggers {
		var definition, enabled string
		err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_triggerdef(g.oid),g.tgenabled::text FROM pg_catalog.pg_trigger g JOIN pg_catalog.pg_class t ON t.oid=g.tgrelid WHERE t.relnamespace=$1::regnamespace AND t.relname=$2 AND g.tgname=$3 AND NOT g.tgisinternal`, r.target.Schema, o.Table, o.Name).Scan(&definition, &enabled)
		definition = strings.Replace(definition, " ON "+r.target.Schema+".", " ON public.", 1)
		if err != nil || definition != o.Definition || enabled != "O" {
			return fail("Triggers", o.Table, o.Name)
		}
	}
	// Reject extra triggers/rules: they could silently alter bootstrap INSERT.
	triggerCount := len(c.Triggers)
	if r.target.Profile == IdentityProfile {
		triggerCount += 2
	}
	err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_catalog.pg_trigger g JOIN pg_catalog.pg_class t ON t.oid=g.tgrelid WHERE t.relnamespace=$1::regnamespace AND NOT g.tgisinternal)=$2 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_rewrite w JOIN pg_catalog.pg_class t ON t.oid=w.ev_class WHERE t.relnamespace=$1::regnamespace AND t.relkind='r')`, r.target.Schema, triggerCount).Scan(&valid)
	if err != nil || !valid {
		return fail("extra triggers or table rules")
	}
	for _, o := range c.Functions {
		var definition string
		err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_functiondef(p.oid) FROM pg_catalog.pg_proc p WHERE p.pronamespace=$1::regnamespace AND p.proname=$2 AND pg_catalog.pg_get_function_identity_arguments(p.oid)=$3`, r.target.Schema, o.Name, o.Arguments).Scan(&definition)
		if err != nil {
			return fail("Functions", o.Table, o.Name)
		}
		definition = strings.Replace(definition, "FUNCTION "+r.target.Schema+".", "FUNCTION public.", 1)
		if o.Definition != "" && definition != o.Definition {
			return fail("Functions", o.Table, o.Name)
		}
	}
	for _, o := range c.Indexes {
		var definition string
		err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_indexdef(c.oid) FROM pg_catalog.pg_class c WHERE c.relnamespace=$1::regnamespace AND c.relname=$2 AND c.relkind='i'`, r.target.Schema, o.Name).Scan(&definition)
		definition = strings.ReplaceAll(definition, r.target.Schema+".", "public.")
		if err != nil || definition != o.Definition {
			return fail("Indexes", o.Table, o.Name)
		}
	}
	for _, o := range c.Views {
		var definition string
		err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_viewdef(c.oid) FROM pg_catalog.pg_class c WHERE c.relnamespace=$1::regnamespace AND c.relname=$2 AND c.relkind='v'`, r.target.Schema, o.Name).Scan(&definition)
		if err != nil || definition != o.Definition {
			return fail("Views", o.Table, o.Name)
		}
	}
	// Explicit bootstrap column shape, identity and defaults. Other columns are
	// covered only insofar as the declared constraints/functions require them.
	err = tx.QueryRow(ctx, `SELECT count(*)=3 AND count(*) FILTER(WHERE attname='id' AND atttypid='pg_catalog.int8'::regtype AND attnotnull AND attidentity='a')=1 AND count(*) FILTER(WHERE attname='name' AND atttypid='pg_catalog.text'::regtype AND attnotnull AND attidentity='' AND attgenerated='')=1 AND count(*) FILTER(WHERE attname='created_at' AND atttypid='pg_catalog.timestamptz'::regtype AND attnotnull)=1 FROM pg_catalog.pg_attribute WHERE attrelid=$1::regclass AND attnum>0 AND NOT attisdropped`, pgx.Identifier{r.target.Schema, "warehouses"}.Sanitize()).Scan(&valid)
	if err != nil || !valid {
		return fail()
	}
	err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_expr(d.adbin,d.adrelid)='now()' FROM pg_catalog.pg_attrdef d JOIN pg_catalog.pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum WHERE a.attrelid=$1::regclass AND a.attname='created_at'`, pgx.Identifier{r.target.Schema, "warehouses"}.Sanitize()).Scan(&valid)
	if err != nil || !valid {
		return fail("warehouse timestamp default")
	}
	err = tx.QueryRow(ctx, `SELECT a.atttypid='pg_catalog.int8'::regtype AND a.attnotnull AND a.attgenerated='' AND pg_catalog.pg_get_expr(d.adbin,d.adrelid)='0' FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=$1::regclass AND a.attname='pending_revision' AND NOT a.attisdropped`, pgx.Identifier{r.target.Schema, "receiving_sessions"}.Sanitize()).Scan(&valid)
	if err != nil || !valid {
		return fail("pending revision column")
	}
	return nil
}
