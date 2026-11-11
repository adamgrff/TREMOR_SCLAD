package migrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"reflect"
	"strings"
	"testing"
)

type readinessErrorRow struct{ err error }

func (r readinessErrorRow) Scan(...any) error { return r.err }

type readinessValueRow struct{ values []any }

func (r readinessValueRow) Scan(dest ...any) error {
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = r.values[i].(string)
		case *bool:
			*p = r.values[i].(bool)
		}
	}
	return nil
}

type readinessFakeTx struct {
	pgx.Tx
	path     string
	context  securityCollectionContext
	fail     string
	commands []string
	restores int
}

func (r *readinessFakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.commands = append(r.commands, sql)
	if r.fail == "set" {
		return pgconn.CommandTag{}, errors.New("TEST set denied")
	}
	r.path = "legacy-context"
	return pgconn.NewCommandTag("SET"), nil
}
func (r *readinessFakeTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	switch sql {
	case securityPathReadSQL:
		if r.fail == "read" {
			return readinessErrorRow{errors.New("TEST read failed")}
		}
		return readinessValueRow{[]any{r.path}}
	case securityPathRestoreSQL:
		r.restores++
		if r.fail == "restore" {
			return readinessErrorRow{errors.New("TEST restore failed")}
		}
		r.path = args[0].(string)
		if r.fail == "restore-mismatch" {
			return readinessValueRow{[]any{"unexpected"}}
		}
		return readinessValueRow{[]any{r.path}}
	case securityContextReadSQL:
		if r.fail == "context-read" {
			return readinessErrorRow{errors.New("TEST context read failed")}
		}
		return readinessValueRow{[]any{r.context.path, r.context.timezone, r.context.rowSecurity}}
	case securityContextWriteSQL:
		r.restores++
		if r.fail == "context-write" {
			return readinessErrorRow{errors.New("TEST context write failed")}
		}
		r.context = securityCollectionContext{args[0].(string), args[1].(string), args[2].(string)}
		return readinessValueRow{[]any{r.context.path, r.context.timezone, r.context.rowSecurity}}
	}
	if strings.Contains(sql, "unsupported") || strings.HasPrefix(sql, "WITH ns AS") {
		return readinessValueRow{[]any{true}}
	}
	return readinessErrorRow{errors.New("unexpected TEST query")}
}
func (r *readinessFakeTx) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if r.fail == "catalog" {
		return nil, errors.New("TEST catalog unavailable")
	}
	if sql != extensionCatalogSQL {
		return nil, errors.New("unexpected catalog query")
	}
	return &parameterRows{values: [][]any{{`["synthetic"]`}}}, nil
}

func TestCollectorLegacyReadinessContext(t *testing.T) {
	ctx := context.Background()
	for _, schema := range []string{"public", "warehouse_scope", "order"} {
		t.Run(schema, func(t *testing.T) {
			tx := &readinessFakeTx{path: `"$user", pg_catalog, public`}
			hash, err := collectorLegacyReadiness(ctx, tx, schema)
			if err != nil || hash != catalogRecordsDigest([]string{`["synthetic"]`}) {
				t.Fatal("legacy projection failed", err)
			}
			if len(tx.commands) != 1 || tx.commands[0] != "SET LOCAL search_path TO pg_catalog,"+(pgx.Identifier{schema}).Sanitize() {
				t.Fatal("incorrect E1A identifier context")
			}
			if tx.path != `"$user", pg_catalog, public` || tx.restores != 1 {
				t.Fatal("caller context not restored")
			}
		})
	}
	for _, schema := range []string{"", "pg_catalog", "information_schema", "public,evil", `bad"schema`, "MixedCase"} {
		tx := &readinessFakeTx{}
		if hash, err := collectorLegacyReadiness(ctx, tx, schema); err == nil || hash != "" || len(tx.commands) != 0 {
			t.Fatal("invalid schema accepted")
		}
	}
}
func TestCollectorLegacyReadinessFailures(t *testing.T) {
	for _, stage := range []string{"read", "set", "catalog", "restore", "restore-mismatch"} {
		t.Run(stage, func(t *testing.T) {
			tx := &readinessFakeTx{path: "pg_catalog", fail: stage}
			hash, err := collectorLegacyReadiness(context.Background(), tx, "public")
			if err == nil || hash != "" {
				t.Fatal("failure produced successful readiness")
			}
			if stage != "read" && tx.restores != 1 {
				t.Fatal("restore not attempted")
			}
		})
	}
}
func TestCollectorCanonicalContextFailures(t *testing.T) {
	for _, stage := range []string{"context-read", "context-write"} {
		t.Run(stage, func(t *testing.T) {
			tx := &readinessFakeTx{context: securityCollectionContext{"public", "UTC", "on"}, fail: stage}
			result, err := collectSecuritySnapshotTx(context.Background(), tx, Target{}, 0, SecurityTransitionDescriptor{}, TransitionScope{}, nil)
			if err == nil || !reflect.DeepEqual(result, CollectedSecuritySnapshot{}) {
				t.Fatal("context failure yielded partial collection")
			}
		})
	}
}
func TestSecurityFingerprintSeparateFromReadiness(t *testing.T) {
	_, _, s := transitionFixture(t)
	seal := func(s TransitionSnapshot) CollectedSecuritySnapshot {
		c := CollectedSecuritySnapshot{snapshot: s}
		c.seal = transitionDigest([]any{c.snapshot, c.receipt, c.privileges, c.paths})
		return c
	}
	c := seal(s)
	before, err := c.SecurityFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	s.ReadinessSHA = digest([]byte("different legacy projection"))
	changedReady := seal(s)
	sameSecurity, err := changedReady.SecurityFingerprint()
	if err != nil || before != sameSecurity {
		t.Fatal("legacy readiness silently replaced security fingerprint")
	}
	s.Metadata[transitionTestIndex(s.Metadata, "role:tremor_api")].DefinitionSHA = digest([]byte("security change"))
	changedSecurity := seal(s)
	after, err := changedSecurity.SecurityFingerprint()
	if err != nil || before == after {
		t.Fatal("security metadata omitted")
	}
	if _, err = (CollectedSecuritySnapshot{}).SecurityFingerprint(); err == nil {
		t.Fatal("unsealed evidence fingerprint accepted")
	}
}
