package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"reflect"
	"strings"
	"testing"
)

func TestSessionDefaultsValidation(t *testing.T) {
	d, _, source := transitionFixture(t)
	role := source.Metadata[transitionTestIndex(source.Metadata, "role:tremor_api")]
	tests := []struct {
		name   string
		mutate func(*TransitionMetadata)
	}{
		{"missing-layers", func(m *TransitionMetadata) { m.SessionDefaults = nil }},
		{"missing-one-layer", func(m *TransitionMetadata) { m.SessionDefaults = m.SessionDefaults[:3] }},
		{"duplicate-layer", func(m *TransitionMetadata) { m.SessionDefaults[3].Level = "role" }},
		{"unknown-read", func(m *TransitionMetadata) { m.SessionDefaults[3].Settings = nil }},
		{"absent-with-value", func(m *TransitionMetadata) {
			m.SessionDefaults[3].Settings = []TransitionSessionSetting{{"row_security", "on"}}
		}},
		{"replica", func(m *TransitionMetadata) {
			m.SessionDefaults[3] = TransitionSessionLayer{"role_database", true, []TransitionSessionSetting{{"session_replication_role", "replica"}}}
		}},
		{"rls-off", func(m *TransitionMetadata) {
			m.SessionDefaults[3] = TransitionSessionLayer{"role_database", true, []TransitionSessionSetting{{"row_security", "off"}}}
		}},
		{"unsafe-path", func(m *TransitionMetadata) {
			m.SessionDefaults[3] = TransitionSessionLayer{"role_database", true, []TransitionSessionSetting{{"search_path", "public, pg_catalog"}}}
		}},
		{"unknown-guc", func(m *TransitionMetadata) {
			m.SessionDefaults[3] = TransitionSessionLayer{"role_database", true, []TransitionSessionSetting{{"custom.unreviewed", "redacted"}}}
		}},
		{"security-check-disabled", func(m *TransitionMetadata) {
			m.SessionDefaults[3] = TransitionSessionLayer{"role_database", true, []TransitionSessionSetting{{"check_function_bodies", "off"}}}
		}},
		{"duplicate-setting", func(m *TransitionMetadata) {
			m.SessionDefaults[3] = TransitionSessionLayer{"role_database", true, []TransitionSessionSetting{{"row_security", "on"}, {"row_security", "on"}}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := transitionClone(role)
			tc.mutate(&m)
			if validateSessionLayers(m, d.Before) == nil {
				t.Fatal("invalid defaults accepted")
			}
		})
	}
	t.Run("shared-database-contradiction", func(t *testing.T) {
		m := transitionClone(role)
		m.SessionDefaults[1] = TransitionSessionLayer{"database", true, []TransitionSessionSetting{{"row_security", "on"}}}
		if validateSessionLayers(m, d.Before) == nil {
			t.Fatal("contradictory shared defaults accepted")
		}
	})
	t.Run("parse-duplicates-and-malformed", func(t *testing.T) {
		for _, raw := range [][]string{{"row_security"}, {"row_security=on", "row_security=on"}, {"=x"}} {
			if _, err := parseSessionConfig(raw, d.Before); err == nil {
				t.Fatal("invalid config accepted")
			}
		}
	})
	t.Run("catalog-precedence", func(t *testing.T) {
		m := transitionClone(role)
		for i, value := range []string{"120000ms", "110000ms", "100000ms", "90000ms"} {
			m.SessionDefaults[i].Present = true
			m.SessionDefaults[i].Settings = []TransitionSessionSetting{{"statement_timeout", value}}
		}
		got, err := CatalogLoginOverrides(m, []TransitionMetadata{m})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []TransitionSessionSetting{{"statement_timeout", "90000ms"}}) {
			t.Fatal("incorrect catalog precedence")
		}
		if len(m.SessionDefaults) != 4 {
			t.Fatal("overridden layers lost")
		}
	})
}

func TestSessionDefaultsBindingRecovery(t *testing.T) {
	d, p, source := transitionFixture(t)
	a := transitionApproveFixture(t, d, p)
	bound, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	bad := transitionClone(source)
	i := transitionTestIndex(bad.Metadata, "role:tremor_api")
	bad.Metadata[i].SessionDefaults[3] = TransitionSessionLayer{"role_database", true, []TransitionSessionSetting{{"row_security", "on"}}}
	if transitionDigest(bad.Metadata) == transitionDigest(source.Metadata) {
		t.Fatal("setting absent from fingerprint")
	}
	if _, err = BindSecurityTransition(a, d.Target, bad); err == nil {
		t.Fatal("unapproved setting bound")
	}
	// All public paths must refuse evidence that omits a mandatory layer.
	incomplete := transitionClone(source)
	incomplete.Metadata[i].SessionDefaults = nil
	if _, err = BindSecurityTransition(a, d.Target, incomplete); err == nil {
		t.Fatal("incomplete binding")
	}
	final := transitionClone(incomplete)
	final.Metadata = transitionClone(d.After)
	final.Metadata[i].SessionDefaults = nil
	final.ReadinessSHA = d.FinalReadinessSHA
	transitionTestFinalEvidence(&final, d)
	if err = CompareSecurityTransition(bound, final); err == nil {
		t.Fatal("incomplete final accepted")
	}
	if ClassifyTransitionRecovery(bound, &incomplete, nil) != TransitionEvidenceRejected {
		t.Fatal("incomplete recovery accepted")
	}
	receipt := bound.ExpectedReceipt()
	if ClassifyTransitionRecovery(bound, &final, &receipt) != TransitionEvidenceRejected {
		t.Fatal("false final confirmation")
	}
	if ClassifyTransitionRecovery(bound, nil, nil) != TransitionOutcomeUnknown {
		t.Fatal("unreadable evidence not unknown")
	}
	// Even an independently approved digest cannot make structurally incomplete
	// source and final inventories valid.
	d.Before[i].SessionDefaults = nil
	d.After[i].SessionDefaults = nil
	if validateTransitionDescriptor(d) == nil {
		t.Fatal("same incomplete source/final approved")
	}
}

