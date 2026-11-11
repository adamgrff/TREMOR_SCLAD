package migrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"reflect"
)

// Each independently approved plan permits exactly ONE next extension. Approval
// of a later step is separate; ordinary legacy approvals/comparators are untouched.
type ProtectedCheckpoint struct {
	Frontier     TransitionFrontier   `json:"frontier"`
	Scope        TransitionScope      `json:"scope"`
	Metadata     []TransitionMetadata `json:"metadata"`
	ReadinessSHA string               `json:"readiness_sha256"`
}
type ProtectedMigrationPlan struct {
	Protocol      string              `json:"protocol"`
	TransitionSHA string              `json:"transition_sha256"`
	ManifestSHA   string              `json:"manifest_sha256"`
	Before        ProtectedCheckpoint `json:"before"`
	After         ProtectedCheckpoint `json:"after"`
	PriorManifest ExtensionPlan       `json:"prior_manifest"`
}
type ApprovedProtectedMigration struct {
	plan ProtectedMigrationPlan
	sha  string
}

func LoadApprovedProtectedMigration(raw []byte) (ApprovedProtectedMigration, error) {
	return approveProtectedMigration(raw, nil)
}
func approveProtectedMigration(raw []byte, hashes []string) (ApprovedProtectedMigration, error) {
	var p ProtectedMigrationPlan
	if strictJSON(raw, &p) != nil || transitionJSONShape(raw, reflect.TypeOf(p)) != nil {
		return ApprovedProtectedMigration{}, ErrUnsafe
	}
	sha := transitionDigest(p)
	if p.Protocol != "e1b-protected-apply-v1" || !approvedHash(sha, hashes) {
		return ApprovedProtectedMigration{}, ErrExtensionUnapproved
	}
	return ApprovedProtectedMigration{p, sha}, nil
}
func protectedPolicyDescriptor(b BoundSecurityTransition, c ProtectedCheckpoint) SecurityTransitionDescriptor {
	d := b.approved.descriptor
	d.Frontier = c.Frontier
	return d
}
func checkProtectedCheckpoint(ctx context.Context, tx pgx.Tx, t Target, m *ExtensionManifest, b BoundSecurityTransition, c ProtectedCheckpoint, oid uint32, historyOperation, historyManifest string) (CollectedSecuritySnapshot, ExtensionState, error) {
	state, err := inspectSecurityLineage(ctx, tx, t, m, b.source.Target.DeploymentID, "tremor_ledger_owner", "tremor_ledger_owner", false)
	if err != nil {
		return CollectedSecuritySnapshot{}, state, err
	}
	got, err := collectSecuritySnapshotTx(ctx, tx, t, oid, protectedPolicyDescriptor(b, c), c.Scope, m)
	if err != nil {
		return got, state, err
	}
	if err = foundationSecurity(got); err != nil {
		return got, state, err
	}
	if boundHistoryValid(b, got.snapshot, historyOperation, historyManifest) != nil {
		return got, state, historyError()
	}
	receipt := b.ExpectedReceipt()
	if got.receipt == nil || *got.receipt != receipt || !reflect.DeepEqual(got.snapshot.Metadata, c.Metadata) || got.snapshot.ReadinessSHA != c.ReadinessSHA {
		return got, state, ErrUnsafe
	}
	return got, state, nil
}
func ApplyProtectedExtension(ctx context.Context, t Target, m *ExtensionManifest, b BoundSecurityTransition, a ApprovedProtectedMigration) error {
	p := a.plan
	k := len(p.Before.Frontier.Entries)
	if !validBoundSecurityTransition(b) || t.User != "tremor_deployer" || !securityTargetMatches(t, b.source.Target) || a.sha == "" || a.sha != transitionDigest(p) || p.TransitionSHA != b.approved.sha || m == nil || m.base == nil || p.ManifestSHA != m.ManifestSHA256() || p.Before.Frontier.ExtensionManifestSHA != p.ManifestSHA || p.After.Frontier.ExtensionManifestSHA != p.ManifestSHA || k != len(b.source.Frontier.Entries) || len(p.After.Frontier.Entries) != k+1 || len(m.plan.Migrations) != k+1 || m.plan.BaselineSHA256 != b.source.Frontier.BaselineManifestSHA || p.Before.ReadinessSHA != b.approved.descriptor.FinalReadinessSHA || !reflect.DeepEqual(p.Before.Metadata, b.approved.descriptor.After) {
		return ErrUnsafe
	}
	if !reflect.DeepEqual(p.Before.Scope, b.approved.descriptor.FinalScope) || !reflect.DeepEqual(p.Before.Frontier.Entries, b.source.Frontier.Entries) {
		return ErrUnsafe
	}
	for _, c := range []ProtectedCheckpoint{p.Before, p.After} {
		if validateTransitionScope(c.Scope, b.source.Target, c.Metadata) != nil || !checksumPattern.MatchString(c.ReadinessSHA) || c.Frontier.BaselineLineageSHA != b.source.Frontier.BaselineLineageSHA || c.Frontier.BaselineManifestSHA != baselineDigest(m.base) {
			return ErrUnsafe
		}
		rows := []Applied{}
		for i, e := range c.Frontier.Entries {
			if i >= len(m.plan.Migrations) || e != (TransitionEntry{m.plan.Migrations[i].Version, m.plan.Migrations[i].Name, m.plan.Migrations[i].SHA256, m.chains[i], i + 1}) {
				return ErrUnsafe
			}
			rows = append(rows, Applied{Entry: m.plan.Migrations[i].Entry, Profile: m.chains[i]})
		}
		if m.prefix(rows) != nil {
			return ErrUnsafe
		}
	}
	if transitionDigest(p.PriorManifest) != b.source.Frontier.ExtensionManifestSHA || p.PriorManifest.BaselineReadinessSHA256 != m.plan.BaselineReadinessSHA256 || p.PriorManifest.BaselineSHA256 != m.plan.BaselineSHA256 || len(p.PriorManifest.Migrations) > len(m.plan.Migrations) {
		return ErrUnsafe
	}
	for i, old := range p.PriorManifest.Migrations {
		if old != m.plan.Migrations[i] {
			return ErrUnsafe
		}
	}
	if k == 0 && m.plan.BaselineReadinessSHA256 != b.approved.descriptor.SourceReadinessSHA {
		return ErrUnsafe
	}
	conn, id, err := t.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConnection(conn)
	if uint32(id.DatabaseOID) != b.source.DatabaseOID {
		return ErrUnsafe
	}
	if _, err = conn.Exec(ctx, `SELECT pg_catalog.pg_advisory_lock($1)`, advisoryKey); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, o := range p.Before.Metadata {
		if o.Kind == "table" {
			if _, err = tx.Exec(ctx, "LOCK TABLE "+qualifiedSecurityKey(o.Key)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				return err
			}
		}
	}
	var applied int
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass($1) IS NOT NULL`, t.Schema+".schema_extensions").Scan(&exists); err != nil {
		return err
	}
	if exists {
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t.Schema, "schema_extensions"}.Sanitize()).Scan(&applied); err != nil {
			return err
		}
	}
	if applied == k+1 {
		_, _, err = checkProtectedCheckpoint(ctx, tx, t, m, b, p.After, uint32(id.DatabaseOID), a.sha, p.ManifestSHA)
		return err
	}
	if applied != k {
		return ErrUnsafe
	}

	preflight, state, err := checkProtectedCheckpoint(ctx, tx, t, m, b, p.Before, uint32(id.DatabaseOID), b.approved.sha, b.source.Frontier.ExtensionManifestSHA)
	if err != nil {
		return err
	}
	before, err := securityLedgerSnapshot(ctx, tx, t, m, state)
	if err != nil {
		return err
	}
	ledger := pgx.Identifier{t.Schema, "schema_extensions"}.Sanitize()
	if !exists {
		sql := `CREATE TABLE ` + ledger + `(version integer PRIMARY KEY CONSTRAINT ledger_version_positive CHECK(version>24),name text NOT NULL UNIQUE,checksum text NOT NULL CONSTRAINT ledger_checksum_hex CHECK(checksum ~ '^[0-9a-f]{64}$'),profile text NOT NULL CONSTRAINT ledger_profile_clean CHECK(profile ~ '^[0-9a-f]{64}$'),ordinal integer NOT NULL UNIQUE CONSTRAINT ledger_order_positive CHECK(ordinal>0),applied_at timestamptz NOT NULL DEFAULT pg_catalog.clock_timestamp()); COMMENT ON TABLE ` + ledger + ` IS 'TREMOR extension ledger v1; ` + baselineDigest(m.base) + `; ` + b.source.Target.DeploymentID + `'; ALTER TABLE ` + ledger + ` OWNER TO tremor_ledger_owner; REVOKE ALL ON ` + ledger + ` FROM PUBLIC,tremor_api,tremor_executor,tremor_migrator; GRANT SELECT ON ` + ledger + ` TO tremor_migrator;`
		if _, err = tx.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{t.Schema}.Sanitize()+",pg_catalog"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, m.bodies[k], pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}
	step := m.plan.Migrations[k]
	tag, err := tx.Exec(ctx, "INSERT INTO "+ledger+"(version,name,checksum,profile,ordinal) VALUES($1,$2,$3,$4,$5)", step.Version, step.Name, step.SHA256, m.chains[k], step.Order)
	if err != nil || tag.RowsAffected() != 1 {
		return errors.New("protected migration receipt failure")
	}
	previousSHA := preflight.snapshot.History.Records[len(preflight.snapshot.History.Records)-1].SHA
	if err = recordLedgerHistory(ctx, tx, t, b, p.After.Frontier, a.sha, previousSHA); err != nil {
		return err
	}
	_, post, err := checkProtectedCheckpoint(ctx, tx, t, m, b, p.After, uint32(id.DatabaseOID), a.sha, p.ManifestSHA)
	if err != nil {
		return err
	}
	expected, err := securityLedgerSnapshot(ctx, tx, t, m, post)
	if err != nil {
		return err
	}
	if err = preservedLedgers(before, expected, 1, true); err != nil {
		return err
	}
	commitErr := tx.Commit(ctx)
	closeConnection(conn)
	// Always reconnect: this routine has no migration execution/retry callback.
	fresh, fid, e := t.connect(ctx)
	if e != nil {
		return ErrCommitUnknown
	}
	defer closeConnection(fresh)
	if fid.DatabaseOID != id.DatabaseOID {
		return ErrCommitUnknown
	}
	read, e := fresh.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return ErrCommitUnknown
	}
	defer rollback(read)
	_, durable, e := checkProtectedCheckpoint(ctx, read, t, m, b, p.After, uint32(fid.DatabaseOID), a.sha, p.ManifestSHA)
	if e != nil {
		return ErrCommitUnknown
	}
	actual, e := securityLedgerSnapshot(ctx, read, t, m, durable)
	if e != nil || preservedLedgers(expected, actual, 0, true) != nil {
		return ErrCommitUnknown
	}
	_ = commitErr
	return nil
}

// Diagnostic verification only. No SQL body, receipt writer or retry callback.
func VerifyProtectedExtension(ctx context.Context, t Target, m *ExtensionManifest, b BoundSecurityTransition, a ApprovedProtectedMigration) error {
	if !validBoundSecurityTransition(b) || (t.User != "tremor_migrator" && t.User != "tremor_deployer") || !securityTargetMatches(t, b.source.Target) || a.sha == "" || a.sha != transitionDigest(a.plan) || a.plan.TransitionSHA != b.approved.sha || m == nil || a.plan.ManifestSHA != m.ManifestSHA256() {
		return ErrUnsafe
	}
	conn, id, err := t.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConnection(conn)
	if uint32(id.DatabaseOID) != b.source.DatabaseOID {
		return ErrUnsafe
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	_, _, err = checkProtectedCheckpoint(ctx, tx, t, m, b, a.plan.After, uint32(id.DatabaseOID), a.sha, a.plan.ManifestSHA)
	return err
}
