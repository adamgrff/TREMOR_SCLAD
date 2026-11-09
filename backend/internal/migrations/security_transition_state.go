package migrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

var transitionFunctionIdentity = regexp.MustCompile(`^[a-z_][a-z0-9_]*\([a-z0-9_., ]*\)$`)

type TransitionLedgerSnapshot struct {
	Key            string        `json:"key"`
	OID            uint32        `json:"oid"`
	Marker         string        `json:"marker"`
	ConstraintsSHA string        `json:"constraints_sha256"`
	RowsSHA        string        `json:"rows_sha256"`
	Owner          string        `json:"owner"`
	ACL            TransitionACL `json:"acl"`
}

// Physical identities belong to target-bound evidence, not release descriptors.
// Column OID is its parent relation OID; SubID is the positive attnum.
type TransitionObjectIdentity struct {
	Key   string `json:"key"`
	OID   uint32 `json:"oid"`
	SubID int32  `json:"sub_id"`
}

// These digests are evidence supplied by a future trusted collector, not proofs
// that the caller actually inspected PostgreSQL. No collector ships in I1.
type TransitionSnapshot struct {
	History             *LedgerHistoryEvidence        `json:"ledger_history,omitempty"`
	ParameterIdentities []TransitionParameterIdentity `json:"parameter_identities"`
	Target              TransitionTarget              `json:"target"`
	DatabaseOID         uint32                        `json:"database_oid"`
	Frontier            TransitionFrontier            `json:"frontier"`
	ReadinessSHA        string                        `json:"readiness_sha256"`
	Metadata            []TransitionMetadata          `json:"metadata"`
	Ledgers             []TransitionLedgerSnapshot    `json:"ledgers"`
	BusinessDataSHA     string                        `json:"business_data_sha256"`
	IdentitySHA         string                        `json:"identity_sha256"`
	BusinessReceiptsSHA string                        `json:"business_receipts_sha256"`
	SequenceStateSHA    string                        `json:"sequence_state_sha256"`
	ScopeSHA            string                        `json:"scope_sha256"`
	ObjectIdentities    []TransitionObjectIdentity    `json:"object_identities"`
}

type TransitionReceipt struct {
	TransitionID     string `json:"transition_id"`
	DescriptorSHA    string `json:"descriptor_sha256"`
	PayloadSHA       string `json:"payload_sha256"`
	BoundSnapshotSHA string `json:"bound_snapshot_sha256"`
	FrontierSHA      string `json:"frontier_sha256"`
}

// A bound value owns deep copies and cannot be manufactured outside this package.
type BoundSecurityTransition struct {
	approved ApprovedSecurityTransition
	source   TransitionSnapshot
	boundSHA string
}

func BindSecurityTransition(a ApprovedSecurityTransition, expected TransitionTarget, source TransitionSnapshot) (BoundSecurityTransition, error) {
	if a.sha == "" || a.sha != transitionDigest(a.descriptor) || validateTransitionDescriptor(a.descriptor) != nil || expected != a.descriptor.Target || source.Target != expected {
		return BoundSecurityTransition{}, errors.New("unapproved/mismatched transition binding")
	}
	if err := validateTransitionSnapshot(source, a.descriptor.SourceScope); err != nil {
		return BoundSecurityTransition{}, err
	}
	if !reflect.DeepEqual(source.Frontier, a.descriptor.Frontier) || source.ReadinessSHA != a.descriptor.SourceReadinessSHA || !reflect.DeepEqual(source.Metadata, a.descriptor.Before) {
		return BoundSecurityTransition{}, errors.New("source is not approved frontier/security state")
	}
	var copy TransitionSnapshot
	raw, _ := json.Marshal(source)
	_ = json.Unmarshal(raw, &copy)
	return BoundSecurityTransition{approved: a, source: copy, boundSHA: transitionDigest(copy)}, nil
}

