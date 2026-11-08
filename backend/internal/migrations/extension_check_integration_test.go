package migrations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// NOT RUN until a separately authorized disposable PG regression run.
func TestExtensionIntegrationCHECKLiteralRegression(t *testing.T) {
	for _, check := range []struct{ name, column string }{{"ledger_checksum_hex", "checksum"}, {"ledger_profile_clean", "profile"}} {
		t.Run(check.name, func(t *testing.T) {
			r, conn := extensionIntegrationDeployment(t)
			firstPath := crashFixture(t, r, conn)
			r = fixtureExtensionRunner(t, r.base.target, r.manifest, r.options)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// Positive: real PG18 ledger definitions and extension 025 are accepted.
			if _, err := r.Run(ctx, "apply"); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Run(ctx, "verify"); err != nil {
				t.Fatal(err)
			}
			path, plan := extensionFixture(t, r.base.manifest, r.manifest.plan.BaselineReadinessSHA256, 2)
			plan.Migrations[0] = r.manifest.plan.Migrations[0]
			sql, err := os.ReadFile(filepath.Join(filepath.Dir(firstPath), plan.Migrations[0].Name))
			if err != nil {
				t.Fatal(err)
			}
			// Both steps have a non-idempotent row effect; old admission MUST fail
			// this test by applying 026 and creating an extra receipt.
			plan.Migrations[1].SHA256 = digest(sql)
			for _, entry := range plan.Migrations {
				if err = os.WriteFile(filepath.Join(filepath.Dir(path), entry.Name), sql, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeExtensionPlan(t, path, plan)
			m, err := LoadExtension(path, r.base.manifest)
			if err != nil {
				t.Fatal(err)
			}
			r = fixtureExtensionRunner(t, r.base.target, m, r.options)
			var beforeDef, beforeExpr, kind string
			readCheck := func() (string, string, string, int) {
				var def, expr, typ string
				var count int
				err := conn.QueryRow(ctx, `SELECT pg_catalog.pg_get_constraintdef(oid),pg_catalog.pg_get_expr(conbin,conrelid),contype::text,(SELECT count(*) FROM pg_catalog.pg_constraint WHERE conrelid=$1::regclass AND contype='c') FROM pg_catalog.pg_constraint WHERE conrelid=$1::regclass AND conname=$2`, r.ledger(), check.name).Scan(&def, &expr, &typ, &count)
				if err != nil {
					t.Fatal(err)
				}
				return def, expr, typ, count
			}
			beforeDef, beforeExpr, kind, count := readCheck()
			if kind != "c" || count != 4 || !strings.Contains(beforeDef, "'^[0-9a-f]{64}$'") {
				t.Fatal("unexpected PG constraint fixture")
			}
			writeTestSQL(t, conn, `ALTER TABLE `+r.ledger()+` DROP CONSTRAINT `+check.name+`; ALTER TABLE `+r.ledger()+` ADD CONSTRAINT `+check.name+` CHECK (`+check.column+` ~ '.*'::text)`)
			afterDef, afterExpr, afterKind, afterCount := readCheck()
			if afterKind != kind || afterCount != count || afterDef != strings.Replace(beforeDef, "'^[0-9a-f]{64}$'", "'.*'", 1) {
				t.Fatalf("fixture must change ONLY literal: %s -> %s", beforeDef, afterDef)
			}
			if oldCheckNormalizationForTest(beforeExpr) != oldCheckNormalizationForTest(afterExpr) {
				t.Fatal("fixture does not reproduce former defect")
			}
			before := extensionDataSnapshot(t, conn, r.base.target.Schema)
			receipts := func() string {
				var value string
				if err := conn.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(e) ORDER BY ordinal)::text FROM `+r.ledger()+` e`).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			oldReceipts := receipts()
			if _, err = r.Run(ctx, "apply"); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("literal-only drift accepted: %v", err)
			}
			if !reflect.DeepEqual(before, extensionDataSnapshot(t, conn, r.base.target.Schema)) || oldReceipts != receipts() {
				t.Fatal("rejected apply changed rows or receipts")
			}
			// Restore the correct TEST CHECK, then prove pending 026 still works.
			writeTestSQL(t, conn, `ALTER TABLE `+r.ledger()+` DROP CONSTRAINT `+check.name+`; ALTER TABLE `+r.ledger()+` ADD CONSTRAINT `+check.name+` CHECK (`+check.column+` ~ '^[0-9a-f]{64}$'::text)`)
			state, err := r.Run(ctx, "apply")
			if err != nil || len(state.Applied) != 2 {
				t.Fatalf("positive 026: %+v %v", state, err)
			}
			var effect string
			if err = conn.QueryRow(ctx, `SELECT name FROM `+pgx.Identifier{r.base.target.Schema, "warehouses"}.Sanitize()+` WHERE name LIKE 'T0 crash effect%'`).Scan(&effect); err != nil || effect != "T0 crash effect-executed-executed" {
				t.Fatalf("expected one effect per step: %q %v", effect, err)
			}
			if _, err = r.Run(ctx, "verify"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
