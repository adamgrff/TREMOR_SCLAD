package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type queryCounter struct{ n atomic.Int64 }

func (q *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.n.Add(1)
	return ctx
}
func (q *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Separate API container: no administrator/verification password or policy mount.
func TestProtectedHTTPServiceDisposable(t *testing.T) {
	if os.Getenv("I3_HTTP_TEST") != "yes" {
		t.Skip("disposable API HTTP service only")
	}
	if os.Getenv("PGPASSWORD") != "" || os.Getenv("DATABASE_URL") != "" {
		t.Fatal("privileged/fallback credential leaked")
	}
	for _, p := range []string{"/private/password", "/private/bundle.json", "/var/run/docker.sock"} {
		if _, e := os.ReadFile(p); !os.IsNotExist(e) {
			t.Fatal("secret isolation")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Exercise the exact executable configuration path, not a mocked gate.
	pool, gate, e := openRuntime(ctx, "protected")
	if e != nil {
		t.Fatal("runtime startup", e)
	}
	defer pool.Close()
	cfg := pool.Config()
	pool.Close()
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter
	acquire := cfg.BeforeAcquire
	cfg.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		ok := acquire(ctx, conn)
		if !ok {
			fmt.Printf("I4_RUNTIME_ACQUIRE_REJECT context=%v\n", ctx.Err())
		}
		return ok
	}
	connect := cfg.AfterConnect
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		err := connect(ctx, conn)
		if err != nil {
			fmt.Printf("I4_RUNTIME_CONNECT_REJECT error=%v context=%v\n", err, ctx.Err())
		}
		return err
	}
	pool, e = pgxpool.NewWithConfig(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	routes := apiRoutes(pool, "protected", gate).(*modeMux)
	root := http.NewServeMux()
	root.Handle("/", corsMiddleware(routes))
	var tested atomic.Bool
	root.HandleFunc("GET /internal/test-sql-count", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(counter.n.Load()) })
	root.HandleFunc("GET /internal/test-ready", func(w http.ResponseWriter, r *http.Request) {
		if !tested.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})
	listener, e := net.Listen("tcp", "127.0.0.1:8081")
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: root, ReadHeaderTimeout: time.Second}
	defer server.Close()
	go server.Serve(listener)
	client := &http.Client{Timeout: 20 * time.Second}
	request := func(method, path string) (int, []byte) {
		t.Helper()
		r, e := http.NewRequest(method, "http://127.0.0.1:8081"+path, strings.NewReader(`{}`))
		if e != nil {
			t.Fatal(e)
		}
		resp, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		if e != nil {
			t.Fatal(e)
		}
		return resp.StatusCode, raw
	}
	status, raw := request("GET", "/api/product-catalog")
	if status != 200 {
		t.Fatalf("catalog status %d: %s", status, raw)
	}
	var catalog struct {
		Products []struct {
			SKU string `json:"sku"`
		} `json:"products"`
	}
	if json.Unmarshal(raw, &catalog) != nil {
		t.Fatal("synthetic product fixture missing")
	}
	sku := "MISSING-SKU"
	cellCode := "MISSING-CELL"
	if len(catalog.Products) > 0 {
		cellCode = "T0-1"
	}
	if len(catalog.Products) > 0 {
		sku = catalog.Products[0].SKU
	}
	for _, pattern := range routes.patterns {
		t.Run(pattern, func(t *testing.T) {
			method, path := routeExample(pattern)
			if strings.Contains(path, "/api/products/") {
				path = strings.Replace(path, "/api/products/1", "/api/products/"+sku, 1)
			}
			if pattern == "GET /api/cells/{cellName}" {
				path = "/api/cells/" + cellCode
			}
			before := counter.n.Load()
			status, body := request(method, path)
			if !protectedReads[pattern] {
				if status != 501 || !strings.Contains(string(body), "protected_operation_unsupported") {
					t.Fatalf("write/blocked read reached handler: %d %s", status, body)
				}
				if counter.n.Load() != before {
					t.Fatal("blocked route executed SQL")
				}
				return
			}
			// The populated fixture has T0-1 and session 1; 404 is only
			// acceptable for known-missing resources in the empty fixture.
			expected := 200
			if pattern == "GET /api/receiving/sessions/latest" {
				expected = 404
			}
			if len(catalog.Products) == 0 && (strings.HasPrefix(pattern, "GET /api/products/") || pattern == "GET /api/cells/{cellName}" || pattern == "GET /api/receiving/sessions/{sessionID}") {
				expected = 404
			}

			if status != expected {
				t.Fatalf("read status=%d body=%s", status, body)
			}
			if pattern == "GET /api/products/{sku}/stock" && len(catalog.Products) > 0 {
				var stock productStockResponse
				if json.Unmarshal(body, &stock) != nil || stock.SKU != sku || len(stock.Cells) != 1 || stock.Cells[0].Quantity != 3 {
					t.Fatal("stock DTO/fixture mismatch")
				}
			}
			if !json.Valid(body) {
				t.Fatal("JSON HTTP contract lost")
			}
		})
	}
	// Browser-style wrong input remains a normal 400, not an unsupported/readiness error.
	if status, _ = request("GET", "/api/home/statistics?period=invalid"); status != 400 {
		t.Fatal("user validation changed")
	}
	for _, path := range []string{"/api/assembly/orders", "/api/assembly-preview/draft"} {
		if status, _ = request("HEAD", path); status != 501 {
			t.Fatal("HEAD bypass")
		}
	}
	for _, path := range []string{"/api//receiving/sessions", "/api/receiving/../receiving/sessions"} {
		if status, _ = request("POST", path); status != 501 {
			t.Fatal("canonical route bypass")
		}
	}
	// Real target mismatch must reject before handler SQL.
	rawConfig, e := os.ReadFile(os.Getenv("TREMOR_RUNTIME_CONFIG"))
	if e != nil {
		t.Fatal(e)
	}
	var wrong runtimeConfig
	if json.Unmarshal(rawConfig, &wrong) != nil {
		t.Fatal("runtime config")
	}
	wrong.Target.Deployment = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	badGate, e := runtimeClient(wrong)
	if e != nil {
		t.Fatal(e)
	}
	denied := httptest.NewServer(apiRoutes(pool, "protected", badGate))
	before := counter.n.Load()
	bad, e := client.Get(denied.URL + "/api/racks")
	if e != nil {
		t.Fatal(e)
	}
	bad.Body.Close()
	denied.Close()
	if bad.StatusCode != 503 || counter.n.Load() != before {
		t.Fatal("wrong target reached SQL")
	}
	for round := 0; round < 3; round++ {
		statuses := make(chan int, 8)
		for i := 0; i < 8; i++ {
			go func() {
				r, e := client.Get("http://127.0.0.1:8081/api/racks")
				if e != nil {
					statuses <- 0
					return
				}
				r.Body.Close()
				statuses <- r.StatusCode
			}()
		}
		for i := 0; i < 8; i++ {
			code := <-statuses
			if code != 200 && code != 503 {
				t.Fatalf("concurrent status %d", code)
			}
		}
		fmt.Printf("I4_HTTP_CONCURRENCY round=%d requests=8 status=200-or-503 no-retry=true\n", round)
	}
	var identity string
	if e = pool.QueryRow(context.Background(), "SELECT current_user").Scan(&identity); e != nil || identity != "tremor_api" {
		t.Fatal("runtime identity")
	}
	t.Logf("API_HTTP_CASES_PASS routes=%d reads=%d identity=%s blocked_SQL=0", len(routes.patterns), len(protectedReads), identity)
	tested.Store(true)
	// Remain available to the separate administrative TEST fault controller.
	<-time.After(240 * time.Second)
	t.Fatal(fmt.Errorf("TEST service was not cleaned up in time"))
}

func TestExplicitLegacyHTTPIntegration(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("isolated legacy PostgreSQL only")
	}
	db := receivingTestDB(t)
	server := httptest.NewServer(apiRoutes(db, "legacy-development", nil))
	defer server.Close()
	r, e := http.Get(server.URL + "/api/product-catalog")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal("legacy read contract changed")
	}
	r, e = http.Post(server.URL+"/api/racks", "application/json", strings.NewReader(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatalf("legacy write handler did not run: %d", r.StatusCode)
	}
}
