package migrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// This is the separately admitted E1B inspection path. It does not construct an
// invalid legacy ExtensionRunner or relax its LedgerOwner == Target.User guard.
func inspectSecurityLineage(ctx context.Context, tx pgx.Tx, t Target, m *ExtensionManifest, deployment, baselineOwner, extensionOwner string, legacyWarehouse bool) (ExtensionState, error) {
	state := ExtensionState{Applied: []Applied{}, Pending: append([]ExtensionEntry{}, m.plan.Migrations...)}
	base, err := New(t, m.base)
	if err != nil {
		return state, err
	}
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog,"+pgx.Identifier{t.Schema}.Sanitize()); err != nil {
		return state, err
	}
	baseline, err := base.readState(ctx, tx)
	if err != nil || !baseline.Initialized || len(baseline.Pending) != 0 {
		return state, ErrIncomplete
	}
	if err = checkSecurityDeployment(ctx, tx, t, TransitionTarget{DeploymentID: deployment}); err != nil {
		return state, err
	}
	var owner string
	if err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_userbyid(relowner) FROM pg_catalog.pg_class WHERE oid=$1::regclass`, base.ledger()).Scan(&owner); err != nil || owner != baselineOwner {
		return state, ErrUnsafe
	}
	var oid int64
	var kind, marker string
	var rls, force bool
	err = tx.QueryRow(ctx, `SELECT c.oid::bigint,c.relkind::text,COALESCE(pg_catalog.obj_description(c.oid,'pg_class'),''),pg_catalog.pg_get_userbyid(c.relowner),c.relrowsecurity,c.relforcerowsecurity FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='schema_extensions'`, t.Schema).Scan(&oid, &kind, &marker, &owner, &rls, &force)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return state, err
	}
	if err == nil {
		if kind != "r" || owner != extensionOwner || marker != "TREMOR extension ledger v1; "+baselineDigest(m.base)+"; "+deployment || rls || force {
			return state, ErrUnsafe
		}
		if err = base.checkLedgerShape(ctx, tx, oid); err != nil {
			return state, err
		}
		var valid bool
		err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*)=4 AND bool_and(convalidated AND conenforced) FROM pg_catalog.pg_constraint WHERE conrelid=$1 AND contype='c')
 AND (SELECT count(*)=3 AND bool_and(indisvalid AND indisready) FROM pg_catalog.pg_index WHERE indrelid=$1)
 AND NOT EXISTS(SELECT FROM pg_catalog.pg_inherits WHERE inhrelid=$1)
 AND (SELECT relpersistence='p' FROM pg_catalog.pg_class WHERE oid=$1)
 AND (SELECT count(*)=1 AND bool_and(a.attname='applied_at' AND pg_catalog.pg_get_expr(d.adbin,d.adrelid)='clock_timestamp()') FROM pg_catalog.pg_attrdef d JOIN pg_catalog.pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum WHERE d.adrelid=$1)`, oid).Scan(&valid)
		if err != nil || !valid {
			return state, ErrUnsafe
		}
		if err = checkExtensionChecks(ctx, tx, oid); err != nil {
			return state, err
		}
		rows, e := tx.Query(ctx, `SELECT version,name,checksum,profile,ordinal,applied_at FROM `+pgx.Identifier{t.Schema, extensionLedgerName}.Sanitize()+` ORDER BY ordinal`)
		if e != nil {
			return state, e
		}
		for rows.Next() {
			var a Applied
			if e = rows.Scan(&a.Version, &a.Name, &a.SHA256, &a.Profile, &a.Order, &a.AppliedAt); e != nil {
				rows.Close()
				return state, e
			}
			state.Applied = append(state.Applied, a)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return state, e
		}
		state.Initialized = true
	}
	if err = m.prefix(state.Applied); err != nil {
		return state, err
	}
	state.Pending = state.Pending[len(state.Applied):]
	if legacyWarehouse && !state.Initialized && len(state.Applied) == 0 {
		contract, e := loadWarehouseContract()
		if e != nil {
			return state, e
		}
		if e = base.checkWarehouseObjects(ctx, tx, contract); e != nil {
			return state, e
		}
	}
	return state, nil
}
func legacyReadiness(m *ExtensionManifest, prefix int) string {
	if prefix == 0 {
		return m.plan.BaselineReadinessSHA256
	}
	return m.plan.Migrations[prefix-1].ReadinessSHA256
}
func securityLedgerSnapshot(ctx context.Context, tx pgx.Tx, t Target, m *ExtensionManifest, state ExtensionState) (extensionLedgerSnapshot, error) {
	result := extensionLedgerSnapshot{}
	base, err := New(t, m.base)
	if err != nil {
		return result, err
	}
	b, err := base.readState(ctx, tx)
	if err != nil {
		return result, err
	}
	result.Baseline = b.Applied
	result.Extensions = append([]Applied{}, state.Applied...)
	if err = tx.QueryRow(ctx, ledgerMetadataSQL, base.ledger()).Scan(&result.BaselineOID, &result.BaselineOwner, &result.BaselineACL, &result.BaselineColumnACL); err != nil {
		return result, err
	}
	if state.Initialized {
		err = tx.QueryRow(ctx, ledgerMetadataSQL, pgx.Identifier{t.Schema, extensionLedgerName}.Sanitize()).Scan(&result.ExtensionOID, &result.ExtensionOwner, &result.ExtensionACL, &result.ExtensionColumnACL)
	}
	return result, err
}
