package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func routeExample(pattern string) (string, string) {
	parts := strings.SplitN(pattern, " ", 2)
	path := parts[1]
	for strings.Contains(path, "{") {
		a := strings.Index(path, "{")
		b := strings.Index(path[a:], "}") + a
		path = path[:a] + "1" + path[b+1:]
	}
	return parts[0], path
}
func TestProtectedAllRegisteredRoutesFailClosed(t *testing.T) {
	mux := apiRoutes(nil, "protected", nil).(*modeMux)
	if len(mux.patterns) != 51 {
		t.Fatalf("registered route inventory changed: %d", len(mux.patterns))
	}
	for _, pattern := range mux.patterns {
		t.Run(pattern, func(t *testing.T) {
			method, path := routeExample(pattern)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
			want := 501
			if protectedReads[pattern] {
				want = 503
			}
			if pattern == "GET /health" {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("%s status=%d want=%d", pattern, w.Code, want)
			}
			if want == 501 && !strings.Contains(w.Body.String(), "protected_operation_unsupported") {
				t.Fatal("missing stable code")
			}
		})
	}
	// HEAD inherits GET policy; canonical path redirection cannot reach a writer.
	for _, path := range []string{"/api/assembly/orders", "/api/assembly-preview/draft"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("HEAD", path, nil))
		if w.Code != 501 {
			t.Fatal("HEAD bypass")
		}
	}
	m := &modeMux{ServeMux: http.NewServeMux(), mode: "protected"}
	called := false
	m.HandleFunc("GET /future", func(http.ResponseWriter, *http.Request) { called = true })
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/future", nil))
	if called || w.Code != 501 {
		t.Fatal("unknown future read admitted")
	}
}
func TestExplicitLegacyAndInvalidMode(t *testing.T) {
	for _, mode := range []string{"", "legacy", "production"} {
		if p, _, e := openRuntime(context.Background(), mode); e == nil || p != nil {
			t.Fatal("implicit mode admitted")
		}
	}
	m := &modeMux{ServeMux: http.NewServeMux(), mode: "legacy-development"}
	called := false
	m.HandleFunc("POST /legacy", func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("POST", "/legacy", nil))
	if !called || w.Code != 204 {
		t.Fatal("explicit legacy behavior changed")
	}
	_, e := runtimeClient(runtimeConfig{})
	if e == nil {
		t.Fatal("unapproved runtime accepted")
	}
}
func TestProtectedUserErrorsRemainDistinct(t *testing.T) {
	w := httptest.NewRecorder()
	protectedError(w, 501, "protected_operation_unsupported", "Unavailable")
	var got map[string]string
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got["code"] != "protected_operation_unsupported" {
		t.Fatal("contract")
	}
}
