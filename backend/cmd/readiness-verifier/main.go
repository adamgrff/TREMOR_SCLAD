package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/migrations"
	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/readiness"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type config struct {
	Listen            string            `json:"listen"`
	Target            migrations.Target `json:"target"`
	PasswordFile      string            `json:"password_file"`
	CAFile            string            `json:"ca_file"`
	CertFile          string            `json:"cert_file"`
	KeyFile           string            `json:"key_file"`
	ClientURI         string            `json:"client_uri"`
	BaselineManifest  string            `json:"baseline_manifest"`
	ExtensionManifest string            `json:"extension_manifest"`
	DescriptorFile    string            `json:"descriptor_file"`
	PayloadFile       string            `json:"payload_file"`
	SourceFile        string            `json:"source_file"`
	ProtectedPlanFile string            `json:"protected_plan_file"`
}

func readJSON(path string, v any) error {
	data, e := os.ReadFile(path)
	if e != nil || len(data) > 16<<20 {
		return errors.New("configuration unavailable")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid configuration")
	}
	return nil
}
func serve(path string) error {
	var cfg config
	if readJSON(path, &cfg) != nil || cfg.Target.User != "tremor_migrator" || cfg.Listen != "127.0.0.1:8443" {
		return errors.New("invalid fixed verifier configuration")
	}
	password, e := os.ReadFile(cfg.PasswordFile)
	if e != nil || len(password) == 0 || len(password) > 4096 {
		return errors.New("verifier credential unavailable")
	}
	cfg.Target.Password = strings.TrimSuffix(string(password), "\n")
	if cfg.Target.Validate() != nil {
		return errors.New("invalid target")
	}
	raw, e := os.ReadFile(cfg.DescriptorFile)
	if e != nil {
		return errors.New("descriptor unavailable")
	}
	payload, e := os.ReadFile(cfg.PayloadFile)
	if e != nil {
		return errors.New("payload unavailable")
	}
	approved, e := migrations.LoadApprovedSecurityTransition(raw, payload)
	if e != nil {
		return errors.New("release approval unavailable; verifier disabled")
	}
	var source migrations.TransitionSnapshot
	if readJSON(cfg.SourceFile, &source) != nil {
		return errors.New("source archive unavailable")
	}
	bound, e := migrations.BindSecurityTransition(approved, source.Target, source)
	if e != nil {
		return errors.New("invalid bound source")
	}
	raw, e = os.ReadFile(cfg.ProtectedPlanFile)
	if e != nil {
		return errors.New("protected plan unavailable")
	}
	plan, e := migrations.LoadApprovedProtectedMigration(raw)
	if e != nil {
		return errors.New("protected release approval unavailable")
	}
	base, e := migrations.Load(cfg.BaselineManifest)
	if e != nil {
		return errors.New("baseline unavailable")
	}
	manifest, e := migrations.LoadExtension(cfg.ExtensionManifest, base)
	if e != nil {
		return errors.New("manifest unavailable")
	}
	cert, e := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if e != nil {
		return errors.New("TLS identity unavailable")
	}
	pem, e := os.ReadFile(cfg.CAFile)
	if e != nil {
		return errors.New("TLS CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return errors.New("invalid TLS CA")
	}
	tlsConfig, e := readiness.ServerTLS(cert, roots, cfg.ClientURI)
	if e != nil {
		return e
	}
	target := readiness.Target{Cluster: source.Target.ClusterID, Database: source.Target.Database, Deployment: source.Target.DeploymentID, Schema: source.Target.Schema}
	probe := func(ctx context.Context) error {
		return migrations.VerifyProtectedExtension(ctx, cfg.Target, manifest, bound, plan)
	}
	initial, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = probe(initial); e != nil {
		return errors.New("initial protected verification failed")
	}
	handler, e := readiness.Handler(target, probe, 5*time.Second, 2)
	if e != nil {
		return e
	}
	server := http.Server{Addr: cfg.Listen, Handler: handler, TLSConfig: tlsConfig, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 8 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	return server.ListenAndServeTLS("", "")
}
func main() {
	path := flag.String("config", "", "fixed private verifier configuration")
	flag.Parse()
	if *path == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "configuration required")
		os.Exit(2)
	}
	if serve(*path) != nil {
		fmt.Fprintln(os.Stderr, "readiness verifier unavailable")
		os.Exit(1)
	}
}
