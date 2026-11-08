package migrations

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
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
	after[2].Owner = "tremor_owner"
	after[2].ACL.Grants = []TransitionGrant{{"tremor_owner", "tremor_api", "SELECT", false}}
	after[3].Owner = "tremor_executor"
	changes := []TransitionDelta{}
	for i := range before {
		changes = append(changes, TransitionDelta{before[i].Key, transitionDigest(before[i]), transitionDigest(after[i]), i == 3})
	}
	payload := []byte("-- reviewed fixture payload; never executed\n")
	f := TransitionFrontier{h, h, h, []TransitionEntry{}}

	for _, role := range []string{"old_owner", "tremor_api", "tremor_deployer", "tremor_executor", "tremor_ledger_owner", "tremor_migrator", "tremor_owner"} {
		m := meta("role:"+role, "role")
		m.Owner = ""
		m.RoleFlags.Login = role == "old_owner" || role == "tremor_api" || role == "tremor_migrator" || role == "tremor_deployer"
		before = append(before, m)
		after = append(after, transitionClone(m))
	}
	for _, schema := range []string{"public", "tremor_commands", "tremor_deployment"} {
		m := meta("schema:"+schema, "schema")
		m.Protected = true
		before = append(before, m)
		after = append(after, transitionClone(m))
		for _, class := range []string{"functions", "sequences", "tables"} {
			m := meta("default_acl:"+schema+":tremor_deployer:"+class, "default_acl")
			m.Owner = "tremor_deployer"
			before = append(before, m)
			after = append(after, transitionClone(m))
		}
	}
	for _, class := range []string{"functions", "sequences", "tables"} {
		m := meta("default_acl::tremor_deployer:"+class, "default_acl")
		m.Owner = "tremor_deployer"
		before = append(before, m)
		after = append(after, transitionClone(m))
	}
	m := meta("public.stock_id_seq", "sequence")
	before = append(before, m)
	after = append(after, transitionClone(m))
	sort.Slice(before, func(i, j int) bool { return before[i].Key < before[j].Key })
	sort.Slice(after, func(i, j int) bool { return after[i].Key < after[j].Key })
	d := SecurityTransitionDescriptor{Protocol: SecurityTransitionProtocol, ID: "e1b-fixture", PayloadSHA: digest(payload), Target: target, Frontier: f, SourceReadinessSHA: h, FinalReadinessSHA: digest([]byte("final")), Before: before, After: after, Changes: changes, Receipt: TransitionReceiptLocation{"tremor_deployment", "security_transitions", 1}}
	d.SourceScope = transitionTestScope(before, target)
	d.FinalScope = transitionTestScope(after, target)

	s := TransitionSnapshot{Target: target, DatabaseOID: 42, Frontier: f, ReadinessSHA: h, Metadata: before, Ledgers: []TransitionLedgerSnapshot{{Key: "public.schema_migrations", OID: 100, Marker: "baseline marker", ConstraintsSHA: h, RowsSHA: h, Owner: "old_owner", ACL: TransitionACL{"explicit", []TransitionGrant{}}}}, BusinessDataSHA: h, IdentitySHA: h, BusinessReceiptsSHA: h, SequenceStateSHA: h}
	s.ScopeSHA = transitionDigest(d.SourceScope)
	s.ObjectIdentities = transitionTestIdentities(s.Metadata)
	s.Ledgers[0].OID = transitionTestIdentityOID(s.ObjectIdentities, "public.schema_migrations")
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

func transitionTestIndex(ms []TransitionMetadata, key string) int {
	for i, m := range ms {
		if m.Key == key {
			return i
		}
	}
	panic("missing test metadata: " + key)
}
func transitionTestDeltaIndex(ms []TransitionDelta, key string) int {
	for i, m := range ms {
		if m.Key == key {
			return i
		}
	}
	panic("missing test delta: " + key)
}
func transitionTestIdentityOID(ms []TransitionObjectIdentity, key string) uint32 {
	for _, m := range ms {
		if m.Key == key {
			return m.OID
		}
	}
	panic("missing identity: " + key)
}

