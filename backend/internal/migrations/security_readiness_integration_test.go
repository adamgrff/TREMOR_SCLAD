package migrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Fixed TEST release artifact, pinned to the pre-F4 E1A evidence. Never derived
// from today's collector output. No production extension approval ships here.
const readinessTestManifest = `{"format":1,"readiness_format":"pg18-catalog-v2","profile":"db04-extension-v1","canonicalization":"utf8-lf-v1","baseline_sha256":"f4f65a9dd08e5f3357671294cf344808f821e191bb2612e2e3f2a28bc63a32d5","baseline_readiness_sha256":"e648ad5a6084d251016ed325bc306fe214f0d460be119cb834c4903a890270f7","migrations":[]}`

func testLegacyReadinessBaseline(t *testing.T, ctx context.Context, admin *pgx.Conn, target Target, base *Manifest) {
	path := filepath.Join(t.TempDir(), "fixed-test-manifest.json")
	if err := os.WriteFile(path, []byte(readinessTestManifest), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	var deployment string
	if err = admin.QueryRow(ctx, `SELECT deployment_id::text FROM public.deployment_identity WHERE singleton`).Scan(&deployment); err != nil {
		t.Fatal(err)
	}
	legacy, err := NewExtension(target, m, ExtensionOptions{DeploymentID: deployment, BaselineOwner: target.User, LedgerOwner: target.User})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("readiness-fixed-manifest-legacy-equality", func(t *testing.T) {
		if _, err := legacy.Run(ctx, "verify"); err != nil {
			t.Fatal("unchanged E1A does not accept fixed TEST manifest", err)
		}
		tx, err := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)
		if _, err = tx.Exec(ctx, `SET LOCAL search_path TO pg_catalog`); err != nil {
			t.Fatal(err)
		}
		raw, err := extensionCatalog(ctx, tx, target.Schema)
		if err != nil {
			t.Fatal(err)
		}
		var qualified string
		if err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_constraintdef(oid) FROM pg_catalog.pg_constraint WHERE conname='cells_warehouse_fk' AND conrelid='public.cells'::regclass`).Scan(&qualified); err != nil {
			t.Fatal(err)
		}
		if _, err = legacy.inspect(ctx, tx); err != nil {
			t.Fatal(err)
		}
		original, err := extensionCatalog(ctx, tx, target.Schema)
		if err != nil {
			t.Fatal(err)
		}
		var visible string
		if err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_constraintdef(oid) FROM pg_catalog.pg_constraint WHERE conname='cells_warehouse_fk' AND conrelid='public.cells'::regclass`).Scan(&visible); err != nil {
			t.Fatal(err)
		}
		if raw == original || qualified == visible {
			t.Fatal("original context-dependent deparser reproduction absent")
		}
		if original != m.plan.BaselineReadinessSHA256 {
			t.Fatal("legacy manifest readiness changed")
		}
		if _, err = tx.Exec(ctx, `SET LOCAL search_path TO public,pg_catalog`); err != nil {
			t.Fatal(err)
		}
		var before string
		if err = tx.QueryRow(ctx, securityPathReadSQL).Scan(&before); err != nil {
			t.Fatal(err)
		}
		fixed, err := collectorLegacyReadiness(ctx, tx, target.Schema)
		if err != nil {
			t.Fatal(err)
		}
		var after string
		if err = tx.QueryRow(ctx, securityPathReadSQL).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if fixed != original || fixed != m.plan.BaselineReadinessSHA256 || after != before {
			t.Fatal("collector/legacy/manifest equality or restoration failed")
		}
		t.Logf("raw-context=%s; E1A=E1B=unchanged TEST manifest=%s", raw, fixed)
	})
	t.Run("readiness-catalog-drift-rejected", func(t *testing.T) {
		tx, err := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite})
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)
		if _, err = tx.Exec(ctx, `GRANT SELECT ON public.warehouses TO PUBLIC`); err != nil {
			t.Fatal(err)
		}
		observed, err := collectorLegacyReadiness(ctx, tx, target.Schema)
		if err != nil {
			t.Fatal(err)
		}
		if observed == m.plan.BaselineReadinessSHA256 {
			t.Fatal("catalog drift masked")
		}
		if _, err = legacy.inspect(ctx, tx); !errors.Is(err, ErrUnsafe) {
			t.Fatal("legacy comparator accepted drift", err)
		}
	})
	t.Run("readiness-unapproved-hash-rejected", func(t *testing.T) {
		// Separate malicious TEST document; never rewrite the fixed artifact.
		var bad ExtensionPlan
		if err := strictJSON([]byte(readinessTestManifest), &bad); err != nil {
			t.Fatal(err)
		}
		bad.BaselineReadinessSHA256 = "a5fb566fbaaebceffefab307678c3b5e06c71573974ce32ccf5784c24b9bc489"
		malicious := filepath.Join(t.TempDir(), "unapproved.json")
		writeExtensionPlan(t, malicious, bad)
		other, err := LoadExtension(malicious, base)
		if err != nil {
			t.Fatal(err)
		}
		otherRunner, err := NewExtension(target, other, legacy.options)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = otherRunner.Run(ctx, "verify"); !errors.Is(err, ErrUnsafe) {
			t.Fatal("old context hash accepted as manifest readiness", err)
		}
		if _, err = otherRunner.Run(ctx, "apply"); !errors.Is(err, ErrExtensionUnapproved) {
			t.Fatal("production approval weakened")
		}
	})
	for _, schema := range []string{"warehouse_scope", "order"} {
		t.Run("readiness-target-schema-"+schema, func(t *testing.T) {
			other := target
			other.Schema = schema
			if err := other.Validate(); err != nil {
				t.Fatal(err)
			}
			var absent bool
			if err := admin.QueryRow(ctx, `SELECT pg_catalog.to_regnamespace($1) IS NULL`, schema).Scan(&absent); err != nil || !absent {
				t.Fatal("TEST schema already exists")
			}
			ns := pgx.Identifier{schema}.Sanitize()
			if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ns+"; CREATE TABLE "+ns+".parent(id integer PRIMARY KEY); CREATE TABLE "+ns+".child(parent_id integer REFERENCES "+ns+".parent(id))", pgx.QueryExecModeSimpleProtocol); err != nil {
				t.Fatal(err)
			}
			var oid uint32
			if err := admin.QueryRow(ctx, `SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=$1`, schema).Scan(&oid); err != nil {
				t.Fatal(err)
			}
			defer func() {
				var actual uint32
				if err := admin.QueryRow(ctx, `SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=$1`, schema).Scan(&actual); err != nil || actual != oid {
					t.Error("TEST schema cleanup identity mismatch")
					return
				}
				if _, err := admin.Exec(ctx, "DROP SCHEMA "+ns+" CASCADE"); err != nil {
					t.Error(err)
				}
			}()
			tx, err := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			defer rollback(tx)
			if _, err = tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog,"+ns); err != nil {
				t.Fatal(err)
			}
			expected, err := extensionCatalog(ctx, tx, schema)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `SET LOCAL search_path TO pg_catalog`); err != nil {
				t.Fatal(err)
			}
			observed, err := collectorLegacyReadiness(ctx, tx, other.Schema)
			if err != nil || observed != expected {
				t.Fatal("Target.Schema not respected", err)
			}
			wrong, err := collectorLegacyReadiness(ctx, tx, "public")
			if err != nil || wrong == expected {
				t.Fatal("hardcoded public would pass")
			}
		})
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != readinessTestManifest {
		t.Fatal("fixed TEST manifest rewritten")
	}
}

