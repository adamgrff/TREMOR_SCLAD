package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Integration tests require a separately approved TEST cluster identity, not
// DATABASE_URL or backend/.env. Every fixture checks actual identity read-only
// before it creates its own new schema; cleanup only drops that exact schema OID.
func testTarget(t *testing.T) Target {
	t.Helper()
	if os.Getenv("MIGRATION_TEST_DATABASE") == "" {
		t.Skip("isolated MIGRATION_TEST_* target and server identity required")
	}
	port, err := strconv.Atoi(os.Getenv("MIGRATION_TEST_PORT"))
	if err != nil {
		t.Fatal("invalid test port")
	}
	target := Target{Host: os.Getenv("MIGRATION_TEST_HOST"), Port: port, Database: os.Getenv("MIGRATION_TEST_DATABASE"), User: os.Getenv("MIGRATION_TEST_USER"), Profile: Profile, Schema: "public", ServerAddress: os.Getenv("MIGRATION_TEST_SERVER_ADDRESS"), ServerID: os.Getenv("MIGRATION_TEST_SERVER_ID"), SSLMode: "disable", Password: os.Getenv("MIGRATION_TEST_PASSWORD")}
	if !strings.HasPrefix(target.Database, "tremor_") || !strings.HasSuffix(target.Database, "_test") || target.Host != "127.0.0.1" {
		t.Fatal("integration target must be an explicit isolated loopback TEST database")
	}
	if err = target.Validate(); err != nil {
		t.Fatal(err)
	}
	return target
}

func fixture(t *testing.T) (Target, *pgx.Conn) {
	t.Helper()
	target := testTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, _, err := target.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target.Schema = fmt.Sprintf("migration_runner_%d", time.Now().UnixNano())
	tx, err := admin.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{target.Schema}.Sanitize()); err != nil {
		tx.Rollback(ctx)
		admin.Close(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	var oid int64
	if err = admin.QueryRow(ctx, `SELECT oid::bigint FROM pg_catalog.pg_namespace WHERE nspname=$1`, target.Schema).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	admin.Close(ctx)
	conn, _, err := target.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, "SET search_path TO "+pgx.Identifier{target.Schema}.Sanitize()+",pg_catalog"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn.Close(cleanupCtx)
		cleanup, _, err := target.connect(cleanupCtx)
		if err != nil {
			t.Error("cannot verify test cleanup target")
			return
		}
		defer cleanup.Close(cleanupCtx)
		var actual int64
		if cleanup.QueryRow(cleanupCtx, `SELECT oid::bigint FROM pg_catalog.pg_namespace WHERE nspname=$1`, target.Schema).Scan(&actual) != nil || actual != oid {
			t.Error("test schema identity changed; cleanup refused")
			return
		}
		tx, err := cleanup.BeginTx(cleanupCtx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
		if err != nil {
			t.Error(err)
			return
		}
		defer tx.Rollback(cleanupCtx)
		if _, err = tx.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{target.Schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error("isolated schema cleanup failed")
			return
		}
		if tx.Commit(cleanupCtx) != nil {
			t.Error("test cleanup commit failed")
		}
	})
	return target, conn
}

