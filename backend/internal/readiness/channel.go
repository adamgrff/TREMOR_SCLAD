// Package readiness implements a minimal authenticated verdict channel. A Probe
// must perform real verification on every call; this package stores no READY.
package readiness

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type Target struct {
	Cluster    string `json:"cluster"`
	Database   string `json:"database"`
	Deployment string `json:"deployment"`
	Schema     string `json:"schema"`
}
type request struct {
	Nonce string `json:"nonce"`
}
type response struct {
	Verdict string `json:"verdict"`
	Nonce   string `json:"nonce"`
	Target  Target `json:"target"`
}
type Probe func(context.Context) error

func validTarget(t Target) bool {
	return t.Cluster != "" && t.Database != "" && t.Deployment != "" && t.Schema != ""
}
func decode(r io.Reader, v any) error {
	raw, e := io.ReadAll(io.LimitReader(r, 4097))
	if e != nil || len(raw) > 4096 {
		return errors.New("invalid payload")
	}
	tokens := json.NewDecoder(bytes.NewReader(raw))
	var value func() error
	value = func() error {
		t, e := tokens.Token()
		if e != nil {
			return e
		}
		if d, ok := t.(json.Delim); ok {
			switch d {
			case '{':
				seen := map[string]bool{}
				for tokens.More() {
					k, e := tokens.Token()
					if e != nil {
						return e
					}
					name, ok := k.(string)
					if !ok || seen[name] {
						return errors.New("duplicate field")
					}
					seen[name] = true
					if e = value(); e != nil {
						return e
					}
				}
				_, e = tokens.Token()
				return e
			case '[':
				for tokens.More() {
					if e = value(); e != nil {
						return e
					}
				}
				_, e = tokens.Token()
				return e
			default:
				return errors.New("invalid delimiter")
			}
		}
		return nil
	}
	if value() != nil {
		return errors.New("invalid payload")
	}
	if _, e = tokens.Token(); e != io.EOF {
		return errors.New("trailing payload")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errors.New("object required")
	}
	allowed := map[string]bool{"nonce": true}
	if _, ok := v.(*response); ok {
		allowed["verdict"] = true
		allowed["target"] = true
		var target map[string]json.RawMessage
		if json.Unmarshal(fields["target"], &target) != nil || len(target) != 4 {
			return errors.New("invalid target")
		}
		for k := range target {
			if k != "cluster" && k != "database" && k != "deployment" && k != "schema" {
				return errors.New("unknown target field")
			}
		}
	}
	if len(fields) != len(allowed) {
		return errors.New("incomplete payload")
	}
	for k := range fields {
		if !allowed[k] {
			return errors.New("unknown field")
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func nonceValid(n string) bool {
	b, e := hex.DecodeString(n)
	return e == nil && len(b) == 32 && strings.ToLower(n) == n
}

// Dedicated CA and exact certificate identity, not arbitrary members of a CA.
func ServerTLS(cert tls.Certificate, ca *x509.CertPool, peerURI string) (*tls.Config, error) {
	if ca == nil || peerURI == "" || len(cert.Certificate) == 0 {
		return nil, errors.New("mTLS identity required")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: ca, ClientAuth: tls.RequireAndVerifyClientCert, VerifyConnection: func(c tls.ConnectionState) error { return verifyPeer(c, peerURI) }}, nil
}
func verifyPeer(c tls.ConnectionState, uri string) error {
	if len(c.VerifiedChains) == 0 || len(c.PeerCertificates) == 0 {
		return errors.New("unverified peer")
	}
	if len(c.PeerCertificates[0].URIs) == 1 && c.PeerCertificates[0].URIs[0].String() == uri {
		return nil
	}
	return errors.New("unexpected peer identity")
}
func Handler(target Target, probe Probe, timeout time.Duration, maxConcurrent int) (http.Handler, error) {
	if !validTarget(target) || probe == nil || timeout <= 0 || timeout > 30*time.Second || maxConcurrent < 1 || maxConcurrent > 16 {
		return nil, errors.New("invalid verifier configuration")
	}
	slots := make(chan struct{}, maxConcurrent)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "unavailable", http.StatusForbidden)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/internal/readiness" || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		var q request
		if r.ContentLength > 4096 || decode(r.Body, &q) != nil || !nonceValid(q.Nonce) {
			http.Error(w, "invalid request", 400)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "unavailable", 503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		verdict := "NOT_READY"
		if probe(ctx) == nil && ctx.Err() == nil {
			verdict = "READY"
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(response{verdict, q.Nonce, target})
	}), nil
}

type Client struct {
	http     *http.Client
	endpoint string
	target   Target
}

func NewClient(endpoint string, target Target, cert tls.Certificate, roots *x509.CertPool, serverName, serverURI string, timeout time.Duration) (*Client, error) {
	parsed, parseErr := url.Parse(endpoint)
	if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/internal/readiness" || !validTarget(target) || len(cert.Certificate) == 0 || roots == nil || serverName == "" || serverURI == "" || timeout <= 0 || timeout > 30*time.Second {
		return nil, errors.New("invalid readiness client")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: serverName, VerifyConnection: func(s tls.ConnectionState) error { return verifyPeer(s, serverURI) }}, MaxConnsPerHost: 2, DisableKeepAlives: true}
	return &Client{&http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}, endpoint, target}, nil
}
func (c *Client) Check(ctx context.Context) error {
	if c == nil {
		return errors.New("readiness unavailable")
	}
	var random [32]byte
	if _, e := rand.Read(random[:]); e != nil {
		return errors.New("readiness unavailable")
	}
	nonce := hex.EncodeToString(random[:])
	body := `{"nonce":"` + nonce + `"}`
	req, e := http.NewRequestWithContext(ctx, "POST", c.endpoint, strings.NewReader(body))
	if e != nil {
		return errors.New("readiness unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.http.Do(req)
	if e != nil {
		return errors.New("readiness unavailable")
	}
	defer resp.Body.Close()
	var result response
	if resp.StatusCode != 200 || decode(resp.Body, &result) != nil || result.Nonce != nonce || result.Target != c.target || result.Verdict != "READY" {
		return errors.New("readiness unavailable")
	}
	return nil
}
func (c *Client) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.Check(r.Context()) != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"code":"protected_not_ready","error":"Protected service unavailable"}`)
			return
		}
		// This fresh verdict authorizes only this operation, not subsequent pool
		// acquisitions on unrelated requests. Re-probing inside the handler can
		// fail after admission under verifier load and turn an unavailable gate
		// into a handler/database error. Identity checks still run on acquisition.
		a := &requestAdmission{client: c}
		a.active.Store(true)
		defer a.active.Store(false)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), admissionKey{}, a)))
	})
}

type admissionKey struct{}
type requestAdmission struct {
	client *Client
	active atomic.Bool
}

func checkRuntimeReadiness(ctx context.Context, c *Client) error {
	if a, ok := ctx.Value(admissionKey{}).(*requestAdmission); ok && a.client == c && a.active.Load() {
		return nil
	}
	return c.Check(ctx)
}
