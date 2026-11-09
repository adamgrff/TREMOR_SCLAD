package migrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"strings"
)

const securityContextReadSQL = `SELECT pg_catalog.current_setting('search_path'),pg_catalog.current_setting('TimeZone'),pg_catalog.current_setting('row_security')`
const securityContextWriteSQL = `SELECT pg_catalog.set_config('search_path',$1,true),pg_catalog.set_config('TimeZone',$2,true),pg_catalog.set_config('row_security',$3,true)`
const securityPathReadSQL = `SELECT pg_catalog.current_setting('search_path')`
const securityPathRestoreSQL = `SELECT pg_catalog.set_config('search_path',$1,true)`

type securityCollectionContext struct{ path, timezone, rowSecurity string }

func readSecurityContext(ctx context.Context, tx pgx.Tx) (securityCollectionContext, error) {
	var c securityCollectionContext
	if err := tx.QueryRow(ctx, securityContextReadSQL).Scan(&c.path, &c.timezone, &c.rowSecurity); err != nil {
		return c, errors.New("cannot read collector context")
	}
	return c, nil
}
func writeSecurityContext(ctx context.Context, tx pgx.Tx, want securityCollectionContext) error {
	var got securityCollectionContext
	if err := tx.QueryRow(ctx, securityContextWriteSQL, want.path, want.timezone, want.rowSecurity).Scan(&got.path, &got.timezone, &got.rowSecurity); err != nil || got != want {
		return errors.New("cannot establish collector context")
	}
	return nil
}

// All internal callers get one canonical metadata/data context too. On every
// return, restore the caller's exact context. No failed restoration yields usable
// evidence. This is transaction-local state, not ALTER ROLE/DATABASE defaults.
func collectSecuritySnapshotTx(ctx context.Context, tx pgx.Tx, target Target, databaseOID uint32, d SecurityTransitionDescriptor, scope TransitionScope, m *ExtensionManifest) (result CollectedSecuritySnapshot, err error) {
	saved, err := readSecurityContext(ctx, tx)
	if err != nil {
		return CollectedSecuritySnapshot{}, err
	}
	defer func() {
		if restore := writeSecurityContext(ctx, tx, saved); restore != nil {
			result = CollectedSecuritySnapshot{}
			err = errors.Join(err, errors.New("cannot restore collector caller context"))
		}
	}()
	if err = writeSecurityContext(ctx, tx, securityCollectionContext{"pg_catalog", "UTC", "off"}); err != nil {
		return CollectedSecuritySnapshot{}, err
	}
	return collectSecuritySnapshotInContext(ctx, tx, target, databaseOID, d, scope, m)
}

// Legacy readiness is the unchanged E1A pg18-catalog-v2 projection. The approved
// Target.Schema is an identifier, never arbitrary descriptor SQL. Metadata stays
// in pg_catalog; only this projection temporarily uses E1A's exact visibility.
func collectorLegacyReadiness(ctx context.Context, tx pgx.Tx, schema string) (hash string, err error) {
	if !schemaName.MatchString(schema) || strings.HasPrefix(schema, "pg_") || schema == "information_schema" {
		return "", errors.New("invalid legacy readiness schema")
	}
	var saved string
	if err = tx.QueryRow(ctx, securityPathReadSQL).Scan(&saved); err != nil {
		return "", errors.New("cannot read readiness context")
	}
	defer func() {
		var restored string
		if restore := tx.QueryRow(ctx, securityPathRestoreSQL, saved).Scan(&restored); restore != nil || restored != saved {
			hash = ""
			err = errors.Join(err, errors.New("cannot restore legacy readiness context"))
		}
	}()
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog,"+pgx.Identifier{schema}.Sanitize()); err != nil {
		return "", errors.New("cannot establish legacy readiness context")
	}
	hash, err = extensionCatalog(ctx, tx, schema)
	if err != nil {
		return "", err
	}
	return hash, nil
}

// This is a security-metadata fingerprint, not legacy readiness, a data digest,
// release approval, or a transition anchor. It has no implicit admission effect.
func (c CollectedSecuritySnapshot) SecurityFingerprint() (string, error) {
	if !c.valid() {
		return "", errors.New("no completed security collection")
	}
	s := c.snapshot
	return transitionDigest(struct {
		Format      string
		Target      TransitionTarget
		DatabaseOID uint32
		ScopeSHA    string
		Metadata    []TransitionMetadata
		Objects     []TransitionObjectIdentity
		Parameters  []TransitionParameterIdentity
	}{"e1b-security-metadata-v1", s.Target, s.DatabaseOID, s.ScopeSHA, s.Metadata, s.ObjectIdentities, s.ParameterIdentities}), nil
}
