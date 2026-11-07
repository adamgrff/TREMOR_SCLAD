package migrations

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestExtensionIntegrationLedgerMetadataT0(t *testing.T) {
	for _, ledger := range []string{"baseline", "extension"} {
		for _, mutation := range []string{"timestamp", "name", "checksum", "ordinal", "table_acl", "column_acl", "oid"} {
			t.Run(ledger+"/"+mutation, func(t *testing.T) {
				r, conn := extensionIntegrationDeployment(t)
				path, _ := extensionFixture(t, r.base.manifest, r.manifest.plan.BaselineReadinessSHA256, 2)
				m, err := LoadExtension(path, r.base.manifest)
				if err != nil {
					t.Fatal(err)
				}
				r = fixtureExtensionRunner(t, r.base.target, m, r.options)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				r.hooks.commit = func(ctx context.Context, _ int, tx pgx.Tx) error {
					if err := tx.Commit(ctx); err != nil {
						return err
					}
					return errors.New("synthetic lost reply at first receipt")
				}
				if _, err = r.Run(ctx, "apply"); !errors.Is(err, ErrResume) {
					t.Fatalf("first prefix: %v", err)
				}
				r.hooks.commit = nil
				before := extensionDataSnapshot(t, conn, r.base.target.Schema)
				receipts := func() string {
					var s string
					if err := conn.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(r) ORDER BY ordinal)::text FROM `+r.ledger()+` r`).Scan(&s); err != nil {
						t.Fatal(err)
					}
					return s
				}
				oldReceipts := receipts()
				metadata := func() extensionLedgerSnapshot {
					tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
					if err != nil {
						t.Fatal(err)
					}
					defer rollback(tx)
					state, err := r.inspect(ctx, tx)
					if err != nil {
						t.Fatal(err)
					}
					snapshot, err := r.ledgerSnapshot(ctx, tx, state)
					if err != nil {
						t.Fatal(err)
					}
					return snapshot
				}
				oldMetadata := metadata()
				table := r.base.ledger()
				if ledger == "extension" {
					table = r.ledger()
				}
				r.hooks.afterLedger = func(_ int, tx pgx.Tx) error {
					sql := ""
					switch mutation {
					case "timestamp":
						sql = `UPDATE ` + table + ` SET applied_at=applied_at+interval '1 second' WHERE ordinal=1`
					case "name":
						sql = `UPDATE ` + table + ` SET name='025_tampered.sql' WHERE ordinal=1`
					case "checksum":
						sql = `UPDATE ` + table + ` SET checksum=repeat('0',64) WHERE ordinal=1`
					case "ordinal":
						sql = `UPDATE ` + table + ` SET ordinal=100 WHERE ordinal=1`
					case "table_acl":
						sql = `GRANT SELECT ON ` + table + ` TO PUBLIC`
					case "column_acl":
						sql = `GRANT UPDATE(name) ON ` + table + ` TO PUBLIC`
					case "oid":
						sql = `ALTER TABLE ` + table + ` RENAME TO t0_old_ledger; CREATE TABLE ` + table + ` (LIKE ` + pgx.Identifier{r.base.target.Schema, "t0_old_ledger"}.Sanitize() + ` INCLUDING ALL); INSERT INTO ` + table + ` SELECT * FROM ` + pgx.Identifier{r.base.target.Schema, "t0_old_ledger"}.Sanitize()
					}
					_, err := tx.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol)
					return err
				}
				if _, err = r.Run(ctx, "apply"); !errors.Is(err, ErrUnsafe) {
					t.Fatalf("persistent ledger mutation accepted: %v", err)
				}
				if !reflect.DeepEqual(before, extensionDataSnapshot(t, conn, r.base.target.Schema)) || oldReceipts != receipts() {
					t.Fatal("ledger mutation was not rolled back")
				}
				if !reflect.DeepEqual(oldMetadata, metadata()) {
					t.Fatal("ledger OID/owner/ACL/rows changed after rollback")
				}
				r.hooks.afterLedger = nil
				if _, err = r.Run(ctx, "apply"); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestExtensionIntegrationAdmissionT0(t *testing.T) {
	for _, kind := range []string{"incomplete_baseline", "baseline_marker", "baseline_checksum", "baseline_profile", "baseline_order", "baseline_owner", "extension_owner", "extension_marker", "extension_checksum", "extension_ahead", "check_missing", "check_extra"} {
		t.Run(kind, func(t *testing.T) {
			r, conn := extensionIntegrationDeployment(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if kind[:5] != "basel" && kind != "incomplete_baseline" {
				if _, err := r.Run(ctx, "apply"); err != nil {
					t.Fatal(err)
				}
			}
			sql := ""
			switch kind {
			case "baseline_owner", "extension_owner":
				alternate := os.Getenv("EXTENSION_TEST_ALTERNATE_OWNER")
				if !simpleName.MatchString(alternate) || alternate == r.base.target.User {
					t.Fatal("explicit alternate TEST owner required")
				}
				table := r.base.ledger()
				if kind == "extension_owner" {
					table = r.ledger()
				}
				sql = `ALTER TABLE ` + table + ` OWNER TO ` + pgx.Identifier{alternate}.Sanitize()
			case "incomplete_baseline":
				sql = `DELETE FROM ` + r.base.ledger() + ` WHERE version=24`
			case "baseline_marker":
				sql = `COMMENT ON TABLE ` + r.base.ledger() + ` IS 'foreign'`
			case "baseline_checksum":
				sql = `UPDATE ` + r.base.ledger() + ` SET checksum=repeat('0',64) WHERE ordinal=1`
			case "baseline_profile":
				sql = `ALTER TABLE ` + r.base.ledger() + ` DROP CONSTRAINT ledger_profile_clean; UPDATE ` + r.base.ledger() + ` SET profile='clean-v1' WHERE ordinal=1`
			case "baseline_order":
				sql = `UPDATE ` + r.base.ledger() + ` SET ordinal=100 WHERE ordinal=1`
			case "extension_marker":
				sql = `COMMENT ON TABLE ` + r.ledger() + ` IS 'foreign'`
			case "extension_checksum":
				sql = `UPDATE ` + r.ledger() + ` SET checksum=repeat('0',64)`
			case "extension_ahead":
				sql = `INSERT INTO ` + r.ledger() + `(version,name,checksum,profile,ordinal) VALUES(26,'026_foreign.sql',repeat('0',64),repeat('0',64),2)`
			case "check_missing":
				sql = `ALTER TABLE ` + r.ledger() + ` DROP CONSTRAINT ledger_order_positive`
			case "check_extra":
				sql = `ALTER TABLE ` + r.ledger() + ` ADD CONSTRAINT unexpected CHECK(ordinal<999)`
			}
			writeTestSQL(t, conn, sql)
			before := extensionDataSnapshot(t, conn, r.base.target.Schema)
			_, err := r.Run(ctx, "apply")
			if !errors.Is(err, ErrUnsafe) && !errors.Is(err, ErrIncomplete) {
				t.Fatalf("%s accepted: %v", kind, err)
			}
			after := extensionDataSnapshot(t, conn, r.base.target.Schema)
			for name, value := range before {
				if after[name] != value {
					t.Fatalf("rejected apply changed %s", name)
				}
			}
		})
	}
}

// A real client TCP socket is closed before COMMIT. This is not a packet-loss
// harness and does not claim to lose a server's successful COMMIT response.
func TestExtensionIntegrationTransportCloseT0(t *testing.T) {
	r, conn := extensionIntegrationDeployment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path := crashFixture(t, r, conn)
	r = fixtureExtensionRunner(t, r.base.target, r.manifest, r.options)
	r.hooks.commit = func(ctx context.Context, _ int, tx pgx.Tx) error {
		if err := tx.Conn().PgConn().Conn().Close(); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	state, err := r.Run(ctx, "apply")
	if !errors.Is(err, ErrCommitUnknown) || state.CommitResolved {
		t.Fatalf("transport outcome falsely confirmed: %+v %v", state, err)
	}
	var name string
	if err = conn.QueryRow(ctx, `SELECT name FROM `+pgx.Identifier{r.base.target.Schema, "warehouses"}.Sanitize()+` WHERE name LIKE 'T0 crash effect%'`).Scan(&name); err != nil || name != "T0 crash effect" {
		t.Fatalf("uncertain SQL was retried: %q %v", name, err)
	}
	extensionRestartChild(t, r, path)
	if err = conn.QueryRow(ctx, `SELECT name FROM `+pgx.Identifier{r.base.target.Schema, "warehouses"}.Sanitize()+` WHERE name LIKE 'T0 crash effect%'`).Scan(&name); err != nil || name != "T0 crash effect-executed" {
		t.Fatalf("explicit retry effect: %q %v", name, err)
	}
}

func TestExtensionIntegrationRecoveryConnectFailureT0(t *testing.T) {
	r, _ := extensionIntegrationDeployment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r.hooks.commit = func(ctx context.Context, _ int, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		r.base.target.Password = "intentionally-wrong-disposable-password"
		return errors.New("synthetic lost reply; real recovery auth failure")
	}
	if state, err := r.Run(ctx, "apply"); !errors.Is(err, ErrCommitUnknown) || state.CommitResolved {
		t.Fatalf("failed recovery accepted: %+v %v", state, err)
	}
}

func TestExtensionIntegrationReceiptRollbackT0(t *testing.T) {
	r, conn := extensionIntegrationDeployment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r.hooks.afterLedger = func(int, pgx.Tx) error { return errors.New("after receipt fault") }
	if _, err := r.Run(ctx, "apply"); err == nil {
		t.Fatal("receipt fault succeeded")
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, r.ledger()).Scan(&exists); err != nil || exists {
		t.Fatalf("ledger initialization survived rollback: %v %v", exists, err)
	}
	r.hooks.afterLedger = nil
	if _, err := r.Run(ctx, "apply"); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionIntegrationUnsupportedT0(t *testing.T) {
	for _, sql := range []string{
		`CREATE MATERIALIZED VIEW unsupported_mv AS SELECT 1 AS id`,
		`CREATE TABLE unsupported_partition(id integer) PARTITION BY RANGE(id)`,
		`CREATE TYPE unsupported_range AS RANGE(subtype=integer)`,
		`CREATE AGGREGATE unsupported_aggregate(integer)(SFUNC=int4pl,STYPE=integer,INITCOND='0')`,
		`CREATE FOREIGN DATA WRAPPER t0_fdw; CREATE SERVER t0_server FOREIGN DATA WRAPPER t0_fdw; CREATE FOREIGN TABLE unsupported_foreign(id integer) SERVER t0_server`,
		`CREATE TYPE unsupported_enum AS ENUM('x'); CREATE CAST (unsupported_enum AS text) WITH INOUT`,
	} {
		t.Run(sql, func(t *testing.T) {
			r, conn := extensionIntegrationDeployment(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
			if err != nil {
				t.Fatal(err)
			}
			defer rollback(tx)
			if _, err = tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{r.base.target.Schema}.Sanitize()+",pg_catalog"); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); err != nil {
				t.Fatal(err)
			}
			if _, err = extensionCatalog(ctx, tx, r.base.target.Schema); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("unsupported class accepted: %v", err)
			}
		})
	}
}

func TestExtensionIntegrationTargetGuardsT0(t *testing.T) {
	r, _ := extensionIntegrationDeployment(t)
	for _, kind := range []string{"cluster", "address", "port", "user", "database", "deployment", "readiness"} {
		t.Run(kind, func(t *testing.T) {
			target := r.base.target
			options := r.options
			switch kind {
			case "cluster":
				target.ServerID = "1"
			case "address":
				target.ServerAddress = "127.0.0.2"
			case "port":
				target.Port = 1
			case "user":
				target.User = "nonexistent_t0"
				options.LedgerOwner = target.User
			case "database":
				target.Database = "tremor_nonexistent_t0_test"
			case "deployment":
				options.DeploymentID = "00000000-0000-4000-8000-000000000001"
			}
			manifest := *r.manifest
			manifest.plan = r.manifest.Plan()
			if kind == "readiness" {
				manifest.plan.BaselineReadinessSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
			}
			if kind == "address" {
				// No Run/connect: this target must be refused by the constructor.
				candidate, err := NewExtension(target, &manifest, options)
				if candidate != nil || !expectedAddressGuard(err) {
					t.Fatalf("expected exact constructor address refusal, got %T: %v", err, err)
				}
				return
			}
			test := fixtureExtensionRunner(t, target, &manifest, options)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			// Port 1 is loopback in the isolated container, never an external endpoint.
			if _, err := test.Run(ctx, "apply"); err == nil {
				t.Fatalf("%s guard accepted", kind)
			}
		})
	}
}

// Production currently returns a plain errors.New, not a sentinel/typed guard.
// Require both its concrete type and exact text; unrelated errors must fail.
func expectedAddressGuard(err error) bool {
	return err != nil && reflect.TypeOf(err) == reflect.TypeOf(errors.New("")) &&
		err.Error() == "unencrypted connections require an exact loopback IP target"
}

func TestExtensionAddressConstructorGuard(t *testing.T) {
	base := extensionBase(t)
	path, _ := extensionFixture(t, base, strings.Repeat("a", 64), 0)
	m, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Host: "127.0.0.1", Port: 5432, Database: "tremor_extension_test", User: "deployer", Profile: IdentityProfile, Schema: "public", ServerAddress: "127.0.0.1", ServerID: "123", SSLMode: "disable"}
	options := ExtensionOptions{"11111111-1111-4111-8111-111111111111", "deployer", "deployer"}
	if _, err = NewExtension(target, m, options); err != nil {
		t.Fatal(err)
	}
	target.ServerAddress = "127.0.0.2"
	if r, err := NewExtension(target, m, options); r != nil || !expectedAddressGuard(err) {
		t.Fatalf("address constructor: %T %v", err, err)
	}
	if expectedAddressGuard(nil) || expectedAddressGuard(errors.New("other constructor failure")) {
		t.Fatal("unrelated error masked")
	}
}
