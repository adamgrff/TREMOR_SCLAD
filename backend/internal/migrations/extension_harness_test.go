package migrations

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These probes are OUTSIDE ^TestExtensionIntegration and never run in PG suite.
func TestExtensionHarnessProbeA(t *testing.T) {
	if os.Getenv("E1A_OFFLINE_HARNESS_CHILD") != "yes" {
		t.Skip("offline child only")
	}
	t.Fatal("synthetic harness failure; no PostgreSQL")
}
func TestExtensionHarnessProbeB(t *testing.T) {
	if os.Getenv("E1A_OFFLINE_HARNESS_CHILD") != "yes" {
		t.Skip("offline child only")
	}
	t.Fatal("MUST_NOT_RUN after failfast")
}

func TestExtensionOfflineFailfastHarness(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if runtime.GOOS == "windows" {
		bash = `C:\Program Files\Git\bin\bash.exe`
		_, err = os.Stat(bash)
	}
	if err != nil {
		t.Fatal("bash required to verify actual Docker harness")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "test", "e1a", "run-suite.sh"))
	if err != nil {
		t.Fatal(err)
	}
	report := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use MSYS paths for Git Bash, not drive-relative C:/ paths interpreted by
	// shell utilities differently from native Windows child process arguments.
	shellPath := func(path string) string {
		path = filepath.ToSlash(path)
		if runtime.GOOS == "windows" && len(path) > 2 && path[1] == ':' {
			return "/" + strings.ToLower(path[:1]) + path[2:]
		}
		return path
	}
	cmd := exec.CommandContext(ctx, bash, shellPath(script), shellPath(binary), ".", "^TestExtensionHarnessProbe")
	cmd.Dir = report
	cmd.Env = append(os.Environ(), "E1A_OFFLINE_HARNESS_CHILD=yes")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("expected exact Go exit 1: %v %s", err, output)
	}
	text := string(output)
	for _, want := range []string{"synthetic harness failure; no PostgreSQL", "RESULT FAIL TestExtensionHarnessProbeA", "RESULT NOT_RUN TestExtensionHarnessProbeB", "INTEGRATION_EXIT=1 LOG_TEE_EXIT=0", "REPORT_AWK_EXIT=0 REPORT_SORT_EXIT=0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "RESULT PASS") || strings.Contains(text, "MUST_NOT_RUN") {
		t.Fatalf("false success or missing failfast: %s", text)
	}
	log, err := os.ReadFile(filepath.Join(report, "suite.log"))
	if err != nil || !strings.Contains(string(log), "synthetic harness failure; no PostgreSQL") {
		t.Fatalf("tee lost failure cause: %v", err)
	}
	t.Log("real child Go FAIL: exit=1; tee=0; first FAIL, second NOT_RUN; cause preserved")
}
