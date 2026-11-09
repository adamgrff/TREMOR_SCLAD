package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type SecurityCollectionPhase string

const (
	CollectTransitionSource SecurityCollectionPhase = "source"
	CollectTransitionFinal  SecurityCollectionPhase = "final"
)

type CollectedPrivilege struct {
	Actor, Object, Privilege string
	Allowed, GrantOption     bool
}
type CollectedRolePath struct {
	Actor, Role            string
	Member, Inherited, Set bool
}

// Only CollectSecuritySnapshot can construct this result outside package tests.
// The DTO accessor returns a copy; it does not itself convey provenance.
type CollectedSecuritySnapshot struct {
	snapshot   TransitionSnapshot
	receipt    *TransitionReceipt
	privileges []CollectedPrivilege
	paths      []CollectedRolePath
	seal       string
}

func (c CollectedSecuritySnapshot) Snapshot() (TransitionSnapshot, error) {
	if !c.valid() {
		return TransitionSnapshot{}, errors.New("no completed security collection")
	}
	var s TransitionSnapshot
	raw, _ := json.Marshal(c.snapshot)
	_ = json.Unmarshal(raw, &s)
	return s, nil
}
func (c CollectedSecuritySnapshot) Privileges() ([]CollectedPrivilege, error) {
	if !c.valid() {
		return nil, errors.New("no completed collection")
	}
	return append([]CollectedPrivilege{}, c.privileges...), nil
}
func (c CollectedSecuritySnapshot) RolePaths() ([]CollectedRolePath, error) {
	if !c.valid() {
		return nil, errors.New("no completed collection")
	}
	return append([]CollectedRolePath{}, c.paths...), nil
}
func (c CollectedSecuritySnapshot) valid() bool {
	return c.seal != "" && c.seal == transitionDigest([]any{c.snapshot, c.receipt, c.privileges, c.paths})
}

func BindCollectedSecurityTransition(a ApprovedSecurityTransition, c CollectedSecuritySnapshot) (BoundSecurityTransition, error) {
	if !c.valid() || c.receipt != nil {
		return BoundSecurityTransition{}, errors.New("uncollected source or existing transition receipt")
	}
	return BindSecurityTransition(a, a.descriptor.Target, c.snapshot)
}

// Receipt is exclusively the value read by the collector, never ExpectedReceipt().
func ClassifyCollectedTransitionRecovery(b BoundSecurityTransition, c CollectedSecuritySnapshot) TransitionRecoveryResult {
	if !c.valid() {
		return TransitionOutcomeUnknown
	}
	return ClassifyTransitionRecovery(b, &c.snapshot, c.receipt)
}

// CollectSecuritySnapshot runs on a new explicit-target connection, never a DSN,
// pool supplied by an operator, ambient PG settings, or SQL from the descriptor.
// Source/final selects an approved policy, not a user-defined fingerprint.
func CollectSecuritySnapshot(ctx context.Context, target Target, a ApprovedSecurityTransition, phase SecurityCollectionPhase, m *ExtensionManifest) (CollectedSecuritySnapshot, error) {
	d := a.descriptor
	if a.sha == "" || a.sha != transitionDigest(d) || validateTransitionDescriptor(d) != nil {
		return CollectedSecuritySnapshot{}, errors.New("release-approved descriptor required")
	}
	var scope TransitionScope
	switch phase {
	case CollectTransitionSource:
		scope = d.SourceScope
	case CollectTransitionFinal:
		scope = d.FinalScope
	default:
		return CollectedSecuritySnapshot{}, errors.New("unknown collection phase")
	}
	if target.ServerID != d.Target.ClusterID || target.Database != d.Target.Database || target.Schema != d.Target.Schema || target.Profile != IdentityProfile || m == nil || m.base == nil || baselineDigest(m.base) != d.Frontier.BaselineManifestSHA || m.ManifestSHA256() != d.Frontier.ExtensionManifestSHA {
		return CollectedSecuritySnapshot{}, errors.New("collection target/manifest mismatch")
	}
	conn, id, err := target.connect(ctx)
	if err != nil {
		return CollectedSecuritySnapshot{}, err
	}
	defer closeConnection(conn)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return CollectedSecuritySnapshot{}, errors.New("cannot begin read-only collection")
	}
	defer rollback(tx)
	c, err := collectSecuritySnapshotTx(ctx, tx, target, uint32(id.DatabaseOID), d, scope, m)
	if err != nil {
		return CollectedSecuritySnapshot{}, fmt.Errorf("security collection rejected: %w", err)
	}
	// Rollback closes a read-only observation; neither transaction nor receipt is applied.
	if err = tx.Rollback(ctx); err != nil {
		return CollectedSecuritySnapshot{}, errors.New("cannot finish security observation")
	}
	c.seal = transitionDigest([]any{c.snapshot, c.receipt, c.privileges, c.paths})
	return c, nil
}

