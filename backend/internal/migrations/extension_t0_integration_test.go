package migrations

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// All SQL in this file is synthetic TEST data. No credentials or live data.
func extensionDataSnapshot(t *testing.T, conn *pgx.Conn, schema string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r' AND c.relname<>'schema_extensions' ORDER BY c.relname`, schema)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	result := map[string]string{}
	for _, name := range names {
		var value string
		// Full rows, including IDs/status/timestamps/JSON payloads; stable multiset order.
		err = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(r) AS v FROM `+pgx.Identifier{schema, name}.Sanitize()+` r) s`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		result[name] = value
	}
	return result
}

func extensionSyntheticBusiness(t *testing.T, r *ExtensionRunner, conn *pgx.Conn) {
	t.Helper()
	schema := pgx.Identifier{r.base.target.Schema}.Sanitize()
	writeTestSQL(t, conn, `SET LOCAL search_path TO `+schema+`,pg_catalog;
 WITH w AS (INSERT INTO warehouses(name) VALUES('T0 synthetic warehouse') RETURNING id),
 c AS (INSERT INTO categories(code,name) VALUES('t0','T0 synthetic category') RETURNING id),
 cell AS (INSERT INTO cells(warehouse_id,category_id,code,row_number,position_number) SELECT w.id,c.id,'T0-1',1,1 FROM w,c RETURNING id,category_id),
 p AS (INSERT INTO products(category_id,sku,name) SELECT category_id,'T0-SKU','T0 synthetic product' FROM cell RETURNING id),
 session AS (INSERT INTO receiving_sessions(status,completed_at) VALUES('active',NULL) RETURNING id),
 placement AS (INSERT INTO receiving_placements(cell_id,receiving_session_id) SELECT cell.id,session.id FROM cell,session RETURNING id),
 stock AS (INSERT INTO cell_stock(cell_id,product_id,quantity) SELECT cell.id,p.id,3 FROM cell,p),
 pending AS (INSERT INTO receiving_session_items(session_id,product_id,quantity) SELECT session.id,p.id,2 FROM session,p),
 intake AS (INSERT INTO receiving_intake_events(session_id,product_id,quantity) SELECT session.id,p.id,5 FROM session,p),
 quantity_receipt AS (INSERT INTO receiving_quantity_requests(request_id,payload,product_id) SELECT 't0-quantity','synthetic',p.id FROM p)
 INSERT INTO receiving_placement_items(placement_id,product_id,quantity) SELECT placement.id,p.id,3 FROM placement,p;
 INSERT INTO receiving_operation_requests(request_id,operation,payload,response,status_code)
 SELECT '00000000-0000-4000-8000-000000000123','place',
 jsonb_build_object('sessionId',s.id,'productId',p.id,'cellId',c.id,'quantity',3),
 jsonb_build_object('placementId',rp.id,'status','active'),200
 FROM receiving_sessions s JOIN receiving_placements rp ON rp.receiving_session_id=s.id
 JOIN cells c ON c.id=rp.cell_id JOIN receiving_placement_items pi ON pi.placement_id=rp.id
 JOIN products p ON p.id=pi.product_id WHERE p.sku='T0-SKU';`)
}

func TestExtensionIntegrationBusinessPreservation(t *testing.T) {
	r, conn := extensionIntegrationDeployment(t)
	extensionSyntheticBusiness(t, r, conn)
	// A real schema extension, with the expected post-DDL fingerprint measured
	// inside a rolled-back TEST transaction, never approved by the ordinary CLI.
	ddl := "CREATE TABLE " + pgx.Identifier{r.base.target.Schema, "t0_upgrade_probe"}.Sanitize() + "(id integer PRIMARY KEY)"
	probe, err := conn.BeginTx(context.Background(), pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = probe.Exec(context.Background(), "SET LOCAL search_path TO pg_catalog,"+pgx.Identifier{r.base.target.Schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err = probe.Exec(context.Background(), ddl); err != nil {
		t.Fatal(err)
	}
	postHash, err := extensionCatalog(context.Background(), probe, r.base.target.Schema)
	rollback(probe)
	if err != nil {
		t.Fatal(err)
	}
	path, plan := extensionFixture(t, r.base.manifest, r.manifest.plan.BaselineReadinessSHA256, 1)
	sql := []byte("BEGIN;\n" + ddl + ";\nCOMMIT;\n")
	plan.Migrations[0].SHA256 = digest(sql)
	plan.Migrations[0].ReadinessSHA256 = postHash
	if err = os.WriteFile(filepath.Join(filepath.Dir(path), plan.Migrations[0].Name), sql, 0600); err != nil {
		t.Fatal(err)
	}
	writeExtensionPlan(t, path, plan)
	m, err := LoadExtension(path, r.base.manifest)
	if err != nil {
		t.Fatal(err)
	}
	r = fixtureExtensionRunner(t, r.base.target, m, r.options)
	before := extensionDataSnapshot(t, conn, r.base.target.Schema)
	for _, name := range []string{"warehouses", "categories", "cells", "products", "cell_stock", "receiving_sessions", "receiving_session_items", "receiving_placements", "receiving_placement_items", "receiving_intake_events", "receiving_quantity_requests", "receiving_operation_requests"} {
		if before[name] == "" || before[name] == "[]" {
			t.Fatalf("empty required fixture: %s", name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if _, err := r.Run(ctx, "apply"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Run(ctx, "verify"); err != nil {
		t.Fatal(err)
	}
	after := extensionDataSnapshot(t, conn, r.base.target.Schema)
	if after["t0_upgrade_probe"] != "[]" {
		t.Fatal("schema extension missing or unexpected data")
	}
	delete(after, "t0_upgrade_probe")
	if !reflect.DeepEqual(before, after) {
		for name, value := range before {
			if value != after[name] {
				t.Errorf("full rows changed: %s", name)
			}
		}
		t.Fatal("business/baseline/identity snapshot changed")
	}
}

func crashFixture(t *testing.T, r *ExtensionRunner, conn *pgx.Conn) string {
	t.Helper()
	table := pgx.Identifier{r.base.target.Schema, "warehouses"}.Sanitize()
	writeTestSQL(t, conn, `INSERT INTO `+table+`(name) VALUES('T0 crash effect')`)
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog,"+pgx.Identifier{r.base.target.Schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	hash, err := extensionCatalog(ctx, tx, r.base.target.Schema)
	rollback(tx)
	if err != nil {
		t.Fatal(err)
	}
	path, p := extensionFixture(t, r.base.manifest, hash, 1)
	sql := []byte("BEGIN;\nUPDATE " + table + " SET name=name||'-executed' WHERE name LIKE 'T0 crash effect%';\nCOMMIT;\n")
	p.Migrations[0].SHA256 = digest(sql)
	if err = os.WriteFile(filepath.Join(filepath.Dir(path), p.Migrations[0].Name), sql, 0600); err != nil {
		t.Fatal(err)
	}
	writeExtensionPlan(t, path, p)
	r.manifest, err = LoadExtension(path, r.base.manifest)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// Real OS process termination, not a network fault. PostgreSQL stays alive.
func TestExtensionIntegrationHardCrash(t *testing.T) {
	for _, phase := range []string{"before_commit", "after_commit"} {
		t.Run(phase, func(t *testing.T) {
			r, conn := extensionIntegrationDeployment(t)
			path := crashFixture(t, r, conn)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestExtensionIntegrationCrashChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), "E1A_CRASH_PHASE="+phase, "E1A_RESTART_SCHEMA="+r.base.target.Schema, "E1A_RESTART_MANIFEST="+path, "E1A_RESTART_DEPLOYMENT="+r.options.DeploymentID)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill() }()
			ready := make(chan bool, 1)
			go func() {
				s := bufio.NewScanner(stdout)
				for s.Scan() {
					if s.Text() == "E1A_CRASH_READY" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					_ = cmd.Wait()
					t.Fatal("child exited before crash barrier")
				}
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("crash barrier timeout")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil {
				t.Fatal("child was not terminated")
			}
			// Read-only observation never retries SQL. Next child takes the deployment lock.
			observed := fixtureExtensionRunner(t, r.base.target, r.manifest, r.options)
			state, err := observed.Run(ctx, "status")
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if phase == "after_commit" {
				want = 1
			}
			if len(state.Applied) != want {
				t.Fatalf("durable prefix %d, want %d", len(state.Applied), want)
			}
			extensionRestartChild(t, r, path)
			var effect string
			if err = conn.QueryRow(ctx, `SELECT name FROM `+pgx.Identifier{r.base.target.Schema, "warehouses"}.Sanitize()+` WHERE name LIKE 'T0 crash effect%'`).Scan(&effect); err != nil || effect != "T0 crash effect-executed" {
				t.Fatalf("SQL must execute exactly once, got %q: %v", effect, err)
			}
			if _, err = observed.Run(ctx, "verify"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExtensionIntegrationCrashChild(t *testing.T) {
	phase := os.Getenv("E1A_CRASH_PHASE")
	if phase == "" || os.Getenv("EXTENSION_TEST_DISPOSABLE_APPROVED") != "yes" {
		t.Skip("approved parent required")
	}
	if phase != "before_commit" && phase != "after_commit" {
		t.Fatal("invalid phase")
	}
	target := testTarget(t)
	target.Profile = IdentityProfile
	target.Schema = os.Getenv("E1A_RESTART_SCHEMA")
	if !strings.HasPrefix(target.Schema, "migration_runner_") || !schemaName.MatchString(target.Schema) {
		t.Fatal("invalid schema")
	}
	m, err := LoadExtension(os.Getenv("E1A_RESTART_MANIFEST"), extensionBase(t))
	if err != nil {
		t.Fatal(err)
	}
	r := fixtureExtensionRunner(t, target, m, ExtensionOptions{os.Getenv("E1A_RESTART_DEPLOYMENT"), target.User, target.User})
	barrier := func() { fmt.Println("E1A_CRASH_READY"); time.Sleep(time.Hour) }
	if phase == "before_commit" {
		r.hooks.afterLedger = func(int, pgx.Tx) error { barrier(); return errors.New("barrier unexpectedly resumed") }
	} else {
		r.hooks.commit = func(ctx context.Context, _ int, tx pgx.Tx) error {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			barrier()
			return errors.New("barrier unexpectedly resumed")
		}
	}
	if _, err = r.Run(context.Background(), "apply"); err != nil {
		t.Fatal(err)
	}
	t.Fatal("parent did not terminate child")
}

func TestExtensionIntegrationOrdinaryCLIT0(t *testing.T) {
	r, _ := extensionIntegrationDeployment(t)
	exe := os.Getenv("E1A_TEST_CLI")
	if exe == "" {
		t.Fatal("separately built ordinary CLI path required")
	}
	path, _ := extensionFixture(t, r.base.manifest, r.manifest.plan.BaselineReadinessSHA256, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target := r.base.target
	args := []string{"apply", "--profile", IdentityProfile, "--manifest", path, "--baseline-manifest", "../../migrations/manifest.clean-v2.json", "--host", target.Host, "--port", strconv.Itoa(target.Port), "--database", target.Database, "--user", target.User, "--schema", target.Schema, "--server-address", target.ServerAddress, "--server-id", target.ServerID, "--sslmode", "disable", "--deployment-id", r.options.DeploymentID, "--baseline-owner", r.options.BaselineOwner, "--ledger-owner", r.options.LedgerOwner}
	cmd := exec.CommandContext(ctx, exe, args...)
	// Deliberately wrong password: approval rejection must precede authentication.
	cmd.Env = append(os.Environ(), "MIGRATION_PASSWORD=intentionally-invalid")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), ErrExtensionUnapproved.Error()) {
		t.Fatalf("ordinary executable bypassed approval: %v %s", err, output)
	}
}
