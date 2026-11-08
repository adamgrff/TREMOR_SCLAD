package migrations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const SecurityTransitionProtocol = "e1b-security-transition-v1"

// TransitionTarget is release-approved identity, not connection configuration.
// DatabaseOID and observed data belong exclusively to the bound snapshot.
type TransitionTarget struct {
	ClusterID    string `json:"cluster_id"`
	Database     string `json:"database"`
	Schema       string `json:"schema"`
	DeploymentID string `json:"deployment_id"`
}

type TransitionEntry struct {
	Version  int    `json:"version"`
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
	Chain    string `json:"chain"`
	Ordinal  int    `json:"ordinal"`
}

// Frontier pins the exact applied extension prefix, independently of row timestamps.
// Baseline lineage and manifest hashes must be checked by a future collector.
type TransitionFrontier struct {
	BaselineManifestSHA  string            `json:"baseline_manifest_sha256"`
	BaselineLineageSHA   string            `json:"baseline_lineage_sha256"`
	ExtensionManifestSHA string            `json:"extension_manifest_sha256"`
	Entries              []TransitionEntry `json:"entries"`
}

// ACLMode distinguishes PostgreSQL NULL/default ACL from an explicit empty ACL.
// Column privileges are separate metadata objects, never folded into table ACL.
type TransitionGrant struct {
	Grantor     string `json:"grantor"`
	Grantee     string `json:"grantee"`
	Privilege   string `json:"privilege"`
	GrantOption bool   `json:"grant_option"`
}

type TransitionACL struct {
	Mode   string            `json:"mode"`
	Grants []TransitionGrant `json:"grants"`
}

type TransitionRoleFlags struct {
	Login       bool `json:"login"`
	Superuser   bool `json:"superuser"`
	CreateDB    bool `json:"create_db"`
	CreateRole  bool `json:"create_role"`
	Replication bool `json:"replication"`
	BypassRLS   bool `json:"bypass_rls"`
	Inherit     bool `json:"inherit"`
}

type TransitionMembership struct {
	Role    string `json:"role"`
	Grantor string `json:"grantor"`
	Admin   bool   `json:"admin"`
	Inherit bool   `json:"inherit"`
	Set     bool   `json:"set"`
}

// Key is a collector-stable, schema-qualified identity including function args or
// column name. DefinitionSHA covers definitions not represented by the typed fields.
// Every collection is ordered, duplicate-free, and non-nil; order is part of v1.
type TransitionMetadata struct {
	Key             string                 `json:"key"`
	Kind            string                 `json:"kind"`
	Owner           string                 `json:"owner"`
	ACL             TransitionACL          `json:"acl"`
	DefinitionSHA   string                 `json:"definition_sha256"`
	SecurityDefiner bool                   `json:"security_definer"`
	SearchPath      []string               `json:"search_path"`
	Protected       bool                   `json:"protected"`
	RoleFlags       TransitionRoleFlags    `json:"role_flags"`
	Memberships     []TransitionMembership `json:"memberships"`
}

type TransitionDelta struct {
	Key       string `json:"key"`
	BeforeSHA string `json:"before_sha256"`
	AfterSHA  string `json:"after_sha256"`
	// Required when an SD function changes owner or execution metadata.
	ExecutionContextChange bool `json:"execution_context_change"`
}

type TransitionReceiptLocation struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Format int    `json:"format"`
}

type SecurityTransitionDescriptor struct {
	Protocol           string                    `json:"protocol"`
	ID                 string                    `json:"id"`
	PayloadSHA         string                    `json:"payload_sha256"`
	Target             TransitionTarget          `json:"target"`
	Frontier           TransitionFrontier        `json:"frontier"`
	SourceReadinessSHA string                    `json:"source_readiness_sha256"`
	FinalReadinessSHA  string                    `json:"final_readiness_sha256"`
	Before             []TransitionMetadata      `json:"before"`
	After              []TransitionMetadata      `json:"after"`
	Changes            []TransitionDelta         `json:"changes"`
	Receipt            TransitionReceiptLocation `json:"receipt"`
	SourceScope        TransitionScope           `json:"source_scope"`
	FinalScope         TransitionScope           `json:"final_scope"`
}

