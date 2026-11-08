package migrations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Source only: NOT RUN until the disposable TEST cluster is separately approved.
func extensionIntegrationDeployment(t *testing.T) (*ExtensionRunner, *pgx.Conn) {
	t.Helper()
	if os.Getenv("EXTENSION_TEST_DISPOSABLE_APPROVED") != "yes" {
		t.Skip("NOT RUN: approved disposable TEST target required")
	}
	base := identityRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := base.Run(ctx, "apply"); err != nil {
		t.Fatal(err)
	}
	conn, _, err := base.target.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeConnection(conn) })
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog,"+pgx.Identifier{base.target.Schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := extensionCatalog(ctx, tx, base.target.Schema)
	if err != nil {
		t.Fatal(err)
	}
	var deployment string
	if err = tx.QueryRow(ctx, `SELECT deployment_id::text FROM `+pgx.Identifier{base.target.Schema, "deployment_identity"}.Sanitize()).Scan(&deployment); err != nil {
		t.Fatal(err)
	}
	path, _ := extensionFixture(t, base.manifest, fingerprint, 1)
	m, err := LoadExtension(path, base.manifest)
	if err != nil {
		t.Fatal(err)
	}
	return fixtureExtensionRunner(t, base.target, m, ExtensionOptions{DeploymentID: deployment, BaselineOwner: base.target.User, LedgerOwner: base.target.User}), conn
}

func TestExtensionIntegrationUnconfirmedCommit(t *testing.T) {
	runner, _ := extensionIntegrationDeployment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	runner.hooks.commit = func(ctx context.Context, _ int, tx pgx.Tx) error {
		if err := tx.Rollback(ctx); err != nil {
			return err
		}
		return errors.New("commit not confirmed")
	}
	if state, err := runner.Run(ctx, "apply"); !errors.Is(err, ErrCommitUnknown) || state.CommitResolved {
		t.Fatalf("absent COMMIT: %+v %v", state, err)
	}
	// Actual OS-process restart with the same immutable test manifest.
	restartPath := filepath.Join(t.TempDir(), "manifest.json")
	plan := runner.manifest.Plan()
	sql := []byte("BEGIN;\nSELECT 1;\nCOMMIT;\n")
	if err := os.WriteFile(filepath.Join(filepath.Dir(restartPath), plan.Migrations[0].Name), sql, 0600); err != nil {
		t.Fatal(err)
	}
	writeExtensionPlan(t, restartPath, plan)
	extensionRestartChild(t, runner, restartPath)
	// A new process/runner has no in-memory state to rely on.
	restarted := fixtureExtensionRunner(t, runner.base.target, runner.manifest, runner.options)
	if state, err := restarted.Run(ctx, "apply"); err != nil || len(state.Applied) != 1 {
		t.Fatalf("restart/retry: %+v %v", state, err)
	}
	if _, err := restarted.Run(ctx, "apply"); err != nil {
		t.Fatal("duplicate apply", err)
	}
}

