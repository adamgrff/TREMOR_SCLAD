package migrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"reflect"
	"testing"
)

func testParameterPrivileges(t *testing.T, ctx context.Context, admin *pgx.Conn, cfg *pgx.ConnConfig, target Target, m *ExtensionManifest, reference func() (ApprovedSecurityTransition, CollectedSecuritySnapshot)) {
	exec := func(t *testing.T, sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("parameter-defaults-and-intrinsic-USERSET", func(t *testing.T) {
		a, before := reference()
		again, err := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.snapshot, again.snapshot) {
			t.Fatal("unstable parameter fingerprint")
		}
		apiRole := before.snapshot.Metadata[transitionTestIndex(before.snapshot.Metadata, "role:tremor_api")]
		if len(apiRole.ParameterPrivileges) != len(a.descriptor.SourceScope.Parameters) || len(before.snapshot.ParameterIdentities) != len(a.descriptor.SourceScope.Parameters) {
			t.Fatal("incomplete mandatory parameter coverage")
		}
		for _, p := range apiRole.ParameterPrivileges {
			if p.Present || p.Set || p.AlterSystem || p.SetGrantOption || p.AlterSystemGrantOption {
				t.Fatal("unexpected delegated parameter right")
			}
			if p.IntrinsicUserSet != (p.Name != "session_replication_role") {
				t.Fatal("USERSET conflated with delegated privilege")
			}
		}
		apiCfg := cfg.Copy()
		apiCfg.User = "tremor_api"
		api, err := pgx.ConnectConfig(ctx, apiCfg)
		if err != nil {
			t.Fatal(err)
		}
		defer closeConnection(api)
		if _, err = api.Exec(ctx, `SET row_security=on`); err != nil {
			t.Fatal("intrinsic USERSET denied")
		}
		if _, err = api.Exec(ctx, `SET session_replication_role=replica`); err == nil {
			t.Fatal("privileged SET unexpectedly allowed")
		} else {
			pe, ok := err.(*pgconn.PgError)
			if !ok || pe.Code != "42501" {
				t.Fatal("unexpected SET error class")
			}
		}
	})
	t.Run("parameter-explicit-approved-USERSET-grant", func(t *testing.T) {
		exec(t, `GRANT SET ON PARAMETER row_security TO tremor_api`)
		defer exec(t, `REVOKE SET ON PARAMETER row_security FROM tremor_api`)
		a, collected := reference() // TEST-only independent fixture approval.
		if _, err := BindCollectedSecurityTransition(a, collected); err != nil {
			t.Fatal(err)
		}
		role := collected.snapshot.Metadata[transitionTestIndex(collected.snapshot.Metadata, "role:tremor_api")]
		var p TransitionParameterPrivilege
		for _, candidate := range role.ParameterPrivileges {
			if candidate.Name == "row_security" {
				p = candidate
			}
		}
		if !p.Present || !p.Set || p.SetGrantOption || !p.IntrinsicUserSet {
			t.Fatal("explicit grant/effective/USERSET distinction lost")
		}
	})

	t.Run("parameter-catalog-read-denied", func(t *testing.T) {
		a, before := reference()
		bound, err := BindCollectedSecurityTransition(a, before)
		if err != nil {
			t.Fatal(err)
		}
		observer := target
		observer.User = "collector_observer"
		if _, err = CollectSecuritySnapshot(ctx, observer, a, CollectTransitionSource, m); err != nil {
			t.Fatal("TEST observer prerequisite failed", err)
		}
		exec(t, `REVOKE SELECT ON pg_catalog.pg_parameter_acl FROM PUBLIC`)
		defer exec(t, `GRANT SELECT ON pg_catalog.pg_parameter_acl TO PUBLIC`)
		observed, err := CollectSecuritySnapshot(ctx, observer, a, CollectTransitionSource, m)
		if err == nil || observed.valid() {
			t.Fatal("unreadable parameter catalog became usable empty evidence")
		}
		if ClassifyCollectedTransitionRecovery(bound, observed) != TransitionOutcomeUnknown {
			t.Fatal("unreadable parameter catalog confirmed recovery")
		}
	})
	for _, tc := range []struct {
		name, apply, restore, actor, param, priv string
		unreadable                               bool
	}{
		{"parameter-unexpected-ALTER-SYSTEM", `GRANT ALTER SYSTEM ON PARAMETER search_path TO tremor_api`, `REVOKE ALTER SYSTEM ON PARAMETER search_path FROM tremor_api`, "tremor_api", "search_path", "ALTER SYSTEM", false},
		{"parameter-PUBLIC", `GRANT SET ON PARAMETER session_replication_role TO PUBLIC`, `REVOKE SET ON PARAMETER session_replication_role FROM PUBLIC`, "tremor_api", "session_replication_role", "SET", false},
		{"parameter-inherited", `GRANT SET ON PARAMETER session_replication_role TO collector_group_root`, `REVOKE SET ON PARAMETER session_replication_role FROM collector_group_root`, "collector_reader", "session_replication_role", "SET", false},
		{"parameter-grant-option", `GRANT SET ON PARAMETER row_security TO tremor_api WITH GRANT OPTION`, `REVOKE SET ON PARAMETER row_security FROM tremor_api`, "tremor_api", "row_security", "SET WITH GRANT OPTION", false},
		{"parameter-additional-reviewed-row", `GRANT SET ON PARAMETER statement_timeout TO tremor_api`, `REVOKE SET ON PARAMETER statement_timeout FROM tremor_api`, "tremor_api", "statement_timeout", "SET", false},
		{"parameter-unknown-grantee", `CREATE ROLE parameter_outside_scope NOLOGIN; GRANT SET ON PARAMETER session_replication_role TO parameter_outside_scope`, `REVOKE SET ON PARAMETER session_replication_role FROM parameter_outside_scope; DROP ROLE parameter_outside_scope`, "parameter_outside_scope", "session_replication_role", "SET", true},
		{"parameter-unreviewed-elevated", `GRANT SET ON PARAMETER log_statement TO tremor_api`, `REVOKE SET ON PARAMETER log_statement FROM tremor_api`, "tremor_api", "log_statement", "SET", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, before := reference()
			bound, err := BindCollectedSecurityTransition(a, before)
			if err != nil {
				t.Fatal(err)
			}
			exec(t, tc.apply)
			defer exec(t, tc.restore)
			var effective bool
			if err = admin.QueryRow(ctx, `SELECT pg_catalog.has_parameter_privilege($1,$2,$3)`, tc.actor, tc.param, tc.priv).Scan(&effective); err != nil || !effective {
				t.Fatal("TEST parameter privilege prerequisite failed")
			}
			observed, err := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
			if tc.unreadable {
				if err == nil || observed.valid() {
					t.Fatal("unknown parameter ACL scope accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(before.snapshot, observed.snapshot) {
				t.Fatal("parameter ACL drift invisible")
			}
			changedFingerprint := false
			for i, old := range before.snapshot.Metadata {
				now := observed.snapshot.Metadata[i]
				if !reflect.DeepEqual(old.SessionDefaults, now.SessionDefaults) {
					t.Fatal("parameter grant unexpectedly changed session defaults")
				}
				if old.Kind == "role" && old.DefinitionSHA != now.DefinitionSHA {
					changedFingerprint = true
				}
			}
			if !changedFingerprint {
				t.Fatal("parameter grant absent from role fingerprint")
			}
			if _, err = BindCollectedSecurityTransition(a, observed); err == nil {
				t.Fatal("unapproved parameter GRANT bound")
			}
			if ClassifyCollectedTransitionRecovery(bound, observed) != TransitionEvidenceRejected {
				t.Fatal("parameter drift confirmed by recovery")
			}
		})
	}
}