func validateTransitionSnapshot(s TransitionSnapshot, scope TransitionScope) error {
	if s.History != nil && (!historyScope(scope) || snapshotHistoryValid(s) != nil) {
		return historyError()
	}
	if validateTransitionTarget(s.Target) != nil || validateTransitionFrontier(s.Frontier) != nil || s.DatabaseOID == 0 || !checksumPattern.MatchString(s.ReadinessSHA) {
		return errors.New("invalid snapshot binding")
	}
	for _, h := range []string{s.BusinessDataSHA, s.IdentitySHA, s.BusinessReceiptsSHA, s.SequenceStateSHA} {
		if !checksumPattern.MatchString(h) {
			return errors.New("incomplete immutable snapshot")
		}
	}
	if _, err := transitionMetadataIndex(s.Metadata); err != nil {
		return err
	}
	if s.ScopeSHA != transitionDigest(scope) {
		return errors.New("snapshot scope is not approved")
	}
	if err := validateTransitionScope(scope, s.Target, s.Metadata); err != nil {
		return err
	}
	if err := validateParameterIdentities(s); err != nil {
		return err
	}
	meta, _ := transitionMetadataIndex(s.Metadata)
	identities, err := validateTransitionIdentities(s, scope)
	if err != nil {
		return err
	}
	want := []string{}
	for _, key := range []string{s.Target.Schema + ".schema_migrations", s.Target.Schema + ".schema_extensions"} {
		if _, ok := meta[key]; ok {
			want = append(want, key)
		}
	}
	if len(s.Ledgers) != len(want) {
		return errors.New("ledger inventory disagrees with scope")
	}
	seenOID := map[uint32]bool{}
	for i, l := range s.Ledgers {
		key := want[i]
		if l.Key != key || l.OID == 0 || seenOID[l.OID] || l.Marker == "" || !checksumPattern.MatchString(l.ConstraintsSHA) || !checksumPattern.MatchString(l.RowsSHA) {
			return errors.New("invalid ledger snapshot")
		}
		m := meta[key]
		if m.Kind != "table" || l.OID != identities[key].OID || l.Owner != m.Owner || !reflect.DeepEqual(l.ACL, m.ACL) {
			return errors.New("ledger identity/owner/ACL contradict metadata")
		}
		seenOID[l.OID] = true
	}
	return nil
}

func (b BoundSecurityTransition) ExpectedReceipt() TransitionReceipt {
	if !validBoundSecurityTransition(b) {
		return TransitionReceipt{}
	}
	d := b.approved.descriptor
	return TransitionReceipt{d.ID, b.approved.sha, d.PayloadSHA, b.boundSHA, transitionDigest(d.Frontier)}
}

// CompareSecurityTransition accepts only the complete approved final inventory.
// Owners/ACL can differ only as validated by descriptor deltas. Everything in
// the immutable snapshot remains exact, including historical ledger timestamps
// encoded in RowsSHA and the already existing business receipts.
func CompareSecurityTransition(b BoundSecurityTransition, final TransitionSnapshot) error {
	if err := compareSecurityTransitionEffects(b, final); err != nil {
		return err
	}
	if historyScope(b.approved.descriptor.FinalScope) {
		return boundHistoryValid(b, final, b.approved.sha, b.source.Frontier.ExtensionManifestSHA)
	}
	return nil
}

// Pre-receipt effects validation is private to the approved writer. It does not
// certify a committed/protected state; the public comparator also requires history.
func compareSecurityTransitionEffects(b BoundSecurityTransition, final TransitionSnapshot) error {
	if !validBoundSecurityTransition(b) {
		return errors.New("unbound transition")
	}
	if err := validateTransitionSnapshot(final, b.approved.descriptor.FinalScope); err != nil {
		return err
	}
	d := b.approved.descriptor
	if final.Target != b.source.Target || final.DatabaseOID != b.source.DatabaseOID || !reflect.DeepEqual(final.Frontier, d.Frontier) || final.ReadinessSHA != d.FinalReadinessSHA || !reflect.DeepEqual(final.Metadata, d.After) {
		return errors.New("unexpected final catalog/frontier/security state")
	}
	if !preservedParameterIdentities(b.source.ParameterIdentities, final.ParameterIdentities) || !sameTransitionLedgers(final.Ledgers, b.source.Ledgers) || !preservedTransitionIdentities(b.source.ObjectIdentities, final.ObjectIdentities) || final.BusinessDataSHA != b.source.BusinessDataSHA || final.IdentitySHA != b.source.IdentitySHA || final.BusinessReceiptsSHA != b.source.BusinessReceiptsSHA || final.SequenceStateSHA != b.source.SequenceStateSHA {
		return errors.New("immutable transition evidence changed")
	}
	return nil
}

type SecurityTransitionState string

const (
	TransitionLegacyVerified SecurityTransitionState = "LEGACY_VERIFIED"
	TransitionPrepared       SecurityTransitionState = "PREPARED"
	TransitionInProgress     SecurityTransitionState = "TRANSITIONING"
	TransitionProtected      SecurityTransitionState = "PROTECTED"
	TransitionUnknownCommit  SecurityTransitionState = "UNKNOWN_COMMIT"
	TransitionRejected       SecurityTransitionState = "REJECTED"
)

type TransitionRecoveryResult string