func TestExtensionIntegrationRecoveryValidation(t *testing.T) {
	for _, kind := range []string{"identity", "readiness"} {
		t.Run(kind, func(t *testing.T) {
			runner, _ := extensionIntegrationDeployment(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			oldID := runner.options.DeploymentID
			oldFingerprint := runner.manifest.plan.Migrations[0].ReadinessSHA256
			runner.hooks.commit = func(ctx context.Context, _ int, tx pgx.Tx) error {
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				if kind == "identity" {
					runner.options.DeploymentID = "00000000-0000-4000-8000-000000000001"
				} else {
					runner.manifest.plan.Migrations[0].ReadinessSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
				}
				return errors.New("lost reply with failed recovery validation")
			}
			if state, err := runner.Run(ctx, "apply"); !errors.Is(err, ErrCommitUnknown) || !errors.Is(err, ErrUnsafe) || state.CommitResolved {
				t.Fatalf("recovery %s: %+v %v", kind, state, err)
			}
			runner.options.DeploymentID = oldID
			runner.manifest.plan.Migrations[0].ReadinessSHA256 = oldFingerprint
			restarted := fixtureExtensionRunner(t, runner.base.target, runner.manifest, runner.options)
			if state, err := restarted.Run(ctx, "apply"); err != nil || len(state.Applied) != 1 {
				t.Fatalf("restart after recovery failure: %+v %v", state, err)
			}
		})
	}
}

func TestExtensionIntegrationConcurrentApply(t *testing.T) {
	first, conn := extensionIntegrationDeployment(t)
	// Non-idempotent durable effect: duplicate SQL execution must be visible.
	crashFixture(t, first, conn)
	first = fixtureExtensionRunner(t, first.base.target, first.manifest, first.options)
	second := fixtureExtensionRunner(t, first.base.target, first.manifest, first.options)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	locked := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	first.hooks.locked = func() {
		close(locked)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	type result struct {
		state ExtensionState
		err   error
	}
	one := make(chan result, 1)
	two := make(chan result, 1)
	go func() { s, e := first.Run(ctx, "apply"); one <- result{s, e} }()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("first apply did not acquire lock")
	}
	// Observe the actual database-local bigint advisory key (objsubid=1).
	high, low := int64(uint64(advisoryKey)>>32), int64(uint64(advisoryKey)&0xffffffff)
	var holder int32
	if err := conn.QueryRow(ctx, `SELECT pid FROM pg_catalog.pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()) AND classid=$1::oid AND objid=$2::oid AND objsubid=1 AND mode='ExclusiveLock' AND granted`, high, low).Scan(&holder); err != nil {
		t.Fatalf("first runner does not hold expected lock: %v", err)
	}
	go func() { s, e := second.Run(ctx, "apply"); two <- result{s, e} }()
	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	var waiter int32
	for {
		select {
		case result := <-two:
			t.Fatalf("second apply completed before observed contention: %+v", result)
		default:
		}
		err := conn.QueryRow(waitCtx, `SELECT w.pid FROM pg_catalog.pg_locks w JOIN pg_catalog.pg_stat_activity a ON a.pid=w.pid JOIN pg_catalog.pg_locks h ON h.pid=$3 AND h.locktype=w.locktype AND h.database=w.database AND h.classid=w.classid AND h.objid=w.objid AND h.objsubid=w.objsubid AND h.granted AND h.mode='ExclusiveLock' WHERE w.locktype='advisory' AND w.database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()) AND w.classid=$1::oid AND w.objid=$2::oid AND w.objsubid=1 AND w.mode='ExclusiveLock' AND NOT w.granted AND w.pid<>$3 AND $3=ANY(pg_catalog.pg_blocking_pids(w.pid)) AND a.datname=current_database() AND a.usename=$4 AND a.application_name='tremor-migrate' AND a.state='active' AND a.wait_event_type='Lock' AND a.wait_event='advisory'`, high, low, holder, first.base.target.User).Scan(&waiter)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("cannot observe lock waiter: %v", err)
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("second runner never observed waiting on first advisory lock")
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Logf("observed concurrent backends: holder=%d waiter=%d key=%d; holder granted, waiter blocked", holder, waiter, advisoryKey)
	close(release)
	for _, ch := range []chan result{one, two} {
		select {
		case result := <-ch:
			if result.err != nil || len(result.state.Applied) != 1 {
				t.Fatalf("concurrent apply: %+v", result)
			}
		case <-ctx.Done():
			t.Fatal("concurrent apply timed out")
		}
	}
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+first.ledger()).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate ledger rows: %d %v", count, err)
	}
	var effect string
	if err := conn.QueryRow(ctx, `SELECT name FROM `+pgx.Identifier{first.base.target.Schema, "warehouses"}.Sanitize()+` WHERE name LIKE 'T0 crash effect%'`).Scan(&effect); err != nil || effect != "T0 crash effect-executed" {
		t.Fatalf("concurrent SQL must commit exactly once: %q %v", effect, err)
	}
	restarted := fixtureExtensionRunner(t, first.base.target, first.manifest, first.options)
	if _, err := restarted.Run(ctx, "verify"); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionIntegrationMetadataV2(t *testing.T) {
	runner, conn := extensionIntegrationDeployment(t)
	schema := runner.base.target.Schema
	table := pgx.Identifier{schema, "metadata_probe"}.Sanitize()
	domain := pgx.Identifier{schema, "metadata_domain"}.Sanitize()
	fn := pgx.Identifier{schema, "metadata_fn"}.Sanitize()
	for _, kind := range []string{"column_acl", "policy_roles", "domain_constraint", "domain_default", "domain_not_null", "domain_acl", "table_acl", "function_acl", "schema_acl", "owner", "domain_owner", "domain_validation", "column_collation", "replica_identity", "inheritance", "default_acl"} {
		t.Run(kind, func(t *testing.T) {
			alternate := os.Getenv("EXTENSION_TEST_ALTERNATE_OWNER")
			if (kind == "owner" || kind == "domain_owner") && alternate == "" {
				t.Skip("existing alternate fixture owner with required memberships must be separately supplied; no roles created")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
			if err != nil {
				t.Fatal(err)
			}
			defer rollback(tx)
			if _, err = tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog,"+pgx.Identifier{schema}.Sanitize()); err != nil {
				t.Fatal(err)
			}
			setup := `CREATE TABLE ` + table + `(id integer,label text COLLATE "default"); CREATE TABLE ` + pgx.Identifier{schema, "metadata_parent"}.Sanitize() + `(id integer); ALTER TABLE ` + table + ` ENABLE ROW LEVEL SECURITY; CREATE POLICY metadata_policy ON ` + table + ` TO CURRENT_USER USING (true); CREATE DOMAIN ` + domain + ` AS integer CONSTRAINT metadata_positive CHECK(VALUE>0); CREATE FUNCTION ` + fn + `() RETURNS integer LANGUAGE sql AS 'SELECT 1'`
			if _, err = tx.Exec(ctx, setup, pgx.QueryExecModeSimpleProtocol); err != nil {
				t.Fatal(err)
			}
			before, err := extensionCatalog(ctx, tx, schema)
			if err != nil {
				t.Fatal(err)
			}
			var mutation string
			switch kind {
			case "domain_owner":
				if !simpleName.MatchString(alternate) || alternate == runner.base.target.User {
					t.Fatal("invalid alternate owner fixture")
				}
				mutation = `ALTER DOMAIN ` + domain + ` OWNER TO ` + pgx.Identifier{alternate}.Sanitize()
			case "domain_validation":
				mutation = `ALTER DOMAIN ` + domain + ` DROP CONSTRAINT metadata_positive; ALTER DOMAIN ` + domain + ` ADD CONSTRAINT metadata_positive CHECK(VALUE>0) NOT VALID`
			case "column_collation":
				mutation = `ALTER TABLE ` + table + ` ALTER COLUMN label TYPE text COLLATE "C"`
			case "replica_identity":
				mutation = `ALTER TABLE ` + table + ` REPLICA IDENTITY FULL`
			case "inheritance":
				mutation = `ALTER TABLE ` + table + ` INHERIT ` + pgx.Identifier{schema, "metadata_parent"}.Sanitize()
			case "default_acl":
				mutation = `ALTER DEFAULT PRIVILEGES IN SCHEMA ` + pgx.Identifier{schema}.Sanitize() + ` GRANT SELECT ON TABLES TO PUBLIC`
			case "column_acl":
				mutation = `GRANT SELECT(id) ON ` + table + ` TO PUBLIC`
			case "policy_roles":
				mutation = `ALTER POLICY metadata_policy ON ` + table + ` TO PUBLIC`
			case "domain_constraint":
				mutation = `ALTER DOMAIN ` + domain + ` DROP CONSTRAINT metadata_positive; ALTER DOMAIN ` + domain + ` ADD CONSTRAINT metadata_positive CHECK(VALUE>1)`
			case "domain_default":
				mutation = `ALTER DOMAIN ` + domain + ` SET DEFAULT 7`
			case "domain_not_null":
				mutation = `ALTER DOMAIN ` + domain + ` SET NOT NULL`
			case "domain_acl":
				mutation = `REVOKE USAGE ON DOMAIN ` + domain + ` FROM PUBLIC`
			case "table_acl":
				mutation = `GRANT SELECT ON ` + table + ` TO PUBLIC`
			case "function_acl":
				mutation = `REVOKE EXECUTE ON FUNCTION ` + fn + `() FROM PUBLIC`
			case "schema_acl":
				mutation = `GRANT USAGE ON SCHEMA ` + pgx.Identifier{schema}.Sanitize() + ` TO PUBLIC`
			case "owner":
				if !simpleName.MatchString(alternate) || alternate == runner.base.target.User {
					t.Fatal("invalid alternate owner fixture")
				}
				mutation = `ALTER TABLE ` + table + ` OWNER TO ` + pgx.Identifier{alternate}.Sanitize()
			}
			if _, err = tx.Exec(ctx, mutation, pgx.QueryExecModeSimpleProtocol); err != nil {
				t.Fatal(err)
			}
			after, err := extensionCatalog(ctx, tx, schema)
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatalf("metadata change not fingerprinted: %s", kind)
			}
		})
	}
}
