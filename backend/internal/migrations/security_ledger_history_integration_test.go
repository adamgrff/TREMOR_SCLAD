package migrations

// Permanent P1 regression, executed only by the disposable TEST controller.
import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestI4HistoricalLedgerTimestampDriftDisposable(t *testing.T) {
	if os.Getenv("I3_MODE") != "faults" || os.Getenv("I3_HTTP_ASSERT") != "yes" {
		t.Skip("isolated I4 controller only")
	}
	i3WaitHTTP(t)
	ctx := context.Background()
	var v i3Bundle
	i3Read(t, "/controller/bundle.json", &v)
	cfg, e := pgx.ParseConfig("host=127.0.0.1 port=5432 user=e1a_test_admin dbname=tremor_e1b_collector_test sslmode=disable")
	if e != nil {
		t.Fatal(e)
	}
	cfg.Password = os.Getenv("PGPASSWORD")
	cfg.Fallbacks = nil
	admin, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer closeConnection(admin)
	var sys, db, fsync, sync string
	var oid uint32
	if e = admin.QueryRow(ctx, `SELECT system_identifier::text,current_database(),(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()),current_setting('fsync'),current_setting('synchronous_commit') FROM pg_catalog.pg_control_system()`).Scan(&sys, &db, &oid, &fsync, &sync); e != nil || sys != v.Target.ServerID || db != "tremor_e1b_collector_test" || oid != v.Source.DatabaseOID {
		t.Fatal("disposable identity mismatch", e)
	}
	t.Logf("TEST_IDENTITY_CONFIRMED system_identifier=%s fsync=%s synchronous_commit=%s", sys, fsync, sync)
	base, e := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if e != nil {
		t.Fatal(e)
	}
	m := &ExtensionManifest{plan: v.Extension, base: base, bodies: v.Bodies, chains: v.Chains}
	b, e := BindSecurityTransition(ApprovedSecurityTransition{v.Descriptor, v.TransitionSHA}, v.Source.Target, v.Source)
	if e != nil {
		t.Fatal(e)
	}
	a := ApprovedProtectedMigration{v.Plan, v.PlanSHA}
	target := v.Target
	raw, e := os.ReadFile("/verifier-password/password")
	if e != nil {
		t.Fatal(e)
	}
	target.Password = string(raw)
	c, _ := i3Client(t, "/runtime")
	if VerifyProtectedExtension(ctx, target, m, b, a) != nil || c.Check(ctx) != nil {
		t.Fatal("positive control not READY")
	}
	i3HTTPIntegrity(t, ctx, admin)
	for _, ledger := range []string{"schema_migrations", "schema_extensions"} {
		t.Run(ledger, func(t *testing.T) {
			table := pgx.Identifier{"public", ledger}.Sanitize()
			var old time.Time
			if e := admin.QueryRow(ctx, "SELECT applied_at FROM "+table+" WHERE ordinal=1").Scan(&old); e != nil {
				t.Fatal(e)
			}
			restore := func() {
				if _, e := admin.Exec(ctx, "UPDATE "+table+" SET applied_at=$1 WHERE ordinal=1", old); e != nil {
					t.Error("restore failed", e)
				}
			}
			defer restore()
			tag, e := admin.Exec(ctx, "UPDATE "+table+" SET applied_at=applied_at+interval '1 second' WHERE ordinal=1")
			if e != nil || tag.RowsAffected() != 1 {
				t.Fatal("timestamp-only fault injection failed", e)
			}

			tx, e := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if e != nil {
				t.Fatal(e)
			}
			current, e := currentHistoryState(ctx, tx, target, oid)
			if e != nil {
				t.Fatal(e)
			}
			ledgerState := current.Baseline
			if ledger == "schema_extensions" {
				ledgerState = current.Extension
			}
			changed := false
			oidSame := false
			for _, l := range v.FinalEvidence.Ledgers {
				if l.Key == "public."+ledger {
					changed = l.RowsSHA != ledgerState.SnapshotSHA
					oidSame = l.OID == ledgerState.OID
				}
			}
			ready, e := collectorLegacyReadiness(ctx, tx, "public")
			if e != nil || ready != v.FinalEvidence.ReadinessSHA {
				t.Fatal("unexpected readiness drift", e)
			}
			st, e := inspectSecurityLineage(ctx, tx, target, m, v.Source.Target.DeploymentID, "tremor_ledger_owner", "tremor_ledger_owner", false)
			if e != nil || len(st.Applied) != len(v.Plan.After.Frontier.Entries) {
				t.Fatal("unexpected manifest prefix drift", e)
			}
			_, collectErr := collectSecuritySnapshotTx(ctx, tx, target, oid, protectedPolicyDescriptor(b, v.Plan.After), v.Plan.After.Scope, m)
			rollback(tx)
			if !changed || !oidSame || collectErr == nil {
				t.Fatal("missing drift detection", changed, oidSame, collectErr)
			}

			directAccepted := VerifyProtectedExtension(ctx, target, m, b, a) == nil
			mtlsReady := c.Check(ctx) == nil
			client := &http.Client{Timeout: 10 * time.Second}
			apiQueries := func() int64 {
				r, e := client.Get("http://127.0.0.1:8081/internal/test-sql-count")
				if e != nil {
					t.Fatal(e)
				}
				defer r.Body.Close()
				var n int64
				if json.NewDecoder(r.Body).Decode(&n) != nil {
					t.Fatal("query counter unavailable")
				}
				return n
			}
			beforeSQL := apiQueries()
			response, e := client.Get("http://127.0.0.1:8081/api/racks")
			if e != nil {
				t.Fatal(e)
			}
			body, e := io.ReadAll(response.Body)
			response.Body.Close()
			if e != nil {
				t.Fatal(e)
			}
			var failure map[string]string
			if json.Unmarshal(body, &failure) != nil || failure["code"] != "protected_not_ready" {
				t.Fatal("unsafe API refusal", string(body))
			}
			if apiQueries() != beforeSQL {
				t.Fatal("failed readiness executed API SQL")
			}
			deploy := target
			deploy.User = "tremor_deployer"
			deploy.Password = os.Getenv("PGPASSWORD")
			if ApplyProtectedExtension(ctx, deploy, m, b, a) == nil {
				t.Fatal("repeat apply accepted changed history")
			}
			var count int
			if e := admin.QueryRow(ctx, "SELECT count(*) FROM tremor_deployment.ledger_history_v1").Scan(&count); e != nil || count != 2 {
				t.Fatal("read/recovery/apply rewrote history")
			}
			t.Logf("P1_REPRO ledger=%s rows_hash_changed=%v oid_same=%v legacy_readiness_same=true manifest_prefix_valid=true direct_verifier_accepted=%v mtls_READY=%v real_API_HTTP=%d", ledger, changed, oidSame, directAccepted, mtlsReady, response.StatusCode)
			if directAccepted || mtlsReady || response.StatusCode != 503 {
				t.Error("historical ledger mutation must reject protected readiness")
			}
		})
	}

	for _, fault := range []struct{ name, apply, restore string }{
		{"multiple-baseline-timestamps", "UPDATE public.schema_migrations SET applied_at=applied_at+interval '1 microsecond'", "UPDATE public.schema_migrations SET applied_at=applied_at-interval '1 microsecond'"},
		{"extension-exact-microsecond", "UPDATE public.schema_extensions SET applied_at=applied_at+interval '1 microsecond' WHERE ordinal=1", "UPDATE public.schema_extensions SET applied_at=applied_at-interval '1 microsecond' WHERE ordinal=1"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			if _, e := admin.Exec(ctx, fault.apply); e != nil {
				t.Fatal(e)
			}
			defer func() {
				if _, e := admin.Exec(ctx, fault.restore); e != nil {
					t.Fatal(e)
				}
			}()
			if VerifyProtectedExtension(ctx, target, m, b, a) == nil || c.Check(ctx) == nil {
				t.Fatal("timestamp drift READY")
			}
			i3HTTPStatus(t, 503)
		})
	}
	var step int
	var sha, payload string
	if e = admin.QueryRow(ctx, "SELECT step,checkpoint_sha256,payload FROM tremor_deployment.ledger_history_v1 WHERE step=1").Scan(&step, &sha, &payload); e != nil {
		t.Fatal(e)
	}
	for _, fault := range []string{"missing-historical-anchor", "malformed-historical-anchor"} {
		t.Run(fault, func(t *testing.T) {
			if fault == "missing-historical-anchor" {
				_, e = admin.Exec(ctx, "DELETE FROM tremor_deployment.ledger_history_v1 WHERE step=1")
			} else {
				_, e = admin.Exec(ctx, "UPDATE tremor_deployment.ledger_history_v1 SET payload='{}' WHERE step=1")
			}
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				_, e := admin.Exec(ctx, "DELETE FROM tremor_deployment.ledger_history_v1 WHERE step=1")
				if e == nil {
					_, e = admin.Exec(ctx, "INSERT INTO tremor_deployment.ledger_history_v1(transition_id,step,checkpoint_sha256,payload) VALUES($1,$2,$3,$4)", v.Descriptor.ID, step, sha, payload)
				}
				if e != nil {
					t.Fatal(e)
				}
			}()
			if VerifyProtectedExtension(ctx, target, m, b, a) == nil || c.Check(ctx) == nil {
				t.Fatal("invalid anchor READY")
			}
			i3HTTPStatus(t, 503)
		})
	}
	// Repeated successful apply is observation-only, preserving both receipts.
	deploy := target
	deploy.User = "tremor_deployer"
	deploy.Password = os.Getenv("PGPASSWORD")
	for i := 0; i < 2; i++ {
		if e = ApplyProtectedExtension(ctx, deploy, m, b, a); e != nil {
			t.Fatal("legitimate repeat", e)
		}
	}
	var historyCount, receiptCount, effects int
	if e = admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM tremor_deployment.ledger_history_v1),(SELECT count(*) FROM public.schema_extensions),(SELECT count(*) FROM public.protected_probe)").Scan(&historyCount, &receiptCount, &effects); e != nil || historyCount != 2 || receiptCount != 1 || effects != 1 {
		t.Fatal("duplicate history/receipt/effects")
	}
	if VerifyProtectedExtension(ctx, target, m, b, a) != nil || c.Check(ctx) != nil {
		t.Fatal("restored positive control failed")
	}
	i3HTTPIntegrity(t, ctx, admin)
}
