package migrations

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Minimal read-only catalogue double: exercises the real baseline ledger reader,
// not a second implementation of its lineage rules. No pgx connection is made.
type extensionRow []any

func (r extensionRow) Scan(dest ...any) error {
	if len(dest) != len(r) {
		return errors.New("scan arity")
	}
	for i, v := range r {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}

type extensionRows struct {
	values []extensionRow
	index  int
}

func (r *extensionRows) Close()                                       {}
func (r *extensionRows) Err() error                                   { return nil }
func (r *extensionRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *extensionRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *extensionRows) Next() bool                                   { r.index++; return r.index <= len(r.values) }
func (r *extensionRows) Scan(dest ...any) error                       { return r.values[r.index-1].Scan(dest...) }
func (r *extensionRows) Values() ([]any, error)                       { return r.values[r.index-1], nil }
func (r *extensionRows) RawValues() [][]byte                          { return nil }
func (r *extensionRows) Conn() *pgx.Conn                              { return nil }

type extensionBaselineDB struct {
	m      *Manifest
	rows   []Applied
	marker string
}

func (d extensionBaselineDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if strings.Contains(sql, "obj_description") {
		return extensionRow{int64(1), "r", d.marker, false, false}
	}
	return extensionRow{true}
}
func (d extensionBaselineDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	var rows []extensionRow
	if strings.Contains(sql, "pg_attribute") {
		for _, column := range [][2]string{{"version", "integer"}, {"name", "text"}, {"checksum", "text"}, {"profile", "text"}, {"ordinal", "integer"}, {"applied_at", "timestamp with time zone"}} {
			rows = append(rows, extensionRow{column[0], column[1], true, "", ""})
		}
	} else {
		for _, r := range d.rows {
			rows = append(rows, extensionRow{r.Version, r.Name, r.SHA256, r.Profile, r.Order, r.AppliedAt})
		}
	}
	return &extensionRows{values: rows}, nil
}
func TestExtensionBaselineLineage(t *testing.T) {
	m := extensionBase(t)
	runner := &Runner{target: Target{Profile: IdentityProfile, Schema: "public"}, manifest: m}
	var rows []Applied
	for _, e := range m.plan.Migrations {
		rows = append(rows, Applied{Entry: e, Profile: IdentityProfile, AppliedAt: time.Unix(1, 0)})
	}
	db := extensionBaselineDB{m: m, rows: rows, marker: runner.marker()}
	state, err := runner.readState(context.Background(), db)
	if err != nil || len(state.Pending) != 0 || len(state.Applied) != 22 {
		t.Fatalf("complete lineage: %+v %v", state, err)
	}
	db.rows = rows[:21]
	state, err = runner.readState(context.Background(), db)
	if err != nil || len(state.Pending) != 1 {
		t.Fatal("incomplete base not identified")
	}
	for _, field := range []string{"version", "name", "checksum", "profile", "order"} {
		t.Run(field, func(t *testing.T) {
			db.rows = append([]Applied{}, rows...)
			switch field {
			case "version":
				db.rows[0].Version = 2
			case "name":
				db.rows[0].Name = "other.sql"
			case "checksum":
				db.rows[0].SHA256 = strings.Repeat("0", 64)
			case "profile":
				db.rows[0].Profile = Profile
			case "order":
				db.rows[0].Order = 2
			}
			if _, err := runner.readState(context.Background(), db); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("lineage mismatch accepted: %v", err)
			}
		})
	}
	db.rows = rows
	db.marker = "foreign"
	if _, err = runner.readState(context.Background(), db); !errors.Is(err, ErrUnsafe) {
		t.Fatal("foreign ledger accepted")
	}
}