func collectSecuritySnapshotInContext(ctx context.Context, tx pgx.Tx, target Target, databaseOID uint32, d SecurityTransitionDescriptor, scope TransitionScope, m *ExtensionManifest) (CollectedSecuritySnapshot, error) {
	c := CollectedSecuritySnapshot{privileges: []CollectedPrivilege{}, paths: []CollectedRolePath{}}
	s := TransitionSnapshot{Target: d.Target, DatabaseOID: databaseOID, Metadata: []TransitionMetadata{}, Ledgers: []TransitionLedgerSnapshot{}, ObjectIdentities: []TransitionObjectIdentity{}, ScopeSHA: transitionDigest(scope)}
	var db, user, server, deployment string
	var actualOID uint32
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.current_database(),current_user,(SELECT system_identifier::text FROM pg_catalog.pg_control_system()),(SELECT oid FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database())`).Scan(&db, &user, &server, &actualOID); err != nil {
		return c, errors.New("cannot verify transaction identity")
	}
	if db != target.Database || user != target.User || server != target.ServerID || actualOID != databaseOID {
		return c, errors.New("transaction identity mismatch")
	}
	if err := tx.QueryRow(ctx, `SELECT CASE WHEN count(*)=1 AND bool_and(singleton) THEN min(deployment_id::text) END FROM `+pgx.Identifier{target.Schema, "deployment_identity"}.Sanitize()).Scan(&deployment); err != nil || deployment != d.Target.DeploymentID {
		return c, errors.New("deployment identity mismatch/unreadable")
	}
	r, err := New(target, m.base)
	if err != nil {
		return c, err
	}
	state, err := r.readState(ctx, tx)
	if err != nil || !state.Initialized || len(state.Pending) != 0 {
		return c, errors.New("complete baseline lineage required")
	}
	lineage := []any{}
	for _, e := range state.Applied {
		lineage = append(lineage, []any{e.Entry, e.Profile})
	}
	s.Frontier = TransitionFrontier{BaselineManifestSHA: baselineDigest(m.base), BaselineLineageSHA: transitionDigest(lineage), ExtensionManifestSHA: m.ManifestSHA256(), Entries: []TransitionEntry{}}
	metaByKey := map[string]TransitionMetadata{}
	identities := map[string]TransitionObjectIdentity{}
	for _, req := range scope.Objects {
		obj, physical, exists, err := collectSecurityObject(ctx, tx, req)
		if err != nil {
			return c, fmt.Errorf("%s: %w", req.Key, err)
		}
		if exists != (req.Presence == "present") {
			return c, fmt.Errorf("unexpected object presence: %s", req.Key)
		}
		if !exists {
			continue
		}
		s.Metadata = append(s.Metadata, obj)
		metaByKey[obj.Key] = obj
		if req.Kind != "default_acl" {
			s.ObjectIdentities = append(s.ObjectIdentities, physical)
			identities[obj.Key] = physical
		}
	}
	// Discover catalog objects independently: a scoped inventory cannot hide extra
	// relations, columns or routines inside its schemas. Definitions cover triggers,
	// constraints, indexes, rules and policies on scoped relations.
	if err = collectSecurityCoverage(ctx, tx, scope); err != nil {
		return c, err
	}
	var databaseOwner, databaseSecurity string
	if err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_userbyid(datdba),jsonb_build_array(pg_catalog.pg_get_userbyid(datdba),datacl::text,datallowconn,datconnlimit,datistemplate)::text FROM pg_catalog.pg_database WHERE oid=$1`, databaseOID).Scan(&databaseOwner, &databaseSecurity); err != nil {
		return c, errors.New("database security unreadable")
	}
	if owner, exists := metaByKey["role:"+databaseOwner]; !exists || owner.Kind != "role" {
		return c, errors.New("database owner outside approved role scope")
	}
	for i, m := range s.Metadata {
		if m.Key == "schema:"+target.Schema {
			s.Metadata[i].DefinitionSHA = transitionDigest([]any{m.DefinitionSHA, databaseSecurity})
		}
	}
	for _, name := range []string{"schema_migrations", "schema_extensions"} {
		key := target.Schema + "." + name
		obj, exists := metaByKey[key]
		if !exists {
			continue
		}
		oid := identities[key].OID
		if err = r.checkLedgerShape(ctx, tx, int64(oid)); err != nil {
			return c, err
		}
		if name == "schema_extensions" {
			if err = checkExtensionChecks(ctx, tx, int64(oid)); err != nil {
				return c, err
			}
		}
		marker := ""
		if err = tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.obj_description($1,'pg_class'),'')`, oid).Scan(&marker); err != nil {
			return c, err
		}
		rowsHash, err := collectSecurityTableRows(ctx, tx, target.Schema, name)
		if err != nil {
			return c, err
		}
		var constraintText string
		if err = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_array(conname,contype,pg_catalog.pg_get_constraintdef(oid),convalidated,conenforced) ORDER BY conname)::text,'[]') FROM pg_catalog.pg_constraint WHERE conrelid=$1`, oid).Scan(&constraintText); err != nil {
			return c, err
		}
		s.Ledgers = append(s.Ledgers, TransitionLedgerSnapshot{Key: key, OID: oid, Marker: marker, ConstraintsSHA: digest([]byte(constraintText)), RowsSHA: rowsHash, Owner: obj.Owner, ACL: obj.ACL})
		if name == "schema_extensions" {
			rows, e := tx.Query(ctx, `SELECT version,name,checksum,profile,ordinal,applied_at FROM `+pgx.Identifier{target.Schema, name}.Sanitize()+` ORDER BY ordinal`)
			if e != nil {
				return c, e
			}
			applied := []Applied{}
			for rows.Next() {
				var a Applied
				if e = rows.Scan(&a.Version, &a.Name, &a.SHA256, &a.Profile, &a.Order, &a.AppliedAt); e != nil {
					rows.Close()
					return c, e
				}
				applied = append(applied, a)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return c, e
			}
			if e = m.prefix(applied); e != nil {
				return c, e
			}
			for _, a := range applied {
				s.Frontier.Entries = append(s.Frontier.Entries, TransitionEntry{a.Version, a.Name, a.SHA256, a.Profile, a.Order})
			}
			if marker != "TREMOR extension ledger v1; "+baselineDigest(m.base)+"; "+deployment {
				return c, errors.New("extension marker mismatch")
			}
		}
	}
	if !reflect.DeepEqual(s.Frontier, d.Frontier) {
		return c, errors.New("observed migration frontier differs from release policy")
	}
	s.ReadinessSHA, err = collectorLegacyReadiness(ctx, tx, target.Schema)
	if err != nil {
		return c, err
	}
	business := []any{}
	receipts := []any{}
	sequenceBefore := []any{}
	for _, req := range scope.Objects {
		if req.Presence != "present" {
			continue
		}
		if req.Kind == "table" && req.Key != target.Schema+".schema_migrations" && req.Key != target.Schema+".schema_extensions" {
			if req.Key == "tremor_deployment.security_transitions" {
				continue
			}
			name := strings.TrimPrefix(req.Key, req.Schema+".")
			h, e := collectSecurityTableRows(ctx, tx, req.Schema, name)
			if e != nil {
				return c, e
			}
			business = append(business, []any{req.Key, h})
			if strings.Contains(name, "requests") || strings.Contains(name, "receipts") {
				receipts = append(receipts, []any{req.Key, h})
			}
			if req.Key == target.Schema+".deployment_identity" {
				s.IdentitySHA = h
			}
		}
		if req.Kind == "sequence" {
			value, e := collectSecuritySequence(ctx, tx, req)
			if e != nil {
				return c, e
			}
			sequenceBefore = append(sequenceBefore, value)
		}
	}
	if s.IdentitySHA == "" {
		return c, errors.New("deployment_identity must be in collection scope")
	}
	historyHash := digest(nil)
	if _, exists := metaByKey["tremor_deployment.security_transitions"]; exists {
		historyHash, err = collectSecurityRowsHash(ctx, tx, `SELECT pg_catalog.to_jsonb(t)::text FROM tremor_deployment.security_transitions t WHERE transition_id<>$1 ORDER BY pg_catalog.to_jsonb(t)::text COLLATE "C"`, d.ID)
		if err != nil {
			return c, err
		}
	}
	// Exclude only the new transition's receipt; preserve all older receipts,
	// including when an initially absent transition table is created later.
	business = append(business, []any{"transition_history", historyHash})
	receipts = append(receipts, []any{"transition_history", historyHash})
	s.BusinessDataSHA = transitionDigest(business)
	s.BusinessReceiptsSHA = transitionDigest(receipts)
	s.SequenceStateSHA = transitionDigest(sequenceBefore)
	if err = collectSecurityEffective(ctx, tx, &c, scope, identities, databaseOID, target.Database); err != nil {
		return c, err
	}
	if err = collectSessionDefaults(ctx, tx, &s); err != nil {
		return c, err
	}
	if err = collectParameterPrivileges(ctx, tx, &s, scope); err != nil {
		return c, err
	}
	// Effective inquiries include implicit memberships (e.g. pg_database_owner),
	// superuser privileges and PUBLIC. Bind their results into compared metadata,
	// not merely an auxiliary report that callers might accidentally ignore.
	for i, m := range s.Metadata {
		if m.Kind != "role" {
			continue
		}
		actor := strings.TrimPrefix(m.Key, "role:")
		ps := []CollectedPrivilege{}
		paths := []CollectedRolePath{}
		for _, p := range c.privileges {
			if p.Actor == actor {
				ps = append(ps, p)
			}
		}
		for _, p := range c.paths {
			if p.Actor == actor {
				paths = append(paths, p)
			}
		}
		s.Metadata[i].DefinitionSHA = transitionDigest([]any{m.DefinitionSHA, ps, paths, m.SessionDefaults, m.ParameterPrivileges})
	}
	// Sequence values are not MVCC. Detect observable movement, never claim this is
	// a lock or protection against concurrent ABA; deployment collection needs quiescence.
	sequenceAfter := []any{}
	for _, req := range scope.Objects {
		if req.Presence == "present" && req.Kind == "sequence" {
			v, e := collectSecuritySequence(ctx, tx, req)
			if e != nil {
				return c, e
			}
			sequenceAfter = append(sequenceAfter, v)
		}
	}
	if !reflect.DeepEqual(sequenceBefore, sequenceAfter) {
		return c, errors.New("sequence moved during collection")
	}
	if _, exists := metaByKey["tremor_deployment.security_transitions"]; exists {
		rows, e := tx.Query(ctx, `SELECT transition_id,descriptor_sha256,payload_sha256,bound_snapshot_sha256,frontier_sha256 FROM tremor_deployment.security_transitions WHERE transition_id=$1`, d.ID)
		if e != nil {
			return c, errors.New("unreadable transition receipt")
		}
		for rows.Next() {
			if c.receipt != nil {
				rows.Close()
				return c, errors.New("ambiguous transition receipt")
			}
			var receipt TransitionReceipt
			if e = rows.Scan(&receipt.TransitionID, &receipt.DescriptorSHA, &receipt.PayloadSHA, &receipt.BoundSnapshotSHA, &receipt.FrontierSHA); e != nil {
				rows.Close()
				return c, e
			}
			c.receipt = &receipt
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return c, e
		}
	}
	if err = validateTransitionSnapshot(s, scope); err != nil {
		return c, err
	}
	c.snapshot = s
	return c, nil
}

