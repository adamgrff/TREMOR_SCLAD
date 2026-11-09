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

func TestParameterPrivilegesValidation(t *testing.T) {
	d, _, source := transitionFixture(t)
	i := transitionTestIndex(source.Metadata, "role:tremor_api")
	for _, tc := range []struct {
		name   string
		change func(*TransitionSnapshot)
	}{
		{"missing", func(s *TransitionSnapshot) { s.Metadata[i].ParameterPrivileges = nil }},
		{"required-parameter-omitted", func(s *TransitionSnapshot) { s.Metadata[i].ParameterPrivileges = s.Metadata[i].ParameterPrivileges[:2] }},
		{"duplicate", func(s *TransitionSnapshot) {
			s.Metadata[i].ParameterPrivileges[1] = s.Metadata[i].ParameterPrivileges[0]
		}},
		{"unknown-parameter", func(s *TransitionSnapshot) { s.Metadata[i].ParameterPrivileges[2].Name = "unreviewed_setting" }},
		{"intrinsic-not-delegated", func(s *TransitionSnapshot) { s.Metadata[i].ParameterPrivileges[0].Set = true }},
		{"wrong-USERSET-context", func(s *TransitionSnapshot) { s.Metadata[i].ParameterPrivileges[0].Context = "superuser" }},
		{"unknown-grantor", func(s *TransitionSnapshot) {
			s.Metadata[i].ParameterPrivileges[2].ACL.Grants = []TransitionGrant{{"missing", "tremor_api", "SET", false}}
		}},
		{"unknown-grantee", func(s *TransitionSnapshot) {
			s.Metadata[i].ParameterPrivileges[2].ACL.Grants = []TransitionGrant{{"old_owner", "missing", "SET", false}}
		}},
		{"duplicate-grant", func(s *TransitionSnapshot) {
			s.Metadata[i].ParameterPrivileges[2].ACL.Grants = []TransitionGrant{{"old_owner", "tremor_api", "SET", false}, {"old_owner", "tremor_api", "SET", false}}
		}},
		{"contradictory-effective", func(s *TransitionSnapshot) { s.Metadata[i].ParameterPrivileges[2].AlterSystem = true }},
		{"unexpected-grant-option", func(s *TransitionSnapshot) { s.Metadata[i].ParameterPrivileges[2].SetGrantOption = true }},
		{"missing-physical-inventory", func(s *TransitionSnapshot) { s.ParameterIdentities = nil }},
		{"unexpected-physical-identity", func(s *TransitionSnapshot) { s.ParameterIdentities[0].OID = 999 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := transitionClone(source)
			tc.change(&s)
			if validateTransitionSnapshot(s, d.SourceScope) == nil {
				t.Fatal("invalid parameter evidence accepted")
			}
		})
	}
	if err := validateTransitionSnapshot(source, d.SourceScope); err != nil {
		t.Fatal(err)
	}
}

func TestParameterPrivilegesBindingRecovery(t *testing.T) {
	d, p, source := transitionFixture(t)
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
		t.Fatal("complete final evidence refused")
	}
	i := transitionTestIndex(final.Metadata, "role:tremor_api")
	final.Metadata[i].ParameterPrivileges = nil
	if _, err = BindSecurityTransition(a, d.Target, final); err == nil {
		t.Fatal("incomplete binding")
	}
	if ClassifySecurityTransition(bound, TransitionObservation{Snapshot: &final, Receipt: &receipt, CommitUncertain: true}) != TransitionRejected {
		t.Fatal("incomplete parameter evidence reached PROTECTED")
	}
	d.Before[i].ParameterPrivileges = nil
	d.After[i].ParameterPrivileges = nil
	if validateTransitionDescriptor(d) == nil {
		t.Fatal("identically incomplete source/final accepted")
	}
	if ClassifyTransitionRecovery(bound, nil, nil) != TransitionOutcomeUnknown {
		t.Fatal("unreadable observation confirmed")
	}
}

func TestParameterPrivilegesJSON(t *testing.T) {
	d, p, _ := transitionFixture(t)
	raw, _ := json.Marshal(d)
	for _, bad := range []string{
		strings.Replace(string(raw), `"name":"row_security"`, `"name":"row_security","extra":true`, 1),
		strings.Replace(string(raw), `"name":"row_security"`, `"name":"row_security","name":"row_security"`, 1),
		strings.Replace(string(raw), `"set":false`, `"set":"false"`, 1),
		strings.Replace(string(raw), `"parameter_privileges":[`, `"parameter_privileges":null,"discarded":[`, 1),
	} {
		if _, _, err := DecodeSecurityTransition([]byte(bad), p); err == nil {
			t.Fatal("malformed parameter JSON accepted")
		}
	}
}

type parameterRows struct {
	pgx.Rows
	values  [][]any
	at      int
	failure error
}

func (r *parameterRows) Next() bool { r.at++; return r.at <= len(r.values) }
func (r *parameterRows) Close()     {}
func (r *parameterRows) Err() error { return r.failure }
func (r *parameterRows) Scan(dest ...any) error {
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = r.values[r.at-1][i].(string)
		case *bool:
			*p = r.values[r.at-1][i].(bool)
		}
	}
	return nil
}

type parameterQueries struct {
	queryer
	catalog, effective   [][]any
	failure, readFailure error
}

