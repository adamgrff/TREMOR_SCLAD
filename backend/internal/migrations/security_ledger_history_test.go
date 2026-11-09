package migrations

import (
	"encoding/json"
	"strings"
	"testing"
)

func historyTestFixture() *LedgerHistoryEvidence {
	hash := func(v string) string { return strings.Repeat(v, 64) }
	t := TransitionTarget{"12345", "test_db", "public", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	row := LedgerHistoryRow{1, "001_init.sql", hash("a"), IdentityProfile, 1, -946684799999999}
	root := LedgerHistoryState{Format: LedgerHistoryFormat, Target: t, DatabaseOID: 101, TransitionID: "test", TransitionSHA: hash("c"), BoundSHA: hash("d"), OperationSHA: hash("c"), Frontier: TransitionFrontier{hash("e"), hash("f"), hash("a"), []TransitionEntry{}}, Baseline: HistoryLedger{true, 202, hash("b"), []LedgerHistoryRow{row}}, Extension: HistoryLedger{Rows: []LedgerHistoryRow{}}}
	first := LedgerHistoryRecord{transitionDigest(root), root}
	next := transitionClone(root)
	next.Step = 1
	next.PreviousSHA = first.SHA
	next.OperationSHA = hash("e")
	next.Frontier.ExtensionManifestSHA = hash("b")
	r := LedgerHistoryRow{25, "025_test.sql", hash("a"), hash("b"), 1, 987654321012345}
	next.Frontier.Entries = []TransitionEntry{{25, r.Name, r.Checksum, r.Profile, 1}}
	next.Extension = HistoryLedger{true, 303, hash("c"), []LedgerHistoryRow{r}}
	return &LedgerHistoryEvidence{LedgerHistoryFormat, []LedgerHistoryRecord{first, {transitionDigest(next), next}}}
}
func TestLedgerHistoryCanonicalContract(t *testing.T) {
	h := historyTestFixture()
	if e := validateHistoryChain(h); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(h)
	if e != nil {
		t.Fatal(e)
	}
	var round LedgerHistoryEvidence
	if strictJSON(raw, &round) != nil || transitionDigest(round) != transitionDigest(h) {
		t.Fatal("canonical roundtrip")
	}
	before := transitionDigest(h.Records[1].State)
	h.Records[1].State.Extension.Rows[0].AppliedAtPGMicros++
	if transitionDigest(h.Records[1].State) == before || validateHistoryChain(h) == nil {
		t.Fatal("microsecond mutation hidden")
	}
	for name, mutate := range map[string]func(*LedgerHistoryEvidence){
		"protocol":                 func(h *LedgerHistoryEvidence) { h.Format = "new" },
		"missing-root":             func(h *LedgerHistoryEvidence) { h.Records = h.Records[1:] },
		"missing-history":          func(h *LedgerHistoryEvidence) { h.Records = nil },
		"duplicate-step":           func(h *LedgerHistoryEvidence) { h.Records[1].State.Step = 0 },
		"previous-sha":             func(h *LedgerHistoryEvidence) { h.Records[1].State.PreviousSHA = strings.Repeat("0", 64) },
		"incomplete-rows":          func(h *LedgerHistoryEvidence) { h.Records[1].State.Extension.Rows = nil },
		"foreign-target":           func(h *LedgerHistoryEvidence) { h.Records[1].State.Target.Database = "other" },
		"baseline-oid":             func(h *LedgerHistoryEvidence) { h.Records[1].State.Baseline.OID++ },
		"old-timestamp-reanchored": func(h *LedgerHistoryEvidence) { h.Records[1].State.Baseline.Rows[0].AppliedAtPGMicros++ },
		"prefix": func(h *LedgerHistoryEvidence) {
			h.Records[1].State.Frontier.Entries[0].Checksum = strings.Repeat("f", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := historyTestFixture()
			mutate(h)
			for i := range h.Records {
				h.Records[i].SHA = transitionDigest(h.Records[i].State)
			}
			if validateHistoryChain(h) == nil {
				t.Fatal("invalid history accepted")
			}
		})
	}
}
