package migrations

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
)

func i4Enabled() bool { return os.Getenv("E1B_I4_ACCEPTANCE") == "yes" }

// The host controller validates the project labels and system identifier before
// SIGKILL of the TEST postmaster. No Docker socket is mounted in this process.
func i4Crash(t *testing.T, phase, server string) {
	t.Helper()
	root := "/i3/control"
	if !i4Enabled() {
		t.Fatal("acceptance permission absent")
	}
	if e := os.WriteFile(filepath.Join(root, phase+".request"), []byte(server), 0600); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		raw, e := os.ReadFile(filepath.Join(root, phase+".ack"))
		if e == nil && string(raw) == server {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("host crash/recovery acknowledgement absent")
}

// Minimal TEST-only transport proxy, attached only to the authenticated commit
// connection. The TCP client never receives a successful PostgreSQL response.
// No production transport or target checks are changed.
func i4CommitFault(ctx context.Context, tx pgx.Tx, forward bool) (bool, error) {
	if e := tx.Conn().PgConn().SyncConn(ctx); e != nil {
		return false, e
	}
	h, e := tx.Conn().PgConn().Hijack()
	if e != nil {
		return false, e
	}
	defer h.Conn.Close()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return false, e
	}
	defer listener.Close()
	outcome := make(chan bool, 1)
	go func() {
		committed := false
		defer func() { outcome <- committed }()
		peer, e := listener.Accept()
		if e != nil {
			return
		}
		defer peer.Close()
		_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
		raw := make([]byte, 12) // Query message containing COMMIT\0.
		if _, e = io.ReadFull(peer, raw); e != nil || string(raw[5:]) != "COMMIT\x00" || !forward {
			return
		}
		if _, e = h.Conn.Write(raw); e != nil {
			return
		}
		_ = h.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		msg, e := h.Frontend.Receive()
		if ack, ok := msg.(*pgproto3.CommandComplete); e == nil && ok && string(ack.CommandTag) == "COMMIT" {
			committed = true
		}
		// Drop CommandComplete AND ReadyForQuery before the TCP client sees either.
	}()
	client, e := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if e != nil {
		return false, e
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(6 * time.Second))
	raw := append([]byte{'Q', 0, 0, 0, 0}, []byte("COMMIT\x00")...)
	binary.BigEndian.PutUint32(raw[1:5], uint32(len(raw)-1))
	if _, e = client.Write(raw); e != nil {
		return false, e
	}
	var b [1]byte
	n, readErr := client.Read(b[:])
	committed := <-outcome
	if n != 0 || readErr == nil {
		return committed, errors.New("TEST proxy delivered unexpected response")
	}
	return committed, fmt.Errorf("TEST commit response transport closed: %w", readErr)
}