func (q parameterQueries) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if q.failure != nil {
		return nil, q.failure
	}
	v := q.effective
	if sql == securityParameterCatalogSQL {
		v = q.catalog
	}
	return &parameterRows{values: v, failure: q.readFailure}, nil
}
func TestParameterPrivilegesIncompleteRead(t *testing.T) {
	_, _, s := transitionFixture(t)
	catalog := [][]any{}
	for _, p := range defaultParameterPrivileges("old_owner", false) {
		raw, _ := json.Marshal(map[string]any{"oid": 0, "name": p.Name, "context": p.Context, "owner": "old_owner", "present": false, "acl_mode": "default", "grants": []TransitionGrant{}})
		catalog = append(catalog, []any{string(raw)})
	}
	effective := [][]any{}
	for _, role := range s.Metadata {
		if role.Kind == "role" {
			for _, p := range role.ParameterPrivileges {
				effective = append(effective, []any{strings.TrimPrefix(role.Key, "role:"), p.Name, false, false, false, false})
			}
		}
	}
	for _, tc := range []struct {
		name string
		q    parameterQueries
	}{
		{"query-denied", parameterQueries{failure: errors.New("TEST denied")}},
		{"partial-catalog-read", parameterQueries{catalog: catalog, readFailure: errors.New("TEST incomplete")}},
		{"empty-catalog-not-absence", parameterQueries{}},
		{"required-row-missing", parameterQueries{catalog: catalog[:2], effective: effective}},
		{"effective-row-missing", parameterQueries{catalog: catalog, effective: effective[1:]}},
		{"duplicate-effective", parameterQueries{catalog: catalog, effective: append(append([][]any{}, effective...), effective[0])}},
		{"unknown-effective-role", parameterQueries{catalog: catalog, effective: [][]any{{"missing", "row_security", false, false, false, false}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := transitionClone(s)
			if collectParameterPrivileges(context.Background(), tc.q, &copy, transitionTestScope(copy.Metadata, copy.Target)) == nil {
				t.Fatal("partial parameter collection accepted")
			}
		})
	}
	if err := collectParameterPrivileges(context.Background(), parameterQueries{catalog: catalog, effective: effective}, &s, transitionTestScope(s.Metadata, s.Target)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.ParameterIdentities, defaultParameterIdentities()) {
		t.Fatal("absence identities not explicit")
	}
}

func TestParameterPrivilegesApprovedDelta(t *testing.T) {
	d, p, source := transitionFixture(t)
	grants := []TransitionGrant{{"old_owner", "old_owner", "ALTER SYSTEM", false}, {"old_owner", "old_owner", "SET", false}, {"old_owner", "tremor_api", "SET", false}}
	for i, m := range d.After {
		if m.Kind == "role" {
			x := &d.After[i].ParameterPrivileges[0]
			x.Present = true
			x.ACL = TransitionACL{"explicit", grants}
			x.Set = m.Key == "role:old_owner" || m.Key == "role:tremor_api"
			x.AlterSystem = m.Key == "role:old_owner"
			x.SetGrantOption = m.Key == "role:old_owner"
			x.AlterSystemGrantOption = m.Key == "role:old_owner"
		}
	}
	transitionTestRebuildPolicy(&d, &source)
	a := transitionApproveFixture(t, d, p)
	bound, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	final := transitionClone(source)
	final.Metadata = transitionClone(d.After)
	final.ReadinessSHA = d.FinalReadinessSHA
	final.ParameterIdentities[0].OID = 3333
	transitionTestFinalEvidence(&final, d)
	receipt := bound.ExpectedReceipt()
	if ClassifyTransitionRecovery(bound, &final, &receipt) != TransitionFinalConfirmed {
		t.Fatal("approved parameter ACL delta refused")
	}
	beforeSHA := transitionDigest(final.Metadata)
	final.Metadata[transitionTestIndex(final.Metadata, "role:tremor_api")].ParameterPrivileges[0].SetGrantOption = true
	if transitionDigest(final.Metadata) == beforeSHA {
		t.Fatal("parameter permission missing from metadata digest")
	}
	if ClassifyTransitionRecovery(bound, &final, &receipt) != TransitionEvidenceRejected {
		t.Fatal("unapproved parameter option confirmed")
	}
	if preservedParameterIdentities([]TransitionParameterIdentity{{"row_security", 123}}, []TransitionParameterIdentity{{"row_security", 456}}) {
		t.Fatal("existing parameter OID replacement accepted")
	}
}

func TestParameterPrivilegesQueryCoverage(t *testing.T) {
	if !strings.Contains(securityParameterCatalogSQL, "pg_catalog.pg_parameter_acl") || !strings.Contains(securityParameterEffectiveSQL, "has_parameter_privilege") {
		t.Fatal("parameter privilege coverage lost")
	}
	for _, sql := range []string{securityParameterCatalogSQL, securityParameterEffectiveSQL} {
		if !strings.HasPrefix(sql, "SELECT") && !strings.HasPrefix(sql, "WITH") {
			t.Fatal("non-read-only parameter query")
		}
	}
}

func TestParameterPrivilegesScope(t *testing.T) {
	d, _, s := transitionFixture(t)
	for _, names := range [][]string{nil, {}, {"row_security", "search_path"}, {"row_security", "row_security", "session_replication_role"}, {"row_security", "search_path", "session_replication_role", "unknown_parameter"}} {
		scope := transitionClone(d.SourceScope)
		scope.Parameters = names
		if validateTransitionScope(scope, s.Target, s.Metadata) == nil {
			t.Fatal("missing or substituted parameter scope accepted")
		}
	}
	extra := transitionClone(d.SourceScope)
	extra.Parameters = append(extra.Parameters, "statement_timeout")
	if validateTransitionScope(extra, s.Target, s.Metadata) == nil {
		t.Fatal("missing approved optional parameter accepted")
	}
}
