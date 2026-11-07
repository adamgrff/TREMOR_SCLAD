package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExtensionOptions deliberately has no password fallback, provisioning, grants
// or SET ROLE. Inspection always authenticates Target.User first. A later role
// transition requires its own reviewed contract.
type ExtensionOptions struct {
	DeploymentID  string
	BaselineOwner string
	LedgerOwner   string
}

type ExtensionState struct {
	Profile                  string                    `json:"profile"`
	Readiness                string                    `json:"readiness"`
	SecurityGate             string                    `json:"security_gate"`
	BaselineLedgerAuthority  *ExtensionLedgerAuthority `json:"baseline_ledger_authority,omitempty"`
	ExtensionLedgerAuthority *ExtensionLedgerAuthority `json:"extension_ledger_authority,omitempty"`
	Initialized              bool                      `json:"initialized"`
	Applied                  []Applied                 `json:"applied"`
	Pending                  []ExtensionEntry          `json:"pending"`
	CommitResolved           bool                      `json:"commit_resolved,omitempty"`
}

type ExtensionRunner struct {
	base     *Runner
	manifest *ExtensionManifest
	options  ExtensionOptions
	hooks    faultHooks
	approved bool
}

func NewExtension(target Target, manifest *ExtensionManifest, options ExtensionOptions) (*ExtensionRunner, error) {
	if manifest == nil || target.Profile != IdentityProfile || !simpleName.MatchString(options.BaselineOwner) || !simpleName.MatchString(options.LedgerOwner) || options.LedgerOwner != target.User || target.User == "tremor_api" || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(options.DeploymentID) || options.DeploymentID == "00000000-0000-0000-0000-000000000000" {
		return nil, errors.New("explicit extension owners, non-runtime user and deployment UUID required")
	}
	base, err := New(target, manifest.base)
	if err != nil {
		return nil, err
	}
	return &ExtensionRunner{base: base, manifest: manifest, options: options, approved: releaseApproves(manifest.ManifestSHA256())}, nil
}
func (r *ExtensionRunner) ledger() string {
	return pgx.Identifier{r.base.target.Schema, extensionLedgerName}.Sanitize()
}
func (r *ExtensionRunner) marker() string {
	return "TREMOR extension ledger v1; " + r.manifest.plan.BaselineSHA256 + "; " + r.options.DeploymentID
}

func (r *ExtensionRunner) inspect(ctx context.Context, tx pgx.Tx) (ExtensionState, error) {
	state := ExtensionState{Profile: ExtensionProfile, Readiness: "baseline", SecurityGate: "E1B-ledger-privilege-separation-required", Applied: []Applied{}, Pending: append([]ExtensionEntry{}, r.manifest.plan.Migrations...)}
	if _, pathErr := tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog,"+pgx.Identifier{r.base.target.Schema}.Sanitize()); pathErr != nil {
		return state, errors.New("cannot set readiness search path")
	}
	base, err := r.base.readState(ctx, tx)
	if err != nil {
		return state, err
	}
	if !base.Initialized || len(base.Pending) != 0 {
		return state, ErrIncomplete
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_userbyid(c.relowner)=$2 FROM pg_catalog.pg_class c WHERE c.oid=$1::regclass`, r.base.ledger(), r.options.BaselineOwner).Scan(&valid)
	if err != nil || !valid {
		return state, fmt.Errorf("%w: baseline ledger owner", ErrUnsafe)
	}
	if err = r.base.checkDeployment(ctx, tx); err != nil {
		return state, err
	}
	var deployment string
	if tx.QueryRow(ctx, `SELECT deployment_id::text FROM `+pgx.Identifier{r.base.target.Schema, "deployment_identity"}.Sanitize()+` WHERE singleton`).Scan(&deployment) != nil || deployment != r.options.DeploymentID {
		return state, fmt.Errorf("%w: deployment UUID mismatch", ErrUnsafe)
	}
	var oid int64
	var kind, marker, owner string
	var rls, force bool
	err = tx.QueryRow(ctx, `SELECT c.oid::bigint,c.relkind::text,COALESCE(pg_catalog.obj_description(c.oid,'pg_class'),''),pg_catalog.pg_get_userbyid(c.relowner),c.relrowsecurity,c.relforcerowsecurity FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, r.base.target.Schema, extensionLedgerName).Scan(&oid, &kind, &marker, &owner, &rls, &force)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return state, errors.New("cannot inspect extension ledger")
	}
	if err == nil {
		if kind != "r" || marker != r.marker() || owner != r.options.LedgerOwner || rls || force {
			return state, fmt.Errorf("%w: foreign extension ledger", ErrUnsafe)
		}
		if err = r.base.checkLedgerShape(ctx, tx, oid); err != nil {
			return state, err
		}
		err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*)=4 AND bool_and(convalidated AND conenforced) FROM pg_catalog.pg_constraint WHERE conrelid=$1 AND contype='c')
 AND (SELECT count(*)=3 AND bool_and(indisvalid AND indisready) FROM pg_catalog.pg_index WHERE indrelid=$1)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_inherits WHERE inhrelid=$1)
 AND (SELECT relpersistence='p' FROM pg_catalog.pg_class WHERE oid=$1)
 AND (SELECT count(*)=1 AND bool_and(a.attname='applied_at' AND pg_catalog.pg_get_expr(d.adbin,d.adrelid)='clock_timestamp()') FROM pg_catalog.pg_attrdef d JOIN pg_catalog.pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum WHERE d.adrelid=$1)