func collectSecurityTableRows(ctx context.Context, q queryer, schema, name string) (string, error) {
	return collectSecurityRowsHash(ctx, q, `SELECT pg_catalog.to_jsonb(t)::text FROM `+pgx.Identifier{schema, name}.Sanitize()+` t ORDER BY pg_catalog.to_jsonb(t)::text COLLATE "C"`)
}

func collectSecurityRowsHash(ctx context.Context, q queryer, sql string, args ...any) (string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return "", errors.New("required table rows unreadable")
	}
	defer rows.Close()
	h := sha256.New()
	var total int
	var frame [8]byte
	for rows.Next() {
		var row string
		if err = rows.Scan(&row); err != nil {
			return "", err
		}
		total += len(row)
		if total > 128<<20 {
			return "", errors.New("collection row budget exceeded")
		}
		binary.BigEndian.PutUint64(frame[:], uint64(len(row)))
		_, _ = h.Write(frame[:])
		_, _ = h.Write([]byte(row))
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func collectSecuritySequence(ctx context.Context, q queryer, r TransitionRequirement) (any, error) {
	var last int64
	var called bool
	err := q.QueryRow(ctx, `SELECT last_value,is_called FROM `+pgx.Identifier{r.Schema, strings.TrimPrefix(r.Key, r.Schema+".")}.Sanitize()).Scan(&last, &called)
	return []any{r.Key, last, called}, err
}

func collectSecurityCoverage(ctx context.Context, q queryer, scope TransitionScope) error {
	schemas := []string{}
	expected := map[string]string{}
	for _, r := range scope.Objects {
		if r.Presence == "present" {
			expected[r.Key] = r.Kind
			if r.Kind == "schema" {
				schemas = append(schemas, r.Schema)
			}
		}
	}
	rows, err := q.Query(ctx, securityCoverageSQL, schemas)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, kind string
		if err = rows.Scan(&key, &kind); err != nil {
			return err
		}
		if expected[key] != kind {
			return fmt.Errorf("unscoped/unsupported catalog object: %s", key)
		}
	}
	return rows.Err()
}

