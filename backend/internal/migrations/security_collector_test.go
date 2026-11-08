package migrations

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSecurityCollectorOfflineGuards(t *testing.T) {
	var zero CollectedSecuritySnapshot
	if _, err := zero.Snapshot(); err == nil {
		t.Fatal("zero collection admitted")
	}
	if _, err := zero.Privileges(); err == nil {
		t.Fatal("zero privilege collection admitted")
	}
	if _, err := BindCollectedSecurityTransition(ApprovedSecurityTransition{}, zero); err == nil {
		t.Fatal("zero bound")
	}
	if got := ClassifyCollectedTransitionRecovery(BoundSecurityTransition{}, zero); got != TransitionOutcomeUnknown {
		t.Fatal(got)
	}
	d, payload, s := transitionFixture(t)
	a := transitionApproveFixture(t, d, payload)
	// Invalid arguments reject before a connection attempt.
	target := Target{Database: "tremor_sklad"}
	if _, err := CollectSecuritySnapshot(context.Background(), target, a, CollectTransitionSource, nil); err == nil {
		t.Fatal("protected target accepted")
	}
	if _, err := CollectSecuritySnapshot(context.Background(), target, a, "operator-phase", nil); err == nil {
		t.Fatal("unknown phase accepted")
	}
	matching := Target{Database: d.Target.Database, Schema: d.Target.Schema, ServerID: d.Target.ClusterID, Profile: IdentityProfile}
	if _, err := CollectSecuritySnapshot(context.Background(), matching, a, CollectTransitionSource, &ExtensionManifest{}); err == nil {
		t.Fatal("zero manifest admitted")
	}
	c := CollectedSecuritySnapshot{snapshot: s, privileges: []CollectedPrivilege{}, paths: []CollectedRolePath{}}
	c.seal = transitionDigest([]any{c.snapshot, c.receipt, c.privileges, c.paths})
	dto, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	dto.Metadata[0].Owner = "tampered"
	again, err := c.Snapshot()
	if err != nil || !reflect.DeepEqual(again, s) {
		t.Fatal("snapshot accessor aliases sealed data")
	}
	c.snapshot.IdentitySHA = digest([]byte("tampered"))
	if c.valid() {
		t.Fatal("altered observation kept provenance")
	}
}

func TestSecurityCollectorCatalogContracts(t *testing.T) {
	for _, tc := range []struct {
		config []string
		want   []string
		bad    bool
	}{
		{[]string{}, []string{}, false}, {[]string{"search_path=pg_catalog, pg_temp"}, []string{"pg_catalog", "pg_temp"}, false},
		{[]string{"search_path=\"user schema\", pg_temp"}, nil, true}, {[]string{"search_path=public", "search_path=pg_catalog"}, nil, true},
	} {
		got, err := securityFunctionPath(tc.config)
		if (err != nil) != tc.bad || !tc.bad && !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("path %v: %v %v", tc.config, got, err)
		}
	}
	for _, sql := range []string{securityRoleSQL, securitySchemaSQL, securityRelationSQL, securityColumnSQL, securityFunctionSQL, securityDefaultACLSQL, securityCoverageSQL, securityEffectiveSQL, securityRolePathsSQL} {
		if !strings.HasPrefix(strings.TrimSpace(sql), "SELECT") && !strings.HasPrefix(sql, "WITH") {
			t.Fatal("non-read-only catalog statement")
		}
	}
	// NULL ACL and PUBLIC must be represented explicitly; effective privileges use
	// server inquiries, never an inference from an empty raw ACL.
	if !strings.Contains(securityFunctionSQL, "acldefault") || !strings.Contains(securityACLGrantsSQL, "PUBLIC") || !strings.Contains(securityEffectiveSQL, "has_column_privilege") {
		t.Fatal("ACL semantics missing")
	}
}
