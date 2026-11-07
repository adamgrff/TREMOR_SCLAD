package migrations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Never run by the offline test command. Even an existing MIGRATION_TEST_*
// configuration is insufficient: explicit disposable-cluster approval is needed.
func TestExtensionIntegrationResume(t *testing.T) {
	if os.Getenv("EXTENSION_TEST_DISPOSABLE_APPROVED") != "yes" {
		t.Skip("NOT RUN: separately approved disposable PostgreSQL 18 TEST target required")
	}
	base := identityRunner(t)
	ctx := context.Background()
	if _, err := base.Run(ctx, "apply"); err != nil {
		t.Fatal(err)
	}
	if _, err := base.BootstrapWarehouse(ctx, "E1A disposable warehouse"); err != nil {
		t.Fatal(err)
	}
	conn, _, err := base.target.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConnection(conn)
	snapshot := func() string {
		var value string
		if err := conn.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(m) ORDER BY ordinal)::text FROM `+base.ledger()+` m`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog,"+pgx.Identifier{base.target.Schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := extensionCatalog(ctx, tx, base.target.Schema)
	if err != nil {
		t.Fatal(err)
	}
	var deployment string
	if err = tx.QueryRow(ctx, `SELECT deployment_id::text FROM `+pgx.Identifier{base.target.Schema, "deployment_identity"}.Sanitize()).Scan(&deployment); err != nil {
		t.Fatal(err)
	}
	rollback(tx)
	path, plan := extensionFixture(t, base.manifest, fingerprint, 2)
	// Only in this explicitly approved disposable fixture, obtain the expected
	// catalogue for a harmless new table inside a transaction that is rolled back.
	probeSQL := "BEGIN;\nCREATE TABLE extension_probe(id integer PRIMARY KEY);\nCOMMIT;\n"
	probeTx, probeErr := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	if _, probeErr = probeTx.Exec(ctx, "SET LOCAL search_path TO pg_catalog,"+pgx.Identifier{base.target.Schema}.Sanitize()); probeErr != nil {
		t.Fatal(probeErr)
	}
	if _, probeErr = probeTx.Exec(ctx, "CREATE TABLE "+pgx.Identifier{base.target.Schema, "extension_probe"}.Sanitize()+"(id integer PRIMARY KEY)"); probeErr != nil {
		t.Fatal(probeErr)
	}
	extendedFingerprint, probeErr := extensionCatalog(ctx, probeTx, base.target.Schema)
	rollback(probeTx)
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	plan.Migrations[0].SHA256 = digest([]byte(probeSQL))
	plan.Migrations[0].ReadinessSHA256 = extendedFingerprint
	plan.Migrations[1].ReadinessSHA256 = extendedFingerprint
	if err = os.WriteFile(filepath.Join(filepath.Dir(path), plan.Migrations[0].Name), []byte(probeSQL), 0600); err != nil {
		t.Fatal(err)
	}
	writeExtensionPlan(t, path, plan)
	manifest, err := LoadExtension(path, base.manifest)
	if err != nil {
		t.Fatal(err)
	}
	options := ExtensionOptions{DeploymentID: deployment, BaselineOwner: base.target.User, LedgerOwner: base.target.User}
	runner := fixtureExtensionRunner(t, base.target, manifest, options)
	if state, err := runner.Run(ctx, "status"); err != nil || len(state.Pending) != 2 {
		t.Fatalf("status %+v %v", state, err)
	}
	if _, err = runner.Run(ctx, "verify"); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("incomplete verify %v", err)
	}
	runner.hooks.afterSQL = func(int, pgx.Tx) error { return errors.New("injected rollback") }
	if _, err = runner.Run(ctx, "apply"); err == nil {
		t.Fatal("fault silently succeeded")
	}
	runner.hooks.afterSQL = func(_ int, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE `+base.ledger()+` SET applied_at=applied_at+interval '1 second' WHERE ordinal=1`)
		return err
	}
	if _, err = runner.Run(ctx, "apply"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("baseline timestamp rewrite accepted: %v", err)
	}
	if snapshot() != before {
		t.Fatal("baseline rewrite not rolled back")
	}
	runner.hooks.afterSQL = nil
	runner.hooks.commit = func(ctx context.Context, version int, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return errors.New("simulated lost COMMIT reply")
	}
	state, err := runner.Run(ctx, "apply")
	if !errors.Is(err, ErrResume) || !state.CommitResolved || len(state.Applied) != 1 {
		t.Fatalf("resume %+v %v", state, err)
	}
	for _, column := range []string{"applied_at", "name"} {
		runner.hooks.afterSQL = func(_ int, tx pgx.Tx) error {
			expression := "applied_at+interval '1 second'"
			if column == "name" {
				expression = "'rewritten.sql'"
			}
			_, err := tx.Exec(ctx, `UPDATE `+runner.ledger()+` SET `+pgx.Identifier{column}.Sanitize()+`=`+expression+` WHERE ordinal=1`)
			return err
		}
		if _, err = runner.Run(ctx, "apply"); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("prior extension %s rewrite accepted: %v", column, err)
		}
	}
	runner.hooks.afterSQL = nil
	if state, err = runner.Run(ctx, "apply"); err != nil || len(state.Applied) != 2 || !state.CommitResolved {
		t.Fatalf("apply %+v %v", state, err)
	}
	runner.hooks.commit = nil
	if state, err = runner.Run(ctx, "apply"); err != nil || len(state.Applied) != 2 {
		t.Fatalf("duplicate %+v %v", state, err)
	}
	if _, err = runner.Run(ctx, "verify"); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before {
		t.Fatal("baseline ledger changed")
	}
	var warehouseCount int
	if conn.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{base.target.Schema, "warehouses"}.Sanitize()).Scan(&warehouseCount) != nil || warehouseCount != 1 {
		t.Fatal("business rows changed")
	}
	// Unknown catalogue changes must fail closed, even with a valid ledger.
	writeTestSQL(t, conn, `CREATE TABLE `+pgx.Identifier{base.target.Schema, "unapproved_probe"}.Sanitize()+`(id integer)`)
	if _, err = runner.Run(ctx, "apply"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unapproved catalogue accepted: %v", err)
	}
}
