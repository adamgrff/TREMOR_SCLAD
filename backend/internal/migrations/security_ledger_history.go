package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
)

const LedgerHistoryFormat = "e1b-ledger-history-v1"
const ledgerHistoryKey = "tremor_deployment.ledger_history_v1"

var ErrLedgerHistory = errors.New("ledger history evidence rejected")

func historyUnavailable(e error) error { return fmt.Errorf("history evidence unavailable: %w", e) }
func historyError() error              { return errors.Join(ErrUnsafe, ErrLedgerHistory) }

// Timestamps are exact PostgreSQL microseconds since 2000-01-01 UTC. They
// never pass through floating point or timezone-dependent text formatting.
type LedgerHistoryRow struct {
	Version           int    `json:"version"`
	Name              string `json:"name"`
	Checksum          string `json:"checksum"`
	Profile           string `json:"profile"`
	Ordinal           int    `json:"ordinal"`
	AppliedAtPGMicros int64  `json:"applied_at_pg_micros"`
}
type HistoryLedger struct {
	Present     bool               `json:"present"`
	OID         uint32             `json:"oid"`
	SnapshotSHA string             `json:"snapshot_sha256"`
	Rows        []LedgerHistoryRow `json:"rows"`
}
type LedgerHistoryState struct {
	Format        string             `json:"format"`
	Target        TransitionTarget   `json:"target"`
	DatabaseOID   uint32             `json:"database_oid"`
	TransitionID  string             `json:"transition_id"`
	TransitionSHA string             `json:"transition_sha256"`
	BoundSHA      string             `json:"bound_sha256"`
	Step          int                `json:"step"`
	OperationSHA  string             `json:"operation_sha256"`
	PreviousSHA   string             `json:"previous_sha256"`
	Frontier      TransitionFrontier `json:"frontier"`
	Baseline      HistoryLedger      `json:"baseline"`
	Extension     HistoryLedger      `json:"extension"`
}
type LedgerHistoryRecord struct {
	SHA   string             `json:"sha256"`
	State LedgerHistoryState `json:"state"`
}
type LedgerHistoryEvidence struct {
	Format  string                `json:"format"`
	Records []LedgerHistoryRecord `json:"records"`
}

// Inert, versioned control DDL: it must be in an approved transition payload,
// included in its FinalScope/deltas. Neither verification nor recovery runs it.
const LedgerHistoryControlSQL = `
CREATE TABLE tremor_deployment.ledger_history_v1(
 transition_id text NOT NULL,
 step integer NOT NULL CHECK(step>=0),
 checkpoint_sha256 text NOT NULL CHECK(checkpoint_sha256 ~ '^[0-9a-f]{64}$'),
 payload text NOT NULL,
 PRIMARY KEY(transition_id,step), UNIQUE(checkpoint_sha256));
ALTER TABLE tremor_deployment.ledger_history_v1 OWNER TO tremor_ledger_owner;
REVOKE ALL ON tremor_deployment.ledger_history_v1 FROM PUBLIC,tremor_api,tremor_executor,tremor_migrator;
GRANT SELECT ON tremor_deployment.ledger_history_v1 TO tremor_migrator;
`

