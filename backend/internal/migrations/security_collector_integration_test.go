package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSecurityCollectorIntegrationDisposable(t *testing.T) {
	if os.Getenv("E1B_COLLECTOR_TEST_APPROVED") != "yes" {
		t.Skip("NOT RUN: separately authorized isolated collector TEST container required")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig("host=127.0.0.1 port=5432 user=e1a_test_admin dbname=tremor_e1b_collector_test sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Host = "127.0.0.1"
	cfg.Port = 5432
	cfg.User = "e1a_test_admin"
	cfg.Database = "tremor_e1b_collector_test"
	cfg.Password = os.Getenv("PGPASSWORD")
	cfg.Fallbacks = nil
	cfg.RuntimeParams = map[string]string{"search_path": "pg_catalog", "client_encoding": "UTF8"}
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal("cannot connect to own disposable TEST cluster")
	}
	defer closeConnection(admin)
	var serverID, version string
	if err = admin.QueryRow(ctx, `SELECT system_identifier::text,version() FROM pg_catalog.pg_control_system()`).Scan(&serverID, &version); err != nil {
		t.Fatal(err)
	}
	t.Logf("TEST_POSTGRES=%s TEST_SYSTEM_IDENTIFIER=%s", version, serverID)
	target := Target{Host: "127.0.0.1", Port: 5432, Database: "tremor_e1b_collector_test", User: "e1a_test_admin", Schema: "public", Profile: IdentityProfile, ServerAddress: "127.0.0.1", ServerID: serverID, SSLMode: "disable", Password: cfg.Password}
	base, err := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(target, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Run(ctx, "apply"); err != nil {
		t.Fatal(err)
	}
	testLegacyReadinessBaseline(t, ctx, admin, target, base)
	// Preparation only in this new TEST cluster. No migration ledger transition.
	setup := `CREATE ROLE tremor_owner NOLOGIN; CREATE ROLE tremor_ledger_owner NOLOGIN; CREATE ROLE tremor_executor NOLOGIN; CREATE ROLE tremor_api LOGIN; CREATE ROLE tremor_migrator LOGIN; CREATE ROLE tremor_deployer LOGIN;
 CREATE ROLE collector_reader NOLOGIN; CREATE ROLE collector_group NOLOGIN; CREATE ROLE collector_group_root NOLOGIN; CREATE ROLE collector_observer LOGIN;
 GRANT collector_group TO collector_reader WITH INHERIT TRUE, SET FALSE, ADMIN FALSE;
 GRANT collector_group_root TO collector_group WITH INHERIT TRUE, SET TRUE, ADMIN TRUE;
 CREATE SCHEMA tremor_commands; CREATE SCHEMA tremor_deployment;
 CREATE TABLE tremor_deployment.security_transitions(transition_id text PRIMARY KEY,descriptor_sha256 text NOT NULL,payload_sha256 text NOT NULL,bound_snapshot_sha256 text NOT NULL,frontier_sha256 text NOT NULL);
 INSERT INTO tremor_deployment.security_transitions VALUES('synthetic-history',repeat('a',64),repeat('b',64),repeat('c',64),repeat('d',64));
 CREATE TABLE public.collector_probe(id integer PRIMARY KEY,label text NOT NULL); INSERT INTO public.collector_probe VALUES(1,'synthetic');
 CREATE VIEW public.collector_probe_view AS SELECT label FROM public.collector_probe;
 CREATE FUNCTION tremor_commands.read_probe(x integer) RETURNS integer LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS 'SELECT x';
 REVOKE ALL ON FUNCTION tremor_commands.read_probe(integer) FROM PUBLIC;
 GRANT USAGE ON SCHEMA public,tremor_commands TO tremor_api;
 GRANT SELECT(label) ON public.collector_probe TO tremor_api; GRANT SELECT ON public.collector_probe_view TO tremor_api;
 GRANT EXECUTE ON FUNCTION tremor_commands.read_probe(integer) TO tremor_api;
 ALTER DEFAULT PRIVILEGES FOR ROLE tremor_deployer REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
 ALTER DEFAULT PRIVILEGES FOR ROLE tremor_deployer IN SCHEMA public GRANT SELECT ON TABLES TO tremor_api;
 GRANT USAGE ON SCHEMA public,tremor_commands,tremor_deployment TO collector_observer;
 GRANT SELECT ON ALL TABLES IN SCHEMA public,tremor_deployment TO collector_observer;
 GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO collector_observer;
 GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO collector_observer;`
	if _, err = admin.Exec(ctx, setup, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	// New TEST-only login password; never printed or written to source/evidence.
	if _, err = admin.Exec(ctx, `ALTER ROLE tremor_api PASSWORD '`+strings.ReplaceAll(cfg.Password, "'", "''")+`'`); err != nil {
		t.Fatal("cannot configure TEST API login")
	}
	if _, err = admin.Exec(ctx, `ALTER ROLE collector_observer PASSWORD '`+strings.ReplaceAll(cfg.Password, "'", "''")+`'`); err != nil {
		t.Fatal("cannot configure TEST observer login")
	}
	manifestFile := filepath.Join(t.TempDir(), "extensions.json")
	plan := ExtensionPlan{Format: 1, ReadinessFormat: ExtensionReadinessFormat, Profile: ExtensionProfile, Canonicalization: Canon, BaselineSHA256: baselineDigest(base), BaselineReadinessSHA256: digest([]byte("fixture-plan")), Migrations: []ExtensionEntry{}}
	raw, _ := json.Marshal(plan)
	if err = os.WriteFile(manifestFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadExtension(manifestFile, base)
	if err != nil {
		t.Fatal(err)
	}
	scope := collectorTestScope(t, admin)
	reference := func() (ApprovedSecurityTransition, CollectedSecuritySnapshot) {
		t.Helper()
		tx, e := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if e != nil {
			t.Fatal(e)
		}
		defer rollback(tx)
		st, e := runner.readState(ctx, tx)
		if e != nil {
			t.Fatal(e)
		}
		lineage := []any{}
		for _, a := range st.Applied {
			lineage = append(lineage, []any{a.Entry, a.Profile})
		}
		var deployment string
		if e = tx.QueryRow(ctx, `SELECT deployment_id::text FROM public.deployment_identity WHERE singleton`).Scan(&deployment); e != nil {
			t.Fatal(e)
		}
		d := SecurityTransitionDescriptor{Protocol: SecurityTransitionProtocol, ID: "collector-fixture", Target: TransitionTarget{ClusterID: serverID, Database: target.Database, Schema: target.Schema, DeploymentID: deployment}, Frontier: TransitionFrontier{BaselineManifestSHA: baselineDigest(base), BaselineLineageSHA: transitionDigest(lineage), ExtensionManifestSHA: m.ManifestSHA256(), Entries: []TransitionEntry{}}, SourceScope: scope, FinalScope: scope, Receipt: TransitionReceiptLocation{Schema: "tremor_deployment", Table: "security_transitions", Format: 1}}
		// Test-only observation used to construct synthetic test approval. This is not
		// a production approval path and no approval code exists in the collector.
		var oid uint32
		if e = tx.QueryRow(ctx, `SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()`).Scan(&oid); e != nil {
			t.Fatal(e)
		}
		c, e := collectSecuritySnapshotTx(ctx, tx, target, oid, d, scope, m)
		if e != nil {
			t.Fatal(e)
		}
		d.Before = transitionClone(c.snapshot.Metadata)
		d.After = transitionClone(d.Before)
		d.SourceReadinessSHA = c.snapshot.ReadinessSHA
		d.FinalReadinessSHA = digest([]byte("not-applied-fixture-final"))
		// A reviewed hypothetical final metadata change, never executed in this test.
		i := transitionTestIndex(d.After, "role:collector_reader")
		before := d.After[i]
		d.After[i].RoleFlags.Login = true
		d.Changes = []TransitionDelta{{Key: before.Key, BeforeSHA: transitionDigest(before), AfterSHA: transitionDigest(d.After[i])}}
		payload := []byte("-- synthetic test approval only; never executed\n")
		d.PayloadSHA = digest(payload)
		a := transitionApproveFixture(t, d, payload)
		c.seal = transitionDigest([]any{c.snapshot, c.receipt, c.privileges, c.paths})
		return a, c
	}
	a, expected := reference()
	var first CollectedSecuritySnapshot
	t.Run("complete-repeatable-source", func(t *testing.T) {
		first, err = CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if err != nil {
			t.Fatal(err)
		}
		second, e := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(first.snapshot, second.snapshot) || !reflect.DeepEqual(first.snapshot, expected.snapshot) {
			t.Fatal("unchanged snapshot not repeatable")
		}
		bound, e := BindCollectedSecurityTransition(a, first)
		if e != nil {
			t.Fatal(e)
		}
		if got := ClassifyCollectedTransitionRecovery(bound, first); got != TransitionSourceConfirmed {
			t.Fatal(got)
		}
		if len(first.snapshot.Ledgers) != 1 || len(first.privileges) == 0 || len(first.paths) == 0 {
			t.Fatal("incomplete collection")
		}
		foundPath, foundColumn, foundGlobal := false, false, false
		foundTransitive, foundFunction, foundLocal, foundAdmin := false, false, false, false
		for _, p := range first.paths {
			if p.Actor == "collector_reader" && p.Role == "collector_group" {
				foundPath = p.Member && p.Inherited && !p.Set
			}
			if p.Actor == "collector_reader" && p.Role == "collector_group_root" {
				foundTransitive = p.Member && p.Inherited && !p.Set
			}
		}
		for _, p := range first.privileges {
			if p.Actor == "tremor_api" && p.Object == "public.collector_probe.label" && p.Privilege == "SELECT" {
				foundColumn = p.Allowed
			}
		}
		for _, o := range first.snapshot.Metadata {
			if o.Key == "default_acl::tremor_deployer:functions" {
				foundGlobal = o.ACL.Mode == "explicit"
			}
			if o.Key == "tremor_commands.read_probe(x integer)" {
				foundFunction = o.Owner == "e1a_test_admin" && o.SecurityDefiner && reflect.DeepEqual(o.SearchPath, []string{"pg_catalog", "pg_temp"})
			}
			if o.Key == "default_acl:public:tremor_deployer:tables" {
				for _, g := range o.ACL.Grants {
					foundLocal = foundLocal || g.Grantee == "tremor_api" && g.Privilege == "SELECT"
				}
			}
			if o.Key == "role:collector_group" {
				for _, edge := range o.Memberships {
					foundAdmin = foundAdmin || edge.Role == "collector_group_root" && edge.Admin && edge.Set && edge.Inherit
				}
			}
		}
		if !foundPath || !foundColumn || !foundGlobal || !foundTransitive || !foundFunction || !foundLocal || !foundAdmin {
			t.Fatal("actual memberships/column/global privileges not captured")
		}
	})
	if !first.valid() {
		t.Fatal("positive prerequisite failed")
	}
	t.Run("limited-reader-RLS-refuses-partial-data", func(t *testing.T) {
		a, _ := reference()
		observer := target
		observer.User = "collector_observer"
		if _, e := CollectSecuritySnapshot(ctx, observer, a, CollectTransitionSource, m); e != nil {
			t.Fatal("explicitly granted TEST observer failed", e)
		}
		if _, e := admin.Exec(ctx, `ALTER TABLE public.collector_probe ENABLE ROW LEVEL SECURITY; CREATE POLICY collector_hidden ON public.collector_probe USING(false)`, pgx.QueryExecModeSimpleProtocol); e != nil {
			t.Fatal(e)
		}
		defer func() {
			_, e := admin.Exec(ctx, `DROP POLICY collector_hidden ON public.collector_probe; ALTER TABLE public.collector_probe DISABLE ROW LEVEL SECURITY`, pgx.QueryExecModeSimpleProtocol)
			if e != nil {
				t.Fatal(e)
			}
		}()
		if c, e := CollectSecuritySnapshot(ctx, observer, a, CollectTransitionSource, m); e == nil || c.valid() {
			t.Fatal("RLS-filtered partial success admitted")
		}
	})
	t.Run("historical-transition-receipt", func(t *testing.T) {
		a, c := reference()
		b, e := BindCollectedSecurityTransition(a, c)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = admin.Exec(ctx, `UPDATE tremor_deployment.security_transitions SET payload_sha256=repeat('e',64) WHERE transition_id='synthetic-history'`); e != nil {
			t.Fatal(e)
		}
		defer func() {
			_, _ = admin.Exec(ctx, `UPDATE tremor_deployment.security_transitions SET payload_sha256=repeat('b',64) WHERE transition_id='synthetic-history'`)
		}()
		changed, e := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if e != nil {
			t.Fatal(e)
		}
		if changed.snapshot.BusinessReceiptsSHA == c.snapshot.BusinessReceiptsSHA {
			t.Fatal("historical receipt change omitted")
		}
		if got := ClassifyCollectedTransitionRecovery(b, changed); got != TransitionEvidenceRejected {
			t.Fatal("changed history confirmed", got)
		}
	})
	t.Run("actual-receipt-without-transition", func(t *testing.T) {
		a, c := reference()
		b, e := BindCollectedSecurityTransition(a, c)
		if e != nil {
			t.Fatal(e)
		}
		r := b.ExpectedReceipt()
		if _, e = admin.Exec(ctx, `INSERT INTO tremor_deployment.security_transitions VALUES($1,$2,$3,$4,$5)`, r.TransitionID, r.DescriptorSHA, r.PayloadSHA, r.BoundSnapshotSHA, r.FrontierSHA); e != nil {
			t.Fatal(e)
		}
		defer func() {
			_, _ = admin.Exec(ctx, `DELETE FROM tremor_deployment.security_transitions WHERE transition_id=$1`, r.TransitionID)
		}()
		observed, e := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = BindCollectedSecurityTransition(a, observed); e == nil {
			t.Fatal("receipt-only state admitted")
		}
		if got := ClassifyCollectedTransitionRecovery(b, observed); got != TransitionEvidenceRejected {
			t.Fatal("false COMMIT confirmation", got)
		}
	})

	t.Run("parameter-grant-drift-regression", func(t *testing.T) {
		a, before := reference()
		bound, e := BindCollectedSecurityTransition(a, before)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = admin.Exec(ctx, `GRANT SET ON PARAMETER session_replication_role TO tremor_api`); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if _, e := admin.Exec(ctx, `REVOKE SET ON PARAMETER session_replication_role FROM tremor_api`); e != nil {
				t.Fatal(e)
			}
		}()
		var granted bool
		if e = admin.QueryRow(ctx, `SELECT pg_catalog.has_parameter_privilege('tremor_api','session_replication_role','SET')`).Scan(&granted); e != nil || !granted {
			t.Fatal("TEST parameter grant prerequisite failed")
		}
		observed, e := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if e != nil {
			return
		}
		if reflect.DeepEqual(before.snapshot, observed.snapshot) && ClassifyCollectedTransitionRecovery(bound, observed) == TransitionSourceConfirmed {
			t.Fatal("BLOCKER: parameter GRANT invisible; source falsely confirmed")
		}
		if _, e = BindCollectedSecurityTransition(a, observed); e == nil {
			t.Fatal("parameter grant drift bound")
		}
		if ClassifyCollectedTransitionRecovery(bound, observed) != TransitionEvidenceRejected {
			t.Fatal("parameter grant drift recovery accepted")
		}
	})

	testCollectorReadinessContext(t, ctx, admin, target, m, reference)
	testParameterPrivileges(t, ctx, admin, cfg, target, m, reference)
	testCatalogSessionDefaults(t, ctx, admin, cfg, target, m, reference)
	t.Run("db-specific-role-setting-drift", func(t *testing.T) {
		a, initial := reference()
		bound, e := BindCollectedSecurityTransition(a, initial)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = admin.Exec(ctx, `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test SET session_replication_role=replica`); e != nil {
			t.Fatal(e)
		}
		defer func() {
			_, _ = admin.Exec(ctx, `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test RESET session_replication_role`)
		}()
		apiCfg := cfg.Copy()
		apiCfg.User = "tremor_api"
		api, e := pgx.ConnectConfig(ctx, apiCfg)
		if e != nil {
			t.Fatal(e)
		}
		defer closeConnection(api)
		var setting string
		if e = api.QueryRow(ctx, `SELECT current_setting('session_replication_role')`).Scan(&setting); e != nil {
			t.Fatal(e)
		}
		if setting != "replica" {
			t.Fatal("regression startup prerequisite not reproduced")
		}
		observed, e := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if e != nil {
			return
		}
		if reflect.DeepEqual(initial.snapshot, observed.snapshot) && ClassifyCollectedTransitionRecovery(bound, observed) == TransitionSourceConfirmed {
			t.Fatal("BLOCKER: database-role startup setting changed but collector still confirms original source")
		}
	})
	for _, tc := range []struct{ name, apply, restore string }{
		{"missing-role", `ALTER ROLE tremor_migrator RENAME TO missing_migrator`, `ALTER ROLE missing_migrator RENAME TO tremor_migrator`},
		{"unexpected-grant", `GRANT UPDATE ON public.collector_probe TO tremor_api`, `REVOKE UPDATE ON public.collector_probe FROM tremor_api`},
		{"unexpected-database-CREATE", `GRANT CREATE ON DATABASE tremor_e1b_collector_test TO tremor_api`, `REVOKE CREATE ON DATABASE tremor_e1b_collector_test FROM tremor_api`},
		{"dangerous-set-role", `GRANT tremor_owner TO tremor_api WITH SET TRUE,INHERIT FALSE`, `REVOKE tremor_owner FROM tremor_api`},
		{"changed-owner", `ALTER TABLE public.collector_probe OWNER TO tremor_api`, `ALTER TABLE public.collector_probe OWNER TO e1a_test_admin`},
		{"missing-schema", `ALTER SCHEMA tremor_commands RENAME TO collector_unknown`, `ALTER SCHEMA collector_unknown RENAME TO tremor_commands`},
		{"public-execute", `GRANT EXECUTE ON FUNCTION tremor_commands.read_probe(integer) TO PUBLIC`, `REVOKE EXECUTE ON FUNCTION tremor_commands.read_probe(integer) FROM PUBLIC`},
		{"changed-SD", `ALTER FUNCTION tremor_commands.read_probe(integer) SECURITY INVOKER`, `ALTER FUNCTION tremor_commands.read_probe(integer) SECURITY DEFINER`},
		{"missing-function", `ALTER FUNCTION tremor_commands.read_probe(integer) RENAME TO missing_probe`, `ALTER FUNCTION tremor_commands.missing_probe(integer) RENAME TO read_probe`},
		{"unscoped-object", `CREATE TABLE public.unexpected_collector_object(id integer)`, `DROP TABLE public.unexpected_collector_object`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, baseline := reference()
			bound, e := BindCollectedSecurityTransition(a, baseline)
			if e != nil {
				t.Fatal(e)
			}
			if _, e := admin.Exec(ctx, tc.apply); e != nil {
				t.Fatal(e)
			}
			defer func() {
				if _, e := admin.Exec(ctx, tc.restore); e != nil {
					t.Fatal("TEST restore failed", e)
				}
			}()
			observed, e := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
			if e == nil {
				if _, e = BindCollectedSecurityTransition(a, observed); e == nil {
					t.Fatal("changed TEST security state was admitted")
				}
				if ClassifyCollectedTransitionRecovery(bound, observed) == TransitionFinalConfirmed || ClassifySecurityTransition(bound, TransitionObservation{Snapshot: &observed.snapshot, Receipt: observed.receipt, CommitUncertain: true}) == TransitionProtected {
					t.Fatal("false PROTECTED for changed TEST state")
				}
			} else if observed.valid() {
				t.Fatal("failed collection returned usable partial evidence")
			}
		})
	}
	t.Run("insufficient-collector-rights", func(t *testing.T) {
		a, _ := reference()
		limited := target
		limited.User = "tremor_api"
		if _, e := CollectSecuritySnapshot(ctx, limited, a, CollectTransitionSource, m); e == nil {
			t.Fatal("unprivileged collector succeeded")
		}
	})
	t.Run("foreign-deployment", func(t *testing.T) {
		a, _ := reference()
		d := transitionClone(a.descriptor)
		d.Target.DeploymentID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		a = transitionApproveFixture(t, d, []byte("-- synthetic test approval only; never executed\n"))
		if _, e := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m); e == nil {
			t.Fatal("foreign deployment admitted")
		}
	})
	t.Run("contradictory-offline-evidence", func(t *testing.T) {
		a, c := reference()
		b, e := BindCollectedSecurityTransition(a, c)
		if e != nil {
			t.Fatal(e)
		}
		for _, mutate := range []func(*TransitionSnapshot){func(s *TransitionSnapshot) { s.Ledgers[0].OID++ }, func(s *TransitionSnapshot) { s.Ledgers = nil }, func(s *TransitionSnapshot) { s.Metadata = s.Metadata[:1] }} {
			bad := transitionClone(c.snapshot)
			mutate(&bad)
			if _, e = BindSecurityTransition(a, a.descriptor.Target, bad); e == nil {
				t.Fatal("bad DTO bound")
			}
			if got := ClassifyTransitionRecovery(b, &bad, nil); got != TransitionEvidenceRejected {
				t.Fatal("false recovery confirmation", got)
			}
		}
	})
	t.Run("real-runtime-denials", func(t *testing.T) {
		// ALTER OWNER can rewrite grant attribution. Re-establish the explicit
		// runtime fixture after owner-negative tests, never assume REVOKE/restore
		// reproduces the original raw NULL-vs-explicit ACL representation.
		if _, e := admin.Exec(ctx, `GRANT SELECT(label) ON public.collector_probe TO tremor_api; GRANT EXECUTE ON FUNCTION tremor_commands.read_probe(integer) TO tremor_api`, pgx.QueryExecModeSimpleProtocol); e != nil {
			t.Fatal(e)
		}
		apiCfg := cfg.Copy()
		apiCfg.User = "tremor_api"
		api, err := pgx.ConnectConfig(ctx, apiCfg)
		if err != nil {
			t.Fatal("TEST API login failed")
		}
		defer closeConnection(api)
		for _, sql := range []string{`SELECT label FROM public.collector_probe`, `SELECT tremor_commands.read_probe(3)`} {
			if _, e := api.Exec(ctx, sql); e != nil {
				t.Fatal("allowed TEST operation rejected", e)
			}
		}
		for _, sql := range []string{`SELECT id FROM public.collector_probe`, `UPDATE public.collector_probe SET label='forbidden'`, `CREATE TABLE public.forbidden(id integer)`, `SET ROLE tremor_owner`, `DELETE FROM public.schema_migrations`} {
			_, e := api.Exec(ctx, sql)
			var pgErr *pgconn.PgError
			if !errors.As(e, &pgErr) || pgErr.Code != "42501" {
				t.Fatal("expected typed permission denial", e)
			}
		}
	})
	t.Run("ledger-and-identity-preserved", func(t *testing.T) {
		_, last := reference()
		if !reflect.DeepEqual(first.snapshot.Ledgers, last.snapshot.Ledgers) || first.snapshot.IdentitySHA != last.snapshot.IdentitySHA || first.snapshot.SequenceStateSHA != last.snapshot.SequenceStateSHA {
			t.Fatal("collector TEST changed protected baseline evidence")
		}
	})
}

func collectorTestScope(t *testing.T, q queryer) TransitionScope {
	t.Helper()
	s := TransitionScope{Parameters: []string{"lock_timeout", "row_security", "search_path", "session_replication_role", "statement_timeout"}, Profile: "prepared-v1", Objects: []TransitionRequirement{}, Creators: []string{"e1a_test_admin", "tremor_deployer"}}
	for _, role := range []string{"e1a_test_admin", "pg_database_owner", "tremor_api", "tremor_deployer", "tremor_executor", "tremor_ledger_owner", "tremor_migrator", "tremor_owner", "collector_group", "collector_group_root", "collector_reader", "collector_observer"} {
		s.Objects = append(s.Objects, TransitionRequirement{Key: "role:" + role, Kind: "role", Presence: "present"})
	}
	schemas := []string{"public", "tremor_commands", "tremor_deployment"}
	for _, schema := range schemas {
		s.Objects = append(s.Objects, TransitionRequirement{Key: "schema:" + schema, Kind: "schema", Schema: schema, Presence: "present"})
	}
	for _, creator := range s.Creators {
		for _, schema := range append([]string{""}, schemas...) {
			for _, class := range []string{"functions", "sequences", "tables"} {
				s.Objects = append(s.Objects, TransitionRequirement{Key: "default_acl:" + schema + ":" + creator + ":" + class, Kind: "default_acl", Schema: schema, Parent: "role:" + creator, Presence: "present"})
			}
		}
	}
	rows, err := q.Query(context.Background(), securityCoverageSQL, schemas)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key, kind string
		if err = rows.Scan(&key, &kind); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		r := TransitionRequirement{Key: key, Kind: kind, Schema: strings.SplitN(key, ".", 2)[0], Presence: "present"}
		if kind == "column" {
			r.Parent = key[:strings.LastIndex(key, ".")]
		}
		s.Objects = append(s.Objects, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	s.Objects = append(s.Objects, TransitionRequirement{Key: "public.schema_extensions", Kind: "table", Schema: "public", Presence: "absent"})
	sort.Slice(s.Objects, func(i, j int) bool { return s.Objects[i].Key < s.Objects[j].Key })
	return s
}