func testRunner(t *testing.T, target Target) *Runner {
	t.Helper()
	r, err := New(target, repositoryManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func writeTestSQL(t *testing.T, conn *pgx.Conn, sql string) {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
func ledgerSnapshot(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var value string
	if err := conn.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY ordinal),'[]'::jsonb)::text FROM schema_migrations m`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func exists(t *testing.T, conn *pgx.Conn, name string) bool {
	t.Helper()
	var yes bool
	if conn.QueryRow(context.Background(), `SELECT pg_catalog.to_regclass($1) IS NOT NULL`, name).Scan(&yes) != nil {
		t.Fatal("cannot inspect test relation")
	}
	return yes
}

func TestIntegrationFreshRepeatAndReadOnlyModes(t *testing.T) {
	target, conn := fixture(t)
	r := testRunner(t, target)
	state, err := r.Run(context.Background(), "status")
	if err != nil || state.Initialized || len(state.Pending) != 21 || exists(t, conn, "schema_migrations") {
		t.Fatalf("status wrote data: %+v %v", state, err)
	}
	if _, err = r.Run(context.Background(), "verify"); !errors.Is(err, ErrIncomplete) || exists(t, conn, "schema_migrations") {
		t.Fatal("verify must be read-only and incomplete")
	}
	state, err = r.Run(context.Background(), "apply")
	if err != nil || len(state.Applied) != 21 || len(state.Pending) != 0 {
		t.Fatalf("apply: %+v %v", state, err)
	}
	var tables, business int
	if conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r'`, target.Schema).Scan(&tables) != nil || tables != 45 {
		t.Fatalf("table count=%d", tables)
	}
	if conn.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM warehouses)+(SELECT count(*) FROM products)+(SELECT count(*) FROM categories)+(SELECT count(*) FROM cells)+(SELECT count(*) FROM cell_stock)+(SELECT count(*) FROM receiving_sessions)+(SELECT count(*) FROM receiving_intake_events)+(SELECT count(*) FROM assembly_order_records)`).Scan(&business) != nil || business != 0 {
		t.Fatal("demo/bootstrap data created")
	}
	before := ledgerSnapshot(t, conn)
	for _, mode := range []string{"status", "verify", "apply"} {
		if _, err = r.Run(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
		if ledgerSnapshot(t, conn) != before {
			t.Fatal("repeat changed migration rows/timestamps")
		}
	}
}

func TestIntegrationRefusesUnknownDatabaseAndLedger(t *testing.T) {
	for _, kind := range []string{"data", "fake-ledger", "function", "collation"} {
		t.Run(kind, func(t *testing.T) {
			target, conn := fixture(t)
			switch kind {
			case "data":
				writeTestSQL(t, conn, `CREATE TABLE important(value TEXT); INSERT INTO important VALUES('keep me')`)
			case "fake-ledger":
				writeTestSQL(t, conn, `CREATE TABLE schema_migrations(version INTEGER)`)
			case "collation":
				writeTestSQL(t, conn, `CREATE COLLATION extra FROM pg_catalog."C"`)
			case "function":
				writeTestSQL(t, conn, `CREATE FUNCTION pg_control_system() RETURNS integer LANGUAGE sql AS $$ SELECT 1 $$`)
			}
			r := testRunner(t, target)
			for _, mode := range []string{"status", "verify", "apply"} {
				if _, err := r.Run(context.Background(), mode); !errors.Is(err, ErrUnsafe) {
					t.Fatalf("unknown schema accepted: %v", err)
				}
			}
			if exists(t, conn, "warehouses") {
				t.Fatal("wrote migration SQL into an unknown database")
			}
			if kind == "data" {
				var count int
				conn.QueryRow(context.Background(), `SELECT count(*) FROM important WHERE value='keep me'`).Scan(&count)
				if count != 1 {
					t.Fatal("existing data changed")
				}
			}
		})
	}
}

func TestIntegrationServerIdentityMismatch(t *testing.T) {
	target, conn := fixture(t)
	target.ServerID = "1"
	_, err := testRunner(t, target).Run(context.Background(), "apply")
	if err == nil || exists(t, conn, "schema_migrations") {
		t.Fatal("mismatched actual server was modified")
	}
}

func TestIntegrationAmbientPGSettingsCannotRedirectTarget(t *testing.T) {
	t.Setenv("PGHOST", "192.0.2.1")
	t.Setenv("PGPORT", "65432")
	t.Setenv("PGDATABASE", "tremor_sklad")
	t.Setenv("PGUSER", "unexpected")
	t.Setenv("PGPASSWORD", "never-print-this-password")
	t.Setenv("PGOPTIONS", "-c search_path=evil -c default_transaction_read_only=off")
	target, conn := fixture(t)
	cfg := conn.Config()
	if cfg.Host != target.Host || int(cfg.Port) != target.Port || cfg.Database != target.Database || cfg.User != target.User || cfg.Password != target.Password {
		t.Fatal("ambient PG settings redirected explicit configuration")
	}
	if _, err := testRunner(t, target).Run(context.Background(), "apply"); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationLedgerMetadataConflicts(t *testing.T) {
	for _, change := range []string{`checksum=repeat('0',64)`, `version=999`, `ordinal=99`, `name='wrong.sql'`} {
		t.Run(change, func(t *testing.T) {
			target, conn := fixture(t)
			r := testRunner(t, target)
			if _, err := r.Run(context.Background(), "apply"); err != nil {
				t.Fatal(err)
			}
			writeTestSQL(t, conn, `UPDATE schema_migrations SET `+change+` WHERE version=1`)
			before := ledgerSnapshot(t, conn)
			if _, err := r.Run(context.Background(), "apply"); !errors.Is(err, ErrUnsafe) {
				t.Fatal("ledger conflict accepted", err)
			}
			if ledgerSnapshot(t, conn) != before {
				t.Fatal("conflict rewrote ledger")
			}
		})
	}
}

func TestIntegrationRollbackAndResume(t *testing.T) {
	for _, phase := range []string{"sql", "after-sql", "after-ledger", "disconnect-after-sql", "removed-ledger"} {
		t.Run(phase, func(t *testing.T) {
			target, conn := fixture(t)
			r := testRunner(t, target)
			switch phase {
			case "sql":
				path := copyManifest(t)
				file := filepath.Join(filepath.Dir(path), schemaNames[1])
				raw, _ := os.ReadFile(file)
				canonical, _ := CanonicalSQL(raw)
				body, _ := MigrationBody(canonical)
				bad := []byte("BEGIN;" + body + "SELECT 1/0;\nCOMMIT;\n")
				if err := os.WriteFile(file, bad, 0600); err != nil {
					t.Fatal(err)
				}
				plan := r.manifest.Plan()
				sum := sha256.Sum256(bad)
				plan.Migrations[1].SHA256 = hex.EncodeToString(sum[:])
				encoded, _ := json.Marshal(plan)
				if err := os.WriteFile(path, encoded, 0600); err != nil {
					t.Fatal(err)
				}
				m, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				r, _ = New(target, m)
			case "disconnect-after-sql":
				r.hooks.afterSQL = func(v int, tx pgx.Tx) error {
					if v == 3 {
						_ = tx.Conn().Close(context.Background())
						return io.ErrUnexpectedEOF
					}
					return nil
				}
			case "after-sql":
				r.hooks.afterSQL = func(v int, _ pgx.Tx) error {
					if v == 3 {
						return errors.New("injected password-do-not-log")
					}
					return nil
				}
			case "removed-ledger":
				r.hooks.afterLedger = func(v int, tx pgx.Tx) error {
					if v == 3 {
						_, err := tx.Exec(context.Background(), `DELETE FROM `+r.ledger()+` WHERE version=3`)
						return err
					}
					return nil
				}
			case "after-ledger":
				r.hooks.afterLedger = func(v int, _ pgx.Tx) error {
					if v == 3 {
						return errors.New("injected password-do-not-log")
					}
					return nil
				}
			}
			if _, err := r.Run(context.Background(), "apply"); err == nil || strings.Contains(err.Error(), "password-do-not-log") {
				t.Fatal("fault missing or secret leaked")
			}
			if exists(t, conn, "receiving_placements") || !exists(t, conn, "warehouses") {
				t.Fatal("failed migration was partially committed or prior migration lost")
			}
			before := ledgerSnapshot(t, conn)
			var count int
			conn.QueryRow(context.Background(), `SELECT count(*) FROM schema_migrations`).Scan(&count)
			if count != 1 {
				t.Fatal("unsuccessful receipt retained")
			}
			good := testRunner(t, target)
			if _, err := good.Run(context.Background(), "apply"); err != nil {
				t.Fatal(err)
			}
			var original string
			conn.QueryRow(context.Background(), `SELECT jsonb_build_array(to_jsonb(m))::text FROM schema_migrations m WHERE version=1`).Scan(&original)
			var old, newValue any
			json.Unmarshal([]byte(before), &old)
			json.Unmarshal([]byte(original), &newValue)
			if fmt.Sprint(old) != fmt.Sprint(newValue) {
				t.Fatal("previous successful migration changed on resume")
			}
		})
	}
}

func TestIntegrationUncertainCommit(t *testing.T) {
	for _, committed := range []bool{true, false} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			target, conn := fixture(t)
			r := testRunner(t, target)
			r.hooks.commit = func(ctx context.Context, v int, tx pgx.Tx) error {
				if v != 3 {
					return tx.Commit(ctx)
				}
				if committed {
					if err := tx.Commit(ctx); err != nil {
						return err
					}
				} else {
					_ = tx.Rollback(ctx)
				}
				return io.ErrUnexpectedEOF
			}
			state, err := r.Run(context.Background(), "apply")
			if committed {
				if !errors.Is(err, ErrResume) || !state.CommitResolved || len(state.Applied) != 2 || !exists(t, conn, "receiving_placements") {
					t.Fatalf("committed result unresolved: %+v %v", state, err)
				}
			} else {
				if !errors.Is(err, ErrCommitUnknown) || exists(t, conn, "receiving_placements") {
					t.Fatal("absence treated as success or blindly retried", err)
				}
			}
			good := testRunner(t, target)
			if _, err = good.Run(context.Background(), "apply"); err != nil {
				t.Fatal(err)
			}
			var count int
			conn.QueryRow(context.Background(), `SELECT count(*) FROM schema_migrations`).Scan(&count)
			if count != 21 {
				t.Fatal("commit recovery duplicated a migration")
			}
		})
	}
}

func TestIntegrationConcurrentRuns(t *testing.T) {
	target, conn := fixture(t)
	a, b := testRunner(t, target), testRunner(t, target)
	locked, release := make(chan struct{}), make(chan struct{})
	a.hooks.locked = func() { close(locked); <-release }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { _, err := a.Run(ctx, "apply"); done <- err }()
	<-locked
	go func() { _, err := b.Run(ctx, "apply"); done <- err }()
	for {
		var waiting bool
		if conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_locks WHERE locktype='advisory' AND NOT granted)`).Scan(&waiting) != nil {
			close(release)
			t.Fatal("cannot observe advisory lock")
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			close(release)
			t.Fatal("concurrent runner did not wait on the session lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var count int
	conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count)
	if count != 21 {
		t.Fatal("concurrent runners duplicated migrations")
	}
}

