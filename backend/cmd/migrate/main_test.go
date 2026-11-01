package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanIsOfflineAndExplicit(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://password_should_not_appear@127.0.0.1/tremor_sklad")
	t.Setenv("PGHOST", "invalid-host")
	var output, diagnostics bytes.Buffer
	code := run(context.Background(), []string{"plan", "--profile", "clean-v1", "--manifest", "../../migrations/manifest.clean-v1.json"}, &output, &diagnostics)
	if code != 0 {
		t.Fatal(diagnostics.String())
	}
	var plan struct {
		Migrations []any
		Excluded   []any
		Gaps       []any
	}
	if json.Unmarshal(output.Bytes(), &plan) != nil || len(plan.Migrations) != 21 || len(plan.Excluded) != 1 || len(plan.Gaps) != 1 {
		t.Fatal("invalid offline plan")
	}
}

func TestCLIRejectsMissingTargetAndRedactsBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"apply", "--profile", "clean-v1", "--manifest", "../../migrations/manifest.clean-v1.json"},
		{"status", "--profile", "clean-v1", "--manifest", "../../migrations/manifest.clean-v1.json", "--database", "TrEmOr_SkLaD"},
		{"apply", "--password=do-not-print-this"},
		{"plan", "--profile", "demo"},
	} {
		var output, diagnostics bytes.Buffer
		if run(context.Background(), args, &output, &diagnostics) == 0 {
			t.Fatal("unsafe CLI invocation accepted")
		}
		if strings.Contains(diagnostics.String(), "do-not-print-this") {
			t.Fatal("password leaked in flag error")
		}
	}
}
