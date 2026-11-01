package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestWarehouseCLIRequiresNameAndExplicitSafeTarget(t *testing.T) {
	for _, args := range [][]string{nil, {"bootstrap"}, {"validate"}, {"bootstrap", "--warehouse-name", "Synthetic TEST warehouse", "--database", "TrEmOr_SkLaD"}, {"bootstrap", "--password", "do-not-echo-secret"}, {"validate", "--warehouse-name", "Synthetic TEST warehouse"}} {
		var out, errout bytes.Buffer
		if run(context.Background(), args, &out, &errout) == 0 {
			t.Fatal("unsafe invocation accepted")
		}
		if strings.Contains(errout.String(), "do-not-echo-secret") {
			t.Fatal("secret leaked")
		}
	}
}
