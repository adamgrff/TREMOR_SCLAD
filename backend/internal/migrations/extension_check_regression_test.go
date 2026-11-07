package migrations

import (
	"strings"
	"testing"
)

// The former algorithm, retained only as a regression witness, not a fallback.
func oldCheckNormalizationForTest(expression string) string {
	tokens, _ := sqlTokens(expression)
	var parts []string
	for _, token := range tokens {
		parts = append(parts, token.text)
	}
	return strings.Join(parts, "\x00")
}

func TestExtensionCHECKQuotedRegression(t *testing.T) {
	for _, column := range []string{"checksum", "profile"} {
		t.Run(column, func(t *testing.T) {
			good := "(" + column + " ~ '^[0-9a-f]{64}$'::text)"
			bad := "(" + column + " ~ '.*'::text)"
			if oldCheckNormalizationForTest(good) != oldCheckNormalizationForTest(bad) {
				t.Fatal("fixture does not reproduce old defect")
			}
			a, err := normalizedCheck(good)
			if err != nil {
				t.Fatal(err)
			}
			b, err := normalizedCheck(bad)
			if err != nil {
				t.Fatal(err)
			}
			if a == b {
				t.Fatal("literal-only substitution accepted")
			}
			checks := []extensionCheck{{"ledger_version_positive", "c", "(version > 24)"}, {"ledger_order_positive", "c", "(ordinal > 0)"}, {"ledger_checksum_hex", "c", "(checksum ~ '^[0-9a-f]{64}$'::text)"}, {"ledger_profile_clean", "c", "(profile ~ '^[0-9a-f]{64}$'::text)"}}
			if err := validateExtensionChecks(checks); err != nil {
				t.Fatal(err)
			}
			index := 2
			if column == "profile" {
				index = 3
			}
			checks[index].Expression = bad
			if validateExtensionChecks(checks) == nil {
				t.Fatal("CHECK set with changed literal accepted")
			}
		})
	}
	for _, pair := range [][2]string{
		{`(checksum ~ 'Ab C'::text)`, `(checksum ~ 'ab C'::text)`},
		{`(checksum ~ 'a''b'::text)`, `(checksum ~ 'a''c'::text)`},
		{`("CheckSum" ~ 'x'::text)`, `("checksum" ~ 'x'::text)`},
		{`("a""b" ~ 'x'::text)`, `("a""c" ~ 'x'::text)`},
		{`(checksum ~ 'a b'::text)`, `(checksum ~ 'ab'::text)`},
		{`(checksum ~ 'Ж'::text)`, `(checksum ~ 'ж'::text)`},
	} {
		a, err := normalizedCheck(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := normalizedCheck(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if a == b {
			t.Fatalf("distinct quoted contents collapsed: %q / %q", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]string{
		{`(checksum ~ 'a'' b'::text)`, " \t( CHECKSUM\n~ 'a'' b' :: TEXT )\r\n"},
		{`("a"" B" ~ 'x'::text)`, ` ( "a"" B" ~ 'x' :: TEXT ) `},
	} {
		a, err := normalizedCheck(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := normalizedCheck(pair[1])
		if err != nil || a != b {
			t.Fatalf("external whitespace/case normalization: %v", err)
		}
	}
}

func TestExtensionCHECKUnsupportedForms(t *testing.T) {
	for _, sql := range []string{"", " ", `(checksum ~ 'unterminated)`, `("unterminated > 24)`, `(checksum ~ E'x'::text)`, `(checksum ~ $$x$$::text)`, `(checksum ~ U&'x'::text)`, `(version /* comment */ > 24)`, `(version > 24) --comment`, `(version > 24);`, `(version > 2 4)`, `(checksum ~ 'x' : : text)`, "(version > 24)\x00", string([]byte{0xff})} {
		if normalized, err := normalizedCheck(sql); err == nil || normalized != "" {
			t.Fatalf("unsupported/tokenization error accepted: %q", sql)
		}
	}
}
