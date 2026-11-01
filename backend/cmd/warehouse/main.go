// warehouse bootstraps or validates a clean-v1 database with an explicit target.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/migrations"
)

func run(ctx context.Context, args []string, out, diagnostics io.Writer) int {
	if len(args) == 0 || (args[0] != "bootstrap" && args[0] != "validate") {
		fmt.Fprintln(diagnostics, "usage: warehouse bootstrap|validate --warehouse-name NAME [explicit target flags]")
		return 2
	}
	f := flag.NewFlagSet("warehouse", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var target migrations.Target
	var name, manifest string
	var timeout time.Duration
	f.StringVar(&name, "warehouse-name", "", "required exact warehouse name")
	f.StringVar(&manifest, "manifest", "migrations/manifest.clean-v1.json", "reviewed local manifest")
	f.StringVar(&target.Host, "host", "", "explicit host")
	f.IntVar(&target.Port, "port", 0, "explicit port")
	f.StringVar(&target.Database, "database", "", "explicit database")
	f.StringVar(&target.User, "user", "", "explicit existing user")
	f.StringVar(&target.Profile, "profile", "", "required clean-v1 profile")
	f.StringVar(&target.Schema, "schema", "public", "existing schema")
	f.StringVar(&target.ServerAddress, "server-address", "", "expected server IP")
	f.StringVar(&target.ServerID, "server-id", "", "expected cluster identifier")
	f.StringVar(&target.SSLMode, "sslmode", "", "explicit TLS mode")
	f.StringVar(&target.SSLRootCert, "sslrootcert", "", "explicit CA file")
	f.DurationVar(&timeout, "timeout", 5*time.Minute, "deadline including locks")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || name == "" || timeout <= 0 {
		fmt.Fprintln(diagnostics, "invalid flags or missing explicit warehouse name")
		return 2
	}
	if err := target.Validate(); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	m, err := migrations.Load(manifest)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	target.Password = os.Getenv("MIGRATION_PASSWORD")
	r, err := migrations.New(target, m)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var result migrations.Readiness
	if args[0] == "bootstrap" {
		result, err = r.BootstrapWarehouse(ctx, name)
	} else {
		result, err = r.ValidateWarehouse(ctx, name)
	}
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	if json.NewEncoder(out).Encode(result) != nil {
		return 1
	}
	return 0
}

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }
