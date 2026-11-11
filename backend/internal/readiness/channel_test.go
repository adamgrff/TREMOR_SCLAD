package readiness

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testPKI struct {
	ca    *x509.Certificate
	key   *ecdsa.PrivateKey
	roots *x509.CertPool
}

func pki(t *testing.T) testPKI {
	t.Helper()
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	c := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "TEST CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, e := x509.CreateCertificate(rand.Reader, c, c, &k.PublicKey, k)
	if e != nil {
		t.Fatal(e)
	}
	ca, e := x509.ParseCertificate(raw)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return testPKI{ca, k, pool}
}
func (p testPKI) leaf(t *testing.T, uri string, server bool) tls.Certificate {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(uri)
	usage := x509.ExtKeyUsageClientAuth
	if server {
		usage = x509.ExtKeyUsageServerAuth
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if e != nil {
		t.Fatal(e)
	}
	c := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), URIs: []*url.URL{u}, DNSNames: []string{"verifier.test"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	raw, e := x509.CreateCertificate(rand.Reader, c, p.ca, &key.PublicKey, p.key)
	if e != nil {
		t.Fatal(e)
	}
	return tls.Certificate{Certificate: [][]byte{raw}, PrivateKey: key}
}

var testTarget = Target{"123", "test_db", "test_deployment", "public"}

func TestRequestAdmissionIsFreshBoundAndRevoked(t *testing.T) {
	p := pki(t)
	var calls atomic.Int32
	h, e := Handler(testTarget, func(context.Context) error {
		if calls.Add(1) != 1 {
			return errors.New("TEST verifier unavailable")
		}
		return nil
	}, time.Second, 1)
	if e != nil {
		t.Fatal(e)
	}
	s := testServer(t, p, h)
	c := testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
	other := testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
	var saved context.Context
	entered := 0
	next := c.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered++
		saved = r.Context()
		for i := 0; i < 3; i++ {
			if checkRuntimeReadiness(saved, c) != nil {
				t.Error("same admitted operation re-probed")
			}
		}
		if calls.Load() != 1 {
			t.Error("same operation created duplicate probes")
		}
		if checkRuntimeReadiness(saved, other) == nil {
			t.Error("live admission accepted by different client")
		}
	}))
	next.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "https://api/", nil))
	if entered != 1 || calls.Load() != 2 {
		t.Fatal("duplicate probe or absent handler")
	}
	if checkRuntimeReadiness(saved, c) == nil {
		t.Fatal("completed operation retained READY")
	}
	w := httptest.NewRecorder()
	next.ServeHTTP(w, httptest.NewRequest("GET", "https://api/", nil))
	if entered != 1 || w.Code != 503 {
		t.Fatal("unavailable verifier entered handler")
	}
	if checkRuntimeReadiness(saved, other) == nil {
		t.Fatal("foreign client admission reused")
	}
}

func testServer(t *testing.T, p testPKI, h http.Handler) *httptest.Server {
	t.Helper()
	cfg, e := ServerTLS(p.leaf(t, "spiffe://tremor/verifier", true), p.roots, "spiffe://tremor/api")
	if e != nil {
		t.Fatal(e)
	}
	s := httptest.NewUnstartedServer(h)
	s.TLS = cfg
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}
func testClient(t *testing.T, p testPKI, s *httptest.Server, cert tls.Certificate, target Target, timeout time.Duration) *Client {
	t.Helper()
	c, e := NewClient(s.URL+"/internal/readiness", target, cert, p.roots, "verifier.test", "spiffe://tremor/verifier", timeout)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestRealMTLS(t *testing.T) {
	p := pki(t)
	var calls atomic.Int32
	h, e := Handler(testTarget, func(context.Context) error { calls.Add(1); return nil }, time.Second, 1)
	if e != nil {
		t.Fatal(e)
	}
	s := testServer(t, p, h)
	t.Run("fresh-ready", func(t *testing.T) {
		c := testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
		if c.Check(context.Background()) != nil || c.Check(context.Background()) != nil || calls.Load() != 2 {
			t.Fatal("fresh probes not executed")
		}
	})
	t.Run("wrong-client-SAN", func(t *testing.T) {
		c := testClient(t, p, s, p.leaf(t, "spiffe://tremor/other", false), testTarget, time.Second)
		if c.Check(context.Background()) == nil {
			t.Fatal("wrong SAN accepted")
		}
	})
	t.Run("untrusted-client", func(t *testing.T) {
		other := pki(t)
		c := testClient(t, p, s, other.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
		if c.Check(context.Background()) == nil {
			t.Fatal("untrusted CA accepted")
		}
	})
	t.Run("wrong-target", func(t *testing.T) {
		wrong := testTarget
		wrong.Database = "other"
		c := testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), wrong, time.Second)
		if c.Check(context.Background()) == nil {
			t.Fatal("wrong target accepted")
		}
	})
	t.Run("wrong-server-SAN", func(t *testing.T) {
		c := testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
		c.http.Transport.(*http.Transport).TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error { return verifyPeer(state, "spiffe://tremor/other") }
		if c.Check(context.Background()) == nil {
			t.Fatal("wrong server accepted")
		}
	})
}
func TestReplayAndMalformedReply(t *testing.T) {
	p := pki(t)
	var saved response
	var mu sync.Mutex
	s := testServer(t, p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q request
		_ = decode(r.Body, &q)
		mu.Lock()
		defer mu.Unlock()
		if saved.Nonce == "" {
			saved = response{"READY", q.Nonce, testTarget}
		}
		_ = json.NewEncoder(w).Encode(saved)
	}))
	c := testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
	if e := c.Check(context.Background()); e != nil {
		t.Fatal(e)
	}
	if c.Check(context.Background()) == nil {
		t.Fatal("replayed response accepted")
	}
	for _, raw := range []string{`{"nonce":"a","nonce":"b"}`, `{"Nonce":"a"}`, `{"nonce":"a","target":"evil"}`, `{"nonce":"a"}{}`, `{"nonce":"a"}` + strings.Repeat(" ", 4096)} {
		var q request
		if decode(strings.NewReader(raw), &q) == nil {
			t.Fatal("invalid protocol accepted")
		}
	}
}
func TestTimeoutLimitAndFailClosed(t *testing.T) {
	p := pki(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	h, _ := Handler(testTarget, func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, 200*time.Millisecond, 1)
	s := testServer(t, p, h)
	c := testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
	done := make(chan error, 1)
	go func() { done <- c.Check(context.Background()) }()
	<-entered
	if c.Check(context.Background()) == nil {
		t.Fatal("concurrency limit bypassed")
	}
	if <-done == nil {
		t.Fatal("timeout produced READY")
	}
	close(release)
	denied, _ := Handler(testTarget, func(context.Context) error { return errors.New("private internal failure") }, time.Second, 1)
	s2 := testServer(t, p, denied)
	c2 := testClient(t, p, s2, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
	called := false
	w := httptest.NewRecorder()
	c2.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, httptest.NewRequest("GET", "https://api/", nil))
	if w.Code != 503 || called || strings.Contains(w.Body.String(), "private") {
		t.Fatal("unsafe middleware refusal")
	}
}

func TestUntrustedServerAndMissingClient(t *testing.T) {
	p := pki(t)
	h, _ := Handler(testTarget, func(context.Context) error { return nil }, time.Second, 1)
	s := testServer(t, p, h)
	c := testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
	c.http.Transport.(*http.Transport).TLSClientConfig.RootCAs = pki(t).roots
	if c.Check(context.Background()) == nil {
		t.Fatal("untrusted server accepted")
	}
	c = testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
	c.http.Transport.(*http.Transport).TLSClientConfig.Certificates = nil
	if c.Check(context.Background()) == nil {
		t.Fatal("anonymous client accepted")
	}
}
func TestHandlerStrictRequestAndStatus(t *testing.T) {
	p := pki(t)
	h, _ := Handler(testTarget, func(context.Context) error { return errors.New("unavailable") }, time.Second, 1)
	s := testServer(t, p, h)
	c := testClient(t, p, s, p.leaf(t, "spiffe://tremor/api", false), testTarget, time.Second)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"nonce":"` + strings.Repeat("a", 64) + `"}`, 503},
		{`{"nonce":"` + strings.Repeat("a", 64) + `","sql":"SELECT 1"}`, 400},
		{`{"nonce":"a","target":"other"}`, 400},
	} {
		r, e := c.http.Post(s.URL+"/internal/readiness", "application/json", strings.NewReader(tc.body))
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if r.StatusCode != tc.status {
			t.Fatalf("status %d, want %d", r.StatusCode, tc.status)
		}
	}
}