const (
	TransitionSourceConfirmed  TransitionRecoveryResult = "SOURCE_CONFIRMED"
	TransitionFinalConfirmed   TransitionRecoveryResult = "FINAL_CONFIRMED"
	TransitionOutcomeUnknown   TransitionRecoveryResult = "OUTCOME_UNKNOWN"
	TransitionEvidenceRejected TransitionRecoveryResult = "EVIDENCE_REJECTED"
)

// Recovery has evidence only: no SQL executor, callback, retry or connection.
// Unavailable evidence is unknown; contradictory available evidence is rejected.
func ClassifyTransitionRecovery(b BoundSecurityTransition, observed *TransitionSnapshot, receipt *TransitionReceipt) TransitionRecoveryResult {
	if !validBoundSecurityTransition(b) {
		return TransitionEvidenceRejected
	}
	if observed == nil {
		if receipt != nil {
			return TransitionEvidenceRejected
		}
		return TransitionOutcomeUnknown
	}
	if reflect.DeepEqual(*observed, b.source) {
		if validateTransitionSnapshot(*observed, b.approved.descriptor.SourceScope) != nil {
			return TransitionEvidenceRejected
		}
		if receipt != nil {
			return TransitionEvidenceRejected
		}
		return TransitionSourceConfirmed
	}
	if CompareSecurityTransition(b, *observed) == nil && receipt != nil && *receipt == b.ExpectedReceipt() {
		if historyScope(b.approved.descriptor.FinalScope) && boundHistoryValid(b, *observed, b.approved.sha, b.source.Frontier.ExtensionManifestSHA) != nil {
			return TransitionEvidenceRejected
		}
		return TransitionFinalConfirmed
	}
	return TransitionEvidenceRejected
}

type TransitionObservation struct {
	Snapshot        *TransitionSnapshot
	Receipt         *TransitionReceipt
	Prepared        bool
	InTransaction   bool
	CommitUncertain bool
}

// PROTECTED requires committed evidence (never an in-transaction postflight).
// PREPARED describes local preparation only, not permission or proof of SQL work.
func ClassifySecurityTransition(b BoundSecurityTransition, o TransitionObservation) SecurityTransitionState {
	r := ClassifyTransitionRecovery(b, o.Snapshot, o.Receipt)
	if r == TransitionEvidenceRejected || o.InTransaction && o.CommitUncertain {
		return TransitionRejected
	}
	if o.InTransaction {
		if !o.Prepared || o.Snapshot == nil {
			return TransitionRejected
		}
		return TransitionInProgress
	}
	switch r {
	case TransitionFinalConfirmed:
		return TransitionProtected
	case TransitionOutcomeUnknown:
		if o.CommitUncertain {
			return TransitionUnknownCommit
		}
		return TransitionRejected
	case TransitionSourceConfirmed:
		if o.Prepared {
			return TransitionPrepared
		}
		return TransitionLegacyVerified
	}
	return TransitionRejected
}

func validBoundSecurityTransition(b BoundSecurityTransition) bool {
	d := b.approved.descriptor
	return b.boundSHA != "" && b.boundSHA == transitionDigest(b.source) && b.approved.sha != "" && b.approved.sha == transitionDigest(d) && validateTransitionDescriptor(d) == nil && validateTransitionSnapshot(b.source, d.SourceScope) == nil && b.source.Target == d.Target && reflect.DeepEqual(b.source.Frontier, d.Frontier) && b.source.ReadinessSHA == d.SourceReadinessSHA && reflect.DeepEqual(b.source.Metadata, d.Before)
}

func sameTransitionLedgers(a, b []TransitionLedgerSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].OID != b[i].OID || a[i].Marker != b[i].Marker || a[i].ConstraintsSHA != b[i].ConstraintsSHA || a[i].RowsSHA != b[i].RowsSHA {
			return false
		}
	}
	return true
}

func preservedTransitionIdentities(before, after []TransitionObjectIdentity) bool {
	index := map[string]TransitionObjectIdentity{}
	for _, o := range after {
		index[o.Key] = o
	}
	for _, o := range before {
		if a, ok := index[o.Key]; ok && a != o {
			return false
		}
	}
	return true
}

