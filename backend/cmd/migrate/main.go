// migrate is the explicit-target clean schema runner. It never loads .env.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/migrations"
)

func run(ctx context.Context, args []string, output, diagnostics io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(diagnostics, "usage: migrate plan|status|verify|apply --profile clean-v1 [explicit target flags]")
		return 2
	}
	mode := args[0]
	if mode != "plan" && mode != "status" && mode != "verify" && mode != "apply" {
		fmt.Fprintln(diagnostics, "invalid migration mode")
		return 2
	}
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	// Never print raw flag errors: a mistaken password/DSN flag may contain a secret.
	flags.SetOutput(io.Discard)
	var target migrations.Target
	var manifest string
	var timeout time.Duration
	flags.StringVar(&manifest, "manifest", "migrations/manifest.clean-v1.json", "versioned local manifest")
	flags.StringVar(&target.Profile, "profile", "", "required approved profile")
	flags.StringVar(&target.Host, "host", "", "explicit server host")
	flags.IntVar(&target.Port, "port", 0, "explicit TCP port")
	flags.StringVar(&target.Database, "database", "", "explicit database")
	flags.StringVar(&target.User, "user", "", "explicit existing user")
	flags.StringVar(&target.Schema, "schema", "public", "existing target schema")
	flags.StringVar(&target.ServerAddress, "server-address", "", "expected inet_server_addr IP")
	flags.StringVar(&target.ServerID, "server-id", "", "expected PostgreSQL system_identifier")
	flags.StringVar(&target.SSLMode, "sslmode", "", "verify-full or loopback-only disable")
	flags.StringVar(&target.SSLRootCert, "sslrootcert", "", "explicit TLS CA file for verify-full")
	flags.DurationVar(&timeout, "timeout", 5*time.Minute, "deadline including deployment lock")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || timeout <= 0 || (target.Profile != migrations.Profile && target.Profile != migrations.IdentityProfile) {
		fmt.Fprintln(diagnostics, "invalid flags or missing approved profile")
		return 2
	}
	if strings.EqualFold(target.Database, "tremor_sklad") {
		fmt.Fprintln(diagnostics, "protected database is forbidden")
		return 1
	}
	manifestData, err := migrations.Load(manifest)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	if manifestData.Plan().Profile != target.Profile {
		fmt.Fprintln(diagnostics, "target and manifest profiles must match")
		return 1
	}
	if mode == "plan" {
		if json.NewEncoder(output).Encode(manifestData.Plan()) != nil {
			return 1
		}
		return 0
	}
	target.Password = os.Getenv("MIGRATION_PASSWORD")
	runner, err := migrations.New(target, manifestData)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	state, err := runner.Run(ctx, mode)
	if json.NewEncoder(output).Encode(state) != nil {
		return 1
	}
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	return 0
}

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }
