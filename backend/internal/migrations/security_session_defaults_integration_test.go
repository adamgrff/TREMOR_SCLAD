package migrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"reflect"
	"testing"
)

// This helper operates only inside the explicitly approved disposable fixture.
// The reference closure creates TEST approvals; it is not production approval.
func testCatalogSessionDefaults(t *testing.T, ctx context.Context, admin *pgx.Conn, cfg *pgx.ConnConfig, target Target, m *ExtensionManifest, reference func() (ApprovedSecurityTransition, CollectedSecuritySnapshot)) {
	exec := func(t *testing.T, sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("catalog-default-precedence-and-new-login", func(t *testing.T) {
		exec(t, `ALTER ROLE ALL SET statement_timeout='120000ms'; ALTER DATABASE tremor_e1b_collector_test SET statement_timeout='110000ms'; ALTER ROLE tremor_api SET statement_timeout='100000ms'; ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test SET statement_timeout='90000ms'`)
		defer exec(t, `ALTER ROLE ALL RESET statement_timeout; ALTER DATABASE tremor_e1b_collector_test RESET statement_timeout; ALTER ROLE tremor_api RESET statement_timeout; ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test RESET statement_timeout`)
		a, actual := reference()
		if _, err := BindCollectedSecurityTransition(a, actual); err != nil {
			t.Fatal(err)
		}
		again, err := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual.snapshot, again.snapshot) {
			t.Fatal("unstable defaults snapshot")
		}
		role := actual.snapshot.Metadata[transitionTestIndex(actual.snapshot.Metadata, "role:tremor_api")]
		for i, value := range []string{"120000ms", "110000ms", "100000ms", "90000ms"} {
			if !role.SessionDefaults[i].Present || !reflect.DeepEqual(role.SessionDefaults[i].Settings, []TransitionSessionSetting{{"statement_timeout", value}}) {
				t.Fatal("applicable catalog layer missing")
			}
		}
		predicted, err := CatalogLoginOverrides(role, actual.snapshot.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(predicted, []TransitionSessionSetting{{"statement_timeout", "90000ms"}}) {
			t.Fatal("precedence mismatch")
		}
		apiCfg := cfg.Copy()
		apiCfg.User = "tremor_api"
		api, err := pgx.ConnectConfig(ctx, apiCfg)
		if err != nil {
			t.Fatal(err)
		}
		defer closeConnection(api)
		var ms int
		if err = api.QueryRow(ctx, `SELECT setting::integer FROM pg_catalog.pg_settings WHERE name='statement_timeout'`).Scan(&ms); err != nil || ms != 90000 {
			t.Fatal("new login disagrees with catalog prediction")
		}
		fresh, err := pgx.ConnectConfig(ctx, cfg.Copy())
		if err != nil {
			t.Fatal(err)
		}
		defer closeConnection(fresh)
		if _, err = fresh.Exec(ctx, `SET ROLE tremor_api`); err != nil {
			t.Fatal(err)
		}
		if err = fresh.QueryRow(ctx, `SELECT setting::integer FROM pg_catalog.pg_settings WHERE name='statement_timeout'`).Scan(&ms); err != nil || ms != 110000 {
			t.Fatal("SET ROLE incorrectly treated as login")
		}
	})
	for _, tc := range []struct{ name, apply, restore string }{
		{"database-wide-default-drift", `ALTER DATABASE tremor_e1b_collector_test SET statement_timeout='90000ms'`, `ALTER DATABASE tremor_e1b_collector_test RESET statement_timeout`},
		{"role-database-only-default-drift", `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test SET row_security=on`, `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test RESET row_security`},
		{"unexpected-search-path", `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test SET search_path=public`, `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test RESET search_path`},
		{"row-security-disabled", `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test SET row_security=off`, `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test RESET row_security`},
		{"unreviewed-custom-guc", `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test SET test.unreviewed='synthetic'`, `ALTER ROLE tremor_api IN DATABASE tremor_e1b_collector_test RESET test.unreviewed`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, before := reference()
			bound, err := BindCollectedSecurityTransition(a, before)
			if err != nil {
				t.Fatal(err)
			}
			var rolconfigBefore, rolconfigAfter string
			if err = admin.QueryRow(ctx, `SELECT COALESCE(rolconfig::text,'NULL') FROM pg_catalog.pg_roles WHERE rolname='tremor_api'`).Scan(&rolconfigBefore); err != nil {
				t.Fatal(err)
			}
			exec(t, tc.apply)
			defer exec(t, tc.restore)
			if err = admin.QueryRow(ctx, `SELECT COALESCE(rolconfig::text,'NULL') FROM pg_catalog.pg_roles WHERE rolname='tremor_api'`).Scan(&rolconfigAfter); err != nil {
				t.Fatal(err)
			}
			if rolconfigBefore != rolconfigAfter {
				t.Fatal("database-specific test unexpectedly changed role-wide config")
			}
			observed, err := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
			if err != nil {
				if observed.valid() {
					t.Fatal("failed collection has usable seal")
				}
				return
			}
			if reflect.DeepEqual(before.snapshot, observed.snapshot) {
				t.Fatal("database defaults absent from security evidence")
			}
			if _, err = BindCollectedSecurityTransition(a, observed); err == nil {
				t.Fatal("unapproved defaults bound")
			}
			if ClassifyCollectedTransitionRecovery(bound, observed) != TransitionEvidenceRejected {
				t.Fatal("drift admitted by recovery")
			}
		})
	}
	t.Run("other-database-default-excluded", func(t *testing.T) {
		a, before := reference()
		exec(t, `CREATE DATABASE tremor_e1b_unrelated_test`)
		defer exec(t, `DROP DATABASE tremor_e1b_unrelated_test`)
		exec(t, `ALTER ROLE tremor_api IN DATABASE tremor_e1b_unrelated_test SET row_security=off`)
		after, err := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.snapshot, after.snapshot) {
			t.Fatal("unrelated database polluted target defaults")
		}
	})
}