func validateTransitionIdentities(s TransitionSnapshot, scope TransitionScope) (map[string]TransitionObjectIdentity, error) {
	want := map[string]TransitionRequirement{}
	for _, r := range scope.Objects {
		if r.Presence == "present" && r.Kind != "default_acl" {
			want[r.Key] = r
		}
	}
	if s.ObjectIdentities == nil || len(s.ObjectIdentities) != len(want) {
		return nil, errors.New("incomplete physical object identities")
	}
	index := map[string]TransitionObjectIdentity{}
	seen := map[string]bool{}
	previous := ""
	for _, o := range s.ObjectIdentities {
		r, ok := want[o.Key]
		if !ok || o.Key <= previous || o.OID == 0 || (r.Kind == "column" && o.SubID <= 0) || (r.Kind != "column" && o.SubID != 0) {
			return nil, errors.New("invalid/duplicate object identity")
		}
		previous = o.Key
		catalog := r.Kind
		if r.Kind == "table" || r.Kind == "view" || r.Kind == "sequence" || r.Kind == "column" {
			catalog = "relation"
		}
		identity := fmt.Sprintf("%s/%d/%d", catalog, o.OID, o.SubID)
		if seen[identity] {
			return nil, errors.New("duplicate logical object identity")
		}
		seen[identity] = true
		index[o.Key] = o
	}
	for _, r := range scope.Objects {
		if r.Presence == "present" && r.Kind == "column" && index[r.Key].OID != index[r.Parent].OID {
			return nil, errors.New("column identity contradicts parent relation")
		}
	}
	return index, nil
}

// Fixed minima supplement (and cannot be removed by) the approved object scope.
// legacy-v1 permits explicit absence of not-yet-provisioned target roles/schemas;
// prepared-v1 requires them present. Neither profile permits an absent baseline.
func validateTransitionScope(scope TransitionScope, target TransitionTarget, objects []TransitionMetadata) error {
	if err := validateParameterScope(scope.Parameters, objects); err != nil {
		return err
	}
	if scope.Profile != "legacy-v1" && scope.Profile != "prepared-v1" {
		return errors.New("unsupported security scope profile")
	}
	meta, err := transitionMetadataIndex(objects)
	if err != nil {
		return err
	}
	if scope.Objects == nil || scope.Creators == nil || len(scope.Creators) == 0 {
		return errors.New("explicit security scope/creators required")
	}
	reqs := map[string]TransitionRequirement{}
	previous := ""
	present := 0
	for _, r := range scope.Objects {
		if r.Key <= previous || (r.Presence != "present" && r.Presence != "absent") || !transitionRequirementIdentity(r) {
			return errors.New("invalid/duplicate scope identity")
		}
		previous = r.Key
		reqs[r.Key] = r
		m, exists := meta[r.Key]
		if exists != (r.Presence == "present") || exists && m.Kind != r.Kind {
			return errors.New("metadata presence/class contradicts approved scope")
		}
		if exists {
			present++
		}
	}
	if present != len(meta) {
		return errors.New("metadata outside approved scope")
	}
	for _, name := range []string{"tremor_api", "tremor_deployer", "tremor_executor", "tremor_ledger_owner", "tremor_migrator", "tremor_owner"} {
		r, ok := reqs["role:"+name]
		if !ok || r.Kind != "role" || scope.Profile == "prepared-v1" && r.Presence != "present" {
			return errors.New("missing required role scope")
		}
		if m, ok := meta[r.Key]; ok && scope.Profile == "prepared-v1" && name != "tremor_deployer" {
			f := m.RoleFlags
			if f.Superuser || f.CreateDB || f.CreateRole || f.Replication || f.BypassRLS || len(m.Memberships) != 0 || (name == "tremor_owner" || name == "tremor_ledger_owner" || name == "tremor_executor") && f.Login {
				return errors.New("unsafe prepared role attributes/memberships")
			}
		}
	}
	for _, name := range []string{target.Schema, "tremor_commands", "tremor_deployment"} {
		r, ok := reqs["schema:"+name]
		if !ok || r.Kind != "schema" || (name == target.Schema || scope.Profile == "prepared-v1") && r.Presence != "present" {
			return errors.New("missing protected schema scope")
		}
		if m, ok := meta[r.Key]; ok && !m.Protected {
			return errors.New("schema protection evidence required")
		}
	}
	for _, name := range []string{"schema_migrations", "schema_extensions"} {
		r, ok := reqs[target.Schema+"."+name]
		if !ok || r.Kind != "table" || name == "schema_migrations" && r.Presence != "present" {
			return errors.New("explicit ledger presence policy required")
		}
	}
	rolePresent := func(name string) bool { m, ok := meta["role:"+name]; return ok && m.Kind == "role" }
	for _, r := range scope.Objects {
		if r.Presence != "present" {
			continue
		}
		m := meta[r.Key]
		if r.Kind != "role" && !rolePresent(m.Owner) {
			return errors.New("unknown object owner role")
		}
		if r.Kind != "schema" && r.Kind != "role" && !(r.Kind == "default_acl" && r.Schema == "") {
			if schema, ok := meta["schema:"+r.Schema]; !ok || schema.Kind != "schema" {
				return errors.New("unknown object schema")
			}
		}
		if r.Kind == "column" {
			p, ok := meta[r.Parent]
			if !ok || (p.Kind != "table" && p.Kind != "view") || p.Owner != m.Owner {
				return errors.New("unknown/inconsistent column parent")
			}
		}
		if r.Kind == "default_acl" && r.Parent != "role:"+m.Owner {
			return errors.New("default privilege creator mismatch")
		}
		for _, g := range m.ACL.Grants {
			if !rolePresent(g.Grantor) || g.Grantee != "PUBLIC" && !rolePresent(g.Grantee) {
				return errors.New("unknown ACL role reference")
			}
		}
		for _, edge := range m.Memberships {
			if !rolePresent(edge.Role) || !rolePresent(edge.Grantor) || r.Key == "role:"+edge.Role {
				return errors.New("unknown/self membership dependency")
			}
		}
	}
	previous = ""
	for _, creator := range scope.Creators {
		if creator <= previous || !rolePresent(creator) {
			return errors.New("unknown/duplicate creator scope")
		}
		previous = creator
		for _, class := range []string{"functions", "sequences", "tables"} {
			key := "default_acl::" + creator + ":" + class
			if r, ok := reqs[key]; !ok || r.Kind != "default_acl" || r.Presence != "present" {
				return errors.New("missing global default privilege evidence")
			}
		}
		for _, requirement := range scope.Objects {
			if requirement.Kind != "schema" || requirement.Presence != "present" {
				continue
			}
			schema := requirement.Schema
			for _, class := range []string{"functions", "sequences", "tables"} {
				key := "default_acl:" + schema + ":" + creator + ":" + class
				if r, ok := reqs[key]; !ok || r.Kind != "default_acl" || r.Presence != "present" {
					return errors.New("missing applicable default privilege evidence")
				}
			}
		}
	}
	if scope.Profile == "prepared-v1" {
		found := false
		for _, r := range scope.Creators {
			found = found || r == "tremor_deployer"
		}
		if !found {
			return errors.New("deployment creator coverage required")
		}
	}
	// Check the complete in-scope dependency graph, including edges with SET or
	// INHERIT false. No global cluster inventory is requested.
	visiting := map[string]bool{}
	done := map[string]bool{}
	var walk func(string) bool
	walk = func(key string) bool {
		if visiting[key] {
			return false
		}
		if done[key] {
			return true
		}
		visiting[key] = true
		for _, e := range meta[key].Memberships {
			if !walk("role:" + e.Role) {
				return false
			}
		}
		delete(visiting, key)
		done[key] = true
		return true
	}
	for key, m := range meta {
		if m.Kind == "role" && !walk(key) {
			return errors.New("cyclic membership graph")
		}
	}
	return nil
}

