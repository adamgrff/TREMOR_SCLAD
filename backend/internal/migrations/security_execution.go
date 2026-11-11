package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"reflect"
	"strings"
	"time"
)

// Separate release approval for cluster-global provisioning. Credentials are an
// independent administrative concern; new LOGIN roles start without a password.
type ApprovedSecurityRoles struct {
	target TransitionTarget
	sha    string
}

func LoadApprovedSecurityRoles(target TransitionTarget) (ApprovedSecurityRoles, error) {
	return approveSecurityRoles(target, nil)
}
func approveSecurityRoles(target TransitionTarget, hashes []string) (ApprovedSecurityRoles, error) {
	sha := transitionDigest([]any{"e1b-roles-v1", target})
	if validateTransitionTarget(target) != nil || !approvedHash(sha, hashes) {
		return ApprovedSecurityRoles{}, errors.New("role provisioning not release-approved")
	}
	return ApprovedSecurityRoles{target, sha}, nil
}

var securityRoleNames = []string{"tremor_deployer", "tremor_owner", "tremor_ledger_owner", "tremor_migrator", "tremor_executor", "tremor_api"}

func ProvisionSecurityRoles(ctx context.Context, target Target, a ApprovedSecurityRoles) error {
	if a.sha == "" || a.sha != transitionDigest([]any{"e1b-roles-v1", a.target}) || !securityTargetMatches(target, a.target) || target.User == "tremor_api" || target.User == "tremor_migrator" {
		return errors.New("unapproved provisioning target")
	}
	conn, _, err := target.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConnection(conn)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = checkSecurityDeployment(ctx, tx, target, a.target); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock($1)`, advisoryKey); err != nil {
		return err
	}
	for _, name := range securityRoleNames {
		login := name == "tremor_deployer" || name == "tremor_api" || name == "tremor_migrator"
		super := name == "tremor_deployer"
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_catalog.pg_roles WHERE rolname=$1)`, name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			attrs := " NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT"
			if login {
				attrs = " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT"
			}
			if super {
				attrs = " LOGIN SUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT"
			}
			if _, err = tx.Exec(ctx, "CREATE ROLE "+pgx.Identifier{name}.Sanitize()+attrs); err != nil {
				return errors.New("cannot provision approved role")
			}
		}
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT rolcanlogin=$2 AND rolsuper=$3 AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls AND NOT rolinherit AND NOT EXISTS(SELECT FROM pg_catalog.pg_auth_members m WHERE m.member=r.oid) FROM pg_catalog.pg_roles r WHERE rolname=$1`, name, login, super).Scan(&valid); err != nil || !valid {
			return errors.New("conflicting existing security role")
		}
	}
	// Separate provisioning capability: minimal local identity-read grant, not
	// part of the later business/ledger metadata transaction. No pg_monitor.
	if _, err = tx.Exec(ctx, "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO tremor_migrator"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func securityTargetMatches(t Target, b TransitionTarget) bool {
	return t.Profile == IdentityProfile && t.Database == b.Database && t.Schema == b.Schema && t.ServerID == b.ClusterID
}
func checkSecurityDeployment(ctx context.Context, tx pgx.Tx, t Target, b TransitionTarget) error {
	var deployment string
	if err := tx.QueryRow(ctx, `SELECT CASE WHEN count(*)=1 AND bool_and(singleton) THEN min(deployment_id::text) END FROM `+pgx.Identifier{t.Schema, "deployment_identity"}.Sanitize()).Scan(&deployment); err != nil || deployment != b.DeploymentID {
		return errors.New("security deployment identity mismatch")
	}
	return nil
}

// All hooks are package-private fault injection, never operator configuration.
type securityExecutionHooks struct {
	stage       func(string, pgx.Tx) error
	commit      func(context.Context, pgx.Tx) error
	afterCommit func(*pgx.Conn) error
}
type SecurityTransitionAnchor struct{ DescriptorSHA, FrontierSHA, LegacyReadinessSHA, MetadataSHA, ScopeSHA string }

func approvedTransitionAnchor(a ApprovedSecurityTransition) SecurityTransitionAnchor {
	d := a.descriptor
	return SecurityTransitionAnchor{a.sha, transitionDigest(d.Frontier), d.FinalReadinessSHA, transitionDigest(d.After), transitionDigest(d.FinalScope)}
}

type SecurityTransitionRunner struct {
	target   Target
	manifest *ExtensionManifest
	approved ApprovedSecurityTransition
	body     string
	bound    BoundSecurityTransition
	hooks    securityExecutionHooks
}

func NewSecurityTransition(target Target, m *ExtensionManifest, a ApprovedSecurityTransition, payload []byte) (*SecurityTransitionRunner, error) {
	if !historyScope(a.descriptor.FinalScope) || historyScope(a.descriptor.SourceScope) {
		return nil, historyError()
	}
	raw, _ := json.Marshal(a.descriptor)
	_, sha, err := DecodeSecurityTransition(raw, payload)
	if err != nil || a.sha == "" || sha != a.sha || !securityTargetMatches(target, a.descriptor.Target) || target.User != "tremor_deployer" || m == nil || m.base == nil || m.ManifestSHA256() != a.descriptor.Frontier.ExtensionManifestSHA || baselineDigest(m.base) != a.descriptor.Frontier.BaselineManifestSHA {
		return nil, errors.New("unapproved security execution")
	}
	if a.descriptor.SourceReadinessSHA != legacyReadiness(m, len(a.descriptor.Frontier.Entries)) {
		return nil, errors.New("source is not the unchanged legacy readiness anchor")
	}
	body, err := MigrationBody(payload)
	if err != nil {
		return nil, err
	}
	return &SecurityTransitionRunner{target: target, manifest: m, approved: a, body: body}, nil
}
func (r *SecurityTransitionRunner) Bound() (BoundSecurityTransition, error) {
	if !validBoundSecurityTransition(r.bound) {
		return BoundSecurityTransition{}, errors.New("no bound transition")
	}
	return r.bound, nil
}
func (b BoundSecurityTransition) SourceSnapshot() (TransitionSnapshot, error) {
	if !validBoundSecurityTransition(b) {
		return TransitionSnapshot{}, errors.New("invalid bound transition")
	}
	var s TransitionSnapshot
	raw, _ := json.Marshal(b.source)
	_ = json.Unmarshal(raw, &s)
	return s, nil
}
func securityStage(h securityExecutionHooks, stage string, tx pgx.Tx) error {
	if h.stage != nil {
		return h.stage(stage, tx)
	}
	return nil
}
func normalizeSecurityTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('TimeZone','UTC',true),pg_catalog.set_config('row_security','off',true)`)
	return err
}

