package migrations

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func identityRunner(t *testing.T) *Runner {
	t.Helper()
	target, conn := fixture(t)
	_ = conn
	target.Profile = IdentityProfile
	m, err := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(target, m)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIdentityProfileFreshAndIsolation(t *testing.T) {
	ctx := context.Background()
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := identityRunner(t)
			if _, err := r.Run(ctx, "apply"); err != nil {
				t.Fatal(err)
			}
			if s, err := r.ValidateWarehouse(ctx, syntheticWarehouse); err != nil || s.State != "schema-ready" {
				t.Fatalf("%+v %v", s, err)
			}
			if _, err := r.BootstrapWarehouse(ctx, syntheticWarehouse); err != nil {
				t.Fatal(err)
			}
			conn, _, err := r.target.connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var id, again string
			q := `SELECT deployment_id::text FROM ` + `"` + r.target.Schema + `".deployment_identity WHERE singleton`
			if conn.QueryRow(ctx, q).Scan(&id) != nil {
				t.Fatal("identity absent")
			}
			closeConnection(conn)
			if _, err = r.Run(ctx, "apply"); err != nil {
				t.Fatal(err)
			}
			conn, _, err = r.target.connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = conn.QueryRow(ctx, q).Scan(&again)
			closeConnection(conn)
			if err != nil || id != again || ids[id] {
				t.Fatal("identity unstable/not independent")
			}
			ids[id] = true
		})
	}
}

func TestIdentityProfileDoesNotAdoptV1(t *testing.T) {
	r, _ := warehouseFixture(t)
	ctx := context.Background()
	m, err := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	target := r.target
	target.Profile = IdentityProfile
	v2, err := New(target, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v2.Run(ctx, "apply"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("v1 adoption: %v", err)
	}
	if _, err = r.Run(ctx, "verify"); err != nil {
		t.Fatal("v1 changed", err)
	}
}
