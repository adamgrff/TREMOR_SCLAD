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

const ExtensionProfile = "db04-extension-v1"
const ExtensionReadinessFormat = "pg18-catalog-v2"
const extensionLedgerName = "schema_extensions"

type ExtensionEntry struct {
	Entry
	ReadinessSHA256 string `json:"readiness_sha256"`
}

// ExtensionPlan is a reviewed, append-only chain. Catalog fingerprints describe
// schema, never business rows. No business migration ships with E1A.
type ExtensionPlan struct {
	Format                  int              `json:"format"`
	ReadinessFormat         string           `json:"readiness_format"`
	Profile                 string           `json:"profile"`
	Canonicalization        string           `json:"canonicalization"`
	BaselineSHA256          string           `json:"baseline_sha256"`
	BaselineReadinessSHA256 string           `json:"baseline_readiness_sha256"`
	Migrations              []ExtensionEntry `json:"migrations"`
}

type ExtensionManifest struct {
	plan   ExtensionPlan
	base   *Manifest
	bodies []string
	chains []string
}

func digest(input []byte) string           { h := sha256.Sum256(input); return hex.EncodeToString(h[:]) }
func baselineDigest(base *Manifest) string { raw, _ := json.Marshal(base.Plan()); return digest(raw) }

var checksumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func strictJSON(raw []byte, value any) error {
	if err := uniqueJSONKeys(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid strict JSON document")
	}
	return nil
}

func LoadExtension(path string, base *Manifest) (*ExtensionManifest, error) {
	if base == nil || base.plan.Profile != IdentityProfile {
		return nil, errors.New("complete clean-v2 manifest required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read extension manifest")
	}
	m := &ExtensionManifest{base: base}
	if err = strictJSON(raw, &m.plan); err != nil {
		return nil, err
	}
	p := m.plan
	if p.Format != 1 || p.ReadinessFormat != ExtensionReadinessFormat || p.Profile != ExtensionProfile || p.Canonicalization != Canon || p.BaselineSHA256 != baselineDigest(base) || !checksumPattern.MatchString(p.BaselineReadinessSHA256) || p.Migrations == nil {
		return nil, errors.New("incompatible extension manifest or baseline")
	}
	previous := digest([]byte(p.Profile + ":" + p.ReadinessFormat + ":" + p.BaselineSHA256 + ":" + p.BaselineReadinessSHA256))
	last := 24
	for i, e := range p.Migrations {
		if e.Order != i+1 || e.Version <= last || !bytes.HasPrefix([]byte(e.Name), []byte(fmt.Sprintf("%03d_", e.Version))) || !regexp.MustCompile(`^[0-9]{3}_[a-z][a-z0-9_]*\.sql$`).MatchString(e.Name) || !checksumPattern.MatchString(e.SHA256) || !checksumPattern.MatchString(e.ReadinessSHA256) {
			return nil, errors.New("invalid extension version/name/order/checksum")
		}
		input, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), e.Name))
		if readErr != nil {
			return nil, errors.New("cannot read extension SQL")
		}
		canonical, canonErr := CanonicalSQL(input)
		if canonErr != nil || digest(canonical) != e.SHA256 {
			return nil, errors.New("extension SQL checksum mismatch")
		}
		body, bodyErr := MigrationBody(canonical)
		if bodyErr != nil {
			return nil, fmt.Errorf("invalid extension transaction envelope: %w", bodyErr)
		}
		encoded, _ := json.Marshal(e)
		previous = digest([]byte(previous + ":" + string(encoded)))
		m.bodies = append(m.bodies, body)
		m.chains = append(m.chains, previous)
		last = e.Version
	}
	return m, nil
}

func (m *ExtensionManifest) Plan() ExtensionPlan {
	p := m.plan
	p.Migrations = append([]ExtensionEntry{}, p.Migrations...)
	return p
}

func (m *ExtensionManifest) prefix(rows []Applied) error {
	if len(rows) > len(m.plan.Migrations) {
		return fmt.Errorf("%w: extension ledger ahead of manifest", ErrUnsafe)
	}
	for i, row := range rows {
		if row.Entry != m.plan.Migrations[i].Entry || row.Profile != m.chains[i] {
			return fmt.Errorf("%w: conflicting extension prefix", ErrUnsafe)
		}
	}
	return nil
}

// ManifestSHA256 identifies manifest semantics; it is not permission to apply.
func (m *ExtensionManifest) ManifestSHA256() string {
	raw, _ := json.Marshal(m.plan)
	return digest(raw)
}
