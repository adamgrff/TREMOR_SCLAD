package migrations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// All ledger row fields plus relation identities. This detects persistent changes
// by reviewed migrations, not adversarial owner SQL that can change and restore
// objects, session state or side effects. Privilege separation is an E1B gate.
type extensionLedgerSnapshot struct {
	Baseline                                                 []Applied
	Extensions                                               []Applied
	BaselineOID                                              int64
	ExtensionOID                                             int64
	BaselineOwner, BaselineACL, ExtensionOwner, ExtensionACL string
	BaselineColumnACL, ExtensionColumnACL                    string
}

func (r *ExtensionRunner) ledgerSnapshot(ctx context.Context, tx pgx.Tx, state ExtensionState) (extensionLedgerSnapshot, error) {
	var result extensionLedgerSnapshot
	base, err := r.base.readState(ctx, tx)
	if err != nil {
		return result, err
	}
	result.Baseline = append([]Applied{}, base.Applied...)
	result.Extensions = append([]Applied{}, state.Applied...)
	if err = tx.QueryRow(ctx, ledgerMetadataSQL, r.base.ledger()).Scan(&result.BaselineOID, &result.BaselineOwner, &result.BaselineACL, &result.BaselineColumnACL); err != nil {
		return result, err
	}
	if state.Initialized {
		if err = tx.QueryRow(ctx, ledgerMetadataSQL, r.ledger()).Scan(&result.ExtensionOID, &result.ExtensionOwner, &result.ExtensionACL, &result.ExtensionColumnACL); err != nil {
			return result, err
		}
	}
	return result, nil
}
func sameApplied(a, b Applied) bool {
	return a.Entry == b.Entry && a.Profile == b.Profile && a.AppliedAt.Equal(b.AppliedAt)
}
func preservedLedgers(before, after extensionLedgerSnapshot, additional int, exact bool) error {
	fail := func() error { return fmt.Errorf("%w: immutable ledger snapshot changed", ErrUnsafe) }
	if before.BaselineOwner != after.BaselineOwner || before.BaselineACL != after.BaselineACL || before.BaselineColumnACL != after.BaselineColumnACL || before.BaselineOID != after.BaselineOID || len(before.Baseline) != len(after.Baseline) {
		return fail()
	}
	if before.ExtensionOID != 0 && (before.ExtensionOID != after.ExtensionOID || before.ExtensionOwner != after.ExtensionOwner || before.ExtensionACL != after.ExtensionACL || before.ExtensionColumnACL != after.ExtensionColumnACL) {
		return fail()
	}
	for i, row := range before.Baseline {
		if !sameApplied(row, after.Baseline[i]) {
			return fail()
		}
	}
	minimum := len(before.Extensions) + additional
	if len(after.Extensions) < minimum || (exact && len(after.Extensions) != minimum) {
		return fail()
	}
	for i, row := range before.Extensions {
		if !sameApplied(row, after.Extensions[i]) {
			return fail()
		}
	}
	return nil
}

// Recovery has only a read capability. Offline fault tests exercise this exact
// classification path. Production supplies a fresh target/identity/readiness
// inspected snapshot, never an execution/retry callback.
func recoverExtensionCommit(expected extensionLedgerSnapshot, read func() (ExtensionState, extensionLedgerSnapshot, error)) (ExtensionState, error) {
	state, actual, err := read()
	if err != nil {
		return state, fmt.Errorf("%w: recovery check failed: %w", ErrCommitUnknown, err)
	}
	if err = preservedLedgers(expected, actual, 0, false); err != nil {
		return state, fmt.Errorf("%w: durable ledger not confirmed: %w", ErrCommitUnknown, err)
	}
	state.CommitResolved = true
	if len(state.Pending) > 0 {
		return state, ErrResume
	}
	return state, nil
}

type extensionCheck struct{ Name, Type, Expression string }