func TestIntegrationRefusesBusinessDataOnResume(t *testing.T) {
	target, conn := fixture(t)
	r := testRunner(t, target)
	r.hooks.afterSQL = func(v int, _ pgx.Tx) error {
		if v == 3 {
			return errors.New("interrupt")
		}
		return nil
	}
	if _, err := r.Run(context.Background(), "apply"); err == nil {
		t.Fatal("fault missing")
	}
	writeTestSQL(t, conn, `INSERT INTO warehouses(name) VALUES('keep existing business data')`)
	if _, err := testRunner(t, target).Run(context.Background(), "apply"); !errors.Is(err, ErrUnsafe) {
		t.Fatal("clean construction continued over existing data")
	}
}

func TestIntegrationLastCommitConfirmed(t *testing.T) {
	target, conn := fixture(t)
	r := testRunner(t, target)
	r.hooks.commit = func(ctx context.Context, v int, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if v == 23 {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	state, err := r.Run(context.Background(), "apply")
	if err != nil || !state.CommitResolved || len(state.Pending) != 0 || len(state.Applied) != 21 {
		t.Fatalf("last commit not reconciled: %+v %v", state, err)
	}
	before := ledgerSnapshot(t, conn)
	if _, err = testRunner(t, target).Run(context.Background(), "apply"); err != nil || before != ledgerSnapshot(t, conn) {
		t.Fatal("confirmed final commit repeated")
	}
}

func TestIntegrationPreservesQuotedSQL(t *testing.T) {
	target, conn := fixture(t)
	path := copyManifest(t)
	file := filepath.Join(filepath.Dir(path), schemaNames[0])
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalSQL(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := MigrationBody(canonical)
	if err != nil {
		t.Fatal(err)
	}
	extra := `
 /* outer BEGIN; /* inner COMMIT; */ still */
 CREATE TABLE quoted_proof(value TEXT);
 INSERT INTO quoted_proof VALUES ('it''s; COMMIT;'),(E'line\n; ROLLBACK;');
 CREATE FUNCTION quoted_function() RETURNS TEXT LANGUAGE plpgsql AS $test$
 BEGIN RETURN 'BEGIN; COMMIT;'; END;
 $test$;
 `
	sql := []byte("BEGIN;" + body + extra + "COMMIT;\n")
	if err = os.WriteFile(file, sql, 0600); err != nil {
		t.Fatal(err)
	}
	plan := repositoryManifest(t).Plan()
	digest := sha256.Sum256(sql)
	plan.Migrations[0].SHA256 = hex.EncodeToString(digest[:])
	encoded, _ := json.Marshal(plan)
	if err = os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(target, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Run(context.Background(), "apply"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err = conn.QueryRow(context.Background(), `SELECT quoted_function()`).Scan(&value); err != nil || value != "BEGIN; COMMIT;" {
		t.Fatal("dollar body was changed")
	}
	rows, err := conn.Query(context.Background(), `SELECT value FROM quoted_proof ORDER BY value`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		if err = rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if rows.Err() != nil || len(values) != 2 || values[0] != "it's; COMMIT;" || values[1] != "line\n; ROLLBACK;" {
		t.Fatalf("SQL literal bytes changed: %q", values)
	}
}

func TestIntegrationRefusesCustomLedgerTriggers(t *testing.T) {
	target, conn := fixture(t)
	r := testRunner(t, target)
	r.hooks.afterSQL = func(_ int, _ pgx.Tx) error { return errors.New("stop before first migration") }
	if _, err := r.Run(context.Background(), "apply"); err == nil {
		t.Fatal("fault missing")
	}
	writeTestSQL(t, conn, `CREATE FUNCTION suppress_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END; $$;
 CREATE TRIGGER skip_ledger BEFORE INSERT ON schema_migrations FOR EACH ROW EXECUTE FUNCTION suppress_receipt()`)
	if _, err := testRunner(t, target).Run(context.Background(), "apply"); !errors.Is(err, ErrUnsafe) {
		t.Fatal("custom receipt suppression accepted", err)
	}
	if exists(t, conn, "warehouses") {
		t.Fatal("migration executed before ledger integrity check")
	}
}