func i4TransitionFaults(t *testing.T, ctx context.Context, admin *pgx.Conn, target Target, m *ExtensionManifest, a ApprovedSecurityTransition, sql string) {
	t.Helper()
	t.Run("server-SIGKILL-before-transition-COMMIT", func(t *testing.T) {
		r, e := NewSecurityTransition(target, m, a, []byte(sql))
		if e != nil {
			t.Fatal(e)
		}
		r.hooks.stage = func(stage string, tx pgx.Tx) error {
			if stage == "before_receipt" {
				i4Crash(t, "before-commit", target.ServerID)
				return errors.New("TEST postmaster killed before commit")
			}
			return nil
		}
		if state, e := r.Run(ctx); e == nil || state == TransitionProtected {
			t.Fatal("crashed transition accepted")
		}
		b, _ := r.Bound()
		state, recoveryErr := RecoverSecurityTransition(ctx, target, m, b)
		observed, e := CollectSecuritySnapshot(ctx, target, a, CollectTransitionSource, m)
		if e != nil {
			t.Fatal("post-crash source unavailable", e)
		}
		actual, _ := observed.Snapshot()
		want := transitionClone(b.source)
		different := []string{}
		av, wv := reflect.ValueOf(actual), reflect.ValueOf(want)
		for i := 0; i < av.NumField(); i++ {
			if !reflect.DeepEqual(av.Field(i).Interface(), wv.Field(i).Interface()) {
				different = append(different, av.Type().Field(i).Name)
			}
		}
		if state != TransitionLegacyVerified || !errors.Is(recoveryErr, ErrCommitUnknown) {
			// PostgreSQL pre-logs sequence values: a server crash can consume IDs
			// without changing committed rows. Do not weaken source binding or
			// silently approve the new sequence state for this availability case.
			if state != TransitionRejected || !reflect.DeepEqual(different, []string{"SequenceStateSHA"}) {
				t.Fatalf("crash rollback evidence state=%s differences=%v error=%v", state, different, recoveryErr)
			}
			t.Log("RECOVERY_CONSERVATIVE_REJECTION=PASS only sequence WAL state changed; original source binding not reapproved")
		}
		t.Log("REAL_SERVER_SIGKILL_UNCOMMITTED=PASS source owners/ACL/data/ledgers restored; no history receipt")
	})
	t.Run("COMMIT-request-dropped-before-server", func(t *testing.T) {
		r, e := NewSecurityTransition(target, m, a, []byte(sql))
		if e != nil {
			t.Fatal(e)
		}
		r.hooks.commit = func(ctx context.Context, tx pgx.Tx) error {
			sent, e := i4CommitFault(ctx, tx, false)
			if sent {
				return errors.New("unexpected COMMIT")
			}
			return e
		}
		state, e := r.Run(ctx)
		if state != TransitionLegacyVerified || !errors.Is(e, ErrCommitUnknown) {
			t.Fatal("uncommitted transport fault", state, e)
		}
		t.Log("TRANSPORT_COMMIT_NOT_FORWARDED=PASS independent source evidence; no replay")
	})
}

// Both attempts finish source collection before either acquires the deployment
// advisory lock. One commits, the other must reject the changed source.
func i4ConcurrentTransition(t *testing.T, ctx context.Context, admin *pgx.Conn, target Target, m *ExtensionManifest, a ApprovedSecurityTransition, sql string) *SecurityTransitionRunner {
	t.Helper()
	if _, e := admin.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryKey); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryKey)
	type result struct {
		r     *SecurityTransitionRunner
		state SecurityTransitionState
		e     error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		r, e := NewSecurityTransition(target, m, a, []byte(sql))
		if e != nil {
			t.Fatal(e)
		}
		r.hooks.commit = func(ctx context.Context, tx pgx.Tx) error {
			committed, e := i4CommitFault(ctx, tx, true)
			if !committed {
				return errors.New("TEST server did not confirm COMMIT to proxy")
			}
			return e
		}
		go func() { s, e := r.Run(ctx); results <- result{r, s, e} }()
	}
	deadline := time.Now().Add(10 * time.Second)
	waiting := 0
	for time.Now().Before(deadline) {
		if e := admin.QueryRow(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity s USING(pid) WHERE l.locktype='advisory' AND NOT l.granted AND s.datname=current_database() AND s.usename='tremor_deployer'`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if waiting != 2 {
		t.Fatal("two attempts did not wait for advisory lock", waiting)
	}
	if _, e := admin.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryKey); e != nil {
		t.Fatal(e)
	}
	var winner *SecurityTransitionRunner
	successes := 0
	for i := 0; i < 2; i++ {
		v := <-results
		if v.e == nil && v.state == TransitionProtected {
			winner = v.r
			successes++
		} else if v.e == nil {
			t.Fatal("unexpected deployment result", v.state)
		}
	}
	if successes != 1 {
		t.Fatal("transition applied more or less than once", successes)
	}
	var receipts, history int
	if e := admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM tremor_deployment.security_transitions),(SELECT count(*) FROM tremor_deployment.ledger_history_v1)").Scan(&receipts, &history); e != nil || receipts != 1 || history != 1 {
		t.Fatal("duplicate receipt", e)
	}
	t.Log("REAL_LOST_COMMIT_RESPONSE=PASS proxy suppressed CommandComplete; recovery confirmed committed state on independent connection; two advisory waiters, one transition/receipt/history; no SQL replay")
	return winner
}

func i4ConcurrentApply(t *testing.T, ctx context.Context, admin *pgx.Conn, target Target, m *ExtensionManifest, b BoundSecurityTransition, a ApprovedProtectedMigration) {
	t.Helper()
	_, e := admin.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryKey)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryKey)
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- ApplyProtectedExtension(ctx, target, m, b, a) }()
	}
	deadline := time.Now().Add(10 * time.Second)
	waiting := 0
	for time.Now().Before(deadline) {
		if e = admin.QueryRow(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity s USING(pid) WHERE l.locktype='advisory' AND NOT l.granted AND s.datname=current_database() AND s.usename='tremor_deployer'`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, _ = admin.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryKey)
	wg.Wait()
	close(failures)
	if waiting != 2 {
		t.Fatal("two apply lock waiters absent")
	}
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	t.Log("CONCURRENT_PROTECTED_APPLY=PASS two advisory waiters; exactly one effect/receipt checked below")
}

