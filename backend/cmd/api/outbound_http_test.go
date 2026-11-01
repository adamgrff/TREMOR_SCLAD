package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsolatedOutboundAndCORS(t *testing.T) {
	if response, err := (disabledOutboundHTTP{}).RoundTrip(httptest.NewRequest("GET", "https://example.invalid", nil)); err == nil || response != nil {
		t.Fatal("outbound request accepted")
	}
	t.Setenv("FRONTEND_ORIGIN", "http://127.0.0.1:5174")
	for _, origin := range []string{"http://127.0.0.1:5174", "http://127.0.0.1:5173", "http://localhost:5173"} {
		request := httptest.NewRequest("OPTIONS", "/api/racks", nil)
		request.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		corsMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("preflight reached handler") })).ServeHTTP(w, request)
		if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatal("CORS failed")
		}
	}
}
