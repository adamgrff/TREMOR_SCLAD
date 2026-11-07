package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func extensionBase(t *testing.T) *Manifest {
	t.Helper()
	m, err := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func extensionFixture(t *testing.T, base *Manifest, readiness string, count int) (string, ExtensionPlan) {
	t.Helper()
	dir := t.TempDir()
	p := ExtensionPlan{Format: 1, ReadinessFormat: ExtensionReadinessFormat, Profile: ExtensionProfile, Canonicalization: Canon, BaselineSHA256: baselineDigest(base), BaselineReadinessSHA256: readiness, Migrations: []ExtensionEntry{}}
	for i := 0; i < count; i++ {
		name := []string{"025_probe.sql", "026_probe.sql"}[i]
		sql := []byte("BEGIN;\nSELECT 1;\nCOMMIT;\n")
		if err := os.WriteFile(filepath.Join(dir, name), sql, 0600); err != nil {
			t.Fatal(err)
		}
		p.Migrations = append(p.Migrations, ExtensionEntry{Entry{25 + i, name, digest(sql), i + 1}, readiness})
	}
	path := filepath.Join(dir, "manifest.json")
	writeExtensionPlan(t, path, p)
	return path, p
}
func writeExtensionPlan(t *testing.T, path string, p ExtensionPlan) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionManifest(t *testing.T) {
	base := extensionBase(t)
	path, plan := extensionFixture(t, base, strings.Repeat("a", 64), 2)
	m, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	p := m.Plan()
	p.Migrations[0].Name = "changed"
	if m.Plan().Migrations[0].Name == "changed" {
		t.Fatal("mutable manifest")
	}
	original := m.bodies[0]
	if err = os.WriteFile(filepath.Join(filepath.Dir(path), plan.Migrations[0].Name), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if m.bodies[0] != original {
		t.Fatal("SQL snapshot changed")
	}
	if _, err = LoadExtension(path, base); err == nil {
		t.Fatal("SQL drift accepted")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ExtensionPlan)
	}{
		{"baseline", func(p *ExtensionPlan) { p.BaselineSHA256 = strings.Repeat("0", 64) }},
		{"profile", func(p *ExtensionPlan) { p.Profile = "other" }},
		{"format", func(p *ExtensionPlan) { p.Format = 2 }},
		{"readiness_format", func(p *ExtensionPlan) { p.ReadinessFormat = "old-or-unknown" }},
		{"canonicalization", func(p *ExtensionPlan) { p.Canonicalization = "other" }},
		{"readiness", func(p *ExtensionPlan) { p.BaselineReadinessSHA256 = "" }},
		{"null_chain", func(p *ExtensionPlan) { p.Migrations = nil }},
		{"version", func(p *ExtensionPlan) { p.Migrations[0].Version = 24 }},
		{"duplicate", func(p *ExtensionPlan) { p.Migrations[1] = p.Migrations[0] }},
		{"order", func(p *ExtensionPlan) { p.Migrations[0].Order = 2 }},
		{"path", func(p *ExtensionPlan) { p.Migrations[0].Name = "../025_probe.sql" }},
		{"short_name", func(p *ExtensionPlan) { p.Migrations[0].Name = "x" }},
		{"checksum", func(p *ExtensionPlan) { p.Migrations[0].SHA256 = strings.Repeat("0", 64) }},
		{"step_readiness", func(p *ExtensionPlan) { p.Migrations[0].ReadinessSHA256 = "invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, p := extensionFixture(t, base, strings.Repeat("a", 64), 2)
			tc.mutate(&p)
			writeExtensionPlan(t, path, p)
			if _, err := LoadExtension(path, base); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	path, _ = extensionFixture(t, base, strings.Repeat("a", 64), 0)
	if _, err = LoadExtension(path, base); err != nil {
		t.Fatal("empty extension plan", err)
	}
	if _, err = LoadExtension(path, repositoryManifest(t)); err == nil {
		t.Fatal("v1 baseline accepted")
	}
	if err = os.WriteFile(path, []byte(`{"unknown":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadExtension(path, base); err == nil {
		t.Fatal("unknown JSON accepted")
	}
}

func TestExtensionPrefix(t *testing.T) {
	base := extensionBase(t)
	path, _ := extensionFixture(t, base, strings.Repeat("a", 64), 2)
	m, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	rows := []Applied{{Entry: m.plan.Migrations[0].Entry, Profile: m.chains[0]}, {Entry: m.plan.Migrations[1].Entry, Profile: m.chains[1]}}
	for i := 0; i <= 2; i++ {
		if err = m.prefix(rows[:i]); err != nil {
			t.Fatal("valid resume prefix", i, err)
		}
	}
	for _, bad := range [][]Applied{{rows[1]}, {rows[0], rows[0]}, append(append([]Applied{}, rows...), rows[1])} {
		if !errors.Is(m.prefix(bad), ErrUnsafe) {
			t.Fatal("conflict accepted")
		}
	}
	changed := rows[0]
	changed.SHA256 = strings.Repeat("b", 64)
	if m.prefix([]Applied{changed}) == nil {
		t.Fatal("checksum drift accepted")
	}
	p := m.Plan()
	p.Migrations[0].ReadinessSHA256 = strings.Repeat("b", 64)
	writeExtensionPlan(t, path, p)
	other, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if other.prefix(rows) == nil {
		t.Fatal("readiness drift accepted")
	}
	path, p = extensionFixture(t, base, strings.Repeat("a", 64), 2)
	p.Migrations = p.Migrations[:1]
	writeExtensionPlan(t, path, p)
	short, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if short.chains[0] != m.chains[0] {
		t.Fatal("append rewrites prefix")
	}
}

func TestExtensionOptions(t *testing.T) {
	base := extensionBase(t)
	path, _ := extensionFixture(t, base, strings.Repeat("a", 64), 0)
	m, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Host: "127.0.0.1", Port: 5432, Database: "tremor_extension_test", User: "deployer", Profile: IdentityProfile, Schema: "public", ServerAddress: "127.0.0.1", ServerID: "123", SSLMode: "disable"}
	options := ExtensionOptions{DeploymentID: "11111111-1111-4111-8111-111111111111", BaselineOwner: "deployer", LedgerOwner: "deployer"}
	if _, err = NewExtension(target, m, options); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tremor_sklad", "TREMOR_SKLAD", "postgres", "template1"} {
		bad := target
		bad.Database = name
		if _, err = NewExtension(bad, m, options); err == nil {
			t.Fatal("protected target accepted")
		}
	}
	bad := options
	bad.DeploymentID = ""
	if _, err = NewExtension(target, m, bad); err == nil {
		t.Fatal("missing deployment accepted")
	}
	bad = options
	bad.LedgerOwner = "other"
	if _, err = NewExtension(target, m, bad); err == nil {
		t.Fatal("unexpected execution owner accepted")
	}
	target.User = "tremor_api"
	options.LedgerOwner = "tremor_api"
	if _, err = NewExtension(target, m, options); err == nil {
		t.Fatal("runtime DDL credentials accepted")
	}
}

func TestExtensionApprovalAndSQLContracts(t *testing.T) {
	base := extensionBase(t)
	for _, sql := range []string{"SELECT 1;", "BEGIN; SELECT 1; COMMIT; COMMIT;", "BEGIN; DROP TABLE products; COMMIT;", "BEGIN; TRUNCATE products; COMMIT;", "BEGIN; SET ROLE tremor_owner; COMMIT;"} {
		path, p := extensionFixture(t, base, strings.Repeat("a", 64), 1)
		p.Migrations[0].SHA256 = digest([]byte(sql))
		writeExtensionPlan(t, path, p)
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), p.Migrations[0].Name), []byte(sql), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadExtension(path, base); err == nil {
			t.Fatalf("unsafe envelope accepted: %q", sql)
		}
	}
	path, p := extensionFixture(t, base, strings.Repeat("a", 64), 1)
	m, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	before := m.ManifestSHA256()
	p.Migrations[0].ReadinessSHA256 = strings.Repeat("b", 64)
	writeExtensionPlan(t, path, p)
	changed, err := LoadExtension(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if before == changed.ManifestSHA256() {
		t.Fatal("manifest approval does not bind readiness")
	}
	target := Target{Host: "127.0.0.1", Port: 5432, Database: "tremor_extension_test", User: "deployer", Profile: IdentityProfile, Schema: "public", ServerAddress: "127.0.0.1", ServerID: "123", SSLMode: "disable"}
	options := ExtensionOptions{DeploymentID: "11111111-1111-4111-8111-111111111111", BaselineOwner: "deployer", LedgerOwner: "deployer"}
	runner, err := NewExtension(target, changed, options)
	if err != nil {
		t.Fatal(err)
	}
	// Neither obtaining a new hash nor changing it in an external approval file
	// can authorize apply in this reviewed executable. Guard regressions cannot
	// connect: the target is deliberately protected after constructor validation.
	runner.base.target.Database = "tremor_sklad"
	if _, err = runner.Run(context.Background(), "apply"); !errors.Is(err, ErrExtensionUnapproved) {
		t.Fatalf("unapproved apply: %v", err)
	}
	if approvedHash(changed.ManifestSHA256(), []string{before}) {
		t.Fatal("changed manifest accepted by old approval")
	}
	if releaseApproves(changed.ManifestSHA256()) {
		t.Fatal("fixture approved in release")
	}
}

// Only test binaries have this fixture approval entry point. Production exposes
// no method/flag/environment input for replacing the compiled release list.
func fixtureExtensionRunner(t *testing.T, target Target, m *ExtensionManifest, options ExtensionOptions) *ExtensionRunner {
	t.Helper()
	r, err := NewExtension(target, m, options)
	if err != nil {
		t.Fatal(err)
	}
	r.approved = approvedHash(m.ManifestSHA256(), []string{m.ManifestSHA256()})
	return r
}