// Test-only policy construction, never used by production admission. Fixed roles
// and schema/default coverage are still independently enforced by the validator.
func transitionTestScope(ms []TransitionMetadata, target TransitionTarget) TransitionScope {
	s := TransitionScope{Profile: "prepared-v1", Objects: []TransitionRequirement{}, Creators: []string{"tremor_deployer"}}
	ext := false
	for _, m := range ms {
		r := TransitionRequirement{Key: m.Key, Kind: m.Kind, Presence: "present"}
		switch m.Kind {
		case "schema":
			r.Schema = strings.TrimPrefix(m.Key, "schema:")
		case "role":
		case "default_acl":
			p := strings.Split(m.Key, ":")
			r.Schema = p[1]
			r.Parent = "role:" + p[2]
		default:
			r.Schema = strings.SplitN(m.Key, ".", 2)[0]
			if m.Kind == "column" {
				r.Parent = m.Key[:strings.LastIndex(m.Key, ".")]
			}
		}
		ext = ext || m.Key == target.Schema+".schema_extensions"
		s.Objects = append(s.Objects, r)
	}
	if !ext {
		s.Objects = append(s.Objects, TransitionRequirement{Key: target.Schema + ".schema_extensions", Kind: "table", Schema: target.Schema, Presence: "absent"})
	}
	sort.Slice(s.Objects, func(i, j int) bool { return s.Objects[i].Key < s.Objects[j].Key })
	return s
}