func TestI4AcceptanceRuntimeDisposable(t *testing.T) {
	if !i4Enabled() || os.Getenv("I3_MODE") != "faults" {
		t.Skip("isolated acceptance controller only")
	}
	i3WaitHTTP(t)
	ctx := context.Background()
	var v i3Bundle
	i3Read(t, "/controller/bundle.json", &v)
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
	var system, fsync, synchronous string
	if e = admin.QueryRow(ctx, "SELECT system_identifier::text,current_setting('fsync'),current_setting('synchronous_commit') FROM pg_control_system()").Scan(&system, &fsync, &synchronous); e != nil || system != v.Target.ServerID || fsync != "on" || synchronous != "on" {
		t.Fatal("TEST durability/identity", e)
	}
	base, e := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if e != nil {
		t.Fatal(e)
	}
	m := &ExtensionManifest{plan: v.Extension, base: base, bodies: v.Bodies, chains: v.Chains}
	b, e := BindSecurityTransition(ApprovedSecurityTransition{v.Descriptor, v.TransitionSHA}, v.Source.Target, v.Source)
	if e != nil {
		t.Fatal(e)
	}
	a := ApprovedProtectedMigration{v.Plan, v.PlanSHA}
	raw, e := os.ReadFile("/verifier-password/password")
	if e != nil {
		t.Fatal(e)
	}
	v.Target.Password = string(raw)
	if e = VerifyProtectedExtension(ctx, v.Target, m, b, a); e != nil {
		t.Fatal(e)
	}
	original := i4RawContents(t, ctx, admin, v.Plan.After.Scope)
	i4Crash(t, "after-commit", system)
	closeConnection(admin)
	admin, e = pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer closeConnection(admin)
	if !reflect.DeepEqual(original, i4RawContents(t, ctx, admin, v.Plan.After.Scope)) {
		t.Fatal("committed data/history changed after postmaster SIGKILL")
	}
	if e = VerifyProtectedExtension(ctx, v.Target, m, b, a); e != nil {
		t.Fatal("post-crash protected verification", e)
	}
	c, _ := i3Client(t, "/runtime")
	if e = c.Check(ctx); e != nil {
		t.Fatal("post-crash mTLS", e)
	}
	i3HTTPStatus(t, 200)
	i3HTTPIntegrity(t, ctx, admin)
	i3Write(t, "/i3/control/restore-evidence.json", original)
	t.Logf("REAL_SERVER_SIGKILL_COMMITTED=PASS system=%s fsync=%s synchronous_commit=%s WAL/history/data/receipts preserved; fresh verifier/mTLS/API200", system, fsync, synchronous)
}

// Backup evidence contains no credentials: exact scoped row hashes and semantic
// ACL/role metadata. Cluster/database/object OIDs are tested separately, not
// silently rebound to their new restore values.
type i4RestoreEvidence struct {
	Rows     map[string]string
	Metadata []TransitionMetadata
}

