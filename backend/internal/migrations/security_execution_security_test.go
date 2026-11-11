package migrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

func executionSecurityTest(t *testing.T, ctx context.Context, admin *pgx.Conn, cfg *pgx.ConnConfig, target Target, m *ExtensionManifest, b BoundSecurityTransition) {
	for _, role := range []string{"tremor_api", "tremor_migrator"} {
		t.Run("denials-"+role, func(t *testing.T) {
			c := cfg.Copy()
			c.User = role
			conn, e := pgx.ConnectConfig(ctx, c)
			if e != nil {
				t.Fatal(e)
			}
			defer closeConnection(conn)
			for _, sql := range []string{`INSERT INTO public.warehouses(name) VALUES('unauthorized')`, `TRUNCATE public.warehouses`, `CREATE TABLE public.unauthorized(id int)`, `DELETE FROM public.schema_migrations`, `DELETE FROM tremor_deployment.security_transitions`, `DELETE FROM tremor_deployment.ledger_history_v1`, `SET ROLE tremor_owner`, `SET ROLE tremor_ledger_owner`, `SELECT public.assembly_events_immutable()`} {
				_, e = conn.Exec(ctx, sql)
				pg, ok := e.(*pgconn.PgError)
				if !ok || pg.Code != "42501" {
					t.Fatalf("expected privilege denial for %s: %v", sql, e)
				}
			}
		})
	}
	t.Run("executor-ledger-denial", func(t *testing.T) {
		tx, e := admin.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
		if e != nil {
			t.Fatal(e)
		}
		defer rollback(tx)
		if _, e = tx.Exec(ctx, `SET LOCAL ROLE tremor_executor`); e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(ctx, `DELETE FROM public.schema_migrations`)
		pg, ok := e.(*pgconn.PgError)
		if !ok || pg.Code != "42501" {
			t.Fatal("executor ledger access", e)
		}
	})
	t.Run("protected-parameter-drift-denied", func(t *testing.T) {
		if _, e := admin.Exec(ctx, `GRANT SET ON PARAMETER session_replication_role TO tremor_api`); e != nil {
			t.Fatal(e)
		}
		defer func() { _, _ = admin.Exec(ctx, `REVOKE SET ON PARAMETER session_replication_role FROM tremor_api`) }()
		state, e := RecoverSecurityTransition(ctx, target, m, b)
		if e == nil || state == TransitionProtected {
			t.Fatal("parameter drift accepted")
		}
	})
}
