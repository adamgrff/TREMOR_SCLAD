package readiness

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RuntimePool never accepts a privileged identity or inherited fallback targets.
// The API receives only its own runtime config and TLS client identity. This does
// not install routes; Require must also wrap each future protected HTTP action.
func RuntimePool(ctx context.Context, cfg *pgxpool.Config, gate *Client, databaseOID uint32) (*pgxpool.Pool, error) {
	if cfg == nil || gate == nil || databaseOID == 0 || cfg.ConnConfig.User != "tremor_api" || cfg.ConnConfig.Database != gate.target.Database {
		return nil, errors.New("invalid protected runtime configuration")
	}
	copy := cfg.Copy()
	copy.BeforeConnect = nil
	copy.AfterRelease = nil
	copy.BeforeClose = nil
	copy.ConnConfig.Fallbacks = nil
	copy.ConnConfig.RuntimeParams = map[string]string{"application_name": "tremor-protected-api", "search_path": "pg_catalog," + pgx.Identifier{gate.target.Schema}.Sanitize(), "client_encoding": "UTF8", "standard_conforming_strings": "on", "default_transaction_read_only": "on"}
	check := func(ctx context.Context, conn *pgx.Conn) error {
		var user, db string
		var oid uint32
		if e := conn.QueryRow(ctx, `SELECT current_user,pg_catalog.current_database(),oid FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()`).Scan(&user, &db, &oid); e != nil || user != "tremor_api" || db != gate.target.Database || oid != databaseOID {
			return errors.New("runtime identity unavailable")
		}
		return checkRuntimeReadiness(ctx, gate)
	}
	copy.AfterConnect = check
	copy.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool { return check(ctx, conn) == nil }
	pool, e := pgxpool.NewWithConfig(ctx, copy)
	if e != nil {
		return nil, errors.New("protected database unavailable")
	}
	if e = pool.Ping(ctx); e != nil {
		pool.Close()
		return nil, errors.New("protected database unavailable")
	}
	return pool, nil
}