func i4RawContents(t *testing.T, ctx context.Context, conn *pgx.Conn, scope TransitionScope) i4RestoreEvidence {
	t.Helper()
	tx, e := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		t.Fatal(e)
	}
	defer rollback(tx)
	if _, e = tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog; SET LOCAL TimeZone TO 'UTC'; SET LOCAL row_security TO off", pgx.QueryExecModeSimpleProtocol); e != nil {
		t.Fatal(e)
	}
	out := i4RestoreEvidence{Rows: map[string]string{}, Metadata: []TransitionMetadata{}}
	for _, r := range scope.Objects {
		if r.Presence != "present" {
			continue
		}
		if r.Kind == "table" {
			h, e := collectSecurityTableRows(ctx, tx, r.Schema, strings.TrimPrefix(r.Key, r.Schema+"."))
			if e != nil {
				t.Fatal(e)
			}
			out.Rows[r.Key] = h
		}
		if r.Kind == "sequence" {
			value, e := collectSecuritySequence(ctx, tx, r)
			if e != nil {
				t.Fatal(e)
			}
			out.Rows["sequence-state:"+r.Key] = transitionDigest(value)
		}
		// Definitions that include OIDs are not a portable backup equivalence proof.
		// Semantic ACL/attributes below remain exact across a physical rebind.
		meta, _, exists, e := collectSecurityObject(ctx, tx, r)
		if e != nil || !exists {
			t.Fatal("restore metadata", r.Key, e)
		}
		if r.Kind == "table" || r.Kind == "view" {
			meta.DefinitionSHA = ""
		}
		out.Metadata = append(out.Metadata, meta)
	}
	return out
}

func TestI4RestoreDisposable(t *testing.T) {
	if os.Getenv("I4_RESTORE") != "yes" {
		t.Skip("own restore TEST cluster only")
	}
	ctx := context.Background()
	var v i3Bundle
	i3Read(t, "/private/bundle.json", &v)
	var want i4RestoreEvidence
	i3Read(t, "/control/restore-evidence.json", &want)
	cfg, e := pgx.ParseConfig("host=127.0.0.1 user=e1a_restore_admin dbname=tremor_e1b_collector_test sslmode=disable")
	if e != nil {
		t.Fatal(e)
	}
	cfg.Password = os.Getenv("PGPASSWORD")
	conn, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer closeConnection(conn)
	var system string
	if e = conn.QueryRow(ctx, "SELECT system_identifier::text FROM pg_control_system()").Scan(&system); e != nil || system == v.Target.ServerID {
		t.Fatal("new restore cluster identity absent", e)
	}
	got := i4RawContents(t, ctx, conn, v.Plan.After.Scope)
	if !reflect.DeepEqual(want, got) {
		t.Fatal("restore row hashes/semantic owner/ACL/role metadata mismatch")
	}
	restored := v.Target
	restored.User = cfg.User
	restored.Password = cfg.Password
	if unexpected, _, err := restored.connect(ctx); err == nil {
		closeConnection(unexpected)
		t.Fatal("restore silently admitted original binding")
	}
	restored.ServerID = system
	actual, _, e := restored.connect(ctx)
	if e != nil {
		t.Fatal("new cluster credentials/connection not valid", e)
	}
	closeConnection(actual)
	password, e := os.ReadFile("/private/password")
	if e != nil || !checksumPattern.Match(password) {
		t.Fatal("restore verifier TEST credential absent")
	}
	if _, e = conn.Exec(ctx, "ALTER ROLE tremor_migrator PASSWORD '"+string(password)+"'"); e != nil {
		t.Fatal("restore verification identity setup failed")
	}
	restored.User = "tremor_migrator"
	restored.Password = string(password)
	mig, _, e := restored.connect(ctx)
	if e != nil {
		t.Fatal("minimal restored verification identity", e)
	}
	closeConnection(mig)
	base, e := Load(filepath.Join("..", "..", "migrations", "manifest.clean-v2.json"))
	if e != nil {
		t.Fatal(e)
	}
	m := &ExtensionManifest{plan: v.Extension, base: base, bodies: v.Bodies, chains: v.Chains}
	b, e := BindSecurityTransition(ApprovedSecurityTransition{v.Descriptor, v.TransitionSHA}, v.Source.Target, v.Source)
	if e != nil {
		t.Fatal(e)
	}
	if VerifyProtectedExtension(ctx, restored, m, b, ApprovedProtectedMigration{v.Plan, v.PlanSHA}) == nil {
		t.Fatal("new system identifier silently reapproved old anchors")
	}
	t.Logf("BACKUP_RESTORE_DATA_LEDGERS_HISTORY_RECEIPTS_ROLES_ACL=PASS original_system=%s restored_system=%s OLD_TARGET_BINDING_REJECTED=PASS explicit new approval/rebind required", v.Target.ServerID, system)
}