func historyScope(scope TransitionScope) bool {
	for _, r := range scope.Objects {
		if r.Key == ledgerHistoryKey && r.Kind == "table" && r.Presence == "present" {
			return true
		}
	}
	return false
}
func validHistoryLedger(l HistoryLedger) bool {
	if l.Rows == nil {
		return false
	}
	if !l.Present {
		return l.OID == 0 && l.SnapshotSHA == "" && len(l.Rows) == 0
	}
	if l.OID == 0 || !checksumPattern.MatchString(l.SnapshotSHA) {
		return false
	}
	for i, r := range l.Rows {
		if r.Ordinal != i+1 || r.Version <= 0 || r.Name == "" || !checksumPattern.MatchString(r.Checksum) || (r.Profile != IdentityProfile && !checksumPattern.MatchString(r.Profile)) {
			return false
		}
		if i > 0 && r.Version <= l.Rows[i-1].Version {
			return false
		}
	}
	return true
}
func historyPrefix(old, next LedgerHistoryState) bool {
	if old.Target != next.Target || old.DatabaseOID != next.DatabaseOID || old.TransitionID != next.TransitionID || old.TransitionSHA != next.TransitionSHA || old.BoundSHA != next.BoundSHA || !reflect.DeepEqual(old.Baseline, next.Baseline) {
		return false
	}
	if next.Frontier.BaselineManifestSHA != old.Frontier.BaselineManifestSHA || next.Frontier.BaselineLineageSHA != old.Frontier.BaselineLineageSHA {
		return false
	}
	n := len(old.Frontier.Entries)
	if len(next.Frontier.Entries) != n+1 || !reflect.DeepEqual(next.Frontier.Entries[:n], old.Frontier.Entries) {
		return false
	}
	if !next.Extension.Present || len(next.Extension.Rows) != len(old.Extension.Rows)+1 {
		return false
	}
	if old.Extension.Present && old.Extension.OID != next.Extension.OID {
		return false
	}
	return reflect.DeepEqual(next.Extension.Rows[:len(old.Extension.Rows)], old.Extension.Rows)
}
func validateHistoryChain(h *LedgerHistoryEvidence) error {
	if h == nil || h.Format != LedgerHistoryFormat || len(h.Records) == 0 {
		return historyError()
	}
	for i, r := range h.Records {
		s := r.State
		if r.SHA != transitionDigest(s) || s.Format != LedgerHistoryFormat || s.Step != i || validateTransitionTarget(s.Target) != nil || s.DatabaseOID == 0 || s.TransitionID == "" || !checksumPattern.MatchString(s.TransitionSHA) || !checksumPattern.MatchString(s.BoundSHA) || !checksumPattern.MatchString(s.OperationSHA) || validateTransitionFrontier(s.Frontier) != nil || !s.Baseline.Present || len(s.Baseline.Rows) == 0 || !validHistoryLedger(s.Baseline) || !validHistoryLedger(s.Extension) {
			return historyError()
		}
		for _, row := range s.Baseline.Rows {
			if row.Profile != IdentityProfile {
				return historyError()
			}
		}
		if len(s.Extension.Rows) != len(s.Frontier.Entries) {
			return historyError()
		}
		for j, e := range s.Frontier.Entries {
			v := s.Extension.Rows[j]
			if v.Version != e.Version || v.Name != e.Name || v.Checksum != e.Checksum || v.Profile != e.Chain || v.Ordinal != e.Ordinal {
				return historyError()
			}
		}
		if i == 0 {
			if s.PreviousSHA != "" || s.OperationSHA != s.TransitionSHA {
				return historyError()
			}
		} else if s.PreviousSHA != h.Records[i-1].SHA || !historyPrefix(h.Records[i-1].State, s) {
			return historyError()
		}
	}
	return nil
}
func snapshotHistoryValid(s TransitionSnapshot) error {
	h := s.History
	if validateHistoryChain(h) != nil {
		return historyError()
	}
	last := h.Records[len(h.Records)-1].State
	if last.Target != s.Target || last.DatabaseOID != s.DatabaseOID || last.Frontier.BaselineManifestSHA != s.Frontier.BaselineManifestSHA || last.Frontier.BaselineLineageSHA != s.Frontier.BaselineLineageSHA || !reflect.DeepEqual(last.Frontier.Entries, s.Frontier.Entries) {
		return historyError()
	}
	count := 0
	for _, l := range s.Ledgers {
		var expected HistoryLedger
		switch l.Key {
		case s.Target.Schema + ".schema_migrations":
			expected = last.Baseline
		case s.Target.Schema + ".schema_extensions":
			expected = last.Extension
		default:
			return historyError()
		}
		if !expected.Present || l.OID != expected.OID || l.RowsSHA != expected.SnapshotSHA {
			return historyError()
		}
		count++
	}
	want := 1
	if last.Extension.Present {
		want++
	}
	if count != want {
		return historyError()
	}
	return nil
}
func boundHistoryValid(b BoundSecurityTransition, s TransitionSnapshot, operationSHA, manifestSHA string) error {
	if snapshotHistoryValid(s) != nil {
		return historyError()
	}
	h := s.History
	root := h.Records[0].State
	last := h.Records[len(h.Records)-1].State
	if root.Target != b.source.Target || root.DatabaseOID != b.source.DatabaseOID || root.TransitionID != b.approved.descriptor.ID || root.TransitionSHA != b.approved.sha || root.BoundSHA != b.boundSHA || !reflect.DeepEqual(root.Frontier, b.source.Frontier) || last.OperationSHA != operationSHA || last.Frontier.ExtensionManifestSHA != manifestSHA {
		return historyError()
	}
	for _, l := range b.source.Ledgers {
		v := root.Baseline
		if l.Key == s.Target.Schema+".schema_extensions" {
			v = root.Extension
		}
		if !v.Present || l.OID != v.OID || l.RowsSHA != v.SnapshotSHA {
			return historyError()
		}
	}
	if len(b.source.Ledgers) == 1 && root.Extension.Present {
		return historyError()
	}
	return nil
}
func collectHistoryLedger(ctx context.Context, tx pgx.Tx, schema, name string) (HistoryLedger, error) {
	l := HistoryLedger{Rows: []LedgerHistoryRow{}}
	var oid *uint32
	if e := tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass($1)::oid`, pgx.Identifier{schema, name}.Sanitize()).Scan(&oid); e != nil {
		return l, historyUnavailable(e)
	}
	if oid == nil {
		return l, nil
	}
	l.Present = true
	l.OID = *oid
	rows, e := tx.Query(ctx, `SELECT version,name,checksum,profile,ordinal,CASE WHEN pg_catalog.isfinite(applied_at) THEN ((extract(epoch FROM applied_at)-946684800)*1000000)::bigint END FROM `+pgx.Identifier{schema, name}.Sanitize()+` ORDER BY ordinal`)
	if e != nil {
		return l, historyUnavailable(e)
	}
	for rows.Next() {
		var r LedgerHistoryRow
		if e = rows.Scan(&r.Version, &r.Name, &r.Checksum, &r.Profile, &r.Ordinal, &r.AppliedAtPGMicros); e != nil {
			rows.Close()
			return l, historyError()
		}
		l.Rows = append(l.Rows, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return l, historyUnavailable(e)
	}
	l.SnapshotSHA, e = collectSecurityTableRows(ctx, tx, schema, name)
	if e != nil {
		return l, historyUnavailable(e)
	}
	if !validHistoryLedger(l) {
		return l, historyError()
	}
	return l, nil
}

// The same canonical context as collector row snapshots, restored even on error.
func currentHistoryState(ctx context.Context, tx pgx.Tx, t Target, oid uint32) (result LedgerHistoryState, err error) {
	saved, e := readSecurityContext(ctx, tx)
	if e != nil {
		return result, historyUnavailable(e)
	}
	defer func() {
		if e := writeSecurityContext(ctx, tx, saved); e != nil {
			result = LedgerHistoryState{}
			err = historyUnavailable(e)
		}
	}()
	if e := writeSecurityContext(ctx, tx, securityCollectionContext{"pg_catalog", "UTC", "off"}); e != nil {
		return result, historyUnavailable(e)
	}
	result.Format = LedgerHistoryFormat
	result.DatabaseOID = oid
	result.Baseline, err = collectHistoryLedger(ctx, tx, t.Schema, "schema_migrations")
	if err != nil {
		return result, err
	}
	result.Extension, err = collectHistoryLedger(ctx, tx, t.Schema, "schema_extensions")
	return result, err
}
func loadLedgerHistory(ctx context.Context, tx pgx.Tx, id string) (*LedgerHistoryEvidence, error) {
	rows, e := tx.Query(ctx, `SELECT step,checkpoint_sha256,payload FROM tremor_deployment.ledger_history_v1 WHERE transition_id=$1 ORDER BY step`, id)
	if e != nil {
		return nil, historyUnavailable(e)
	}
	h := &LedgerHistoryEvidence{Format: LedgerHistoryFormat, Records: []LedgerHistoryRecord{}}
	for rows.Next() {
		var step int
		var r LedgerHistoryRecord
		var raw string
		if e = rows.Scan(&step, &r.SHA, &raw); e != nil {
			rows.Close()
			return nil, historyError()
		}
		if step != len(h.Records) || strictJSON([]byte(raw), &r.State) != nil || transitionJSONShape([]byte(raw), reflect.TypeOf(r.State)) != nil {
			rows.Close()
			return nil, historyError()
		}
		canonical, _ := json.Marshal(r.State)
		if raw != string(canonical) {
			rows.Close()
			return nil, historyError()
		}
		h.Records = append(h.Records, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, historyUnavailable(e)
	}
	if validateHistoryChain(h) != nil {
		return nil, historyError()
	}
	var foreign int
	if e := tx.QueryRow(ctx, "SELECT count(*) FROM tremor_deployment.ledger_history_v1 WHERE transition_id<>$1", id).Scan(&foreign); e != nil {
		return nil, historyUnavailable(e)
	}
	if foreign != 0 {
		return nil, historyError()
	}
	return h, nil
}
func readHistoryEvidence(ctx context.Context, tx pgx.Tx, t Target, s TransitionSnapshot, r TransitionReceipt) (*LedgerHistoryEvidence, error) {
	h, e := loadLedgerHistory(ctx, tx, r.TransitionID)
	if e != nil {
		return nil, e
	}
	root := h.Records[0].State
	if root.TransitionID != r.TransitionID || root.Target != s.Target || root.DatabaseOID != s.DatabaseOID || root.TransitionSHA != r.DescriptorSHA || root.BoundSHA != r.BoundSnapshotSHA || transitionDigest(root.Frontier) != r.FrontierSHA {
		return nil, historyError()
	}
	s.History = h
	if snapshotHistoryValid(s) != nil {
		return nil, historyError()
	}
	current, e := currentHistoryState(ctx, tx, t, s.DatabaseOID)
	if e != nil {
		return nil, e
	}
	last := h.Records[len(h.Records)-1].State
	if !reflect.DeepEqual(current.Baseline, last.Baseline) || !reflect.DeepEqual(current.Extension, last.Extension) {
		return nil, historyError()
	}
	return h, nil
}

// Only approved execution calls this writer, in its existing DDL/receipt tx.
// expectedPrevious is obtained from verified evidence BEFORE migration SQL.
func recordLedgerHistory(ctx context.Context, tx pgx.Tx, t Target, b BoundSecurityTransition, frontier TransitionFrontier, operationSHA, expectedPrevious string) error {
	if !validBoundSecurityTransition(b) || t.User != "tremor_deployer" || !historyScope(b.approved.descriptor.FinalScope) {
		return historyError()
	}
	var count int
	if e := tx.QueryRow(ctx, `SELECT count(*) FROM tremor_deployment.ledger_history_v1 WHERE transition_id=$1`, b.approved.descriptor.ID).Scan(&count); e != nil {
		return historyError()
	}
	h := &LedgerHistoryEvidence{Format: LedgerHistoryFormat, Records: []LedgerHistoryRecord{}}
	if count > 0 {
		var e error
		h, e = loadLedgerHistory(ctx, tx, b.approved.descriptor.ID)
		if e != nil {
			return e
		}
		if h.Records[len(h.Records)-1].SHA != expectedPrevious {
			return historyError()
		}
	} else if expectedPrevious != "" || operationSHA != b.approved.sha || !reflect.DeepEqual(frontier, b.source.Frontier) {
		return historyError()
	}
	s, e := currentHistoryState(ctx, tx, t, b.source.DatabaseOID)
	if e != nil {
		return e
	}
	s.Target = b.source.Target
	s.TransitionID = b.approved.descriptor.ID
	s.TransitionSHA = b.approved.sha
	s.BoundSHA = b.boundSHA
	s.Step = count
	s.OperationSHA = operationSHA
	s.PreviousSHA = expectedPrevious
	s.Frontier = frontier
	r := LedgerHistoryRecord{SHA: transitionDigest(s), State: s}
	h.Records = append(h.Records, r)
	if validateHistoryChain(h) != nil {
		return historyError()
	}
	if count == 0 {
		for _, l := range b.source.Ledgers {
			v := s.Baseline
			if l.Key == t.Schema+".schema_extensions" {
				v = s.Extension
			}
			if l.OID != v.OID || l.RowsSHA != v.SnapshotSHA {
				return historyError()
			}
		}
	}
	raw, _ := json.Marshal(s)
	tag, e := tx.Exec(ctx, `INSERT INTO tremor_deployment.ledger_history_v1(transition_id,step,checkpoint_sha256,payload) VALUES($1,$2,$3,$4)`, s.TransitionID, s.Step, r.SHA, string(raw))
	if e != nil || tag.RowsAffected() != 1 {
		return historyError()
	}
	return nil
}
