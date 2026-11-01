package migrations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const Profile = "clean-v1"
const IdentityProfile = "clean-v2"
const Canon = "utf8-lf-v1"

type Entry struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	Order   int    `json:"order"`
}
type Omission struct {
	Version int    `json:"version"`
	Name    string `json:"name,omitempty"`
	Reason  string `json:"reason"`
}
type Plan struct {
	Format           int        `json:"format"`
	Profile          string     `json:"profile"`
	Canonicalization string     `json:"canonicalization"`
	Migrations       []Entry    `json:"migrations"`
	Excluded         []Omission `json:"excluded"`
	Gaps             []Omission `json:"gaps"`
}
type migration struct {
	Entry
	body string
}
type Manifest struct {
	plan       Plan
	migrations []migration
}

var schemaNames = []string{
	"001_init.sql", "003_receiving_placements.sql", "004_receiving_sessions.sql", "005_receiving_drafts.sql",
	"007_receiving_history_deletion.sql", "008_product_catalog.sql", "009_receiving_quantity_requests.sql",
	"010_rack_cell_codes.sql", "011_assembly_preview_drafts.sql", "012_assembly_problem_storage.sql",
	"013_assembly_problem_context.sql", "014_assembly_stock_checks.sql", "015_assembly_warehouse_operations.sql",
	"016_assembly_sku_mappings.sql", "017_assembly_ozon_operations.sql", "018_assembly_disassembly.sql",
	"019_assembly_disassembly_return_reversals.sql", "020_receiving_intake_events.sql", "021_assembly_lifecycle_events.sql",
	"022_receiving_operation_receipts.sql", "023_receiving_receipts_immutable.sql",
}

func Load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read migration manifest")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan Plan
	if decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid migration manifest JSON")
	}
	names := append([]string(nil), schemaNames...)
	if plan.Profile == IdentityProfile {
		names = append(names, "024_deployment_identity.sql")
	}
	if plan.Format != 1 || (plan.Profile != Profile && plan.Profile != IdentityProfile) || plan.Canonicalization != Canon || len(plan.Migrations) != len(names) {
		return nil, errors.New("unsupported manifest/profile/canonicalization")
	}
	if len(plan.Excluded) != 1 || plan.Excluded[0].Version != 2 || plan.Excluded[0].Name != "002_seed.sql" || plan.Excluded[0].Reason != "demo" || len(plan.Gaps) != 1 || plan.Gaps[0].Version != 6 || plan.Gaps[0].Name != "" || plan.Gaps[0].Reason != "historical-gap" {
		return nil, errors.New("manifest must explicitly exclude demo 002 and historic gap 006")
	}
	m := &Manifest{plan: plan}
	for i, entry := range plan.Migrations {
		var version int
		_, _ = fmt.Sscanf(names[i], "%d_", &version)
		if entry.Name != names[i] || entry.Version != version || entry.Order != i+1 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(entry.SHA256) {
			return nil, errors.New("manifest name/version/order/checksum is invalid")
		}
		input, err := os.ReadFile(filepath.Join(filepath.Dir(path), entry.Name))
		if err != nil {
			return nil, fmt.Errorf("cannot read migration %03d", entry.Version)
		}
		canonical, err := CanonicalSQL(input)
		if err != nil {
			return nil, fmt.Errorf("invalid encoding in migration %03d", entry.Version)
		}
		digest := sha256.Sum256(canonical)
		if hex.EncodeToString(digest[:]) != entry.SHA256 {
			return nil, fmt.Errorf("checksum mismatch in migration %03d", entry.Version)
		}
		body, err := MigrationBody(canonical)
		if err != nil {
			return nil, fmt.Errorf("unsafe transaction envelope in migration %03d: %w", entry.Version, err)
		}
		m.migrations = append(m.migrations, migration{entry, body})
	}
	return m, nil
}

// Plan returns copies: callers cannot alter the validated SQL snapshot.
func (m *Manifest) Plan() Plan {
	p := m.plan
	p.Migrations = append([]Entry(nil), p.Migrations...)
	p.Excluded = append([]Omission(nil), p.Excluded...)
	p.Gaps = append([]Omission(nil), p.Gaps...)
	return p
}
