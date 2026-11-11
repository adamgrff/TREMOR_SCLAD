package readiness

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

func TestRuntimeRejectsPrivilegedConfiguration(t *testing.T) {
	for _, user := range []string{"tremor_deployer", "tremor_migrator", "postgres"} {
		cfg, e := pgxpool.ParseConfig("host=127.0.0.1 user=" + user + " dbname=test_db sslmode=disable")
		if e != nil {
			t.Fatal(e)
		}
		if pool, e := RuntimePool(context.Background(), cfg, &Client{target: testTarget}, 42); e == nil || pool != nil {
			t.Fatal("privileged runtime accepted")
		}
	}
	if pool, e := RuntimePool(context.Background(), nil, nil, 0); e == nil || pool != nil {
		t.Fatal("missing identity accepted")
	}
}
