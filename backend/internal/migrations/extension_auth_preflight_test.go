package migrations

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func passwordAuthenticationFailure(err error) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "28P01"
}

// Explicitly invoked by run.sh BEFORE any migration tests. No DDL/DML.
// Direct pgx is necessary here: Target.connect deliberately sanitizes errors
// and therefore does not expose SQLSTATE to its callers.
func TestExtensionAuthPreflight(t *testing.T) {
	if os.Getenv("EXTENSION_TEST_DISPOSABLE_APPROVED") != "yes" {
		t.Skip("NOT RUN: approved disposable target required")
	}
	target := testTarget(t)
	target.Profile = IdentityProfile
	if target.Host != "127.0.0.1" || target.Port != 5432 || target.Database != "tremor_e1a_test" || target.User != "e1a_test_admin" || target.Password == "" {
		t.Fatal("unexpected Docker TEST identity/credentials")
	}
	cfg, err := pgx.ParseConfig("host=127.0.0.1 port=5432 dbname=tremor_e1a_test user=e1a_test_admin sslmode=disable")
	if err != nil {
		t.Fatal("cannot configure preflight")
	}
	cfg.Password = target.Password
	cfg.Fallbacks = nil
	cfg.ConnectTimeout = 5 * time.Second
	cfg.RuntimeParams = map[string]string{"search_path": "pg_catalog", "client_encoding": "UTF8", "standard_conforming_strings": "on", "default_transaction_read_only": "on", "default_transaction_isolation": "read committed"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("correct TEST password rejected (error type %T)", err)
	}
	defer closeConnection(conn)
	if _, err = target.inspect(ctx, conn); err != nil {
		t.Fatal("preflight target inspection failed")
	}
	var file, rules string
	if err = conn.QueryRow(ctx, "SHOW hba_file").Scan(&file); err != nil {
		t.Fatal("SHOW hba_file failed")
	}
	// Do not log auth options or role password verifiers. Preserve order and
	// relevant metadata only; pg_hba_file_rules reports current file contents.
	if err = conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('rule_number',rule_number,'file',file_name,'line',line_number,'type',type,'database',database,'users',user_name,'address',address,'netmask',netmask,'auth',auth_method,'error',error) ORDER BY rule_number NULLS LAST),'[]'::jsonb)::text FROM pg_catalog.pg_hba_file_rules`).Scan(&rules); err != nil {
		t.Fatal("cannot inspect HBA rules")
	}
	t.Logf("HBA file=%s rules=%s", file, rules)
	var valid, verifier bool
	var rule int
	// Conservative contract for this generated disposable cluster: EVERY TCP
	// rule must use SCRAM, even replication/IPv6 rules, regardless of ordering.
	// Require the standard all/all IPv4 loopback rule; unknown layouts fail closed.
	err = conn.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_catalog.pg_hba_file_rules WHERE error IS NOT NULL OR (type LIKE 'host%' AND auth_method IS DISTINCT FROM 'scram-sha-256')),COALESCE((SELECT min(rule_number) FROM pg_catalog.pg_hba_file_rules WHERE type='host' AND database=ARRAY['all']::text[] AND user_name=ARRAY['all']::text[] AND address='127.0.0.1' AND netmask='255.255.255.255' AND auth_method='scram-sha-256'),0),(SELECT rolpassword LIKE 'SCRAM-SHA-256$%' FROM pg_catalog.pg_authid WHERE rolname=current_user)`).Scan(&valid, &rule, &verifier)
	if err != nil || !valid || rule == 0 || !verifier {
		t.Fatal("HBA/SCRAM verifier admission failed")
	}
	bad := cfg.Copy()
	bad.Password = target.Password + "-intentionally-wrong"
	unexpected, badErr := pgx.ConnectConfig(ctx, bad)
	if unexpected != nil {
		closeConnection(unexpected)
	}
	if !passwordAuthenticationFailure(badErr) {
		code := "none"
		var pgerr *pgconn.PgError
		if errors.As(badErr, &pgerr) {
			code = pgerr.Code
		}
		t.Fatalf("wrong TEST password: expected SQLSTATE 28P01; got SQLSTATE=%s error type %T", code, badErr)
	}
	t.Logf("AUTH PASS: correct password accepted; wrong password SQLSTATE=28P01; loopback SCRAM rule=%d", rule)
}

func TestExtensionAuthFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "28P01"}, true},
		{errors.Join(errors.New("wrapped connect"), &pgconn.PgError{Code: "28P01"}), true},
		{&pgconn.PgError{Code: "28000"}, false},
		{&pgconn.PgError{Code: "3D000"}, false},
		{context.DeadlineExceeded, false}, {errors.New("network failure"), false}, {nil, false},
	} {
		if passwordAuthenticationFailure(tc.err) != tc.want {
			t.Fatalf("incorrect auth classification: %T", tc.err)
		}
	}
}