// Scope is release policy, never inferred from observed evidence. Both phases
// enumerate presence AND absence. No observed "complete" or "validated" flag exists.
type TransitionScope struct {
	Profile  string                  `json:"profile"`
	Objects  []TransitionRequirement `json:"objects"`
	Creators []string                `json:"creators"`
}

type TransitionRequirement struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	Schema   string `json:"schema"`
	Parent   string `json:"parent"`
	Presence string `json:"presence"`
}

// ApprovedSecurityTransition is sealed: callers cannot construct approval by
// hashing their observations or mutate the descriptor after approval.
type ApprovedSecurityTransition struct {
	descriptor SecurityTransitionDescriptor
	sha        string
}

var transitionUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var transitionID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// DecodeSecurityTransition validates content only. It grants no approval.
// Payload uses exact UTF-8/LF bytes: no SQL normalization or interpretation.
func DecodeSecurityTransition(raw, payload []byte) (SecurityTransitionDescriptor, string, error) {
	var d SecurityTransitionDescriptor
	if !utf8.Valid(raw) || len(raw) > 4<<20 || uniqueJSONKeys(raw) != nil || !transitionUnicodeEscapes(raw) {
		return d, "", errors.New("invalid transition JSON")
	}
	if err := transitionJSONShape(raw, reflect.TypeOf(d)); err != nil {
		return d, "", err
	}
	if err := strictJSON(raw, &d); err != nil {
		return d, "", err
	}
	if err := validateTransitionDescriptor(d); err != nil {
		return d, "", err
	}
	if len(payload) == 0 || !utf8.Valid(payload) || bytes.ContainsAny(payload, "\r\x00") || bytes.HasPrefix(payload, []byte{0xef, 0xbb, 0xbf}) || digest(payload) != d.PayloadSHA {
		return d, "", errors.New("transition payload bytes/checksum mismatch")
	}
	return d, transitionDigest(d), nil
}

// No production approvals ship in I1. Future release integration must be reviewed;
// there is deliberately no caller/environment/file-supplied approval override.
func LoadApprovedSecurityTransition(raw, payload []byte) (ApprovedSecurityTransition, error) {
	return approveSecurityTransition(raw, payload, nil)
}

func approveSecurityTransition(raw, payload []byte, releaseHashes []string) (ApprovedSecurityTransition, error) {
	d, sha, err := DecodeSecurityTransition(raw, payload)
	if err != nil {
		return ApprovedSecurityTransition{}, err
	}
	seen := map[string]bool{}
	approved := false
	for _, h := range releaseHashes {
		if !checksumPattern.MatchString(h) || seen[h] {
			return ApprovedSecurityTransition{}, errors.New("invalid release approvals")
		}
		seen[h] = true
		approved = approved || h == sha
	}
	if !approved {
		return ApprovedSecurityTransition{}, errors.New("transition is not release-approved")
	}
	return ApprovedSecurityTransition{descriptor: d, sha: sha}, nil
}

func transitionDigest(v any) string { raw, _ := json.Marshal(v); return digest(raw) }

// encoding/json replaces unpaired UTF-16 surrogate escapes with U+FFFD. Reject
// that lossy input instead of allowing distinct source strings to hash alike.
func transitionUnicodeEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

// All fields use exact spelling and are required, including false booleans and
// empty arrays. Null is forbidden everywhere. This closes encoding/json's null
// scalar and case-insensitive-field behavior without changing E1A's decoder.
func transitionJSONShape(raw json.RawMessage, typ reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null transition field")
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != typ.NumField() {
			return errors.New("missing/unknown transition field")
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			value, ok := fields[f.Tag.Get("json")]
			if !ok {
				return errors.New("missing/exact-case transition field required")
			}
			if err := transitionJSONShape(value, f.Type); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return errors.New("transition array required")
		}
		for _, value := range values {
			if err := transitionJSONShape(value, typ.Elem()); err != nil {
				return err
			}
		}
	default:
		v := reflect.New(typ).Interface()
		if json.Unmarshal(raw, v) != nil {
			return errors.New("invalid transition field type")
		}
	}
	return nil
}

func validateTransitionDescriptor(d SecurityTransitionDescriptor) error {
	if d.Protocol != SecurityTransitionProtocol || !transitionID.MatchString(d.ID) || !checksumPattern.MatchString(d.PayloadSHA) || !checksumPattern.MatchString(d.SourceReadinessSHA) || !checksumPattern.MatchString(d.FinalReadinessSHA) || d.SourceReadinessSHA == d.FinalReadinessSHA {
		return errors.New("invalid transition identity/readiness")
	}
	if err := validateTransitionTarget(d.Target); err != nil {
		return err
	}
	if err := validateTransitionFrontier(d.Frontier); err != nil {
		return err
	}
	if d.Receipt != (TransitionReceiptLocation{Schema: "tremor_deployment", Table: "security_transitions", Format: 1}) {
		return errors.New("unsupported transition receipt")
	}
	before, err := transitionMetadataIndex(d.Before)
	if err != nil {
		return err
	}
	after, err := transitionMetadataIndex(d.After)
	if err != nil {
		return err
	}
	if d.Changes == nil || len(d.Changes) == 0 {
		return errors.New("explicit changes required")
	}
	changes := map[string]TransitionDelta{}
	previous := ""
	for _, c := range d.Changes {
		if c.Key <= previous || !checksumPattern.MatchString(c.BeforeSHA) || !checksumPattern.MatchString(c.AfterSHA) || c.BeforeSHA == c.AfterSHA {
			return errors.New("invalid/unsorted metadata delta")
		}
		previous = c.Key
		changes[c.Key] = c
	}
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	for k := range keys {
		b, bok := before[k]
		a, aok := after[k]
		bh, ah := transitionObjectDigest(b, bok), transitionObjectDigest(a, aok)
		c, changed := changes[k]
		if bh == ah {
			if changed {
				return errors.New("unused metadata delta")
			}
			continue
		}
		contextChange := (bok && b.SecurityDefiner || aok && a.SecurityDefiner) && (!bok || !aok || b.Owner != a.Owner || b.SecurityDefiner != a.SecurityDefiner || !reflect.DeepEqual(b.SearchPath, a.SearchPath) || b.DefinitionSHA != a.DefinitionSHA)
		if !changed || c.BeforeSHA != bh || c.AfterSHA != ah || c.ExecutionContextChange != contextChange {
			return fmt.Errorf("unapproved metadata change: %s", k)
		}
		delete(changes, k)
	}
	if len(changes) != 0 {
		return errors.New("delta for unknown object")
	}
	if err := validateTransitionScope(d.SourceScope, d.Target, d.Before); err != nil {
		return err
	}
	if d.FinalScope.Profile != "prepared-v1" {
		return errors.New("final prepared scope required")
	}
	if err := validateTransitionScope(d.FinalScope, d.Target, d.After); err != nil {
		return err
	}
	for _, key := range []string{d.Target.Schema + ".schema_migrations", d.Target.Schema + ".schema_extensions"} {
		var bp, ap string
		for _, r := range d.SourceScope.Objects {
			if r.Key == key {
				bp = r.Presence
			}
		}
		for _, r := range d.FinalScope.Objects {
			if r.Key == key {
				ap = r.Presence
			}
		}
		if bp != ap {
			return errors.New("transition cannot create/remove migration ledgers")
		}
		if strings.HasSuffix(key, ".schema_extensions") && len(d.Frontier.Entries) > 0 && bp != "present" {
			return errors.New("applied prefix requires extension ledger")
		}
	}
	return nil
}

func validateTransitionTarget(t TransitionTarget) error {
	if _, err := strconv.ParseUint(t.ClusterID, 10, 64); err != nil {
		return errors.New("invalid cluster identifier range")
	}
	if !regexp.MustCompile(`^[1-9][0-9]{0,19}$`).MatchString(t.ClusterID) || !simpleName.MatchString(t.Database) || !schemaName.MatchString(t.Schema) || strings.HasPrefix(t.Schema, "pg_") || t.Schema == "information_schema" || !transitionUUID.MatchString(t.DeploymentID) || t.DeploymentID == "00000000-0000-0000-0000-000000000000" {
		return errors.New("invalid transition target")
	}
	for _, name := range []string{"tremor_sklad", "postgres", "template0", "template1"} {
		if strings.EqualFold(t.Database, name) {
			return errors.New("protected transition database")
		}
	}
	return nil
}

