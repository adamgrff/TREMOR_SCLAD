package migrations

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func transitionFixture(t *testing.T) (SecurityTransitionDescriptor, []byte, TransitionSnapshot) {
	t.Helper()
	h := digest([]byte("fixture"))
	target := TransitionTarget{"7705152489884045356", "e1b_disposable", "public", "12345678-1234-1234-1234-123456789abc"}
	meta := func(key, kind string) TransitionMetadata {
		return TransitionMetadata{Key: key, Kind: kind, Owner: "old_owner", ACL: TransitionACL{"explicit", []TransitionGrant{}}, DefinitionSHA: h, SearchPath: []string{}, Memberships: []TransitionMembership{}}
	}
	before := []TransitionMetadata{meta("public.schema_migrations", "table"), meta("public.stock", "table"), meta("public.stock.quantity", "column"), meta("tremor_commands.place(uuid)", "function")}
	before[3].SecurityDefiner = true
	before[3].SearchPath = []string{"pg_catalog", "pg_temp"}
	var after []TransitionMetadata
	raw, _ := json.Marshal(before)
	_ = json.Unmarshal(raw, &after)
	after[0].Owner = "tremor_ledger_owner"
	after[1].Owner = "tremor_owner"
	after[2].ACL.Grants = []TransitionGrant{{"tremor_owner", "tremor_api", "SELECT", false}}
	after[3].Owner = "tremor_executor"
	changes := []TransitionDelta{}
	for i := range before {
		changes = append(changes, TransitionDelta{before[i].Key, transitionDigest(before[i]), transitionDigest(after[i]), i == 3})
	}
	payload := []byte("-- reviewed fixture payload; never executed\n")
	f := TransitionFrontier{h, h, h, []TransitionEntry{}}
	d := SecurityTransitionDescriptor{SecurityTransitionProtocol, "e1b-fixture", digest(payload), target, f, h, digest([]byte("final")), before, after, changes, TransitionReceiptLocation{"tremor_deployment", "security_transitions", 1}}
	s := TransitionSnapshot{Target: target, DatabaseOID: 42, Frontier: f, ReadinessSHA: h, Metadata: before, Ledgers: []TransitionLedgerSnapshot{{"public.schema_migrations", 100, "baseline marker", h, h}}, BusinessDataSHA: h, IdentitySHA: h, BusinessReceiptsSHA: h, SequenceStateSHA: h}
	return d, payload, s
}

func transitionApproveFixture(t *testing.T, d SecurityTransitionDescriptor, payload []byte) ApprovedSecurityTransition {
	t.Helper()
	raw, _ := json.Marshal(d)
	_, sha, err := DecodeSecurityTransition(raw, payload)
	if err != nil {
		t.Fatal(err)
	}
	a, err := approveSecurityTransition(raw, payload, []string{sha})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func transitionClone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestSecurityTransitionDescriptor(t *testing.T) {
	d, payload, _ := transitionFixture(t)
	raw, _ := json.Marshal(d)
	got, sha, err := DecodeSecurityTransition(raw, payload)
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("decode: %v", err)
	}
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, raw, "", "  ")
	_, other, err := DecodeSecurityTransition(pretty.Bytes(), payload)
	if err != nil || sha != other {
		t.Fatal("layout changed semantic digest", err)
	}
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	reordered, _ := json.Marshal(fields)
	_, other, err = DecodeSecurityTransition(reordered, payload)
	if err != nil || sha != other {
		t.Fatal("key order changed digest", err)
	}
	if _, err = LoadApprovedSecurityTransition(raw, payload); err == nil {
		t.Fatal("operator hash granted production approval")
	}
	if _, err = approveSecurityTransition(raw, payload, []string{digest([]byte("unrelated"))}); err == nil {
		t.Fatal("wrong release approved")
	}
	if _, err = approveSecurityTransition(raw, payload, []string{sha, sha}); err == nil {
		t.Fatal("duplicate approvals accepted")
	}
	for name, mutate := range map[string]func(*SecurityTransitionDescriptor){
		"version":            func(x *SecurityTransitionDescriptor) { x.Protocol = "v2" },
		"checksum":           func(x *SecurityTransitionDescriptor) { x.PayloadSHA = "bad" },
		"payload mismatch":   func(x *SecurityTransitionDescriptor) { x.PayloadSHA = digest([]byte("other")) },
		"empty id":           func(x *SecurityTransitionDescriptor) { x.ID = "" },
		"protected database": func(x *SecurityTransitionDescriptor) { x.Target.Database = "tremor_sklad" },
		"frontier": func(x *SecurityTransitionDescriptor) {
			x.Frontier.Entries = []TransitionEntry{{25, "025_fixture.sql", d.PayloadSHA, d.PayloadSHA, 2}}
		},
		"readiness":      func(x *SecurityTransitionDescriptor) { x.SourceReadinessSHA = "" },
		"unlisted owner": func(x *SecurityTransitionDescriptor) { x.After[1].Owner = "attacker" },
		"SD context":     func(x *SecurityTransitionDescriptor) { x.Changes[3].ExecutionContextChange = false },
		"extra delta": func(x *SecurityTransitionDescriptor) {
			x.Changes = append(x.Changes, TransitionDelta{"unknown", x.PayloadSHA, x.SourceReadinessSHA, false})
		},
		"receipt":                func(x *SecurityTransitionDescriptor) { x.Receipt.Table = "other" },
		"default ACL conflation": func(x *SecurityTransitionDescriptor) { x.After[2].ACL.Mode = "default" },
	} {
		t.Run(name, func(t *testing.T) {
			x := transitionClone(d)
			mutate(&x)
			b, _ := json.Marshal(x)
			if _, _, err := DecodeSecurityTransition(b, payload); err == nil {
				t.Fatal("accepted invalid descriptor")
			}
		})
	}
}