func normalizedCheck(expression string) (string, error) {
	// The shared envelope lexer deliberately leaves quoted token.text empty.
	// Preserve the original quoted bytes here; never change the base lexer.
	if !utf8.ValidString(expression) || strings.ContainsRune(expression, 0) {
		return "", errors.New("invalid CHECK encoding")
	}
	tokens, err := sqlTokens(expression)
	if err != nil {
		return "", err
	}
	var values []string
	previous := 0
	for i, t := range tokens {
		// pg_get_expr does not emit comments. Do not silently discard unsupported
		// syntax in the gaps skipped by the shared lexer.
		if strings.Trim(expression[previous:t.start], " \t\r\n\f\v") != "" {
			return "", errors.New("unsupported CHECK gap")
		}
		// The envelope lexer emits numbers/:: character by character. Do not
		// turn invalid split forms (2 4 or : :) into valid catalogue tokens.
		if i > 0 && t.start > previous && t.kind == 's' && tokens[i-1].kind == 's' {
			prior := tokens[i-1].text
			if (prior == ":" && t.text == ":") || (len(prior) == 1 && prior[0] >= '0' && prior[0] <= '9' && len(t.text) == 1 && t.text[0] >= '0' && t.text[0] <= '9') {
				return "", errors.New("unsupported split CHECK token")
			}
		}
		text := t.text
		switch t.kind {
		case 'q':
			text = expression[t.start:t.end]
			if text[0] != '\'' && text[0] != '"' {
				return "", errors.New("unsupported CHECK quote form")
			}
		case 'w':
			for _, c := range expression[t.start:t.end] {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
					return "", errors.New("unsupported CHECK word")
				}
			}
		case 's':
			if len(text) != 1 || !strings.ContainsRune("0123456789()><=~:", rune(text[0])) {
				return "", errors.New("unsupported CHECK symbol")
			}
		default:
			return "", errors.New("unsupported CHECK token")
		}
		values = append(values, string(t.kind)+":"+text)
		previous = t.end
	}
	if len(values) == 0 || strings.Trim(expression[previous:], " \t\r\n\f\v") != "" {
		return "", errors.New("empty or unsupported CHECK suffix")
	}
	return strings.Join(values, "\x00"), nil
}
func validateExtensionChecks(checks []extensionCheck) error {
	want := map[string]string{
		"ledger_version_positive": "(version > 24)",
		"ledger_order_positive":   "(ordinal > 0)",
		"ledger_checksum_hex":     "(checksum ~ '^[0-9a-f]{64}$'::text)",
		"ledger_profile_clean":    "(profile ~ '^[0-9a-f]{64}$'::text)",
	}
	if len(checks) != len(want) {
		return fmt.Errorf("%w: CHECK count", ErrUnsafe)
	}
	for _, check := range checks {
		expression, ok := want[check.Name]
		if !ok || check.Type != "c" {
			return fmt.Errorf("%w: CHECK name/type", ErrUnsafe)
		}
		expected, expectedErr := normalizedCheck(expression)
		actual, err := normalizedCheck(check.Expression)
		if expectedErr != nil || err != nil || expected != actual {
			return fmt.Errorf("%w: CHECK expression for %s", ErrUnsafe, check.Name)
		}
		delete(want, check.Name)
	}
	return nil
}
func checkExtensionChecks(ctx context.Context, tx pgx.Tx, oid int64) error {
	rows, err := tx.Query(ctx, `SELECT conname,contype::text,pg_catalog.pg_get_expr(conbin,conrelid) FROM pg_catalog.pg_constraint WHERE conrelid=$1 AND contype='c' ORDER BY conname`, oid)
	if err != nil {
		return errors.New("cannot read extension CHECK definitions")
	}
	defer rows.Close()
	var checks []extensionCheck
	for rows.Next() {
		var check extensionCheck
		if rows.Scan(&check.Name, &check.Type, &check.Expression) != nil {
			return errors.New("cannot decode extension CHECK")
		}
		checks = append(checks, check)
	}
	if rows.Err() != nil {
		return errors.New("incomplete extension CHECK read")
	}
	return validateExtensionChecks(checks)
}

const ledgerMetadataSQL = `SELECT c.oid::bigint,pg_catalog.pg_get_userbyid(c.relowner),COALESCE(c.relacl::text,''),COALESCE((SELECT pg_catalog.jsonb_agg(pg_catalog.jsonb_build_array(a.attnum,a.attacl::text) ORDER BY a.attnum)::text FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped),'[]') FROM pg_catalog.pg_class c WHERE c.oid=$1::regclass`

// Diagnostic only: ownership and effective privileges of the authenticated
// migration user. These facts do not constitute the future E1B security gate.
type ExtensionLedgerAuthority struct {
	Owner        string `json:"owner"`
	Insert       bool   `json:"insert"`
	Update       bool   `json:"update"`
	Delete       bool   `json:"delete"`
	Truncate     bool   `json:"truncate"`
	ColumnUpdate bool   `json:"column_update"`
}

func ledgerAuthority(ctx context.Context, tx pgx.Tx, table string) (*ExtensionLedgerAuthority, error) {
	result := &ExtensionLedgerAuthority{}
	err := tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_userbyid(c.relowner),pg_catalog.has_table_privilege(current_user,c.oid,'INSERT'),pg_catalog.has_table_privilege(current_user,c.oid,'UPDATE'),pg_catalog.has_table_privilege(current_user,c.oid,'DELETE'),pg_catalog.has_table_privilege(current_user,c.oid,'TRUNCATE'),pg_catalog.has_any_column_privilege(current_user,c.oid,'UPDATE') FROM pg_catalog.pg_class c WHERE c.oid=$1::regclass`, table).Scan(&result.Owner, &result.Insert, &result.Update, &result.Delete, &result.Truncate, &result.ColumnUpdate)
	return result, err
}