func (r *SecurityTransitionRunner) Run(ctx context.Context) (SecurityTransitionState, error) {
	// A second call is observation-only; uncertain SQL is never resubmitted.
	if validBoundSecurityTransition(r.bound) {
		return RecoverSecurityTransition(ctx, r.target, r.manifest, r.bound)
	}
	before, err := CollectSecuritySnapshot(ctx, r.target, r.approved, CollectTransitionSource, r.manifest)
	if err != nil {
		return TransitionRejected, err
	}
	bound, err := BindCollectedSecurityTransition(r.approved, before)
	if err != nil {
		return TransitionRejected, err
	}
	r.bound = bound
	conn, id, err := r.target.connect(ctx)
	if err != nil {
		return TransitionRejected, err
	}
	defer closeConnection(conn)
	if uint32(id.DatabaseOID) != bound.source.DatabaseOID {
		return TransitionRejected, ErrUnsafe
	}
	if _, err = conn.Exec(ctx, `SELECT pg_catalog.pg_advisory_lock($1)`, advisoryKey); err != nil {
		return TransitionRejected, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite})
	if err != nil {
		return TransitionRejected, err
	}
	defer rollback(tx)
	// Lock every approved existing relation; advisory locks alone do not stop DML.
	for _, o := range bound.source.Metadata {
		if o.Kind == "table" {
			if _, e := tx.Exec(ctx, "LOCK TABLE "+qualifiedSecurityKey(o.Key)+" IN ACCESS EXCLUSIVE MODE"); e != nil {
				return TransitionRejected, e
			}
		}
	}
	if err = normalizeSecurityTx(ctx, tx); err != nil {
		return TransitionRejected, err
	}
	d := r.approved.descriptor
	anchor := approvedTransitionAnchor(r.approved)
	baseOwner := ""
	extOwner := ""
	for _, obj := range d.Before {
		if obj.Key == r.target.Schema+".schema_migrations" {
			baseOwner = obj.Owner
		}
		if obj.Key == r.target.Schema+".schema_extensions" {
			extOwner = obj.Owner
		}
	}
	legacy, e := inspectSecurityLineage(ctx, tx, r.target, r.manifest, d.Target.DeploymentID, baseOwner, extOwner, true)
	if e != nil || len(legacy.Applied) != len(d.Frontier.Entries) {
		return TransitionRejected, errors.New("legacy lineage admission failed")
	}

	actual, err := collectSecuritySnapshotTx(ctx, tx, r.target, uint32(id.DatabaseOID), d, d.SourceScope, r.manifest)
	if err != nil || actual.receipt != nil || !reflect.DeepEqual(actual.snapshot, bound.source) {
		return TransitionRejected, errors.New("source changed under deployment lock")
	}
	if err = securityStage(r.hooks, "before_changes", tx); err == nil {
		_, err = tx.Exec(ctx, r.body, pgx.QueryExecModeSimpleProtocol)
	}
	if err == nil {
		err = securityStage(r.hooks, "after_changes", tx)
	}
	var final CollectedSecuritySnapshot
	if err == nil {
		final, err = collectSecuritySnapshotTx(ctx, tx, r.target, uint32(id.DatabaseOID), d, d.FinalScope, r.manifest)
	}
	if err == nil {
		err = compareSecurityTransitionEffects(bound, final.snapshot)
		if err == nil && (transitionDigest(final.snapshot.Metadata) != anchor.MetadataSHA || final.snapshot.ScopeSHA != anchor.ScopeSHA || final.snapshot.ReadinessSHA != anchor.LegacyReadinessSHA) {
			err = ErrUnsafe
		}
		if err == nil {
			err = foundationSecurity(final)
		}
	}
	if err == nil && final.receipt != nil {
		err = errors.New("unexpected pre-existing transition receipt")
	}
	if err == nil {
		err = securityStage(r.hooks, "before_receipt", tx)
	}
	receipt := bound.ExpectedReceipt()
	if err == nil {
		tag, e := tx.Exec(ctx, `INSERT INTO tremor_deployment.security_transitions(transition_id,descriptor_sha256,payload_sha256,bound_snapshot_sha256,frontier_sha256) VALUES($1,$2,$3,$4,$5)`, receipt.TransitionID, receipt.DescriptorSHA, receipt.PayloadSHA, receipt.BoundSnapshotSHA, receipt.FrontierSHA)
		err = e
		if err == nil && tag.RowsAffected() != 1 {
			err = ErrUnsafe
		}
	}
	if err == nil {
		err = recordLedgerHistory(ctx, tx, r.target, bound, d.Frontier, r.approved.sha, "")
	}
	if err == nil {
		err = securityStage(r.hooks, "after_receipt", tx)
	}
	if err == nil {
		final, err = collectSecuritySnapshotTx(ctx, tx, r.target, uint32(id.DatabaseOID), d, d.FinalScope, r.manifest)
	}
	if err == nil && (final.receipt == nil || *final.receipt != receipt) {
		err = ErrUnsafe
	}
	if err == nil {
		err = CompareSecurityTransition(bound, final.snapshot)
	}
	if err != nil {
		return TransitionRejected, fmt.Errorf("security transition rolled back: %w", err)
	}
	if r.hooks.commit != nil {
		err = r.hooks.commit(ctx, tx)
	} else {
		err = tx.Commit(ctx)
	}
	if err != nil {
		closeConnection(conn)
		return RecoverSecurityTransition(ctx, r.target, r.manifest, bound)
	}
	if r.hooks.afterCommit != nil {
		if err = r.hooks.afterCommit(conn); err != nil {
			closeConnection(conn)
			return RecoverSecurityTransition(ctx, r.target, r.manifest, bound)
		}
	}
	closeConnection(conn)
	return RecoverSecurityTransition(ctx, r.target, r.manifest, bound)
}