func transitionRequirementIdentity(r TransitionRequirement) bool {
	switch r.Kind {
	case "role":
		return strings.HasPrefix(r.Key, "role:") && simpleName.MatchString(strings.TrimPrefix(r.Key, "role:")) && r.Schema == "" && r.Parent == ""
	case "schema":
		return schemaName.MatchString(r.Schema) && r.Key == "schema:"+r.Schema && r.Parent == ""
	case "table", "view", "sequence":
		return schemaName.MatchString(r.Schema) && strings.HasPrefix(r.Key, r.Schema+".") && simpleName.MatchString(strings.TrimPrefix(r.Key, r.Schema+".")) && r.Parent == ""
	case "function":
		return schemaName.MatchString(r.Schema) && strings.HasPrefix(r.Key, r.Schema+".") && transitionFunctionIdentity.MatchString(strings.TrimPrefix(r.Key, r.Schema+".")) && r.Parent == ""
	case "column":
		return schemaName.MatchString(r.Schema) && strings.HasPrefix(r.Parent, r.Schema+".") && simpleName.MatchString(strings.TrimPrefix(r.Parent, r.Schema+".")) && strings.HasPrefix(r.Key, r.Parent+".") && simpleName.MatchString(strings.TrimPrefix(r.Key, r.Parent+"."))
	case "default_acl":
		parts := strings.Split(r.Key, ":")
		return len(parts) == 4 && parts[0] == "default_acl" && parts[1] == r.Schema && (r.Schema == "" || schemaName.MatchString(r.Schema)) && simpleName.MatchString(parts[2]) && r.Parent == "role:"+parts[2] && (parts[3] == "tables" || parts[3] == "functions" || parts[3] == "sequences")
	}
	return false
}