func TestSecurityTransitionStrictJSON(t *testing.T) {
	d, payload, _ := transitionFixture(t)
	raw, _ := json.Marshal(d)
	s := string(raw)
	cases := map[string]string{
		"unknown":           strings.Replace(s, `"protocol":`, `"unknown":1,"protocol":`, 1),
		"duplicate":         strings.Replace(s, `"id":`, `"id":"duplicate","id":`, 1),
		"nested duplicate":  strings.Replace(s, `"cluster_id":`, `"cluster_id":"1","cluster_id":`, 1),
		"escaped duplicate": strings.Replace(s, `"id":`, `"\u0069d":"duplicate","id":`, 1),
		"case alias":        strings.Replace(s, `"protocol":`, `"Protocol":`, 1),
		"trailing":          s + ` {}`,
		"null scalar":       strings.Replace(s, `"format":1`, `"format":null`, 1),
		"null array":        strings.Replace(s, `"entries":[]`, `"entries":null`, 1),
		"wrong type":        strings.Replace(s, `"format":1`, `"format":"1"`, 1),
		"missing false":     strings.Replace(s, `"security_definer":false,`, "", 1),
		"missing array":     strings.Replace(s, `,"entries":[]`, "", 1),
		"invalid utf8":      s + string([]byte{255}),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DecodeSecurityTransition([]byte(input), payload); err == nil {
				t.Fatal("accepted ambiguous JSON")
			}
		})
	}
	for _, p := range [][]byte{append([]byte{0xef, 0xbb, 0xbf}, payload...), []byte("a\r\n"), []byte("a\x00"), nil} {
		x := d
		x.PayloadSHA = digest(p)
		b, _ := json.Marshal(x)
		if _, _, err := DecodeSecurityTransition(b, p); err == nil {
			t.Fatal("accepted ambiguous payload bytes")
		}
	}
}