// Faults below are driver fault injection; no claim of real server failure.
type readinessFaultTx struct {
	pgx.Tx
	stage string
}

func (tx readinessFaultTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx.stage == "legacy-restore" && sql == securityPathRestoreSQL {
		return readinessErrorRow{errors.New("TEST injected readiness restore error")}
	}
	if tx.stage == "caller-restore" && sql == securityContextWriteSQL && args[2] == "on" {
		return readinessErrorRow{errors.New("TEST injected caller restore error")}
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}
func testCollectorReadinessContext(t *testing.T, ctx context.Context, admin *pgx.Conn, target Target, m *ExtensionManifest, reference func() (ApprovedSecurityTransition, CollectedSecuritySnapshot)) {
	t.Run("readiness-full-collector-caller-context", func(t *testing.T) {
		a, baseline := reference()
		tx, err := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)
		want := securityCollectionContext{"public, pg_catalog", "Etc/GMT+3", "on"}
		if err = writeSecurityContext(ctx, tx, want); err != nil {
			t.Fatal(err)
		}
		var oid uint32
		if err = tx.QueryRow(ctx, `SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()`).Scan(&oid); err != nil {
			t.Fatal(err)
		}
		observed, err := collectSecuritySnapshotTx(ctx, tx, target, oid, a.descriptor, a.descriptor.SourceScope, m)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readSecurityContext(ctx, tx)
		if err != nil || got != want {
			t.Fatal("collector caller context not restored")
		}
		if !reflect.DeepEqual(observed.snapshot, baseline.snapshot) {
			t.Fatal("caller settings changed canonical metadata/data/readiness")
		}
		expected, err := baseline.SecurityFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		observed.seal = transitionDigest([]any{observed.snapshot, observed.receipt, observed.privileges, observed.paths})
		fingerprint, err := observed.SecurityFingerprint()
		if err != nil || fingerprint != expected {
			t.Fatal("caller changed security fingerprint")
		}
		if _, err = BindCollectedSecurityTransition(a, observed); err != nil {
			t.Fatal(err)
		}
		bad := observed.snapshot
		bad.ReadinessSHA = digest([]byte("operator supplied hash"))
		if _, err = BindSecurityTransition(a, a.descriptor.Target, bad); err == nil {
			t.Fatal("unapproved hash bound")
		}
	})
	for _, stage := range []string{"legacy-restore", "caller-restore"} {
		t.Run("readiness-fault-"+stage, func(t *testing.T) {
			a, _ := reference()
			tx, err := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			defer rollback(tx)
			var oid uint32
			if err = tx.QueryRow(ctx, `SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()`).Scan(&oid); err != nil {
				t.Fatal(err)
			}
			result, err := collectSecuritySnapshotTx(ctx, readinessFaultTx{tx, stage}, target, oid, a.descriptor, a.descriptor.SourceScope, m)
			if err == nil || result.valid() {
				t.Fatal("restoration fault produced valid collection")
			}
			if _, e := BindCollectedSecurityTransition(a, result); e == nil {
				t.Fatal("failed restoration bound")
			}
		})
	}
	t.Run("readiness-versus-security-parameter-drift", func(t *testing.T) {
		a, before := reference()
		bound, err := BindCollectedSecurityTransition(a, before)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = admin.Exec(ctx, `GRANT SET ON PARAMETER row_security TO tremor_api`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := admin.Exec(ctx, `REVOKE SET ON PARAMETER row_security FROM tremor_api`); err != nil {
				t.Fatal(err)
			}
		}()
		after, err := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if err != nil {
			t.Fatal(err)
		}
		oldFP, _ := before.SecurityFingerprint()
		newFP, _ := after.SecurityFingerprint()
		if before.snapshot.ReadinessSHA != after.snapshot.ReadinessSHA || oldFP == newFP {
			t.Fatal("legacy and security fingerprints conflated")
		}
		if _, err = BindCollectedSecurityTransition(a, after); err == nil {
			t.Fatal("unchanged legacy hash admitted security drift")
		}
		if ClassifyCollectedTransitionRecovery(bound, after) != TransitionEvidenceRejected {
			t.Fatal("parameter drift confirmed")
		}
	})
}
