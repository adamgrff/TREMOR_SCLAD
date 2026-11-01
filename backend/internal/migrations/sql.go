package migrations

import (
	"bytes"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CanonicalSQL defines utf8-lf-v1: valid UTF-8, no BOM/NUL, CRLF -> LF,
// bare CR forbidden. No trimming, Unicode normalization or final-LF insertion.
func CanonicalSQL(input []byte) ([]byte, error) {
	if !utf8.Valid(input) || bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}) {
		return nil, errors.New("SQL must be UTF-8 without BOM")
	}
	output := make([]byte, 0, len(input))
	for i := 0; i < len(input); i++ {
		if input[i] == 0 {
			return nil, errors.New("NUL in SQL")
		}
		if input[i] == '\r' {
			if i+1 == len(input) || input[i+1] != '\n' {
				return nil, errors.New("bare CR in SQL")
			}
			i++
			output = append(output, '\n')
		} else {
			output = append(output, input[i])
		}
	}
	return output, nil
}

type sqlToken struct {
	start, end int
	text       string
	kind       byte
}

func identifier(r rune, first bool) bool {
	return unicode.IsLetter(r) || r == '_' || (!first && (unicode.IsDigit(r) || r == '$'))
}

// sqlTokens is a lexer, not a SQL rewriter. Offsets always refer to the original
// canonical bytes. PostgreSQL validates grammar when executing the intact body.
func sqlTokens(sql string) ([]sqlToken, error) {
	var tokens []sqlToken
	for i := 0; i < len(sql); {
		start := i
		r, size := utf8.DecodeRuneInString(sql[i:])
		if unicode.IsSpace(r) {
			i += size
			continue
		}
		if strings.HasPrefix(sql[i:], "--") {
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			continue
		}
		if strings.HasPrefix(sql[i:], "/*") {
			i += 2
			depth := 1
			for i < len(sql) && depth > 0 {
				if strings.HasPrefix(sql[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(sql[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 {
				return nil, errors.New("unterminated SQL comment")
			}
			continue
		}
		if sql[i] == '\\' {
			return nil, errors.New("psql commands are not allowed")
		}
		// E-strings have backslash escapes; ordinary strings rely on verified
		// standard_conforming_strings=on. Quoted identifiers double quotes.
		escaped := (sql[i] == 'E' || sql[i] == 'e') && i+1 < len(sql) && sql[i+1] == '\''
		if sql[i] == '\'' || sql[i] == '"' || escaped {
			if escaped {
				i++
			}
			quote := sql[i]
			i++
			closed := false
			for i < len(sql) {
				if escaped && sql[i] == '\\' {
					i += 2
					continue
				}
				if sql[i] == quote {
					if i+1 < len(sql) && sql[i+1] == quote {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated SQL quote")
			}
			tokens = append(tokens, sqlToken{start, i, "", 'q'})
			continue
		}
		if sql[i] == '$' {
			j := i + 1
			for j < len(sql) && sql[j] != '$' {
				x, n := utf8.DecodeRuneInString(sql[j:])
				if x == '$' || !identifier(x, j == i+1) {
					break
				}
				j += n
			}
			if j < len(sql) && sql[j] == '$' {
				delimiter := sql[i : j+1]
				end := strings.Index(sql[j+1:], delimiter)
				if end < 0 {
					return nil, errors.New("unterminated dollar quote")
				}
				i = j + 1 + end + len(delimiter)
				tokens = append(tokens, sqlToken{start, i, "", 'q'})
				continue
			}
		}
		if identifier(r, true) {
			i += size
			for i < len(sql) {
				x, n := utf8.DecodeRuneInString(sql[i:])
				if !identifier(x, false) {
					break
				}
				i += n
			}
			tokens = append(tokens, sqlToken{start, i, strings.ToUpper(sql[start:i]), 'w'})
			continue
		}
		i += size
		tokens = append(tokens, sqlToken{start, i, sql[start:i], 's'})
	}
	return tokens, nil
}

// MigrationBody accepts exactly one bare BEGIN; ... COMMIT; envelope and no
// top-level transaction/session controls inside it. Strings/comments/dollar
// bodies are single lexical units. No global replacement or SQL splitting.
func MigrationBody(canonical []byte) (string, error) {
	sql := string(canonical)
	tokens, err := sqlTokens(sql)
	if err != nil {
		return "", err
	}
	n := len(tokens)
	if n < 5 || tokens[0].text != "BEGIN" || tokens[0].kind != 'w' || tokens[1].text != ";" || tokens[n-2].text != "COMMIT" || tokens[n-2].kind != 'w' || tokens[n-1].text != ";" {
		return "", errors.New("migration requires a single bare BEGIN/COMMIT envelope")
	}
	statementStart := true
	for _, token := range tokens[2 : n-2] {
		if token.text == ";" {
			statementStart = true
			continue
		}
		if statementStart {
			switch token.text {
			case "BEGIN", "START", "COMMIT", "END", "ABORT", "ROLLBACK", "SAVEPOINT", "RELEASE", "PREPARE", "SET", "RESET", "DISCARD", "COPY", "DROP", "TRUNCATE":
				return "", errors.New("transaction/session control inside migration body")
			}
			statementStart = false
		}
	}
	// The body must end with its own semicolon, not absorb the final COMMIT.
	if tokens[n-3].text != ";" {
		return "", errors.New("missing body statement terminator")
	}
	return sql[tokens[1].end:tokens[n-2].start], nil
}
