package migrations

import (
	"encoding/json"
	"errors"
	"reflect"
)

type TransitionLedgerSnapshot struct {
	Key            string `json:"key"`
	OID            uint32 `json:"oid"`
	Marker         string `json:"marker"`
	ConstraintsSHA string `json:"constraints_sha256"`
	RowsSHA        string `json:"rows_sha256"`
}

// These digests are evidence supplied by a future trusted collector, not proofs
// that the caller actually inspected PostgreSQL. No collector ships in I1.
type TransitionSnapshot struct {
	Target              TransitionTarget           `json:"target"`
	DatabaseOID         uint32                     `json:"database_oid"`
	Frontier            TransitionFrontier         `json:"frontier"`
	ReadinessSHA        string                     `json:"readiness_sha256"`
	Metadata            []TransitionMetadata       `json:"metadata"`
	Ledgers             []TransitionLedgerSnapshot `json:"ledgers"`
	BusinessDataSHA     string                     `json:"business_data_sha256"`
	IdentitySHA         string                     `json:"identity_sha256"`
	BusinessReceiptsSHA string                     `json:"business_receipts_sha256"`
	SequenceStateSHA    string                     `json:"sequence_state_sha256"`
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
	if err := validateTransitionSnapshot(source); err != nil {
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

func validateTransitionSnapshot(s TransitionSnapshot) error {
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
	want := []string{s.Target.Schema + ".schema_migrations"}
	if len(s.Frontier.Entries) > 0 {
		want = append(want, s.Target.Schema+".schema_extensions")
	}
	// Extension ledger may already exist at an empty frontier, but cannot disappear.
	if len(s.Ledgers) != len(want) && !(len(want) == 1 && len(s.Ledgers) == 2) {
		return errors.New("incomplete ledger inventory")
	}
	seenOID := map[uint32]bool{}
	for i, l := range s.Ledgers {
		key := want[0]
		if i == 1 {
			key = s.Target.Schema + ".schema_extensions"
		}
		if l.Key != key || l.OID == 0 || seenOID[l.OID] || l.Marker == "" || !checksumPattern.MatchString(l.ConstraintsSHA) || !checksumPattern.MatchString(l.RowsSHA) {
			return errors.New("invalid ledger snapshot")
		}
		seenOID[l.OID] = true
	}
	return nil
}

func (b BoundSecurityTransition) ExpectedReceipt() TransitionReceipt {
	if b.boundSHA == "" {
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
	if b.boundSHA == "" || b.boundSHA != transitionDigest(b.source) {
		return errors.New("unbound transition")
	}
	if err := validateTransitionSnapshot(final); err != nil {
		return err
	}
	d := b.approved.descriptor
	if final.Target != b.source.Target || final.DatabaseOID != b.source.DatabaseOID || !reflect.DeepEqual(final.Frontier, d.Frontier) || final.ReadinessSHA != d.FinalReadinessSHA || !reflect.DeepEqual(final.Metadata, d.After) {
		return errors.New("unexpected final catalog/frontier/security state")
	}
	if !reflect.DeepEqual(final.Ledgers, b.source.Ledgers) || final.BusinessDataSHA != b.source.BusinessDataSHA || final.IdentitySHA != b.source.IdentitySHA || final.BusinessReceiptsSHA != b.source.BusinessReceiptsSHA || final.SequenceStateSHA != b.source.SequenceStateSHA {
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
	if b.boundSHA == "" {
		return TransitionEvidenceRejected
	}
	if observed == nil {
		if receipt != nil {
			return TransitionEvidenceRejected
		}
		return TransitionOutcomeUnknown
	}
	if reflect.DeepEqual(*observed, b.source) {
		if receipt != nil {
			return TransitionEvidenceRejected
		}
		return TransitionSourceConfirmed
	}
	if CompareSecurityTransition(b, *observed) == nil && receipt != nil && *receipt == b.ExpectedReceipt() {
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
