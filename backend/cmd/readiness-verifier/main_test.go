package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifierFailsClosedConfiguration(t *testing.T) {
	for _, body := range []string{`{}`, `{"listen":"0.0.0.0:8443"}`, `{"unknown":"value"}`, `{} {}`} {
		p := filepath.Join(t.TempDir(), "config.json")
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		if serve(p) == nil {
			t.Fatal("invalid private configuration accepted")
		}
	}
	if serve(filepath.Join(t.TempDir(), "missing")) == nil {
		t.Fatal("missing configuration accepted")
	}
}