type securityPrivilegeRequest struct {
	Actor     string `json:"actor"`
	Key       string `json:"key"`
	OID       uint32 `json:"oid"`
	SubID     int32  `json:"sub_id"`
	Kind      string `json:"kind"`
	Privilege string `json:"privilege"`
}

func collectSecurityEffective(ctx context.Context, q queryer, c *CollectedSecuritySnapshot, scope TransitionScope, ids map[string]TransitionObjectIdentity, databaseOID uint32, database string) error {
	roles := []string{}
	for _, r := range scope.Objects {
		if r.Kind == "role" && r.Presence == "present" {
			roles = append(roles, strings.TrimPrefix(r.Key, "role:"))
		}
	}
	sort.Strings(roles)
	rows, err := q.Query(ctx, securityRolePathsSQL, roles)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p CollectedRolePath
		if err = rows.Scan(&p.Actor, &p.Role, &p.Member, &p.Inherited, &p.Set); err != nil {
			rows.Close()
			return err
		}
		c.paths = append(c.paths, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	requests := []securityPrivilegeRequest{}
	for _, actor := range roles {
		for _, privilege := range []string{"CONNECT", "CREATE", "TEMPORARY"} {
			requests = append(requests, securityPrivilegeRequest{Actor: actor, Key: "database:" + database, OID: databaseOID, Kind: "database", Privilege: privilege})
		}
		for _, r := range scope.Objects {
			if r.Presence != "present" {
				continue
			}
			var ps []string
			switch r.Kind {
			case "schema":
				ps = []string{"USAGE", "CREATE"}
			case "table", "view":
				ps = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN"}
			case "column":
				ps = []string{"SELECT", "INSERT", "UPDATE", "REFERENCES"}
			case "sequence":
				ps = []string{"USAGE", "SELECT", "UPDATE"}
			case "function":
				ps = []string{"EXECUTE"}
			default:
				continue
			}
			for _, p := range ps {
				requests = append(requests, securityPrivilegeRequest{actor, r.Key, ids[r.Key].OID, ids[r.Key].SubID, r.Kind, p})
			}
		}
	}
	raw, err := json.Marshal(requests)
	if err != nil {
		return err
	}
	rows, err = q.Query(ctx, securityEffectiveSQL, raw)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p CollectedPrivilege
		if err = rows.Scan(&p.Actor, &p.Object, &p.Privilege, &p.Allowed, &p.GrantOption); err != nil {
			rows.Close()
			return err
		}
		c.privileges = append(c.privileges, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(c.privileges) != len(requests) || len(c.paths) != len(roles)*len(roles) {
		return errors.New("incomplete effective security evidence")
	}
	return nil
}
