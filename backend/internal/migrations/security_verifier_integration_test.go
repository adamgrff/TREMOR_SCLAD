package migrations

// TEST-only executable modes reuse the production channel and verifier. Sealed
// TEST approvals are exported by the independently approved I2.2 fixture; the
// runtime containers cannot mint approvals or access provisioning credentials.
import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/readiness"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type i3Bundle struct {
	FinalEvidence  TransitionSnapshot
	Target         Target
	Descriptor     SecurityTransitionDescriptor
	TransitionSHA  string
	Source         TransitionSnapshot
	Plan           ProtectedMigrationPlan
	PlanSHA        string
	Base           Plan
	Extension      ExtensionPlan
	Bodies, Chains []string
}
type i3Public struct {
	Target readiness.Target
	OID    uint32
}

func i3Write(t *testing.T, path string, v any) {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func i3Read(t *testing.T, path string, v any) {
	t.Helper()
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("private TEST configuration unavailable")
	}
	if strictJSON(raw, v) != nil {
		t.Fatal("invalid TEST configuration")
	}
}
func i3Export(t *testing.T, ctx context.Context, admin *pgx.Conn, target Target, m *ExtensionManifest, b BoundSecurityTransition, a ApprovedProtectedMigration, oid uint32) {
	if os.Getenv("I3_EXPORT") != "yes" {
		return
	}
	// Credentials are generated separately, never inherited from the deployer.
	password := func() string {
		buf := make([]byte, 32)
		if _, e := rand.Read(buf); e != nil {
			t.Fatal(e)
		}
		return hex.EncodeToString(buf)
	}
	for _, role := range []string{"tremor_api", "tremor_migrator"} {
		pwd := password()
		if _, e := admin.Exec(ctx, "ALTER ROLE "+pgx.Identifier{role}.Sanitize()+" PASSWORD '"+pwd+"'"); e != nil {
			t.Fatal("TEST credential rotation failed")
		}
		dir := "/i3/api"
		if role == "tremor_migrator" {
			dir = "/i3/verifier"
		}
		if e := os.WriteFile(dir+"/password", []byte(pwd), 0600); e != nil {
			t.Fatal(e)
		}
	}
	target.User = "tremor_migrator"
	target.Password = ""
	root := target
	root.User = "e1a_test_admin"
	tx, e := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		t.Fatal(e)
	}
	evidence, e := collectSecuritySnapshotTx(ctx, tx, root, oid, protectedPolicyDescriptor(b, a.plan.After), a.plan.After.Scope, m)
	rollback(tx)
	if e != nil {
		t.Fatal(e)
	}
	i3Write(t, "/i3/verifier/bundle.json", i3Bundle{FinalEvidence: evidence.snapshot, Target: target, Descriptor: b.approved.descriptor, TransitionSHA: b.approved.sha, Source: b.source, Plan: a.plan, PlanSHA: a.sha, Base: m.base.Plan(), Extension: m.Plan(), Bodies: m.bodies, Chains: m.chains})
	pub := i3Public{readiness.Target{Cluster: b.source.Target.ClusterID, Database: target.Database, Deployment: b.source.Target.DeploymentID, Schema: target.Schema}, oid}
	i3Write(t, "/i3/api/target.json", pub)
	i3Write(t, "/i3/api/runtime.json", map[string]any{"target": pub.Target, "database_oid": oid, "password_file": "/runtime/password", "ca_file": "/runtime/ca.pem", "cert_file": "/runtime/cert.pem", "key_file": "/runtime/key.pem", "verifier_url": "https://127.0.0.1:8443/internal/readiness"})
	caKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Disposable I3 CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if e != nil {
		t.Fatal(e)
	}
	ca, e = x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	for _, side := range []string{"api", "verifier"} {
		dir := "/i3/" + side
		key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		uri, _ := url.Parse("spiffe://tremor/" + side)
		usage := x509.ExtKeyUsageClientAuth
		if side == "verifier" {
			usage = x509.ExtKeyUsageServerAuth
		}
		serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
		c := &x509.Certificate{SerialNumber: serial, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, DNSNames: []string{"verifier.test"}, URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		raw, e := x509.CreateCertificate(rand.Reader, c, ca, &key.PublicKey, caKey)
		if e != nil {
			t.Fatal(e)
		}
		k, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			t.Fatal(e)
		}
		for name, data := range map[string][]byte{"ca.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), "key.pem": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})} {
			if e = os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := VerifyProtectedExtension(ctx, func() Target {
		v := target
		raw, _ := os.ReadFile("/i3/verifier/password")
		v.Password = string(raw)
		return v
	}(), m, b, a); e != nil {
		t.Fatal("rotated migrator verification", e)
	}
}
func i3TLS(t *testing.T, dir string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	c, e := tls.LoadX509KeyPair(dir+"/cert.pem", dir+"/key.pem")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(dir + "/ca.pem")
	if e != nil {
		t.Fatal(e)
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(raw) {
		t.Fatal("CA")
	}
	return c, p
}
func i3Client(t *testing.T, dir string) (*readiness.Client, i3Public) {
	t.Helper()
	var pub i3Public
	i3Read(t, dir+"/target.json", &pub)
	cert, roots := i3TLS(t, dir)
	c, e := readiness.NewClient("https://127.0.0.1:8443/internal/readiness", pub.Target, cert, roots, "verifier.test", "spiffe://tremor/verifier", 3*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	return c, pub
}
func TestI3VerifierServiceDisposable(t *testing.T) {
	if os.Getenv("I3_MODE") != "verifier" {
		t.Skip("isolated TEST verifier only")
	}
	if os.Getenv("PGPASSWORD") != "" {
		t.Fatal("administrative secret leaked")
	}
	var v i3Bundle
	i3Read(t, "/private/bundle.json", &v)
	if v.Target.User != "tremor_migrator" || v.Target.Database != "tremor_e1b_collector_test" || v.Target.Host != "127.0.0.1" || transitionDigest(v.Descriptor) != v.TransitionSHA || transitionDigest(v.Plan) != v.PlanSHA {
		t.Fatal("invalid sealed TEST policy")
	}
	b, e := BindSecurityTransition(ApprovedSecurityTransition{v.Descriptor, v.TransitionSHA}, v.Source.Target, v.Source)
	if e != nil {
		t.Fatal(e)
	}
	base, e := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if e != nil || transitionDigest(base.Plan()) != transitionDigest(v.Base) {
		t.Fatal("TEST baseline mismatch", e)
	}
	m := &ExtensionManifest{plan: v.Extension, base: base, bodies: v.Bodies, chains: v.Chains}
	a := ApprovedProtectedMigration{v.Plan, v.PlanSHA}
	raw, e := os.ReadFile("/private/password")
	if e != nil {
		t.Fatal(e)
	}
	v.Target.Password = string(raw)
	probe := func(ctx context.Context) error { return VerifyProtectedExtension(ctx, v.Target, m, b, a) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = probe(ctx); e != nil {
		t.Fatal("real migrator initial verification", e)
	}
	cert, roots := i3TLS(t, "/private")
	cfg, e := readiness.ServerTLS(cert, roots, "spiffe://tremor/api")
	if e != nil {
		t.Fatal(e)
	}
	target := readiness.Target{Cluster: v.Source.Target.ClusterID, Database: v.Source.Target.Database, Deployment: v.Source.Target.DeploymentID, Schema: v.Source.Target.Schema}
	h, e := readiness.Handler(target, probe, 2*time.Second, 2)
	if e != nil {
		t.Fatal(e)
	}
	t.Log("REAL_VERIFIER_IDENTITY=tremor_migrator INITIAL_PROTECTED=PASS")
	s := http.Server{Addr: "127.0.0.1:8443", Handler: h, TLSConfig: cfg, ReadHeaderTimeout: time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 4 * time.Second}
	if e = s.ListenAndServeTLS("", ""); e != nil {
		t.Fatal(e)
	}
}
func TestI3APIIntegrationDisposable(t *testing.T) {
	if os.Getenv("I3_MODE") != "api" {
		t.Skip("isolated TEST API only")
	}
	if os.Getenv("PGPASSWORD") != "" {
		t.Fatal("admin secret leaked")
	}
	for _, path := range []string{"/private/bundle.json", "/private/password", "/var/run/docker.sock", "/i3/verifier/password"} {
		if _, e := os.ReadFile(path); !os.IsNotExist(e) {
			t.Fatal("unexpected verifier secret or socket access")
		}
	}
	c, pub := i3Client(t, "/runtime")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		if c.Check(ctx) == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("real READY unavailable")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cfg, e := pgxpool.ParseConfig("host=127.0.0.1 port=5432 user=tremor_api dbname=tremor_e1b_collector_test sslmode=disable")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("/runtime/password")
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.Password = string(raw)
	pool, e := readiness.RuntimePool(ctx, cfg, c, pub.OID)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	var user string
	if e = pool.QueryRow(ctx, "SELECT current_user").Scan(&user); e != nil || user != "tremor_api" {
		t.Fatal("runtime identity", e)
	}
	for _, sql := range []string{"SELECT * FROM public.schema_migrations", "SELECT * FROM tremor_deployment.security_transitions", "CREATE TABLE public.forbidden(id int)", "DELETE FROM public.schema_migrations", "SET ROLE tremor_migrator", "SET ROLE tremor_owner", "SELECT public.protected_context()"} {
		if _, e = pool.Exec(ctx, sql); e == nil {
			t.Fatal("API privilege accepted")
		}
	}
	w := httptest.NewRecorder()
	called := false
	c.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })).ServeHTTP(w, httptest.NewRequest("GET", "http://api/protected-probe", nil))
	if !called || w.Code != 204 {
		t.Fatal("protected API action denied")
	}
	t.Log("REAL_CROSS_CONTAINER_MTLS_READY=PASS API_IDENTITY=tremor_api SECRET_ISOLATION=PASS ACL_DENIALS=PASS")
}
func TestI3FaultIntegrationDisposable(t *testing.T) {
	if os.Getenv("I3_HTTP_ASSERT") == "yes" {
		i3WaitHTTP(t)
	}
	if os.Getenv("I3_MODE") != "faults" {
		t.Skip("isolated TEST controller only")
	}
	c, _ := i3Client(t, "/runtime")
	ctx := context.Background()
	cfg, e := pgx.ParseConfig("host=127.0.0.1 user=e1a_test_admin dbname=tremor_e1b_collector_test sslmode=disable")
	if e != nil {
		t.Fatal(e)
	}
	cfg.Password = os.Getenv("PGPASSWORD")
	admin, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer closeConnection(admin)
	if e = c.Check(ctx); e != nil {
		t.Fatal("initial READY", e)
	}
	i3HTTPIntegrity(t, ctx, admin)
	for _, fault := range []struct{ name, apply, restore string }{
		{"grant", "GRANT SELECT ON public.warehouses TO PUBLIC", "REVOKE SELECT ON public.warehouses FROM PUBLIC"},
		{"membership", "GRANT tremor_owner TO tremor_api", "REVOKE tremor_owner FROM tremor_api"},
		{"parameter", "GRANT SET ON PARAMETER session_replication_role TO tremor_api", "REVOKE SET ON PARAMETER session_replication_role FROM tremor_api"},
		{"SD-context", "ALTER FUNCTION public.protected_context() SECURITY INVOKER", "ALTER FUNCTION public.protected_context() SECURITY DEFINER"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			if _, e := admin.Exec(ctx, fault.apply); e != nil {
				t.Fatal(e)
			}
			defer func() {
				if _, e := admin.Exec(ctx, fault.restore); e != nil {
					t.Fatal(e)
				}
			}()
			if e := c.Check(ctx); e == nil {
				t.Fatal("false READY")
			}
			i3HTTPStatus(t, 503)
		})
		if e := c.Check(ctx); e != nil {
			t.Fatal("restored state denied", e)
		}
	}
	// Removing the receipt is rolled back after the independent verifier observes
	// a committed absent receipt; retain exact original rows in a private temp copy.
	if _, e = admin.Exec(ctx, "CREATE TEMP TABLE saved_i3_receipt AS TABLE tremor_deployment.security_transitions; DELETE FROM tremor_deployment.security_transitions", pgx.QueryExecModeSimpleProtocol); e != nil {
		t.Fatal(e)
	}
	if e = c.Check(ctx); e == nil {
		t.Fatal("missing receipt accepted")
	}
	i3HTTPStatus(t, 503)
	if _, e = admin.Exec(ctx, "INSERT INTO tremor_deployment.security_transitions SELECT * FROM saved_i3_receipt", pgx.QueryExecModeSimpleProtocol); e != nil {
		t.Fatal(e)
	}
	if e = c.Check(ctx); e != nil {
		t.Fatal("receipt restoration", e)
	}
	if _, e = admin.Exec(ctx, "UPDATE tremor_deployment.security_transitions SET descriptor_sha256=repeat('a',64)"); e != nil {
		t.Fatal(e)
	}
	if c.Check(ctx) == nil {
		t.Fatal("wrong approved receipt anchor accepted")
	}
	i3HTTPStatus(t, 503)
	if _, e = admin.Exec(ctx, "UPDATE tremor_deployment.security_transitions t SET descriptor_sha256=s.descriptor_sha256 FROM saved_i3_receipt s WHERE t.transition_id=s.transition_id"); e != nil {
		t.Fatal(e)
	}
	if c.Check(ctx) != nil {
		t.Fatal("restored approved anchor denied")
	}
	// Direct read-only identity has no mutation privileges even outside READ ONLY.
	raw, e := os.ReadFile("/verifier-password/password")
	if e != nil {
		t.Fatal(e)
	}
	mig := cfg.Copy()
	mig.User = "tremor_migrator"
	mig.Password = string(raw)
	conn, e := pgx.ConnectConfig(ctx, mig)
	if e != nil {
		t.Fatal(e)
	}
	defer closeConnection(conn)
	for _, sql := range []string{"DELETE FROM public.schema_migrations", "DELETE FROM public.warehouses", "CREATE TABLE public.bad(id int)", "SET ROLE tremor_deployer"} {
		if _, e = conn.Exec(ctx, sql); e == nil {
			t.Fatal("verifier mutation privilege")
		}
	}
	var u string
	if e = conn.QueryRow(ctx, "SELECT current_user").Scan(&u); e != nil || u != "tremor_migrator" {
		t.Fatal("verifier identity")
	}
	i3HTTPStatus(t, 200)
	i3HTTPIntegrity(t, ctx, admin)
	t.Log("REAL_VERIFIER_DRIFT_REFUSAL=PASS READ_ONLY_ROLE_ACL=PASS")
}

