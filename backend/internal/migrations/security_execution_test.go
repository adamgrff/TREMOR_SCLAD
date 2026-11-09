package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSecurityExecutionAdmission(t *testing.T) {
	d, p, s := transitionFixture(t)
	a := transitionApproveFixture(t, d, p)
	if _, e := LoadApprovedSecurityRoles(d.Target); e == nil {
		t.Fatal("production role approval enabled")
	}
	if _, e := NewSecurityTransition(Target{User: "tremor_api"}, nil, a, p); e == nil {
		t.Fatal("runtime transition admitted")
	}
	b, e := BindSecurityTransition(a, d.Target, s)
	if e != nil {
		t.Fatal(e)
	}
	if e = ApplyProtectedExtension(context.Background(), Target{User: "tremor_migrator"}, nil, b, ApprovedProtectedMigration{}); e == nil {
		t.Fatal("migrator apply admitted")
	}
	if e = VerifyProtectedExtension(context.Background(), Target{User: "tremor_api"}, nil, b, ApprovedProtectedMigration{}); e == nil {
		t.Fatal("runtime verify admitted")
	}
	cp := ProtectedCheckpoint{d.Frontier, d.FinalScope, d.After, d.FinalReadinessSHA}
	plan := ProtectedMigrationPlan{"e1b-protected-apply-v1", a.sha, strings.Repeat("a", 64), cp, cp, ExtensionPlan{Migrations: []ExtensionEntry{}}}
	raw, _ := json.Marshal(plan)
	if _, e = LoadApprovedProtectedMigration(raw); !errors.Is(e, ErrExtensionUnapproved) {
		t.Fatal("production protected approval enabled", e)
	}
	for _, bad := range []string{string(raw) + "{}", strings.Replace(string(raw), `"protocol":`, `"extra":1,"protocol":`, 1), strings.Replace(string(raw), `"protocol":`, `"protocol":"duplicate","protocol":`, 1)} {
		if _, e = approveProtectedMigration([]byte(bad), []string{transitionDigest(plan)}); e == nil {
			t.Fatal("malformed protected approval accepted")
		}
	}
}
func TestFoundationCapabilityGuards(t *testing.T) {
	for _, role := range []string{"tremor_api", "tremor_executor", "tremor_migrator"} {
		for _, priv := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE", "CREATE", "TEMPORARY", "EXECUTE"} {
			c := CollectedSecuritySnapshot{privileges: []CollectedPrivilege{{Actor: role, Privilege: priv, Allowed: true}}}
			if foundationSecurity(c) == nil {
				t.Fatal("unsafe foundation capability admitted", role, priv)
			}
		}
	}
	if foundationSecurity(CollectedSecuritySnapshot{privileges: []CollectedPrivilege{{Actor: "tremor_api", Privilege: "SELECT", Allowed: true}}}) != nil {
		t.Fatal("read allowlist rejected")
	}
	c := CollectedSecuritySnapshot{snapshot: TransitionSnapshot{Metadata: []TransitionMetadata{{Key: "role:tremor_api", ParameterPrivileges: []TransitionParameterPrivilege{{Context: "superuser", Set: true}}}}}}
	if foundationSecurity(c) == nil {
		t.Fatal("privileged parameter admitted")
	}
}