func transitionTestIdentities(ms []TransitionMetadata) []TransitionObjectIdentity {
	out := []TransitionObjectIdentity{}
	index := map[string]uint32{}
	for i, m := range ms {
		if m.Kind == "default_acl" || m.Kind == "column" {
			continue
		}
		oid := uint32(100 + i)
		index[m.Key] = oid
		out = append(out, TransitionObjectIdentity{Key: m.Key, OID: oid})
	}
	for _, m := range ms {
		if m.Kind == "column" {
			parent := m.Key[:strings.LastIndex(m.Key, ".")]
			out = append(out, TransitionObjectIdentity{Key: m.Key, OID: index[parent], SubID: 1})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func transitionTestFinalEvidence(s *TransitionSnapshot, d SecurityTransitionDescriptor) {
	s.ScopeSHA = transitionDigest(d.FinalScope)
	for i := range s.Ledgers {
		m := d.After[transitionTestIndex(d.After, s.Ledgers[i].Key)]
		s.Ledgers[i].Owner = m.Owner
		s.Ledgers[i].ACL = transitionClone(m.ACL)
	}
	// Retain existing physical identities; assign deterministic test OIDs to new
	// objects only, so fixture updates never hide a changed historical identity.
	old := map[string]TransitionObjectIdentity{}
	var maxOID uint32
	for _, o := range s.ObjectIdentities {
		old[o.Key] = o
		if o.OID > maxOID {
			maxOID = o.OID
		}
	}
	out := []TransitionObjectIdentity{}
	for _, m := range d.After {
		if m.Kind == "default_acl" {
			continue
		}
		if o, ok := old[m.Key]; ok {
			out = append(out, o)
		} else {
			maxOID++
			out = append(out, TransitionObjectIdentity{Key: m.Key, OID: maxOID})
		}
	}
	s.ObjectIdentities = out
}

func transitionTestRebuildPolicy(d *SecurityTransitionDescriptor, s *TransitionSnapshot) {
	before := map[string]TransitionMetadata{}
	after := map[string]TransitionMetadata{}
	keys := map[string]bool{}
	for _, m := range d.Before {
		before[m.Key] = m
		keys[m.Key] = true
	}
	for _, m := range d.After {
		after[m.Key] = m
		keys[m.Key] = true
	}
	d.Changes = []TransitionDelta{}
	for key := range keys {
		b, bok := before[key]
		a, aok := after[key]
		bh, ah := transitionObjectDigest(b, bok), transitionObjectDigest(a, aok)
		if bh == ah {
			continue
		}
		context := (bok && b.SecurityDefiner || aok && a.SecurityDefiner) && (!bok || !aok || b.Owner != a.Owner || b.SecurityDefiner != a.SecurityDefiner || !reflect.DeepEqual(b.SearchPath, a.SearchPath) || b.DefinitionSHA != a.DefinitionSHA)
		d.Changes = append(d.Changes, TransitionDelta{key, bh, ah, context})
	}
	sort.Slice(d.Changes, func(i, j int) bool { return d.Changes[i].Key < d.Changes[j].Key })
	sort.Slice(d.Before, func(i, j int) bool { return d.Before[i].Key < d.Before[j].Key })
	sort.Slice(d.After, func(i, j int) bool { return d.After[i].Key < d.After[j].Key })
	d.SourceScope = transitionTestScope(d.Before, d.Target)
	d.FinalScope = transitionTestScope(d.After, d.Target)
	s.Metadata = transitionClone(d.Before)
	s.ScopeSHA = transitionDigest(d.SourceScope)
	s.ObjectIdentities = transitionTestIdentities(d.Before)
	for i := range s.Ledgers {
		s.Ledgers[i].OID = transitionTestIdentityOID(s.ObjectIdentities, s.Ledgers[i].Key)
	}
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
		"readiness": func(x *SecurityTransitionDescriptor) { x.SourceReadinessSHA = "" },
		"unlisted owner": func(x *SecurityTransitionDescriptor) {
			x.After[transitionTestIndex(x.After, "public.stock")].Owner = "attacker"
		},
		"SD context": func(x *SecurityTransitionDescriptor) {
			x.Changes[transitionTestDeltaIndex(x.Changes, "tremor_commands.place(uuid)")].ExecutionContextChange = false
		},
		"extra delta": func(x *SecurityTransitionDescriptor) {
			x.Changes = append(x.Changes, TransitionDelta{"unknown", x.PayloadSHA, x.SourceReadinessSHA, false})
		},
		"receipt": func(x *SecurityTransitionDescriptor) { x.Receipt.Table = "other" },
		"default ACL conflation": func(x *SecurityTransitionDescriptor) {
			x.After[transitionTestIndex(x.After, "public.stock.quantity")].ACL.Mode = "default"
		},
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
	transitionTestFinalEvidence(&final, d)
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
		"owner": func(x *TransitionSnapshot) {
			x.Metadata[transitionTestIndex(x.Metadata, "public.stock")].Owner = "attacker"
		},
		"grant": func(x *TransitionSnapshot) {
			x.Metadata[transitionTestIndex(x.Metadata, "public.stock")].ACL.Grants = []TransitionGrant{{"tremor_owner", "tremor_api", "UPDATE", false}}
		},
		"column versus table": func(x *TransitionSnapshot) {
			x.Metadata[transitionTestIndex(x.Metadata, "public.stock")].ACL = x.Metadata[transitionTestIndex(x.Metadata, "public.stock.quantity")].ACL
			x.Metadata[transitionTestIndex(x.Metadata, "public.stock.quantity")].ACL = TransitionACL{"explicit", []TransitionGrant{}}
		},
		"membership": func(x *TransitionSnapshot) {
			x.Metadata = append(x.Metadata, TransitionMetadata{Key: "unexpected", Kind: "role"})
		},
		"SD flag": func(x *TransitionSnapshot) {
			x.Metadata[transitionTestIndex(x.Metadata, "tremor_commands.place(uuid)")].SecurityDefiner = false
		},
		"SD owner": func(x *TransitionSnapshot) {
			x.Metadata[transitionTestIndex(x.Metadata, "tremor_commands.place(uuid)")].Owner = "tremor_owner"
		},
		"SD path": func(x *TransitionSnapshot) {
			x.Metadata[transitionTestIndex(x.Metadata, "tremor_commands.place(uuid)")].SearchPath = []string{"public", "pg_temp"}
		},
		"SD body": func(x *TransitionSnapshot) {
			x.Metadata[transitionTestIndex(x.Metadata, "tremor_commands.place(uuid)")].DefinitionSHA = digest([]byte("other"))
		},
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
		"target":    func(x *TransitionSnapshot) { x.Target.Database = "other" },
		"frontier":  func(x *TransitionSnapshot) { x.Frontier.BaselineLineageSHA = digest([]byte("other")) },
		"readiness": func(x *TransitionSnapshot) { x.ReadinessSHA = digest([]byte("other")) },
		"source metadata": func(x *TransitionSnapshot) {
			x.Metadata[transitionTestIndex(x.Metadata, "public.schema_migrations")].Owner = "other"
		},
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
	transitionTestFinalEvidence(&final, d)
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
	for _, tc := range []struct{ kind, key, priv string }{
		{"schema", "schema:public", "USAGE"}, {"table", "public.stock", "SELECT"}, {"column", "public.stock.quantity", "SELECT"}, {"function", "tremor_commands.place(uuid)", "EXECUTE"}, {"sequence", "public.stock_id_seq", "USAGE"}, {"default_acl", "default_acl:public:tremor_deployer:functions", "EXECUTE"}, {"role", "role:old_owner", ""},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			d, payload, source := transitionFixture(t)
			i := transitionTestIndex(d.After, tc.key)
			if tc.kind == "role" {
				d.After[i].RoleFlags.Inherit = true
				d.After[i].Memberships = []TransitionMembership{{Role: "tremor_owner", Grantor: "tremor_deployer"}}
			} else {
				d.After[i].ACL.Grants = []TransitionGrant{{Grantor: "tremor_deployer", Grantee: "tremor_api", Privilege: tc.priv, GrantOption: true}}
			}
			transitionTestRebuildPolicy(&d, &source)
			a := transitionApproveFixture(t, d, payload)
			b, err := BindSecurityTransition(a, d.Target, source)
			if err != nil {
				t.Fatal(err)
			}
			final := transitionClone(source)
			final.Metadata = transitionClone(d.After)
			final.ReadinessSHA = d.FinalReadinessSHA
			transitionTestFinalEvidence(&final, d)
			if err = CompareSecurityTransition(b, final); err != nil {
				t.Fatal(err)
			}
			bad := transitionClone(final)
			if tc.kind == "role" {
				bad.Metadata[i].Memberships[0].Set = true
			} else {
				bad.Metadata[i].ACL.Grants[0].GrantOption = false
			}
			if CompareSecurityTransition(b, bad) == nil {
				t.Fatal("unlisted class-specific change accepted")
			}
			if tc.kind == "role" {
				bad = transitionClone(final)
				bad.Metadata[i].RoleFlags.Superuser = true
				if CompareSecurityTransition(b, bad) == nil {
					t.Fatal("role attribute change accepted")
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
	ext := transitionClone(d.Before[transitionTestIndex(d.Before, "public.schema_migrations")])
	ext.Key = "public.schema_extensions"
	d.Before = append(d.Before, ext)
	d.After = append(d.After, transitionClone(ext))
	m := TransitionMetadata{Key: "public.created", Kind: "table", Owner: "tremor_ledger_owner", ACL: TransitionACL{"explicit", []TransitionGrant{}}, DefinitionSHA: d.PayloadSHA, SearchPath: []string{}, Memberships: []TransitionMembership{}}
	d.After = append(d.After, m)
	d.Changes = append(d.Changes, TransitionDelta{m.Key, transitionObjectDigest(TransitionMetadata{}, false), transitionDigest(m), false})
	transitionTestRebuildPolicy(&d, &source)
	a := transitionApproveFixture(t, d, payload)
	b, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	final := transitionClone(source)
	final.Metadata = transitionClone(d.After)
	final.ReadinessSHA = d.FinalReadinessSHA
	transitionTestFinalEvidence(&final, d)
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
		"unsorted inventory": func(x *SecurityTransitionDescriptor) {
			x.Before[transitionTestIndex(x.Before, "public.schema_migrations")], x.Before[transitionTestIndex(x.Before, "public.stock")] = x.Before[transitionTestIndex(x.Before, "public.stock")], x.Before[transitionTestIndex(x.Before, "public.schema_migrations")]
		},
		"duplicate object": func(x *SecurityTransitionDescriptor) {
			x.Before = append(x.Before, x.Before[transitionTestIndex(x.Before, "public.schema_migrations")])
		},
		"unknown class": func(x *SecurityTransitionDescriptor) {
			x.Before[transitionTestIndex(x.Before, "public.schema_migrations")].Kind = "foreign_table"
		},
		"duplicate delta": func(x *SecurityTransitionDescriptor) {
			x.Changes = append(x.Changes, x.Changes[transitionTestDeltaIndex(x.Changes, "public.schema_migrations")])
		},
		"duplicate grant": func(x *SecurityTransitionDescriptor) {
			x.After[transitionTestIndex(x.After, "public.stock.quantity")].ACL.Grants = append(x.After[transitionTestIndex(x.After, "public.stock.quantity")].ACL.Grants, x.After[transitionTestIndex(x.After, "public.stock.quantity")].ACL.Grants[0])
		},
		"false SD context marker": func(x *SecurityTransitionDescriptor) {
			x.Changes[transitionTestDeltaIndex(x.Changes, "public.schema_migrations")].ExecutionContextChange = true
		},
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

// These are regression expectations, so FAIL reproduces the I1 defect.
// Synthetic approval comes from the existing I1 test helper, not production.
func TestAuditI1RejectUninventoriedLedger(t *testing.T) {
	d, payload, source := transitionFixture(t)
	a := transitionApproveFixture(t, d, payload)
	// Do not modify or reapprove the descriptor. It has no extension-ledger
	// metadata. Add a ledger observation with no corresponding owner/ACL record.
	phantom := source.Ledgers[0]
	phantom.Key = source.Target.Schema + ".schema_extensions"
	phantom.OID++
	source.Ledgers = append(source.Ledgers, phantom)
	bound, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		return
	}
	final := transitionClone(source)
	final.Metadata = transitionClone(d.After)
	final.ReadinessSHA = d.FinalReadinessSHA
	transitionTestFinalEvidence(&final, d)
	receipt := bound.ExpectedReceipt()
	state := ClassifySecurityTransition(bound, TransitionObservation{Snapshot: &final, Receipt: &receipt})
	if state == TransitionProtected {
		t.Fatalf("BLOCKER: PROTECTED with schema_extensions in Ledgers but absent from approved/observed Metadata; no owner/ACL evidence for that ledger")
	}
}

func TestAuditI1RejectMissingSecurityCoverage(t *testing.T) {
	d, payload, source := transitionFixture(t)
	// A minimally inventoried descriptor is structurally accepted: no roles,
	// schema ownership, memberships, default privileges or function inventory.
	d.Before = []TransitionMetadata{d.Before[transitionTestIndex(d.Before, "public.schema_migrations")]}
	d.After = []TransitionMetadata{d.After[transitionTestIndex(d.After, "public.schema_migrations")]}
	d.Changes = []TransitionDelta{d.Changes[transitionTestDeltaIndex(d.Changes, "public.schema_migrations")]}
	source.Metadata = transitionClone(d.Before)
	raw, _ := json.Marshal(d)
	_, sha, decodeErr := DecodeSecurityTransition(raw, payload)
	if decodeErr != nil {
		return
	}
	a, approvalErr := approveSecurityTransition(raw, payload, []string{sha})
	if approvalErr != nil {
		return
	}
	bound, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		return
	}
	final := transitionClone(source)
	final.Metadata = transitionClone(d.After)
	final.ReadinessSHA = d.FinalReadinessSHA
	transitionTestFinalEvidence(&final, d)
	receipt := bound.ExpectedReceipt()
	if got := ClassifySecurityTransition(bound, TransitionObservation{Snapshot: &final, Receipt: &receipt}); got == TransitionProtected {
		t.Fatalf("BLOCKER: PROTECTED with one table metadata object and no required security coverage")
	}
}

func TestSecurityTransitionScopeAndConsistency(t *testing.T) {
	d, payload, source := transitionFixture(t)
	a := transitionApproveFixture(t, d, payload)
	b, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	remove := func(s *TransitionSnapshot, key string) {
		i := transitionTestIndex(s.Metadata, key)
		s.Metadata = append(s.Metadata[:i], s.Metadata[i+1:]...)
	}
	cases := map[string]func(*TransitionSnapshot){
		"missing baseline ledger": func(s *TransitionSnapshot) { s.Ledgers = s.Ledgers[:0] },
		"ledger metadata missing": func(s *TransitionSnapshot) { remove(s, "public.schema_migrations") },
		"extension expected absent": func(s *TransitionSnapshot) {
			l := transitionClone(s.Ledgers[0])
			l.Key = "public.schema_extensions"
			l.OID++
			s.Ledgers = append(s.Ledgers, l)
		},
		"missing required role":   func(s *TransitionSnapshot) { remove(s, "role:tremor_api") },
		"missing required schema": func(s *TransitionSnapshot) { remove(s, "schema:public") },
		"unknown owner": func(s *TransitionSnapshot) {
			s.Metadata[transitionTestIndex(s.Metadata, "public.stock")].Owner = "missing_owner"
		},
		"unknown membership dependency": func(s *TransitionSnapshot) {
			s.Metadata[transitionTestIndex(s.Metadata, "role:old_owner")].Memberships = []TransitionMembership{{Role: "missing_role", Grantor: "tremor_deployer"}}
		},
		"unknown membership grantor": func(s *TransitionSnapshot) {
			s.Metadata[transitionTestIndex(s.Metadata, "role:old_owner")].Memberships = []TransitionMembership{{Role: "tremor_owner", Grantor: "missing_role"}}
		},
		"duplicate object":                  func(s *TransitionSnapshot) { s.Metadata = append(s.Metadata, s.Metadata[0]) },
		"ledger owner contradicts metadata": func(s *TransitionSnapshot) { s.Ledgers[0].Owner = "tremor_owner" },
		"ledger ACL contradicts metadata":   func(s *TransitionSnapshot) { s.Ledgers[0].ACL = TransitionACL{"default", []TransitionGrant{}} },
		"ledger OID contradicts identity":   func(s *TransitionSnapshot) { s.Ledgers[0].OID++ },
		"duplicate physical identity": func(s *TransitionSnapshot) {
			i := 0
			j := 0
			for n, o := range s.ObjectIdentities {
				if o.Key == "public.stock" {
					i = n
				}
				if o.Key == "public.schema_migrations" {
					j = n
				}
			}
			s.ObjectIdentities[i].OID = s.ObjectIdentities[j].OID
		},
		"missing physical identity":         func(s *TransitionSnapshot) { s.ObjectIdentities = s.ObjectIdentities[:len(s.ObjectIdentities)-1] },
		"missing function":                  func(s *TransitionSnapshot) { remove(s, "tremor_commands.place(uuid)") },
		"missing default privileges":        func(s *TransitionSnapshot) { remove(s, "default_acl:public:tremor_deployer:functions") },
		"missing global default privileges": func(s *TransitionSnapshot) { remove(s, "default_acl::tremor_deployer:functions") },
		"scope substituted":                 func(s *TransitionSnapshot) { s.ScopeSHA = digest([]byte("operator policy")) },
		"column parent OID": func(s *TransitionSnapshot) {
			for i, o := range s.ObjectIdentities {
				if o.Key == "public.stock.quantity" {
					s.ObjectIdentities[i].OID++
				}
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := transitionClone(source)
			mutate(&bad)
			if _, err := BindSecurityTransition(a, d.Target, bad); err == nil {
				t.Fatal("inconsistent source admitted")
			}
			if got := ClassifyTransitionRecovery(b, &bad, nil); got != TransitionEvidenceRejected {
				t.Fatalf("recovery admitted bad evidence: %s", got)
			}
			if got := ClassifySecurityTransition(b, TransitionObservation{Snapshot: &bad, CommitUncertain: true}); got != TransitionRejected {
				t.Fatal("contradictory evidence not rejected", got)
			}
			final := transitionClone(source)
			final.Metadata = transitionClone(d.After)
			final.ReadinessSHA = d.FinalReadinessSHA
			transitionTestFinalEvidence(&final, d)
			mutate(&final)
			if CompareSecurityTransition(b, final) == nil {
				t.Fatal("inconsistent final admitted")
			}
			r := b.ExpectedReceipt()
			if got := ClassifyTransitionRecovery(b, &final, &r); got != TransitionEvidenceRejected {
				t.Fatal("final evidence bypassed validation", got)
			}
		})
	}
	if got := ClassifyTransitionRecovery(b, nil, nil); got != TransitionOutcomeUnknown {
		t.Fatal("unreadable evidence is not unknown")
	}
}

func TestSecurityTransitionScopeCannotBeShrunkByApproval(t *testing.T) {
	d, payload, _ := transitionFixture(t)
	t.Run("equally incomplete descriptor and scope", func(t *testing.T) {
		x := transitionClone(d)
		x.Before = []TransitionMetadata{x.Before[transitionTestIndex(x.Before, "public.schema_migrations")]}
		x.After = []TransitionMetadata{x.After[transitionTestIndex(x.After, "public.schema_migrations")]}
		x.Changes = []TransitionDelta{x.Changes[transitionTestDeltaIndex(x.Changes, "public.schema_migrations")]}
		x.SourceScope = transitionTestScope(x.Before, x.Target)
		x.FinalScope = transitionTestScope(x.After, x.Target)
		raw, _ := json.Marshal(x)
		if _, _, err := DecodeSecurityTransition(raw, payload); err == nil {
			t.Fatal("self-consistent but incomplete release scope admitted")
		}
	})
	for name, mutate := range map[string]func(*SecurityTransitionDescriptor){
		"missing required scope role": func(x *SecurityTransitionDescriptor) {
			for i, r := range x.SourceScope.Objects {
				if r.Key == "role:tremor_api" {
					x.SourceScope.Objects = append(x.SourceScope.Objects[:i], x.SourceScope.Objects[i+1:]...)
					break
				}
			}
		},
		"extension absence not declared": func(x *SecurityTransitionDescriptor) {
			for i, r := range x.SourceScope.Objects {
				if r.Key == "public.schema_extensions" {
					x.SourceScope.Objects = append(x.SourceScope.Objects[:i], x.SourceScope.Objects[i+1:]...)
					break
				}
			}
		},
		"unknown scope profile": func(x *SecurityTransitionDescriptor) { x.SourceScope.Profile = "trust-observation" },
		"unsafe final role": func(x *SecurityTransitionDescriptor) {
			x.After[transitionTestIndex(x.After, "role:tremor_api")].RoleFlags.Superuser = true
		},
		"duplicate logical scope": func(x *SecurityTransitionDescriptor) {
			x.SourceScope.Objects = append(x.SourceScope.Objects, x.SourceScope.Objects[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := transitionClone(d)
			mutate(&x)
			raw, _ := json.Marshal(x)
			if _, _, err := DecodeSecurityTransition(raw, payload); err == nil {
				t.Fatal("invalid scope structurally approved")
			}
		})
	}
	// Even a self-consistent rehashed private value cannot skip admission in source
	// recovery, receipt generation or final comparison. Public callers cannot build
	// this value at all; exercising it guards future internal wiring mistakes.
	a := transitionApproveFixture(t, d, payload)
	_, _, source := transitionFixture(t)
	b, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	i := transitionTestIndex(b.source.Metadata, "role:tremor_api")
	b.source.Metadata = append(b.source.Metadata[:i], b.source.Metadata[i+1:]...)
	b.boundSHA = transitionDigest(b.source)
	if got := ClassifyTransitionRecovery(b, &b.source, nil); got != TransitionEvidenceRejected {
		t.Fatal("source equality bypass", got)
	}
	if b.ExpectedReceipt() != (TransitionReceipt{}) {
		t.Fatal("invalid bound evidence produced receipt")
	}
	if CompareSecurityTransition(b, source) == nil {
		t.Fatal("invalid bound evidence compared")
	}
}

func TestSecurityTransitionExplicitLegacyScope(t *testing.T) {
	d, payload, source := transitionFixture(t)
	absent := map[string]bool{"role:tremor_api": true, "role:tremor_executor": true, "role:tremor_ledger_owner": true, "role:tremor_migrator": true, "role:tremor_owner": true, "schema:tremor_commands": true, "schema:tremor_deployment": true, "tremor_commands.place(uuid)": true}
	before := []TransitionMetadata{}
	for _, m := range d.Before {
		if absent[m.Key] || strings.HasPrefix(m.Key, "default_acl:tremor_commands:") || strings.HasPrefix(m.Key, "default_acl:tremor_deployment:") {
			continue
		}
		before = append(before, m)
	}
	d.Before = before
	transitionTestRebuildPolicy(&d, &source)
	d.SourceScope.Profile = "legacy-v1"
	for _, r := range d.FinalScope.Objects {
		if absent[r.Key] {
			r.Presence = "absent"
			d.SourceScope.Objects = append(d.SourceScope.Objects, r)
		}
	}
	sort.Slice(d.SourceScope.Objects, func(i, j int) bool { return d.SourceScope.Objects[i].Key < d.SourceScope.Objects[j].Key })
	source.ScopeSHA = transitionDigest(d.SourceScope)
	a := transitionApproveFixture(t, d, payload)
	b, err := BindSecurityTransition(a, d.Target, source)
	if err != nil {
		t.Fatal(err)
	}
	final := transitionClone(source)
	final.Metadata = transitionClone(d.After)
	final.ReadinessSHA = d.FinalReadinessSHA
	transitionTestFinalEvidence(&final, d)
	if err = CompareSecurityTransition(b, final); err != nil {
		t.Fatal(err)
	}
	r := b.ExpectedReceipt()
	if got := ClassifyTransitionRecovery(b, &final, &r); got != TransitionFinalConfirmed {
		t.Fatal(got)
	}
}