func TestSecurityTransitionBindingAndMetadata(t *testing.T) {
	d, payload, source := transitionFixture(t)
	a := transitionApproveFixture(t, d, payload)
	b, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	final := transitionClone(source)
	final.Metadata = transitionClone(d.After)
	final.ReadinessSHA = d.FinalReadinessSHA
	if err = CompareSecurityTransition(b, final); err != nil {
		t.Fatal(err)
	}
	// Caller mutation cannot alter the bound baseline.
	source.Metadata[0].Owner = "mutated"
	if err = CompareSecurityTransition(b, final); err != nil {
		t.Fatal("bound source aliased caller", err)
	}
	for name, mutate := range map[string]func(*TransitionSnapshot){
		"foreign deployment":  func(x *TransitionSnapshot) { x.Target.DeploymentID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" },
		"database oid":        func(x *TransitionSnapshot) { x.DatabaseOID++ },
		"ledger oid":          func(x *TransitionSnapshot) { x.Ledgers[0].OID++ },
		"ledger marker":       func(x *TransitionSnapshot) { x.Ledgers[0].Marker = "other" },
		"constraints":         func(x *TransitionSnapshot) { x.Ledgers[0].ConstraintsSHA = digest([]byte("other")) },
		"historical receipts": func(x *TransitionSnapshot) { x.Ledgers[0].RowsSHA = digest([]byte("other")) },
		"business receipts":   func(x *TransitionSnapshot) { x.BusinessReceiptsSHA = digest([]byte("other")) },
		"business rows":       func(x *TransitionSnapshot) { x.BusinessDataSHA = digest([]byte("other")) },
		"identity":            func(x *TransitionSnapshot) { x.IdentitySHA = digest([]byte("other")) },
		"sequence state":      func(x *TransitionSnapshot) { x.SequenceStateSHA = digest([]byte("other")) },
		"owner":               func(x *TransitionSnapshot) { x.Metadata[1].Owner = "attacker" },
		"grant": func(x *TransitionSnapshot) {
			x.Metadata[1].ACL.Grants = []TransitionGrant{{"tremor_owner", "tremor_api", "UPDATE", false}}
		},
		"column versus table": func(x *TransitionSnapshot) {
			x.Metadata[1].ACL = x.Metadata[2].ACL
			x.Metadata[2].ACL = TransitionACL{"explicit", []TransitionGrant{}}
		},
		"membership": func(x *TransitionSnapshot) {
			x.Metadata = append(x.Metadata, TransitionMetadata{Key: "unexpected", Kind: "role"})
		},
		"SD flag":   func(x *TransitionSnapshot) { x.Metadata[3].SecurityDefiner = false },
		"SD owner":  func(x *TransitionSnapshot) { x.Metadata[3].Owner = "tremor_owner" },
		"SD path":   func(x *TransitionSnapshot) { x.Metadata[3].SearchPath = []string{"public", "pg_temp"} },
		"SD body":   func(x *TransitionSnapshot) { x.Metadata[3].DefinitionSHA = digest([]byte("other")) },
		"readiness": func(x *TransitionSnapshot) { x.ReadinessSHA = digest([]byte("other")) },
		"anchor":    func(x *TransitionSnapshot) { x.Frontier.ExtensionManifestSHA = digest([]byte("other")) },
		"prefix": func(x *TransitionSnapshot) {
			x.Frontier.Entries = []TransitionEntry{{25, "025_extra.sql", d.PayloadSHA, d.PayloadSHA, 1}}
		},
		"missing object": func(x *TransitionSnapshot) { x.Metadata = x.Metadata[:3] },
	} {
		t.Run(name, func(t *testing.T) {
			x := transitionClone(final)
			mutate(&x)
			if CompareSecurityTransition(b, x) == nil {
				t.Fatal("accepted unexpected transition")
			}
		})
	}
	_, _, original := transitionFixture(t)
	for name, mutate := range map[string]func(*TransitionSnapshot){
		"target":           func(x *TransitionSnapshot) { x.Target.Database = "other" },
		"frontier":         func(x *TransitionSnapshot) { x.Frontier.BaselineLineageSHA = digest([]byte("other")) },
		"readiness":        func(x *TransitionSnapshot) { x.ReadinessSHA = digest([]byte("other")) },
		"source metadata":  func(x *TransitionSnapshot) { x.Metadata[0].Owner = "other" },
		"missing evidence": func(x *TransitionSnapshot) { x.BusinessDataSHA = "" },
	} {
		t.Run("bind/"+name, func(t *testing.T) {
			x := transitionClone(original)
			mutate(&x)
			if _, err := BindSecurityTransition(a, d.Target, x); err == nil {
				t.Fatal("bound unknown source")
			}
		})
	}
	if _, err = BindSecurityTransition(ApprovedSecurityTransition{}, d.Target, original); err == nil {
		t.Fatal("zero approval bound")
	}
}

func TestSecurityTransitionRecovery(t *testing.T) {
	d, payload, source := transitionFixture(t)
	a := transitionApproveFixture(t, d, payload)
	b, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	final := transitionClone(source)
	final.Metadata = d.After
	final.ReadinessSHA = d.FinalReadinessSHA
	r := b.ExpectedReceipt()
	bad := r
	bad.BoundSnapshotSHA = digest([]byte("other target snapshot"))
	for _, tc := range []struct {
		name string
		s    *TransitionSnapshot
		r    *TransitionReceipt
		want TransitionRecoveryResult
	}{
		{"source", &source, nil, TransitionSourceConfirmed}, {"final", &final, &r, TransitionFinalConfirmed}, {"unavailable", nil, nil, TransitionOutcomeUnknown},
		{"missing receipt", &final, nil, TransitionEvidenceRejected}, {"wrong receipt", &final, &bad, TransitionEvidenceRejected}, {"source with receipt", &source, &r, TransitionEvidenceRejected}, {"receipt without snapshot", nil, &r, TransitionEvidenceRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTransitionRecovery(b, tc.s, tc.r); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name string
		o    TransitionObservation
		want SecurityTransitionState
	}{
		{"legacy", TransitionObservation{Snapshot: &source}, TransitionLegacyVerified},
		{"prepared", TransitionObservation{Snapshot: &source, Prepared: true}, TransitionPrepared},
		{"transaction", TransitionObservation{Snapshot: &source, Prepared: true, InTransaction: true}, TransitionInProgress},
		{"postflight not committed", TransitionObservation{Snapshot: &final, Receipt: &r, Prepared: true, InTransaction: true}, TransitionInProgress},
		{"protected", TransitionObservation{Snapshot: &final, Receipt: &r}, TransitionProtected},
		{"lost reply confirmed", TransitionObservation{Snapshot: &final, Receipt: &r, CommitUncertain: true}, TransitionProtected},
		{"unknown", TransitionObservation{CommitUncertain: true}, TransitionUnknownCommit},
		{"contradiction", TransitionObservation{Snapshot: &source, Receipt: &r}, TransitionRejected},
		{"impossible local phase", TransitionObservation{Snapshot: &source, Prepared: true, InTransaction: true, CommitUncertain: true}, TransitionRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifySecurityTransition(b, tc.o); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestSecurityTransitionMetadataClasses(t *testing.T) {
	for _, kind := range []string{"schema", "table", "column", "function", "sequence", "default_acl", "role"} {
		t.Run(kind, func(t *testing.T) {
			d, payload, source := transitionFixture(t)
			m := TransitionMetadata{Key: "zz." + kind, Kind: kind, Owner: "tremor_owner", ACL: TransitionACL{"explicit", []TransitionGrant{}}, DefinitionSHA: d.PayloadSHA, SearchPath: []string{}, Memberships: []TransitionMembership{}}
			if kind == "role" {
				m.Owner = ""
			}
			n := transitionClone(m)
			switch kind {
			case "schema":
				n.Protected = true
			case "role":
				n.RoleFlags.Login = true
				n.Memberships = []TransitionMembership{{"read_group", "tremor_deployer", false, false, false}}
			case "function":
				n.SecurityDefiner = true
				n.SearchPath = []string{"pg_catalog", "pg_temp"}
			default:
				n.ACL.Grants = []TransitionGrant{{"tremor_owner", "tremor_api", "SELECT", false}}
			}
			d.Before = append(d.Before, m)
			d.After = append(d.After, n)
			d.Changes = append(d.Changes, TransitionDelta{m.Key, transitionDigest(m), transitionDigest(n), kind == "function"})
			source.Metadata = transitionClone(d.Before)
			a := transitionApproveFixture(t, d, payload)
			b, err := BindSecurityTransition(a, d.Target, source)
			if err != nil {
				t.Fatal(err)
			}
			final := transitionClone(source)
			final.Metadata = transitionClone(d.After)
			final.ReadinessSHA = d.FinalReadinessSHA
			if err = CompareSecurityTransition(b, final); err != nil {
				t.Fatal(err)
			}
			last := len(final.Metadata) - 1
			bad := transitionClone(final)
			switch kind {
			case "schema":
				bad.Metadata[last].Protected = false
			case "role":
				bad.Metadata[last].Memberships[0].Set = true
			case "function":
				bad.Metadata[last].SecurityDefiner = false
			default:
				bad.Metadata[last].ACL.Grants[0].GrantOption = true
			}
			if CompareSecurityTransition(b, bad) == nil {
				t.Fatal("unlisted class-specific change accepted")
			}
			if kind == "role" {
				bad = transitionClone(final)
				bad.Metadata[last].RoleFlags.Superuser = true
				if CompareSecurityTransition(b, bad) == nil {
					t.Fatal("role attribute escalation accepted")
				}
			}
		})
	}
}

func TestSecurityTransitionCreatedObjectsAndExactFrontier(t *testing.T) {
	d, payload, source := transitionFixture(t)
	entry := TransitionEntry{25, "025_prior.sql", d.PayloadSHA, d.SourceReadinessSHA, 1}
	d.Frontier.Entries = []TransitionEntry{entry}
	source.Frontier = transitionClone(d.Frontier)
	ledger := source.Ledgers[0]
	ledger.Key = "public.schema_extensions"
	ledger.OID++
	// Ledger order is baseline, extension (not lexical key order).
	source.Ledgers = append(source.Ledgers, ledger)
	m := TransitionMetadata{Key: "zz.created", Kind: "schema", Owner: "tremor_ledger_owner", ACL: TransitionACL{"explicit", []TransitionGrant{}}, DefinitionSHA: d.PayloadSHA, SearchPath: []string{}, Protected: true, Memberships: []TransitionMembership{}}
	d.After = append(d.After, m)
	d.Changes = append(d.Changes, TransitionDelta{m.Key, transitionObjectDigest(TransitionMetadata{}, false), transitionDigest(m), false})
	a := transitionApproveFixture(t, d, payload)
	b, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	final := transitionClone(source)
	final.Metadata = transitionClone(d.After)
	final.ReadinessSHA = d.FinalReadinessSHA
	if err = CompareSecurityTransition(b, final); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "name", "checksum", "chain", "ordinal", "manifest", "baseline"} {
		t.Run(field, func(t *testing.T) {
			bad := transitionClone(final)
			switch field {
			case "version":
				bad.Frontier.Entries[0].Version++
			case "name":
				bad.Frontier.Entries[0].Name = "025_other.sql"
			case "checksum":
				bad.Frontier.Entries[0].Checksum = d.FinalReadinessSHA
			case "chain":
				bad.Frontier.Entries[0].Chain = d.FinalReadinessSHA
			case "ordinal":
				bad.Frontier.Entries[0].Ordinal++
			case "manifest":
				bad.Frontier.ExtensionManifestSHA = d.FinalReadinessSHA
			case "baseline":
				bad.Frontier.BaselineManifestSHA = d.FinalReadinessSHA
			}
			if CompareSecurityTransition(b, bad) == nil {
				t.Fatal("anchor escaped exact frontier")
			}
		})
	}
	bad := transitionClone(final)
	bad.Ledgers = bad.Ledgers[:1]
	if CompareSecurityTransition(b, bad) == nil {
		t.Fatal("missing extension ledger accepted")
	}
}

func TestSecurityTransitionUnicodeAndOrdering(t *testing.T) {
	for input, want := range map[string]bool{`"\ud800"`: false, `"\udfff"`: false, `"\ud800\u0041"`: false, `"\ud83d\ude00"`: true, `"\\ud800"`: true, `"plain"`: true} {
		if got := transitionUnicodeEscapes([]byte(input)); got != want {
			t.Fatalf("unicode escape %q: %v", input, got)
		}
	}
	d, payload, _ := transitionFixture(t)
	for name, mutate := range map[string]func(*SecurityTransitionDescriptor){
		"unsorted inventory": func(x *SecurityTransitionDescriptor) { x.Before[0], x.Before[1] = x.Before[1], x.Before[0] },
		"duplicate object":   func(x *SecurityTransitionDescriptor) { x.Before = append(x.Before, x.Before[0]) },
		"unknown class":      func(x *SecurityTransitionDescriptor) { x.Before[0].Kind = "foreign_table" },
		"duplicate delta":    func(x *SecurityTransitionDescriptor) { x.Changes = append(x.Changes, x.Changes[0]) },
		"duplicate grant": func(x *SecurityTransitionDescriptor) {
			x.After[2].ACL.Grants = append(x.After[2].ACL.Grants, x.After[2].ACL.Grants[0])
		},
		"false SD context marker": func(x *SecurityTransitionDescriptor) { x.Changes[0].ExecutionContextChange = true },
	} {
		t.Run(name, func(t *testing.T) {
			x := transitionClone(d)
			mutate(&x)
			raw, _ := json.Marshal(x)
			if _, _, err := DecodeSecurityTransition(raw, payload); err == nil {
				t.Fatal("ambiguous inventory accepted")
			}
		})
	}
}
