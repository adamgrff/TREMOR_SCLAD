package migrations

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func executionProtectedApplyTest(t *testing.T, ctx context.Context, admin *pgx.Conn, target Target, anchor *ExtensionManifest, b BoundSecurityTransition, oid uint32) {
	t.Helper()
	path, plan := extensionFixture(t, anchor.base, anchor.plan.BaselineReadinessSHA256, 1)
	sql := []byte("BEGIN;\nCREATE TABLE public.protected_probe(id integer PRIMARY KEY);\nALTER TABLE public.protected_probe OWNER TO tremor_owner;\nINSERT INTO public.protected_probe VALUES(1);\nGRANT SELECT ON public.protected_probe TO tremor_migrator;\nCREATE FUNCTION public.protected_context() RETURNS text LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS 'SELECT current_user::text';\nALTER FUNCTION public.protected_context() OWNER TO tremor_owner;\nCOMMIT;\n")
	if os.Getenv("I3_READ_POLICY") == "yes" {
		sql = []byte(strings.Replace(string(sql), "COMMIT;", APIReadPolicyV1+"\nCOMMIT;", 1))
	}
	canonical, err := CanonicalSQL(sql)
	if err != nil {
		t.Fatal(err)
	}
	sql = canonical
	if e := os.WriteFile(filepath.Join(filepath.Dir(path), plan.Migrations[0].Name), sql, 0600); e != nil {
		t.Fatal(e)
	}
	plan.Migrations[0].SHA256 = digest(sql)
	writeExtensionPlan(t, path, plan)
	m, e := LoadExtension(path, anchor.base)
	if e != nil {
		t.Fatal(e)
	}
	before := ProtectedCheckpoint{b.source.Frontier, b.approved.descriptor.FinalScope, b.approved.descriptor.After, b.approved.descriptor.FinalReadinessSHA}
	before.Frontier.ExtensionManifestSHA = m.ManifestSHA256()
	tx, e := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite})
	if e != nil {
		t.Fatal(e)
	}
	defer rollback(tx)
	ddl := `CREATE TABLE public.schema_extensions(version integer PRIMARY KEY CONSTRAINT ledger_version_positive CHECK(version>24),name text NOT NULL UNIQUE,checksum text NOT NULL CONSTRAINT ledger_checksum_hex CHECK(checksum ~ '^[0-9a-f]{64}$'),profile text NOT NULL CONSTRAINT ledger_profile_clean CHECK(profile ~ '^[0-9a-f]{64}$'),ordinal integer NOT NULL UNIQUE CONSTRAINT ledger_order_positive CHECK(ordinal>0),applied_at timestamptz NOT NULL DEFAULT pg_catalog.clock_timestamp()); COMMENT ON TABLE public.schema_extensions IS 'TREMOR extension ledger v1; ` + baselineDigest(m.base) + `; ` + b.source.Target.DeploymentID + `'; ALTER TABLE public.schema_extensions OWNER TO tremor_ledger_owner; REVOKE ALL ON public.schema_extensions FROM PUBLIC,tremor_api,tremor_executor,tremor_migrator; GRANT SELECT ON public.schema_extensions TO tremor_migrator;`
	if _, e = tx.Exec(ctx, ddl, pgx.QueryExecModeSimpleProtocol); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, m.bodies[0], pgx.QueryExecModeSimpleProtocol); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO public.schema_extensions(version,name,checksum,profile,ordinal) VALUES($1,$2,$3,$4,$5)`, 25, m.plan.Migrations[0].Name, m.plan.Migrations[0].SHA256, m.chains[0], 1); e != nil {
		t.Fatal(e)
	}
	frontier := before.Frontier
	frontier.Entries = []TransitionEntry{{25, m.plan.Migrations[0].Name, m.plan.Migrations[0].SHA256, m.chains[0], 1}}
	d := b.approved.descriptor
	d.Frontier = frontier
	scope := executionTestScope(t, tx)
	root := target
	root.User = "e1a_test_admin"
	// Metadata-only TEST rehearsal: hide current control receipts inside this
	// rolled-back transaction. It is deliberately not a verified protected state.
	// The actual apply must write/verify history and receipt atomically.
	if _, e = tx.Exec(ctx, "DELETE FROM tremor_deployment.ledger_history_v1 WHERE transition_id=$1", b.approved.descriptor.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, "DELETE FROM tremor_deployment.security_transitions WHERE transition_id=$1", b.approved.descriptor.ID); e != nil {
		t.Fatal(e)
	}
	observed, e := collectSecuritySnapshotTx(ctx, tx, root, oid, d, scope, m)
	if e != nil {
		t.Fatal(e)
	}
	rollback(tx)
	plan.Migrations[0].ReadinessSHA256 = observed.snapshot.ReadinessSHA
	writeExtensionPlan(t, path, plan)
	m, e = LoadExtension(path, anchor.base)
	if e != nil {
		t.Fatal(e)
	}
	before.Frontier.ExtensionManifestSHA = m.ManifestSHA256()
	frontier.ExtensionManifestSHA = m.ManifestSHA256()
	frontier.Entries[0].Chain = m.chains[0]
	after := ProtectedCheckpoint{frontier, scope, observed.snapshot.Metadata, observed.snapshot.ReadinessSHA}
	p := ProtectedMigrationPlan{"e1b-protected-apply-v1", b.approved.sha, m.ManifestSHA256(), before, after, anchor.Plan()}
	raw, _ := json.Marshal(p)
	approval, e := approveProtectedMigration(raw, []string{transitionDigest(p)})
	if e != nil {
		t.Fatal(e)
	}

	t.Run("protected-foreign-anchor-denied", func(t *testing.T) {
		wrong := p
		wrong.TransitionSHA = digest([]byte("foreign anchor"))
		raw, _ := json.Marshal(wrong)
		wrongA, e := approveProtectedMigration(raw, []string{transitionDigest(wrong)})
		if e != nil {
			t.Fatal(e)
		}
		if e = ApplyProtectedExtension(ctx, target, m, b, wrongA); e == nil {
			t.Fatal("foreign anchor accepted")
		}
	})
	t.Run("protected-catalog-drift-denied", func(t *testing.T) {
		if _, e := admin.Exec(ctx, "GRANT SELECT ON public.warehouses TO PUBLIC"); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if _, e := admin.Exec(ctx, "REVOKE SELECT ON public.warehouses FROM PUBLIC"); e != nil {
				t.Fatal(e)
			}
		}()
		if e := ApplyProtectedExtension(ctx, target, m, b, approval); e == nil {
			t.Fatal("catalog drift accepted")
		}
	})
	t.Run("protected-sql-error-rollback", func(t *testing.T) {
		badPath, badPlan := extensionFixture(t, m.base, m.plan.BaselineReadinessSHA256, 1)
		badSQL := []byte("BEGIN;\nCREATE TABLE public.protected_probe(id integer PRIMARY KEY);\nSELECT 1/0;\nCOMMIT;\n")
		if err := os.WriteFile(filepath.Join(filepath.Dir(badPath), badPlan.Migrations[0].Name), badSQL, 0600); err != nil {
			t.Fatal(err)
		}
		badPlan.Migrations[0].SHA256 = digest(badSQL)
		badPlan.Migrations[0].ReadinessSHA256 = p.After.ReadinessSHA
		writeExtensionPlan(t, badPath, badPlan)
		badM, err := LoadExtension(badPath, m.base)
		if err != nil {
			t.Fatal(err)
		}
		badP := p
		badP.ManifestSHA = badM.ManifestSHA256()
		badP.Before = transitionClone(p.Before)
		badP.After = transitionClone(p.After)
		badP.Before.Frontier.ExtensionManifestSHA = badP.ManifestSHA
		badP.After.Frontier.ExtensionManifestSHA = badP.ManifestSHA
		badP.After.Frontier.Entries[0].Checksum = badM.plan.Migrations[0].SHA256
		badP.After.Frontier.Entries[0].Chain = badM.chains[0]
		raw, _ := json.Marshal(badP)
		badA, err := approveProtectedMigration(raw, []string{transitionDigest(badP)})
		if err != nil {
			t.Fatal(err)
		}
		if err = ApplyProtectedExtension(ctx, target, badM, b, badA); err == nil {
			t.Fatal("bad SQL accepted")
		}
		var absent bool
		if err = admin.QueryRow(ctx, "SELECT to_regclass('public.schema_extensions') IS NULL AND to_regclass('public.protected_probe') IS NULL").Scan(&absent); err != nil || !absent {
			t.Fatal("partial protected migration survived")
		}
	})
	if i4Enabled() {
		i4ConcurrentApply(t, ctx, admin, target, m, b, approval)
	}
	if e = ApplyProtectedExtension(ctx, target, m, b, approval); e != nil {
		t.Fatal("protected apply", e)
	}
	if e = ApplyProtectedExtension(ctx, target, m, b, approval); e != nil {
		t.Fatal("repeat protected apply", e)
	}
	var rows int
	if e = admin.QueryRow(ctx, `SELECT count(*) FROM public.protected_probe`).Scan(&rows); e != nil || rows != 1 {
		t.Fatal("duplicate business effect")
	}

	var owner string
	if e = admin.QueryRow(ctx, "SELECT public.protected_context()").Scan(&owner); e != nil || owner != "tremor_owner" {
		t.Fatal("SD execution context", owner, e)
	}
	observer := target
	observer.User = "tremor_migrator"
	if e = VerifyProtectedExtension(ctx, observer, m, b, approval); e != nil {
		t.Fatal("limited verify", e)
	}
	if _, e = admin.Exec(ctx, "ALTER FUNCTION public.protected_context() SECURITY INVOKER"); e != nil {
		t.Fatal(e)
	}
	if e = VerifyProtectedExtension(ctx, observer, m, b, approval); e == nil {
		t.Fatal("SD drift accepted")
	}
	if _, e = admin.Exec(ctx, "ALTER FUNCTION public.protected_context() SECURITY DEFINER"); e != nil {
		t.Fatal(e)
	}
	i3Export(t, ctx, admin, target, m, b, approval, oid)
	t.Log("protected migration and repeat without replay: PASS")
}
