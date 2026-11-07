package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExtensionJSONDuplicates(t *testing.T) {
	for _, raw := range []string{
		`{"format":1,"format":1}`, `{"format":1,"FORMAT":1}`,
		`{"migrations":[{"version":25,"version":26}]}`,
		`{"a":{"b":{"checksum":"a","checksum":"b"}}}`,
		`{"a":1,"\u0061":2}`, `{"checksum":"a","chec\u212asum":"b"}`, `{"a":1} {"b":2}`,
	} {
		var value any
		if strictJSON([]byte(raw), &value) == nil {
			t.Fatalf("ambiguous JSON accepted: %s", raw)
		}
	}
	var value any
	if strictJSON([]byte(`{"a":[{"x":1},{"x":2}],"b":{"x":3}}`), &value) != nil {
		t.Fatal("distinct nested objects rejected")
	}
	var typed struct {
		Format int `json:"format"`
	}
	if strictJSON([]byte(`{"format":1,"unknown":2}`), &typed) == nil {
		t.Fatal("unknown field accepted")
	}
}
func snapshotFixture() extensionLedgerSnapshot {
	row := Applied{Entry: Entry{Version: 1, Name: "001_init.sql", SHA256: strings.Repeat("a", 64), Order: 1}, Profile: IdentityProfile, AppliedAt: time.Unix(1700000000, 123000).UTC()}
	ext := row
	ext.Version = 25
	ext.Name = "025_probe.sql"
	ext.Profile = strings.Repeat("b", 64)
	return extensionLedgerSnapshot{Baseline: []Applied{row}, Extensions: []Applied{ext}, BaselineOID: 41, ExtensionOID: 42}
}
func copySnapshot(s extensionLedgerSnapshot) extensionLedgerSnapshot {
	s.Baseline = append([]Applied{}, s.Baseline...)
	s.Extensions = append([]Applied{}, s.Extensions...)
	return s
}
func TestExtensionSnapshots(t *testing.T) {
	original := snapshotFixture()
	if err := preservedLedgers(original, copySnapshot(original), 0, true); err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"baseline", "extension"} {
		for _, field := range []string{"timestamp", "version", "name", "checksum", "profile", "order", "deleted", "oid", "owner", "acl", "column_acl"} {
			t.Run(family+"/"+field, func(t *testing.T) {
				after := copySnapshot(original)
				rows := after.Baseline
				if family == "extension" {
					rows = after.Extensions
				}
				switch field {
				case "owner":
					if family == "baseline" {
						after.BaselineOwner = "changed"
					} else {
						after.ExtensionOwner = "changed"
					}
				case "acl":
					if family == "baseline" {
						after.BaselineACL = "changed"
					} else {
						after.ExtensionACL = "changed"
					}
				case "column_acl":
					if family == "baseline" {
						after.BaselineColumnACL = "changed"
					} else {
						after.ExtensionColumnACL = "changed"
					}
				case "timestamp":
					rows[0].AppliedAt = rows[0].AppliedAt.Add(time.Microsecond)
				case "version":
					rows[0].Version++
				case "name":
					rows[0].Name = "changed"
				case "checksum":
					rows[0].SHA256 = strings.Repeat("0", 64)
				case "profile":
					rows[0].Profile = "changed"
				case "order":
					rows[0].Order++
				case "deleted":
					if family == "baseline" {
						after.Baseline = nil
					} else {
						after.Extensions = nil
					}
				case "oid":
					if family == "baseline" {
						after.BaselineOID++
					} else {
						after.ExtensionOID++
					}
				}
				if !errors.Is(preservedLedgers(original, after, 0, true), ErrUnsafe) {
					t.Fatal("ledger mutation accepted")
				}
			})
		}
	}
	appended := copySnapshot(original)
	appended.Extensions = append(appended.Extensions, original.Extensions[0])
	appended.Extensions[1].Version = 26
	if err := preservedLedgers(original, appended, 1, true); err != nil {
		t.Fatal("append rejected", err)
	}
	if preservedLedgers(original, appended, 0, true) == nil {
		t.Fatal("unexpected append accepted")
	}
}
func TestExtensionCHECKDefinitions(t *testing.T) {
	valid := []extensionCheck{{"ledger_version_positive", "c", " ( version > 24 ) "}, {"ledger_order_positive", "c", "(ordinal > 0)"}, {"ledger_checksum_hex", "c", "(checksum ~ '^[0-9a-f]{64}$'::text)"}, {"ledger_profile_clean", "c", "(profile ~ '^[0-9a-f]{64}$'::text)"}}
	if err := validateExtensionChecks(valid); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"same_expression", "wrong_type", "duplicate_name", "missing", "extra"} {
		t.Run(name, func(t *testing.T) {
			bad := append([]extensionCheck{}, valid...)
			switch name {
			case "same_expression":
				for i := range bad {
					bad[i].Expression = bad[0].Expression
				}
			case "wrong_type":
				bad[0].Type = "u"
			case "duplicate_name":
				bad[1].Name = bad[0].Name
			case "missing":
				bad = bad[:3]
			case "extra":
				bad = append(bad, bad[0])
			}
			if validateExtensionChecks(bad) == nil {
				t.Fatal("invalid CHECK set accepted")
			}
		})
	}
}
func TestExtensionRecoveryFaults(t *testing.T) {
	expected := snapshotFixture()
	for _, name := range []string{"committed_reply_lost", "absent", "reconnect_failed", "identity_failed", "readiness_failed", "timestamp_changed", "pending"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			state, err := recoverExtensionCommit(expected, func() (ExtensionState, extensionLedgerSnapshot, error) {
				calls++
				actual := copySnapshot(expected)
				s := ExtensionState{Applied: actual.Extensions}
				switch name {
				case "absent":
					actual.Extensions = nil
				case "reconnect_failed":
					return s, actual, errors.New("transport unavailable")
				case "identity_failed":
					return s, actual, fmt.Errorf("%w: deployment identity", ErrUnsafe)
				case "readiness_failed":
					return s, actual, fmt.Errorf("%w: readiness", ErrUnsafe)
				case "timestamp_changed":
					actual.Extensions[0].AppliedAt = actual.Extensions[0].AppliedAt.Add(time.Second)
				case "pending":
					s.Pending = []ExtensionEntry{{}}
				}
				return s, actual, nil
			})
			if calls != 1 {
				t.Fatal("recovery repeated read/execution")
			}
			switch name {
			case "committed_reply_lost":
				if err != nil || !state.CommitResolved {
					t.Fatalf("%+v %v", state, err)
				}
			case "pending":
				if !errors.Is(err, ErrResume) || !state.CommitResolved {
					t.Fatal(err)
				}
			default:
				if !errors.Is(err, ErrCommitUnknown) || state.CommitResolved {
					t.Fatalf("uncertainty silently succeeded: %+v %v", state, err)
				}
			}
		})
	}
	// Independent concurrent recovery callers only inspect; they cannot execute.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := recoverExtensionCommit(expected, func() (ExtensionState, extensionLedgerSnapshot, error) {
				return ExtensionState{}, copySnapshot(expected), nil
			})
			if err != nil || !s.CommitResolved {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
func TestExtensionRestartPrefix(t *testing.T) {
	base := extensionBase(t)
	path, _ := extensionFixture(t, base, strings.Repeat("a", 64), 2)
	first, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	durable := []Applied{{Entry: first.plan.Migrations[0].Entry, Profile: first.chains[0], AppliedAt: time.Now().UTC()}}
	restarted, err := LoadExtension(path, extensionBase(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.prefix(durable); err != nil {
		t.Fatal("fresh runner cannot resume", err)
	}
	if restarted.plan.Migrations[len(durable)].Version != 26 {
		t.Fatal("restart would repeat 025")
	}
}
func TestExtensionReleaseApproval(t *testing.T) {
	base := extensionBase(t)
	path, p := extensionFixture(t, base, strings.Repeat("a", 64), 0)
	m, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if !approvedHash(m.ManifestSHA256(), []string{m.ManifestSHA256()}) || approvedHash(m.ManifestSHA256(), nil) || approvedHash(m.ManifestSHA256(), []string{strings.Repeat("0", 64)}) {
		t.Fatal("allowlist matching")
	}
	if approvedHash(m.ManifestSHA256(), []string{m.ManifestSHA256(), "invalid"}) || approvedHash(m.ManifestSHA256(), []string{m.ManifestSHA256(), m.ManifestSHA256()}) {
		t.Fatal("malformed release allowlist accepted")
	}
	if releaseApproves(m.ManifestSHA256()) {
		t.Fatal("test fixture approved in shipped tool")
	}
	p.BaselineReadinessSHA256 = strings.Repeat("b", 64)
	writeExtensionPlan(t, path, p)
	changed, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if approvedHash(changed.ManifestSHA256(), []string{m.ManifestSHA256()}) {
		t.Fatal("manifest substitution accepted")
	}
	// A caller-controlled expected hash/approval file has no production input path.
	fakeApproval := filepath.Join(t.TempDir(), "approvals.json")
	raw, _ := json.Marshal([]string{changed.ManifestSHA256()})
	if err = os.WriteFile(fakeApproval, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if releaseApproves(changed.ManifestSHA256()) {
		t.Fatal("external approval file used")
	}
	target := Target{Host: "127.0.0.1", Port: 5432, Database: "tremor_extension_test", User: "deployer", Profile: IdentityProfile, Schema: "public", ServerAddress: "127.0.0.1", ServerID: "123", SSLMode: "disable"}
	runner, err := NewExtension(target, changed, ExtensionOptions{DeploymentID: "11111111-1111-4111-8111-111111111111", BaselineOwner: "deployer", LedgerOwner: "deployer"})
	if err != nil {
		t.Fatal(err)
	}
	runner.base.target.Database = "tremor_sklad" // refuse connections even if approval guard regresses
	if _, err = runner.Run(context.Background(), "apply"); !errors.Is(err, ErrExtensionUnapproved) {
		t.Fatalf("missing release approval: %v", err)
	}
}

func TestExtensionMetadataContract(t *testing.T) {
	// Offline SQL projection contract, not a substitute for PostgreSQL execution.
	// Each property also has a prepared transaction/rollback integration scenario.
	for _, field := range []string{"a.attacl::text", "unnest(p.polroles)", "k.contypid=t.oid", "t.typowner", "t.typacl::text", "t.typnotnull", "t.typdefaultbin", "c.relowner", "c.relacl::text", "p.proowner", "p.proacl::text", "nspowner", "nspacl::text", "d.defaclrole", "a.attmissingval::text", "a.attoptions", "a.attfdwoptions", "a.attcollation", "c.relreplident", "pg_inherits"} {
		if !strings.Contains(extensionCatalogSQL, field) {
			t.Errorf("security/readiness projection missing %s", field)
		}
	}
	for _, property := range []string{"column_acl", "policy_roles", "domain_constraint", "domain_default", "domain_not_null", "domain_acl", "table_acl", "function_acl", "schema_acl", "owner"} {
		before := []string{`["` + property + `","original"]`, `["unchanged","value"]`}
		reversed := []string{before[1], before[0]}
		if catalogRecordsDigest(before) != catalogRecordsDigest(reversed) {
			t.Fatal("catalogue ordering changes digest")
		}
		after := append([]string{}, before...)
		after[0] = `["` + property + `","changed"]`
		if catalogRecordsDigest(before) == catalogRecordsDigest(after) {
			t.Fatalf("projected change lost: %s", property)
		}
	}
}

// Metadata admission is also fail-closed on inspection errors without PostgreSQL.
type extensionSupportQuery struct {
	extensionBaselineDB
	supported bool
	err       error
}
type extensionErrorRow struct{ err error }

func (r extensionErrorRow) Scan(...any) error { return r.err }
func (q extensionSupportQuery) QueryRow(context.Context, string, ...any) pgx.Row {
	if q.err != nil {
		return extensionErrorRow{q.err}
	}
	return extensionRow{q.supported}
}
func TestExtensionCatalogAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		supported bool
		err       error
		wantError bool
	}{{"supported", true, nil, false}, {"unsupported", false, nil, true}, {"metadata_failure", true, errors.New("catalogue unavailable"), true}} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkExtensionCatalogSupport(context.Background(), extensionSupportQuery{supported: tc.supported, err: tc.err})
			if tc.wantError != errors.Is(err, ErrUnsafe) {
				t.Fatalf("unexpected admission result %v", err)
			}
		})
	}
}