`, oid).Scan(&valid)
		if err != nil || !valid {
			return state, fmt.Errorf("%w: extension ledger definitions", ErrUnsafe)
		}
		if err = checkExtensionChecks(ctx, tx, oid); err != nil {
			return state, err
		}
		rows, queryErr := tx.Query(ctx, `SELECT version,name,checksum,profile,ordinal,applied_at FROM `+r.ledger()+` ORDER BY ordinal`)
		if queryErr != nil {
			return state, errors.New("cannot read extension ledger")
		}
		for rows.Next() {
			var row Applied
			if rows.Scan(&row.Version, &row.Name, &row.SHA256, &row.Profile, &row.Order, &row.AppliedAt) != nil {
				rows.Close()
				return state, errors.New("cannot decode extension ledger")
			}
			state.Applied = append(state.Applied, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return state, errors.New("incomplete extension ledger read")
		}
		state.Initialized = true
	}
	if err = r.manifest.prefix(state.Applied); err != nil {
		return state, err
	}
	state.Pending = state.Pending[len(state.Applied):]
	expected := r.manifest.plan.BaselineReadinessSHA256
	if len(state.Applied) > 0 {
		expected = r.manifest.plan.Migrations[len(state.Applied)-1].ReadinessSHA256
		state.Readiness = fmt.Sprintf("extension:%d", state.Applied[len(state.Applied)-1].Version)
	}
	if len(state.Applied) == 0 && !state.Initialized {
		contract, loadErr := loadWarehouseContract()
		if loadErr != nil {
			return state, loadErr
		}
		if err = r.base.checkWarehouseObjects(ctx, tx, contract); err != nil {
			return state, err
		}
	}
	state.BaselineLedgerAuthority, err = ledgerAuthority(ctx, tx, r.base.ledger())
	if err != nil {
		return state, err
	}
	if state.Initialized {
		state.ExtensionLedgerAuthority, err = ledgerAuthority(ctx, tx, r.ledger())
		if err != nil {
			return state, err
		}
	}
	actual, err := extensionCatalog(ctx, tx, r.base.target.Schema)
	if err != nil || actual != expected {
		return state, fmt.Errorf("%w: unapproved readiness catalogue", ErrUnsafe)
	}
	return state, nil
}

// Run never reads DATABASE_URL/.env. Apply serializes with the base runner and
// bootstrap. Every migration and receipt commit together. No business emptiness
// check is performed. A reconnect never automatically resubmits uncertain SQL.
func (r *ExtensionRunner) Run(ctx context.Context, mode string) (ExtensionState, error) {
	if mode != "status" && mode != "verify" && mode != "apply" {
		return ExtensionState{}, errors.New("unknown extension mode")
	}
	if mode == "apply" && !r.approved {
		return ExtensionState{}, ErrExtensionUnapproved
	}
	conn, id, err := r.base.target.connect(ctx)
	if err != nil {
		return ExtensionState{}, err
	}
	defer closeConnection(conn)
	if mode == "apply" {
		if _, err = conn.Exec(ctx, `SELECT pg_catalog.pg_advisory_lock($1)`, advisoryKey); err != nil {
			return ExtensionState{}, errors.New("cannot lock extension deployment")
		}
		if r.hooks.locked != nil {
			r.hooks.locked()
		}
	}
	for {
		access := pgx.ReadOnly
		if mode == "apply" {
			access = pgx.ReadWrite
		}
		tx, beginErr := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: access})
		if beginErr != nil {
			return ExtensionState{}, errors.New("cannot start extension inspection")
		}
		state, inspectErr := r.inspect(ctx, tx)
		if inspectErr != nil {
			rollback(tx)
			return state, inspectErr
		}
		if mode != "apply" || len(state.Pending) == 0 {
			rollback(tx)
			if mode == "verify" && len(state.Pending) > 0 {
				return state, ErrIncomplete
			}
			return state, nil
		}
		before, snapshotErr := r.ledgerSnapshot(ctx, tx, state)
		if snapshotErr != nil {
			rollback(tx)
			return state, snapshotErr
		}
		if !state.Initialized {
			_, err = tx.Exec(ctx, `CREATE TABLE `+r.ledger()+`(
version integer PRIMARY KEY CONSTRAINT ledger_version_positive CHECK(version>24),
name text NOT NULL UNIQUE,
checksum text NOT NULL CONSTRAINT ledger_checksum_hex CHECK(checksum ~ '^[0-9a-f]{64}$'),
profile text NOT NULL CONSTRAINT ledger_profile_clean CHECK(profile ~ '^[0-9a-f]{64}$'),
ordinal integer NOT NULL UNIQUE CONSTRAINT ledger_order_positive CHECK(ordinal>0),
applied_at timestamptz NOT NULL DEFAULT pg_catalog.clock_timestamp()); COMMENT ON TABLE `+r.ledger()+` IS '`+r.marker()+`'`, pgx.QueryExecModeSimpleProtocol)
			if err != nil {
				rollback(tx)
				return state, errors.New("cannot initialize extension ledger")
			}
		}
		i := len(state.Applied)
		m := r.manifest.plan.Migrations[i]
		if _, err = tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{r.base.target.Schema}.Sanitize()+",pg_catalog"); err == nil {
			_, err = tx.Exec(ctx, r.manifest.bodies[i], pgx.QueryExecModeSimpleProtocol)
		}
		if err == nil && r.hooks.afterSQL != nil {
			err = r.hooks.afterSQL(m.Version, tx)
		}
		if err == nil {
			var count int64
			tag, writeErr := tx.Exec(ctx, `INSERT INTO `+r.ledger()+`(version,name,checksum,profile,ordinal) VALUES($1,$2,$3,$4,$5)`, m.Version, m.Name, m.SHA256, r.manifest.chains[i], m.Order)
			err = writeErr
			count = tag.RowsAffected()
			if err == nil && count != 1 {
				err = ErrUnsafe
			}
		}
		if err == nil && r.hooks.afterLedger != nil {
			err = r.hooks.afterLedger(m.Version, tx)
		}
		var committed extensionLedgerSnapshot
		if err == nil {
			var post ExtensionState
			post, err = r.inspect(ctx, tx)
			if err == nil && len(post.Applied) != i+1 {
				err = ErrUnsafe
			}
			if err == nil {
				committed, err = r.ledgerSnapshot(ctx, tx, post)
			}
			if err == nil {
				err = preservedLedgers(before, committed, 1, true)
			}
		}
		if err != nil {
			rollback(tx)
			return state, fmt.Errorf("extension %03d rolled back: %w", m.Version, err)
		}
		if r.hooks.commit != nil {
			err = r.hooks.commit(ctx, m.Version, tx)
		} else {
			err = tx.Commit(ctx)
		}
		if err != nil {
			rollback(tx)
			return r.resolveCommit(conn, id, committed)
		}
	}
}

func (r *ExtensionRunner) resolveCommit(old *pgx.Conn, id identity, expected extensionLedgerSnapshot) (ExtensionState, error) {
	closeConnection(old)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return recoverExtensionCommit(expected, func() (ExtensionState, extensionLedgerSnapshot, error) {
		conn, fresh, err := r.base.target.connect(ctx)
		if err != nil {
			return ExtensionState{}, extensionLedgerSnapshot{}, err
		}
		defer closeConnection(conn)
		if fresh.DatabaseOID != id.DatabaseOID {
			return ExtensionState{}, extensionLedgerSnapshot{}, fmt.Errorf("%w: recovery database OID", ErrUnsafe)
		}
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return ExtensionState{}, extensionLedgerSnapshot{}, err
		}
		defer rollback(tx)
		state, err := r.inspect(ctx, tx)
		if err != nil {
			return state, extensionLedgerSnapshot{}, err
		}
		snapshot, err := r.ledgerSnapshot(ctx, tx, state)
		return state, snapshot, err
	})
}

// Catalog fingerprint v2: versioned readiness independent of table counts and
// business data. Includes all user schemas (even empty), relation columns,
// constraints, indexes, functions, triggers, policies, rules, types, extensions
// and default ACL. Sequence current values are intentionally excluded.
func extensionCatalog(ctx context.Context, tx pgx.Tx, schema string) (string, error) {
	if err := checkExtensionCatalogSupport(ctx, tx); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, extensionCatalogSQL, schema)
	if err != nil {
		return "", errors.New("cannot read readiness catalogue")
	}
	defer rows.Close()
	var records []string
	for rows.Next() {
		var value string
		if rows.Scan(&value) != nil {
			return "", errors.New("invalid readiness catalogue")
		}
		records = append(records, value)
	}
	if rows.Err() != nil {
		return "", errors.New("incomplete readiness catalogue")
	}
	return catalogRecordsDigest(records), nil
}

const extensionCatalogSQL = `WITH ns AS (SELECT oid,nspname,nspowner,nspacl FROM pg_catalog.pg_namespace WHERE nspname !~ '^pg_' AND nspname<>'information_schema'), rel AS (
 SELECT c.* FROM pg_catalog.pg_class c JOIN ns n ON n.oid=c.relnamespace WHERE NOT(n.nspname=$1 AND c.relname='schema_extensions') AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class t ON t.oid=i.indrelid WHERE i.indexrelid=c.oid AND t.relnamespace=$1::regnamespace AND t.relname='schema_extensions')
), objects AS (
 SELECT pg_catalog.jsonb_build_array('schema',nspname,pg_catalog.pg_get_userbyid(nspowner),nspacl::text)::text AS value FROM ns
 UNION ALL SELECT pg_catalog.jsonb_build_array('relation',n.nspname,c.relname,c.relkind,c.relpersistence,c.relrowsecurity,c.relforcerowsecurity,c.reloptions,pg_catalog.pg_get_userbyid(c.relowner),c.relacl::text,c.relreplident,pg_catalog.pg_get_expr(c.relpartbound,c.oid))::text FROM rel c JOIN ns n ON n.oid=c.relnamespace
 UNION ALL SELECT pg_catalog.jsonb_build_array('column',n.nspname,c.relname,a.attnum,a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attidentity,a.attgenerated,a.attacl::text,(SELECT cn.nspname||'.'||co.collname FROM pg_catalog.pg_collation co JOIN pg_catalog.pg_namespace cn ON cn.oid=co.collnamespace WHERE co.oid=a.attcollation),a.attoptions,a.attfdwoptions,a.attmissingval::text,pg_catalog.pg_get_expr(d.adbin,d.adrelid))::text FROM rel c JOIN ns n ON n.oid=c.relnamespace JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
 UNION ALL SELECT pg_catalog.jsonb_build_array('constraint',n.nspname,c.relname,k.conname,pg_catalog.pg_get_constraintdef(k.oid),k.convalidated,k.conenforced)::text FROM rel c JOIN ns n ON n.oid=c.relnamespace JOIN pg_catalog.pg_constraint k ON k.conrelid=c.oid
 UNION ALL SELECT pg_catalog.jsonb_build_array('index',n.nspname,c.relname,pg_catalog.pg_get_indexdef(i.indexrelid),i.indisvalid,i.indisready)::text FROM rel c JOIN ns n ON n.oid=c.relnamespace JOIN pg_catalog.pg_index i ON i.indexrelid=c.oid
 UNION ALL SELECT pg_catalog.jsonb_build_array('function',n.nspname,p.proname,pg_catalog.pg_get_functiondef(p.oid),pg_catalog.pg_get_userbyid(p.proowner),p.proacl::text)::text FROM pg_catalog.pg_proc p JOIN ns n ON n.oid=p.pronamespace WHERE p.prokind IN ('f','p','w')
 UNION ALL SELECT pg_catalog.jsonb_build_array('aggregate',n.nspname,p.proname,pg_catalog.pg_get_function_identity_arguments(p.oid))::text FROM pg_catalog.pg_proc p JOIN ns n ON n.oid=p.pronamespace WHERE p.prokind='a'
 UNION ALL SELECT pg_catalog.jsonb_build_array('trigger',n.nspname,c.relname,g.tgname,pg_catalog.pg_get_triggerdef(g.oid),g.tgenabled)::text FROM rel c JOIN ns n ON n.oid=c.relnamespace JOIN pg_catalog.pg_trigger g ON g.tgrelid=c.oid AND NOT g.tgisinternal
 UNION ALL SELECT pg_catalog.jsonb_build_array('rule',n.nspname,c.relname,w.rulename,pg_catalog.pg_get_ruledef(w.oid))::text FROM rel c JOIN ns n ON n.oid=c.relnamespace JOIN pg_catalog.pg_rewrite w ON w.ev_class=c.oid
 UNION ALL SELECT pg_catalog.jsonb_build_array('policy',n.nspname,c.relname,p.polname,p.polpermissive,p.polcmd,ARRAY(SELECT CASE WHEN role_oid=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(role_oid)::text END FROM unnest(p.polroles) AS roles(role_oid) ORDER BY 1),pg_catalog.pg_get_expr(p.polqual,p.polrelid),pg_catalog.pg_get_expr(p.polwithcheck,p.polrelid))::text FROM rel c JOIN ns n ON n.oid=c.relnamespace JOIN pg_catalog.pg_policy p ON p.polrelid=c.oid
 UNION ALL SELECT pg_catalog.jsonb_build_array('type',n.nspname,t.typname,t.typtype,pg_catalog.pg_get_userbyid(t.typowner),t.typacl::text,t.typnotnull,CASE WHEN t.typbasetype<>0 THEN pg_catalog.format_type(t.typbasetype,t.typtypmod) END,pg_catalog.pg_get_expr(t.typdefaultbin,0))::text FROM pg_catalog.pg_type t JOIN ns n ON n.oid=t.typnamespace WHERE NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class lc WHERE lc.relnamespace=$1::regnamespace AND lc.relname='schema_extensions' AND (t.typrelid=lc.oid OR t.typelem=lc.reltype))
 UNION ALL SELECT pg_catalog.jsonb_build_array('domain_constraint',n.nspname,t.typname,k.conname,k.contype,pg_catalog.pg_get_constraintdef(k.oid),k.convalidated,k.conenforced)::text FROM pg_catalog.pg_type t JOIN ns n ON n.oid=t.typnamespace JOIN pg_catalog.pg_constraint k ON k.contypid=t.oid WHERE t.typtype='d'
 UNION ALL SELECT pg_catalog.jsonb_build_array('inheritance',n.nspname,c.relname,pn.nspname,p.relname,i.inhseqno)::text FROM rel c JOIN ns n ON n.oid=c.relnamespace JOIN pg_catalog.pg_inherits i ON i.inhrelid=c.oid JOIN pg_catalog.pg_class p ON p.oid=i.inhparent JOIN pg_catalog.pg_namespace pn ON pn.oid=p.relnamespace
 UNION ALL SELECT pg_catalog.jsonb_build_array('sequence',n.nspname,c.relname,s.seqtypid::regtype::text,s.seqstart,s.seqincrement,s.seqmax,s.seqmin,s.seqcache,s.seqcycle)::text FROM rel c JOIN ns n ON n.oid=c.relnamespace JOIN pg_catalog.pg_sequence s ON s.seqrelid=c.oid
 UNION ALL SELECT pg_catalog.jsonb_build_array('enum',n.nspname,t.typname,e.enumsortorder,e.enumlabel)::text FROM pg_catalog.pg_enum e JOIN pg_catalog.pg_type t ON t.oid=e.enumtypid JOIN ns n ON n.oid=t.typnamespace
 UNION ALL SELECT pg_catalog.jsonb_build_array('namespace_object',n.nspname,pg_catalog.pg_describe_object(d.classid,d.objid,d.objsubid))::text FROM pg_catalog.pg_depend d JOIN ns n ON d.refclassid='pg_catalog.pg_namespace'::regclass AND d.refobjid=n.oid
 WHERE NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class lc WHERE lc.relnamespace=$1::regnamespace AND lc.relname='schema_extensions' AND (
 (d.classid='pg_catalog.pg_class'::regclass AND (d.objid=lc.oid OR d.objid IN (SELECT indexrelid FROM pg_catalog.pg_index WHERE indrelid=lc.oid)))
 OR (d.classid='pg_catalog.pg_type'::regclass AND d.objid IN (SELECT oid FROM pg_catalog.pg_type WHERE typrelid=lc.oid OR typelem=lc.reltype))
 OR (d.classid='pg_catalog.pg_constraint'::regclass AND d.objid IN (SELECT oid FROM pg_catalog.pg_constraint WHERE conrelid=lc.oid))))
 UNION ALL SELECT pg_catalog.jsonb_build_array('extension',extname,extversion)::text FROM pg_catalog.pg_extension
 UNION ALL SELECT pg_catalog.jsonb_build_array('default_acl',n.nspname,pg_catalog.pg_get_userbyid(d.defaclrole),d.defaclobjtype,d.defaclacl::text)::text FROM pg_catalog.pg_default_acl d LEFT JOIN ns n ON n.oid=d.defaclnamespace
) SELECT value FROM objects ORDER BY value COLLATE "C"`

func catalogRecordsDigest(records []string) string {
	sorted := append([]string{}, records...)
	sort.Strings(sorted)
	raw, _ := json.Marshal(struct {
		Format  string
		Records []string
	}{ExtensionReadinessFormat, sorted})
	return digest(raw)
}