func TestI3OfflineIntegrationDisposable(t *testing.T) {
	if os.Getenv("I3_MODE") != "offline" {
		t.Skip("isolated TEST offline API only")
	}
	c, _ := i3Client(t, "/runtime")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c.Check(ctx) == nil {
		t.Fatal("unavailable verifier accepted")
	}
	called := false
	w := httptest.NewRecorder()
	c.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, httptest.NewRequest("GET", "http://api/protected-probe", nil))
	if called || w.Code != 503 {
		t.Fatal("API failed open")
	}
	i3HTTPStatus(t, 503)
	t.Log("REAL_OFFLINE_VERIFIER_API_503=PASS")
}

func i3WaitHTTP(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, e := client.Get("http://127.0.0.1:8081/internal/test-ready")
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == 204 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("real API HTTP suite did not complete")
}
func i3HTTPStatus(t *testing.T, want int) {
	t.Helper()
	if os.Getenv("I3_HTTP_ASSERT") != "yes" {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	r, e := client.Get("http://127.0.0.1:8081/api/racks")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	if r.StatusCode != want {
		t.Fatalf("real API HTTP status=%d want=%d", r.StatusCode, want)
	}
}

func i3HTTPIntegrity(t *testing.T, ctx context.Context, admin *pgx.Conn) {
	t.Helper()
	if os.Getenv("I3_HTTP_ASSERT") != "yes" {
		return
	}
	var v i3Bundle
	i3Read(t, "/controller/bundle.json", &v)
	base, e := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if e != nil {
		t.Fatal(e)
	}
	m := &ExtensionManifest{plan: v.Extension, base: base, bodies: v.Bodies, chains: v.Chains}
	b, e := BindSecurityTransition(ApprovedSecurityTransition{v.Descriptor, v.TransitionSHA}, v.Source.Target, v.Source)
	if e != nil {
		t.Fatal(e)
	}
	root := v.Target
	root.User = "e1a_test_admin"
	tx, e := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		t.Fatal(e)
	}
	defer rollback(tx)
	got, e := collectSecuritySnapshotTx(ctx, tx, root, v.Source.DatabaseOID, protectedPolicyDescriptor(b, v.Plan.After), v.Plan.After.Scope, m)
	if e != nil {
		t.Fatal(e)
	}
	want := v.FinalEvidence
	s := got.snapshot
	if s.BusinessDataSHA != want.BusinessDataSHA || s.IdentitySHA != want.IdentitySHA || s.BusinessReceiptsSHA != want.BusinessReceiptsSHA || s.SequenceStateSHA != want.SequenceStateSHA || !reflect.DeepEqual(s.Ledgers, want.Ledgers) {
		t.Fatal("HTTP operations changed business/identity/receipts/sequences/ledgers")
	}
	t.Log("REAL_HTTP_BUSINESS_AND_LEDGER_PRESERVATION=PASS")
}
