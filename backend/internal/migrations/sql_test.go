package migrations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestCanonicalPortableVectors(t *testing.T) {
	lf := []byte("BEGIN;\nSELECT 'Ёж';\nCOMMIT;\n")
	crlf := bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
	for _, input := range [][]byte{lf, crlf} {
		canonical, err := CanonicalSQL(input)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canonical, lf) {
			t.Fatal("canonical bytes differ")
		}
		sum := sha256.Sum256(canonical)
		if hex.EncodeToString(sum[:]) != "a3a585229eb08a63926beb51c7364fe1ad085f7c9e3548cf8fe7796afcf3bed3" {
			t.Fatal("portable UTF-8/LF reference vector differs")
		}
	}
	for _, input := range [][]byte{{0xff}, {0}, []byte("SELECT 1;\r"), append([]byte{0xef, 0xbb, 0xbf}, lf...)} {
		if _, err := CanonicalSQL(input); err == nil {
			t.Fatal("invalid encoding accepted")
		}
	}
	noNewline, err := CanonicalSQL([]byte("SELECT 1;"))
	if err != nil || string(noNewline) != "SELECT 1;" {
		t.Fatal("unexpected newline insertion")
	}
}

func TestMigrationEnvelopePreservesBody(t *testing.T) {
	bodies := []string{
		"\n SELECT 'COMMIT; -- /*';\n",
		"\n SELECT 'it''s; BEGIN;';\n",
		` SELECT E'it\\s; COMMIT; \'quoted\''; `,
		` SELECT 'backslash\'; `,
		` SELECT "COMMIT; ""odd"""; `,
		"\n/* outer ; COMMIT; /* inner BEGIN; */ still */ SELECT 1; -- COMMIT;\n",
		"\nCREATE FUNCTION f() RETURNS text AS $$ BEGIN RETURN 'COMMIT;'; END; $$ LANGUAGE plpgsql;\n",
		"\nCREATE FUNCTION f() RETURNS text AS $body$ BEGIN RETURN $$COMMIT;$$; END; $body$ LANGUAGE plpgsql;\n",
		"\nDO $тест$ BEGIN PERFORM 'x;'; END; $тест$;\n",
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			sql := "-- preface\n/* nested /* x */ */ BEGIN;" + body + "COMMIT; -- trailing\n"
			got, err := MigrationBody([]byte(sql))
			if err != nil {
				t.Fatal(err)
			}
			if got != body {
				t.Fatalf("body bytes were altered: %q", got)
			}
		})
	}
}

func TestMigrationEnvelopeFailsClosed(t *testing.T) {
	cases := []string{
		"", "BEGIN; COMMIT;", "SELECT 1;", "BEGIN WORK; SELECT 1; COMMIT;",
		"BEGIN; SELECT 1; END;", "BEGIN; SELECT 1; COMMIT AND CHAIN;",
		"BEGIN; SELECT 1 COMMIT;", "BEGIN; SELECT 1; COMMIT; SELECT 2;",
		"BEGIN; SELECT 1; COMMIT; ;", "BEGIN; SELECT 'missing; COMMIT;",
		"BEGIN; SELECT $$missing; COMMIT;", "BEGIN; SELECT 1; /* missing COMMIT;",
		"BEGIN; SELECT 1; COMMIT; /* unterminated", "BEGIN; SELECT 1; COMMIT; \\echo secret",
	}
	for _, command := range []string{"COMMIT", "ROLLBACK", "BEGIN", "END", "START TRANSACTION", "SAVEPOINT s", "RELEASE s", "PREPARE TRANSACTION 'x'", "SET search_path=public", "RESET ALL", "DISCARD ALL", "COPY t FROM STDIN", "DROP TABLE existing", "TRUNCATE existing"} {
		cases = append(cases, "BEGIN; SELECT 1; /* control */ "+command+"; SELECT 2; COMMIT;")
	}
	for _, sql := range cases {
		if _, err := MigrationBody([]byte(sql)); err == nil {
			t.Fatalf("unsafe envelope accepted: %s", sql)
		}
	}
}

func FuzzMigrationEnvelope(f *testing.F) {
	for _, sql := range []string{"BEGIN; SELECT 1; COMMIT;", "BEGIN; DO $$BEGIN PERFORM ';'; END;$$; COMMIT;", `BEGIN; SELECT E'\''; COMMIT;`} {
		f.Add(sql)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		canonical, err := CanonicalSQL([]byte(sql))
		if err != nil {
			return
		}
		body, err := MigrationBody(canonical)
		if err == nil && !strings.Contains(string(canonical), body) {
			t.Fatal("rewritten SQL body")
		}
	})
}
