package migrations

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A real fresh test process, never the shipped CLI (whose release approvals are
// deliberately empty). Only the separately approved disposable test fixture can
// invoke its private test approval helper. No production bypass is compiled.
func extensionRestartChild(t *testing.T, r *ExtensionRunner, path string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestExtensionIntegrationRestartChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "E1A_RESTART_CHILD=yes", "E1A_RESTART_SCHEMA="+r.base.target.Schema, "E1A_RESTART_MANIFEST="+path, "E1A_RESTART_DEPLOYMENT="+r.options.DeploymentID)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restart child: %v %s", err, output)
	}
}
func TestExtensionIntegrationRestartChild(t *testing.T) {
	if os.Getenv("E1A_RESTART_CHILD") != "yes" || os.Getenv("EXTENSION_TEST_DISPOSABLE_APPROVED") != "yes" {
		t.Skip("NOT RUN: approved parent disposable fixture required")
	}
	target := testTarget(t)
	target.Profile = IdentityProfile
	target.Schema = os.Getenv("E1A_RESTART_SCHEMA")
	if !strings.HasPrefix(target.Schema, "migration_runner_") || !schemaName.MatchString(target.Schema) {
		t.Fatal("invalid parent fixture schema")
	}
	m, err := LoadExtension(os.Getenv("E1A_RESTART_MANIFEST"), extensionBase(t))
	if err != nil {
		t.Fatal(err)
	}
	r := fixtureExtensionRunner(t, target, m, ExtensionOptions{DeploymentID: os.Getenv("E1A_RESTART_DEPLOYMENT"), BaselineOwner: target.User, LedgerOwner: target.User})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if state, err := r.Run(ctx, "apply"); err != nil || len(state.Applied) != 1 {
		t.Fatalf("child resume %+v %v", state, err)
	}
	if _, err := r.Run(ctx, "verify"); err != nil {
		t.Fatal(err)
	}
}
func TestExtensionIntegrationCHECKMapping(t *testing.T) {
	r, conn := extensionIntegrationDeployment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := r.Run(ctx, "apply"); err != nil {
		t.Fatal(err)
	}
	// The old implementation accepted this expression under another valid name.
	writeTestSQL(t, conn, `ALTER TABLE `+r.ledger()+` DROP CONSTRAINT ledger_order_positive; ALTER TABLE `+r.ledger()+` ADD CONSTRAINT ledger_order_positive CHECK(version>24)`)
	if _, err := r.Run(ctx, "status"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("wrong named CHECK accepted: %v", err)
	}
}
