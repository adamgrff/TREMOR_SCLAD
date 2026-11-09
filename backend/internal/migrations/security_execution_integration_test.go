package migrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecurityExecutionIntegrationDisposable(t *testing.T) {
	if os.Getenv("E1B_COLLECTOR_TEST_APPROVED") != "yes" {
		t.Skip("disposable approval required")
	}
	ctx := context.Background()
	cfg, e := pgx.ParseConfig("host=127.0.0.1 port=5432 user=e1a_test_admin dbname=tremor_e1b_collector_test sslmode=disable")
	if e != nil {
		t.Fatal(e)
	}
	cfg.Password = os.Getenv("PGPASSWORD")
	cfg.Fallbacks = nil
	cfg.RuntimeParams = map[string]string{"search_path": "pg_catalog"}
	admin, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer closeConnection(admin)
	var server, version string
	if e = admin.QueryRow(ctx, `SELECT system_identifier::text,version() FROM pg_catalog.pg_control_system()`).Scan(&server, &version); e != nil {
		t.Fatal(e)
	}
	t.Logf("TEST_POSTGRES=%s SYSTEM_IDENTIFIER=%s", version, server)
	target := Target{Host: "127.0.0.1", Port: 5432, Database: cfg.Database, User: cfg.User, Schema: "public", Profile: IdentityProfile, ServerAddress: "127.0.0.1", ServerID: server, SSLMode: "disable", Password: cfg.Password}
	base, e := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if e != nil {
		t.Fatal(e)
	}
	br, e := New(target, base)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = br.Run(ctx, "apply"); e != nil {
		t.Fatal(e)
	}
	var deployment string
	if e = admin.QueryRow(ctx, `SELECT deployment_id::text FROM public.deployment_identity WHERE singleton`).Scan(&deployment); e != nil {
		t.Fatal(e)
	}
	binding := TransitionTarget{server, target.Database, "public", deployment}
	roles, e := approveSecurityRoles(binding, []string{transitionDigest([]any{"e1b-roles-v1", binding})})
	if e != nil {
		t.Fatal(e)
	}
	if e = ProvisionSecurityRoles(ctx, target, roles); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"tremor_deployer", "tremor_api", "tremor_migrator"} {
		if _, e = admin.Exec(ctx, "ALTER ROLE "+pgx.Identifier{name}.Sanitize()+" PASSWORD '"+strings.ReplaceAll(cfg.Password, "'", "''")+"'"); e != nil {
			t.Fatal("TEST credential setup failed")
		}
	}
	if _, e = admin.Exec(ctx, `CREATE SCHEMA tremor_commands; CREATE SCHEMA tremor_deployment`, pgx.QueryExecModeSimpleProtocol); e != nil {
		t.Fatal(e)
	}
	sourceScope := executionTestScope(t, admin)
	tx, e := admin.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		t.Fatal(e)
	}
	ready, e := collectorLegacyReadiness(ctx, tx, "public")
	rollback(tx)
	if e != nil {
		t.Fatal(e)
	}
	path, plan := extensionFixture(t, base, ready, 0)
	m, e := LoadExtension(path, base)
	if e != nil {
		t.Fatal(e)
	}
	_ = plan
	if os.Getenv("E1B_UPGRADE") == "1" {
		legacy, e := NewExtension(target, m, ExtensionOptions{DeploymentID: deployment, BaselineOwner: target.User, LedgerOwner: target.User})
		if e != nil {
			t.Fatal(e)
		}
		extensionSyntheticBusiness(t, legacy, admin)
		t.Log("UPGRADE synthetic stock/history/receipts seeded")
	}
	tx, e = admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		t.Fatal(e)
	}
	st, e := br.readState(ctx, tx)
	if e != nil {
		t.Fatal(e)
	}
	lineage := []any{}
	for _, r := range st.Applied {
		lineage = append(lineage, []any{r.Entry, r.Profile})
	}
	var oid uint32
	if e = tx.QueryRow(ctx, `SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()`).Scan(&oid); e != nil {
		t.Fatal(e)
	}
	d := SecurityTransitionDescriptor{Protocol: SecurityTransitionProtocol, ID: "security-execution-test", Target: binding, Frontier: TransitionFrontier{baselineDigest(base), transitionDigest(lineage), m.ManifestSHA256(), []TransitionEntry{}}, SourceScope: sourceScope, FinalScope: sourceScope, Receipt: TransitionReceiptLocation{"tremor_deployment", "security_transitions", 1}}
	source, e := collectSecuritySnapshotTx(ctx, tx, target, oid, d, sourceScope, m)
	rollback(tx)
	if e != nil {
		t.Fatal(e)
	}
	sql := `BEGIN;
 REVOKE ALL ON SCHEMA public FROM PUBLIC,tremor_api,tremor_executor,tremor_migrator;
 GRANT USAGE ON SCHEMA public TO tremor_api,tremor_migrator;
 ALTER SCHEMA public OWNER TO tremor_owner;
 ALTER SCHEMA tremor_commands OWNER TO tremor_owner;
 ALTER SCHEMA tremor_deployment OWNER TO tremor_ledger_owner;
 CREATE TABLE tremor_deployment.security_transitions(transition_id text PRIMARY KEY,descriptor_sha256 text NOT NULL,payload_sha256 text NOT NULL,bound_snapshot_sha256 text NOT NULL,frontier_sha256 text NOT NULL);
 ALTER TABLE tremor_deployment.security_transitions OWNER TO tremor_ledger_owner;
 REVOKE ALL ON TABLE tremor_deployment.security_transitions FROM PUBLIC,tremor_api,tremor_executor,tremor_migrator;
 GRANT USAGE ON SCHEMA tremor_deployment TO tremor_migrator;
 GRANT SELECT ON tremor_deployment.security_transitions TO tremor_migrator;

 `
	for _, o := range source.snapshot.Metadata {
		key := qualifiedSecurityKey(o.Key)
		switch o.Kind {
		case "table", "view":
			owner := "tremor_owner"
			if o.Key == "public.schema_migrations" || o.Key == "public.deployment_identity" {
				owner = "tremor_ledger_owner"
			}
			sql += "ALTER TABLE " + key + " OWNER TO " + owner + "; REVOKE ALL ON " + key + " FROM PUBLIC,tremor_api,tremor_executor,tremor_migrator; GRANT SELECT ON " + key + " TO tremor_migrator;\n"
		case "sequence":
			sql += "GRANT SELECT ON " + key + " TO tremor_migrator;\n"
		case "function":
			sql += "ALTER FUNCTION " + o.Key + " OWNER TO tremor_owner; REVOKE ALL ON FUNCTION " + o.Key + " FROM PUBLIC,tremor_api,tremor_migrator,tremor_executor;\n"
		}
	}
	sql += LedgerHistoryControlSQL
	sql += `ALTER DEFAULT PRIVILEGES FOR ROLE e1a_test_admin REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC; ALTER DEFAULT PRIVILEGES FOR ROLE tremor_deployer REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC; GRANT SELECT(name) ON public.warehouses TO tremor_api; COMMIT;`
	sql = strings.Replace(sql, "BEGIN;", "BEGIN; REVOKE ALL ON DATABASE "+pgx.Identifier{target.Database}.Sanitize()+" FROM PUBLIC,tremor_api,tremor_migrator,tremor_executor; GRANT CONNECT ON DATABASE "+pgx.Identifier{target.Database}.Sanitize()+" TO tremor_api,tremor_migrator,tremor_deployer;", 1)
	body, e := MigrationBody([]byte(sql))
	if e != nil {
		t.Fatal(e)
	}
	tx, e = admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, body, pgx.QueryExecModeSimpleProtocol); e != nil {
		t.Fatal(e)
	}
	finalScope := executionTestScope(t, tx)
	d.FinalScope = finalScope
	final, e := collectSecuritySnapshotTx(ctx, tx, target, oid, d, finalScope, m)
	rollback(tx)
	if e != nil {
		t.Fatal(e)
	}
	d.Before = source.snapshot.Metadata
	d.After = final.snapshot.Metadata
	d.SourceReadinessSHA = source.snapshot.ReadinessSHA
	d.FinalReadinessSHA = final.snapshot.ReadinessSHA
	d.PayloadSHA = digest([]byte(sql))
	d.Changes = []TransitionDelta{}
	before := map[string]TransitionMetadata{}
	for _, o := range d.Before {
		before[o.Key] = o
	}
	for _, o := range d.After {
		old, ok := before[o.Key]
		b := transitionObjectDigest(old, ok)
		a := transitionDigest(o)
		if b != a {
			d.Changes = append(d.Changes, TransitionDelta{o.Key, b, a, (old.SecurityDefiner || o.SecurityDefiner) && (old.Owner != o.Owner || old.DefinitionSHA != o.DefinitionSHA)})
		}
	}
	approved := transitionApproveFixture(t, d, []byte(sql))
	dep := target
	dep.User = "tremor_deployer"
	for _, fault := range []string{"after_changes", "before_receipt", "after_receipt"} {
		t.Run("rollback-"+fault, func(t *testing.T) {
			r, e := NewSecurityTransition(dep, m, approved, []byte(sql))
			if e != nil {
				t.Fatal(e)
			}
			r.hooks.stage = func(stage string, _ pgx.Tx) error {
				if stage == fault {
					return errors.New("TEST fault")
				}
				return nil
			}
			if state, e := r.Run(ctx); e == nil || state == TransitionProtected {
				t.Fatal("fault accepted")
			}
			observed, e := CollectSecuritySnapshot(ctx, dep, approved, CollectTransitionSource, m)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = BindCollectedSecurityTransition(approved, observed); e != nil {
				t.Fatal(e)
			}
		})
	}

	t.Run("partial-owner-change-SQL-rollback", func(t *testing.T) {
		badSQL := []byte(strings.Replace(sql, "ALTER SCHEMA public OWNER TO tremor_owner;", "ALTER SCHEMA public OWNER TO tremor_owner; SELECT 1/0;", 1))
		badD := d
		badD.PayloadSHA = digest(badSQL)
		badA := transitionApproveFixture(t, badD, badSQL)
		r, e := NewSecurityTransition(dep, m, badA, badSQL)
		if e != nil {
			t.Fatal(e)
		}
		if state, e := r.Run(ctx); e == nil || state == TransitionProtected {
			t.Fatal("partial SQL succeeded")
		}
		actual, e := CollectSecuritySnapshot(ctx, dep, approved, CollectTransitionSource, m)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = BindCollectedSecurityTransition(approved, actual); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("backend-crash-before-commit", func(t *testing.T) {
		r, e := NewSecurityTransition(dep, m, approved, []byte(sql))
		if e != nil {
			t.Fatal(e)
		}
		r.hooks.stage = func(stage string, tx pgx.Tx) error {
			if stage == "before_receipt" {
				var pid int
				if e := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); e != nil {
					return e
				}
				_, e := admin.Exec(ctx, "SELECT pg_terminate_backend($1)", pid)
				return e
			}
			return nil
		}
		if state, e := r.Run(ctx); e == nil || state == TransitionProtected {
			t.Fatal("terminated backend accepted")
		}
		b, e := r.Bound()
		if e != nil {
			t.Fatal(e)
		}
		state, e := RecoverSecurityTransition(ctx, dep, m, b)
		if state != TransitionLegacyVerified || e == nil {
			t.Fatal("crash rollback not confirmed", state, e)
		}
	})
	t.Run("unknown-commit-before-server-commit-history-rollback", func(t *testing.T) {
		r, e := NewSecurityTransition(dep, m, approved, []byte(sql))
		if e != nil {
			t.Fatal(e)
		}
		r.hooks.commit = func(context.Context, pgx.Tx) error { return errors.New("TEST unknown outcome: no COMMIT sent") }
		state, e := r.Run(ctx)
		if state != TransitionLegacyVerified || !errors.Is(e, ErrCommitUnknown) {
			t.Fatal("uncommitted transition not recovered as source", state, e)
		}
		state, e = r.Run(ctx)
		if state != TransitionLegacyVerified || !errors.Is(e, ErrCommitUnknown) {
			t.Fatal("unknown transition replayed SQL", state, e)
		}
		var absent bool
		if e = admin.QueryRow(ctx, "SELECT to_regclass('tremor_deployment.ledger_history_v1') IS NULL").Scan(&absent); e != nil || !absent {
			t.Fatal("history escaped rollback")
		}
	})
	t.Run("commit-and-independent-recovery", func(t *testing.T) {
		r, e := NewSecurityTransition(dep, m, approved, []byte(sql))
		if e != nil {
			t.Fatal(e)
		}
		r.hooks.afterCommit = func(conn *pgx.Conn) error {
			if _, e := admin.Exec(ctx, "SELECT pg_terminate_backend($1)", int(conn.PgConn().PID())); e != nil {
				return e
			}
			return errors.New("TEST backend terminated after successful COMMIT")
		}
		state, e := r.Run(ctx)
		if e != nil || state != TransitionProtected {
			t.Fatal(state, e)
		}
		state, e = r.Run(ctx)
		if e != nil || state != TransitionProtected {
			t.Fatal("second call not observation-only", e)
		}
		b, _ := r.Bound()
		executionHistoryTransitionTest(t, ctx, admin, dep, m, b)
		executionSecurityTest(t, ctx, admin, cfg, dep, m, b)
		executionProtectedApplyTest(t, ctx, admin, dep, m, b, oid)
	})

}
func executionTestScope(t *testing.T, q queryer) TransitionScope {
	s := collectorTestScope(t, q)
	present := false
	for _, r := range s.Objects {
		if r.Key == "public.schema_extensions" && r.Presence == "present" {
			present = true
		}
	}
	out := []TransitionRequirement{}
	for _, r := range s.Objects {
		if (present && r.Key == "public.schema_extensions" && r.Presence == "absent") || strings.HasPrefix(r.Key, "role:collector_") {
			continue
		}
		out = append(out, r)
	}
	s.Objects = out
	return s
}
