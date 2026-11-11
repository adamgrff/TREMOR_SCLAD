package migrations

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Runs immediately after transition, before the legitimate extension append.
func executionHistoryTransitionTest(t *testing.T, ctx context.Context, admin *pgx.Conn, target Target, m *ExtensionManifest, b BoundSecurityTransition) {
	t.Helper()
	observed, e := CollectSecuritySnapshot(ctx, target, b.approved, CollectTransitionFinal, m)
	if e != nil {
		t.Fatal(e)
	}
	if ClassifyCollectedTransitionRecovery(b, observed) != TransitionFinalConfirmed {
		t.Fatal("history root not confirmed")
	}
	snap, _ := observed.Snapshot()
	if snap.History == nil || len(snap.History.Records) != 1 {
		t.Fatal("missing first trusted history receipt")
	}
	t.Run("missing-offline-historical-evidence", func(t *testing.T) {
		bad := transitionClone(snap)
		bad.History = nil
		if CompareSecurityTransition(b, bad) == nil || ClassifyTransitionRecovery(b, &bad, observed.receipt) == TransitionFinalConfirmed {
			t.Fatal("missing history admitted")
		}
	})
	t.Run("canonical-timezone-precision", func(t *testing.T) {
		tx, e := admin.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if e != nil {
			t.Fatal(e)
		}
		defer rollback(tx)
		a, e := currentHistoryState(ctx, tx, target, snap.DatabaseOID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, "SET LOCAL TimeZone TO 'Pacific/Chatham'; SET LOCAL DateStyle TO 'German, DMY'", pgx.QueryExecModeSimpleProtocol); e != nil {
			t.Fatal(e)
		}
		c, e := currentHistoryState(ctx, tx, target, snap.DatabaseOID)
		if e != nil || !reflect.DeepEqual(a, c) {
			t.Fatal("timezone/format changed canonical history", e)
		}
		var zone string
		if e = tx.QueryRow(ctx, "SHOW TimeZone").Scan(&zone); e != nil || zone != "Pacific/Chatham" {
			t.Fatal("context not restored")
		}
	})
	t.Run("transition-recovery-rejects-historical-drift", func(t *testing.T) {
		var original time.Time
		if e := admin.QueryRow(ctx, "SELECT applied_at FROM public.schema_migrations WHERE ordinal=1").Scan(&original); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if _, e := admin.Exec(ctx, "UPDATE public.schema_migrations SET applied_at=$1 WHERE ordinal=1", original); e != nil {
				t.Fatal(e)
			}
		}()
		if _, e := admin.Exec(ctx, "UPDATE public.schema_migrations SET applied_at=applied_at+interval '1 microsecond' WHERE ordinal=1"); e != nil {
			t.Fatal(e)
		}
		state, e := RecoverSecurityTransition(ctx, target, m, b)
		if e == nil || state == TransitionProtected {
			t.Fatal("recovery reanchored corrupt history", state, e)
		}
		var count int
		if e = admin.QueryRow(ctx, "SELECT count(*) FROM tremor_deployment.ledger_history_v1").Scan(&count); e != nil || count != 1 {
			t.Fatal("recovery wrote history")
		}
	})
	t.Run("unchanged-history-root-repeat", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			s, e := RecoverSecurityTransition(ctx, target, m, b)
			if e != nil || s != TransitionProtected {
				t.Fatal(s, e)
			}
		}
	})
}