func validateTransitionFrontier(f TransitionFrontier) error {
	if !checksumPattern.MatchString(f.BaselineManifestSHA) || !checksumPattern.MatchString(f.BaselineLineageSHA) || !checksumPattern.MatchString(f.ExtensionManifestSHA) || f.Entries == nil {
		return errors.New("invalid frontier hashes")
	}
	last := 24
	for i, e := range f.Entries {
		if e.Version <= last || e.Ordinal != i+1 || !regexp.MustCompile(`^[0-9]{3}_[a-z][a-z0-9_]*\.sql$`).MatchString(e.Name) || !strings.HasPrefix(e.Name, fmt.Sprintf("%03d_", e.Version)) || !checksumPattern.MatchString(e.Checksum) || !checksumPattern.MatchString(e.Chain) {
			return errors.New("invalid exact frontier")
		}
		last = e.Version
	}
	return nil
}

func transitionObjectDigest(m TransitionMetadata, exists bool) string {
	if !exists {
		return digest([]byte("absent-transition-object-v1"))
	}
	return transitionDigest(m)
}

func transitionMetadataIndex(objects []TransitionMetadata) (map[string]TransitionMetadata, error) {
	if objects == nil || len(objects) == 0 {
		return nil, errors.New("metadata inventory required")
	}
	result := map[string]TransitionMetadata{}
	previous := ""
	for _, o := range objects {
		if o.Key == "" || !utf8.ValidString(o.Key) || len(o.Key) > 1024 || strings.TrimSpace(o.Key) != o.Key || strings.IndexFunc(o.Key, unicode.IsControl) >= 0 || o.Key <= previous || !checksumPattern.MatchString(o.DefinitionSHA) || o.SearchPath == nil || o.Memberships == nil || o.ACL.Grants == nil {
			return nil, errors.New("invalid/unsorted metadata inventory")
		}
		previous = o.Key
		switch o.Kind {
		case "schema", "table", "column", "function", "sequence", "default_acl", "role":
		default:
			return nil, errors.New("unknown metadata class")
		}
		if o.Kind != "role" && !simpleName.MatchString(o.Owner) {
			return nil, errors.New("metadata owner required")
		}
		if o.Kind == "role" && (o.Owner != "" || o.ACL.Mode != "explicit" || len(o.ACL.Grants) != 0) {
			return nil, errors.New("roles have no object owner/ACL")
		}
		if o.ACL.Mode != "default" && o.ACL.Mode != "explicit" || o.ACL.Mode == "default" && len(o.ACL.Grants) != 0 {
			return nil, errors.New("invalid ACL mode")
		}
		if o.Kind != "function" && (o.SecurityDefiner || len(o.SearchPath) != 0) || o.Kind != "schema" && o.Protected || o.Kind != "role" && (o.RoleFlags != (TransitionRoleFlags{}) || len(o.Memberships) != 0) {
			return nil, errors.New("metadata fields incompatible with class")
		}
		lastGrant := ""
		for _, g := range o.ACL.Grants {
			key := g.Grantor + "/" + g.Grantee + "/" + g.Privilege
			if !simpleName.MatchString(g.Grantor) || !simpleName.MatchString(g.Grantee) || !transitionPrivilege(g.Privilege) || key <= lastGrant {
				return nil, errors.New("invalid/unsorted ACL grant")
			}
			lastGrant = key
		}
		lastMember := ""
		for _, m := range o.Memberships {
			key := m.Role + "/" + m.Grantor
			if !simpleName.MatchString(m.Role) || !simpleName.MatchString(m.Grantor) || key <= lastMember {
				return nil, errors.New("invalid/unsorted membership")
			}
			lastMember = key
		}
		for _, p := range o.SearchPath {
			if p != "pg_temp" && !schemaName.MatchString(p) {
				return nil, errors.New("invalid function search_path")
			}
		}
		result[o.Key] = o
	}
	return result, nil
}

func transitionPrivilege(s string) bool {
	switch s {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN", "USAGE", "CREATE", "EXECUTE", "CONNECT", "TEMPORARY":
		return true
	}
	return false
}