// This path contains observations only and never an apply callback.
func RecoverSecurityTransition(ctx context.Context, t Target, m *ExtensionManifest, b BoundSecurityTransition) (SecurityTransitionState, error) {
	if !validBoundSecurityTransition(b) || !securityTargetMatches(t, b.approved.descriptor.Target) || !historyScope(b.approved.descriptor.FinalScope) {
		return TransitionRejected, ErrUnsafe
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	final, err := CollectSecuritySnapshot(ctx, t, b.approved, CollectTransitionFinal, m)
	if errors.Is(err, ErrLedgerHistory) {
		return TransitionRejected, err
	}
	if err == nil {
		result := ClassifyCollectedTransitionRecovery(b, final)
		if result == TransitionFinalConfirmed {
			return TransitionProtected, nil
		}
		if result == TransitionEvidenceRejected {
			return TransitionRejected, ErrUnsafe
		}
	}
	source, e := CollectSecuritySnapshot(ctx, t, b.approved, CollectTransitionSource, m)
	if e == nil && ClassifyCollectedTransitionRecovery(b, source) == TransitionSourceConfirmed {
		return TransitionLegacyVerified, fmt.Errorf("%w: source confirmed; no replay", ErrCommitUnknown)
	}
	if e == nil {
		return TransitionRejected, ErrUnsafe
	}
	return TransitionUnknownCommit, fmt.Errorf("%w: security evidence unavailable", ErrCommitUnknown)
}

func qualifiedSecurityKey(key string) string {
	return pgx.Identifier(strings.Split(key, ".")).Sanitize()
}

func foundationSecurity(c CollectedSecuritySnapshot) error {
	sequences := map[string]bool{}
	for _, o := range c.snapshot.Metadata {
		if o.Kind == "sequence" {
			sequences[o.Key] = true
		}
	}
	for _, p := range c.privileges {
		if p.Actor != "tremor_api" && p.Actor != "tremor_migrator" && p.Actor != "tremor_executor" {
			continue
		}
		if p.Allowed {
			if sequences[p.Object] && p.Privilege == "USAGE" {
				return errors.New("foundation role can advance an identity sequence")
			}
			switch p.Privilege {
			case "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN", "CREATE", "TEMPORARY", "EXECUTE":
				return errors.New("foundation role has write/DDL privileges")
			}
		}
	}
	for _, o := range c.snapshot.Metadata {
		if o.Key != "role:tremor_api" && o.Key != "role:tremor_migrator" && o.Key != "role:tremor_executor" {
			continue
		}
		for _, p := range o.ParameterPrivileges {
			if p.AlterSystem || p.AlterSystemGrantOption || p.SetGrantOption || (p.Context != "user" && p.Set) {
				return errors.New("foundation role has elevated parameter privileges")
			}
		}
	}
	return nil
}
