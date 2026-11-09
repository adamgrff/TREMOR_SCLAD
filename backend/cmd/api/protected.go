package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/readiness"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
)

// This explicit v1 allowlist never infers safety from the HTTP method.
var protectedReads = map[string]bool{
	"GET /health": true, "GET /api/deployment": true, "GET /api/ozon/status": true,
	"GET /api/products/{sku}": true, "GET /api/products/{sku}/stock": true,
	"GET /api/cells/{cellName}": true, "GET /api/racks": true, "GET /api/product-catalog": true,
	"GET /api/assembly/mapping-products": true, "GET /api/receiving/pending-summary": true,
	"GET /api/assembly/problems/archive": true,
	"GET /api/home/history":              true, "GET /api/home/statistics": true,
	"GET /api/receiving/sessions": true, "GET /api/receiving/sessions/active": true,
	"GET /api/receiving/sessions/latest": true, "GET /api/receiving/sessions/{sessionID}": true,
}

type modeMux struct {
	*http.ServeMux
	mode     string
	gate     *readiness.Client
	patterns []string
}

func (m *modeMux) HandleFunc(pattern string, h http.HandlerFunc) {
	m.patterns = append(m.patterns, pattern)
	var handler http.Handler = h
	if m.mode != "legacy-development" {
		if m.mode != "protected" || !protectedReads[pattern] {
			handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				protectedError(w, 501, "protected_operation_unsupported", "Операция пока недоступна в защищённом режиме. Данные не изменены.")
			})
		} else if pattern != "GET /health" {
			handler = m.gate.Require(h)
		}
	}
	m.ServeMux.Handle(pattern, handler)
}
func protectedError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "error": message})
}

// Only the runtime password and mTLS client key are part of API configuration.
// Production admission remains disabled: this adapter admits the fixed harness DB.
type runtimeConfig struct {
	Target       readiness.Target `json:"target"`
	DatabaseOID  uint32           `json:"database_oid"`
	PasswordFile string           `json:"password_file"`
	CAFile       string           `json:"ca_file"`
	CertFile     string           `json:"cert_file"`
	KeyFile      string           `json:"key_file"`
	VerifierURL  string           `json:"verifier_url"`
}

func runtimeClient(c runtimeConfig) (*readiness.Client, error) {
	if c.Target.Database != "tremor_e1b_collector_test" || c.Target.Schema != "public" || c.DatabaseOID == 0 {
		return nil, errors.New("production admission disabled")
	}
	cert, e := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if e != nil {
		return nil, e
	}
	raw, e := os.ReadFile(c.CAFile)
	if e != nil {
		return nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, errors.New("CA unavailable")
	}
	return readiness.NewClient(c.VerifierURL, c.Target, cert, roots, "verifier.test", "spiffe://tremor/verifier", 3e9)
}
func openRuntime(ctx context.Context, mode string) (*pgxpool.Pool, *readiness.Client, error) {
	if mode == "legacy-development" {
		url := os.Getenv("DATABASE_URL")
		if url == "" {
			return nil, nil, errors.New("explicit legacy credentials required")
		}
		cfg, e := pgxpool.ParseConfig(url)
		if e != nil {
			return nil, nil, errors.New("invalid development target")
		}
		ip := net.ParseIP(cfg.ConnConfig.Host)
		if cfg.ConnConfig.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, nil, errors.New("legacy development requires isolated loopback target")
		}
		cfg.ConnConfig.Fallbacks = nil
		p, e := pgxpool.NewWithConfig(ctx, cfg)
		if e == nil {
			e = p.Ping(ctx)
		}
		if e != nil && p != nil {
			p.Close()
		}
		return p, nil, e
	}
	if mode != "protected" {
		return nil, nil, errors.New("explicit API mode required")
	}
	raw, e := os.ReadFile(os.Getenv("TREMOR_RUNTIME_CONFIG"))
	if e != nil || len(raw) > 16384 {
		return nil, nil, errors.New("runtime config unavailable")
	}
	var cfg runtimeConfig
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || d.Decode(new(any)) != io.EOF {
		return nil, nil, errors.New("invalid runtime config")
	}
	c, e := runtimeClient(cfg)
	if e != nil {
		return nil, nil, e
	}
	password, e := os.ReadFile(cfg.PasswordFile)
	if e != nil || len(password) == 0 || len(password) > 4096 {
		return nil, nil, errors.New("runtime credential unavailable")
	}
	p, e := pgxpool.ParseConfig("host=127.0.0.1 port=5432 dbname=tremor_e1b_collector_test user=tremor_api sslmode=disable")
	if e != nil {
		return nil, nil, e
	}
	p.ConnConfig.Password = string(password)
	pool, e := readiness.RuntimePool(ctx, p, c, cfg.DatabaseOID)
	return pool, c, e
}
