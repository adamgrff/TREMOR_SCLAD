package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/migrations"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtensionCLIFailsBeforeConnection(t *testing.T) {
	for _, args := range [][]string{nil, {"invalid"}, {"apply"}, {"apply", "--database", "tremor_sklad"}, {"apply", "--sql", "SELECT 1"}, {"apply", "--profile", "clean-v1"}, {"apply", "--approved-manifest-sha256", strings.Repeat("a", 64)}, {"plan", "--approvals", "caller-controlled.json"}} {
		var out, diagnostics bytes.Buffer
		if code := run(context.Background(), args, &out, &diagnostics); code == 0 {
			t.Fatalf("unsafe CLI accepted %v", args)
		}
		if strings.Contains(diagnostics.String(), "SELECT 1") {
			t.Fatal("raw flags echoed")
		}
	}
}

func TestExtensionCLIPlanOffline(t *testing.T) {
	base, err := migrations.Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(base.Plan())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	plan := migrations.ExtensionPlan{Format: 1, ReadinessFormat: migrations.ExtensionReadinessFormat, Profile: migrations.ExtensionProfile, Canonicalization: migrations.Canon, BaselineSHA256: hex.EncodeToString(sum[:]), BaselineReadinessSHA256: strings.Repeat("a", 64), Migrations: []migrations.ExtensionEntry{}}
	raw, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	// No host, database, credentials or server identity: plan must remain offline.
	args := []string{"plan", "--profile", "clean-v2", "--manifest", path, "--baseline-manifest", filepath.Join("..", "..", "migrations", "manifest.clean-v2.json")}
	if code := run(context.Background(), args, &out, &diagnostics); code != 0 {
		t.Fatalf("offline plan: %d %s", code, diagnostics.String())
	}
	if !strings.Contains(out.String(), "manifest_sha256") {
		t.Fatal("approval fingerprint missing")
	}
}