// Query-boundary fault injection exercises impossible catalog references and
// incomplete reads without claiming these fabricated rows originate in PG.
type sessionRows struct {
	pgx.Rows
	values  [][]any
	at      int
	failure error
}

func (r *sessionRows) Next() bool { r.at++; return r.at <= len(r.values) }
func (r *sessionRows) Close()     {}
func (r *sessionRows) Err() error { return r.failure }
func (r *sessionRows) Scan(dest ...any) error {
	for i, d := range dest {
		v := r.values[r.at-1][i]
		switch p := d.(type) {
		case *uint32:
			*p = v.(uint32)
		case *string:
			*p = v.(string)
		case *[]string:
			if v == nil {
				*p = nil
			} else {
				*p = v.([]string)
			}
		}
	}
	return nil
}

type sessionQueries struct {
	queryer
	settings, roles [][]any
	failure         error
}

func (q sessionQueries) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if q.failure != nil {
		return nil, q.failure
	}
	values := q.roles
	if sql == securitySessionDefaultsSQL {
		values = q.settings
	}
	return &sessionRows{values: values}, nil
}
func TestSessionDefaultsCollectorRejectsIncomplete(t *testing.T) {
	_, _, snapshot := transitionFixture(t)
	roleRows := [][]any{}
	for _, o := range snapshot.Metadata {
		if o.Kind == "role" {
			roleRows = append(roleRows, []any{transitionTestIdentityOID(snapshot.ObjectIdentities, o.Key), strings.TrimPrefix(o.Key, "role:"), nil})
		}
	}
	var oid uint32
	for _, r := range roleRows {
		oid = r[0].(uint32)
		break
	}
	tests := []struct {
		name string
		q    sessionQueries
	}{
		{"unknown-role", sessionQueries{settings: [][]any{{uint32(0), uint32(999999), []string{"row_security=on"}}}, roles: roleRows}},
		{"unknown-database", sessionQueries{settings: [][]any{{uint32(999999), oid, []string{"row_security=on"}}}, roles: roleRows}},
		{"duplicate-catalog-row", sessionQueries{settings: [][]any{{uint32(0), oid, []string{"row_security=on"}}, {uint32(0), oid, []string{"row_security=on"}}}, roles: roleRows}},
		{"null-config-unknown", sessionQueries{settings: [][]any{{uint32(0), oid, nil}}, roles: roleRows}},
		{"role-omitted", sessionQueries{roles: roleRows[1:]}},
		{"rolconfig-mismatch", sessionQueries{settings: [][]any{{uint32(0), oid, []string{"row_security=on"}}}, roles: roleRows}},
		{"catalog-unreadable", sessionQueries{failure: errors.New("test inaccessible")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := transitionClone(snapshot)
			if collectSessionDefaults(context.Background(), tc.q, &s) == nil {
				t.Fatal("incomplete/contradictory catalog read accepted")
			}
		})
	}
	if err := collectSessionDefaults(context.Background(), sessionQueries{roles: roleRows}, &snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestSessionDefaultsApprovedTransitionAndJSON(t *testing.T) {
	d, p, source := transitionFixture(t)
	i := transitionTestIndex(d.After, "role:tremor_api")
	d.After[i].SessionDefaults[3] = TransitionSessionLayer{"role_database", true, []TransitionSessionSetting{{"row_security", "on"}}}
	transitionTestRebuildPolicy(&d, &source)
	a := transitionApproveFixture(t, d, p)
	bound, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	final := transitionClone(source)
	final.Metadata = transitionClone(d.After)
	final.ReadinessSHA = d.FinalReadinessSHA
	transitionTestFinalEvidence(&final, d)
	receipt := bound.ExpectedReceipt()
	if ClassifyTransitionRecovery(bound, &final, &receipt) != TransitionFinalConfirmed {
		t.Fatal("approved defaults transition refused")
	}
	final.Metadata[i].SessionDefaults[3].Settings[0].Value = "off"
	if ClassifyTransitionRecovery(bound, &final, &receipt) != TransitionEvidenceRejected {
		t.Fatal("contradictory settings confirmed COMMIT")
	}
	if ClassifySecurityTransition(bound, TransitionObservation{Snapshot: &final, Receipt: &receipt, CommitUncertain: true}) != TransitionRejected {
		t.Fatal("contradictory settings reached PROTECTED")
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"level":"global"`, `"level":"global","unknown":true`, 1),
		strings.Replace(string(raw), `"level":"global"`, `"level":"global","level":"global"`, 1),
		strings.Replace(string(raw), `"present":false`, `"present":"false"`, 1),
		strings.Replace(string(raw), `"settings":[]`, `"settings":null`, 1),
	} {
		if _, _, err = DecodeSecurityTransition([]byte(bad), p); err == nil {
			t.Fatal("malformed defaults JSON accepted")
		}
	}
}
