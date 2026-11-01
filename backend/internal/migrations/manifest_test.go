package migrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repositoryManifest(t *testing.T) *Manifest {
	t.Helper()
	m, err := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func copyManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range append(append([]string{}, schemaNames...), "manifest.clean-v1.json") {
		input, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), input, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "manifest.clean-v1.json")
}

func TestManifestProductionProfile(t *testing.T) {
	m := repositoryManifest(t)
	plan := m.Plan()
	if len(plan.Migrations) != 21 || plan.Excluded[0].Version != 2 || plan.Gaps[0].Version != 6 {
		t.Fatal("wrong clean plan")
	}
	for _, m := range plan.Migrations {
		if m.Version == 2 || m.Version == 6 {
			t.Fatal("excluded migration marked applied")
		}
	}
	plan.Migrations[0].SHA256 = "changed"
	if m.Plan().Migrations[0].SHA256 == "changed" {
		t.Fatal("mutable manifest snapshot")
	}
}

func TestManifestWindowsAndLFInputs(t *testing.T) {
	path := copyManifest(t)
	baseline := repositoryManifest(t).Plan()
	for _, entry := range baseline.Migrations {
		p := filepath.Join(filepath.Dir(path), entry.Name)
		input, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		lf, err := CanonicalSQL(input)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n")), 0600); err != nil {
			t.Fatal(err)
		}
	}
	windows, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range baseline.Migrations {
		if windows.Plan().Migrations[i] != entry {
			t.Fatal("CRLF/LF manifest differs")
		}
	}
}

func TestManifestRejectsDriftAndMetadata(t *testing.T) {
	for _, mode := range []string{"checksum", "order", "name", "version", "excluded", "gap", "format", "profile", "canonicalization", "sql-drift"} {
		t.Run(mode, func(t *testing.T) {
			path := copyManifest(t)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var plan Plan
			if json.Unmarshal(raw, &plan) != nil {
				t.Fatal("manifest decode")
			}
			switch mode {
			case "checksum":
				plan.Migrations[0].SHA256 = strings.Repeat("0", 64)
			case "order":
				plan.Migrations[0].Order = 2
			case "name":
				plan.Migrations[0].Name = "../../secret.sql"
			case "version":
				plan.Migrations[0].Version = 2
			case "excluded":
				plan.Excluded = nil
			case "gap":
				plan.Gaps = nil
			case "format":
				plan.Format = 2
			case "profile":
				plan.Profile = "demo"
			case "canonicalization":
				plan.Canonicalization = "raw"
			case "sql-drift":
				p := filepath.Join(filepath.Dir(path), plan.Migrations[0].Name)
				if err = os.WriteFile(p, []byte("BEGIN; SELECT 1; COMMIT;"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			updated, _ := json.Marshal(plan)
			if err = os.WriteFile(path, updated, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = Load(path); err == nil {
				t.Fatal("manifest drift accepted")
			}
		})
	}
}

func validTarget() Target {
	return Target{Host: "127.0.0.1", Port: 55517, Database: "tremor_migration_test", User: "test", Profile: Profile, Schema: "public", ServerAddress: "127.0.0.1", ServerID: "123456789", SSLMode: "disable"}
}

func TestTargetRejectsIncompleteAndProtected(t *testing.T) {
	for _, mode := range []string{"protected", "case", "spaces", "host", "port", "database", "user", "profile", "schema", "server-address", "server-id", "ssl", "remote-insecure"} {
		t.Run(mode, func(t *testing.T) {
			target := validTarget()
			switch mode {
			case "protected":
				target.Database = "tremor_sklad"
			case "case":
				target.Database = "TrEmOr_SkLaD"
			case "spaces":
				target.Database = "tremor_sklad "
			case "host":
				target.Host = ""
			case "port":
				target.Port = 0
			case "database":
				target.Database = ""
			case "user":
				target.User = ""
			case "profile":
				target.Profile = ""
			case "schema":
				target.Schema = "pg_catalog"
			case "server-address":
				target.ServerAddress = ""
			case "server-id":
				target.ServerID = ""
			case "ssl":
				target.SSLMode = "prefer"
			case "remote-insecure":
				target.Host = "warehouse.example"
			}
			if target.Validate() == nil {
				t.Fatal("unsafe target accepted")
			}
		})
	}
	if err := validTarget().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTargetFormattingDoesNotLeakPassword(t *testing.T) {
	target := validTarget()
	target.Password = "never-print-this-password"
	encoded, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{fmt.Sprint(target), fmt.Sprintf("%#v", target), string(encoded)} {
		if strings.Contains(value, target.Password) {
			t.Fatal("credentials leaked")
		}
	}
	for _, name := range []string{"postgres", "template0", "template1"} {
		target.Database = name
		if target.Validate() == nil {
			t.Fatal("maintenance/template target accepted")
		}
	}
}
