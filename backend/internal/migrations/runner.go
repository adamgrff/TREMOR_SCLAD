package migrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const ledgerMarker = "TREMOR migration ledger v1; clean-v1; utf8-lf-v1"
const advisoryKey int64 = 0x5452454d4f524d47

var ErrUnsafe = errors.New("unsafe migration state")
var ErrIncomplete = errors.New("schema is not fully migrated")
var ErrCommitUnknown = errors.New("commit outcome unresolved; inspect status before resuming")
var ErrResume = errors.New("commit confirmed by ledger; rerun apply to continue remaining migrations")

type Applied struct {
	Entry
	Profile   string    `json:"profile"`
	AppliedAt time.Time `json:"applied_at"`
}
type State struct {
	Profile        string    `json:"profile"`
	Initialized    bool      `json:"initialized"`
	Applied        []Applied `json:"applied"`
	Pending        []Entry   `json:"pending"`
	CommitResolved bool      `json:"commit_resolved,omitempty"`
}

// Fault hooks are package-private and only set by integration tests.
type faultHooks struct {
	afterSQL    func(int, pgx.Tx) error
	afterLedger func(int, pgx.Tx) error
	commit      func(context.Context, int, pgx.Tx) error
	locked      func()
}
type Runner struct {
	target   Target
	manifest *Manifest
	hooks    faultHooks
}

func New(target Target, manifest *Manifest) (*Runner, error) {
	if manifest == nil {
		return nil, errors.New("validated manifest required")
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if target.Profile != manifest.plan.Profile {
		return nil, errors.New("target and manifest profiles must match")
	}
	return &Runner{target: target, manifest: manifest}, nil
}

func (r *Runner) ledger() string {
	return pgx.Identifier{r.target.Schema, "schema_migrations"}.Sanitize()
}

func (r *Runner) marker() string {
	if r.target.Profile == IdentityProfile {
		return "TREMOR migration ledger v1; clean-v2; utf8-lf-v1"
	}
	return ledgerMarker
}

func (r *Runner) Run(ctx context.Context, mode string) (State, error) {
	var state State
	if mode != "status" && mode != "verify" && mode != "apply" {
		return state, errors.New("unknown migration mode")
	}
	conn, id, err := r.target.connect(ctx)
	if err != nil {
		return state, err
	}
	defer closeConnection(conn)
	if mode == "apply" {
		// One session lock covers inspection, initialization and every migration.
		// Connection close releases it even when COMMIT/transport is uncertain.
		if _, err = conn.Exec(ctx, `SELECT pg_catalog.pg_advisory_lock($1)`, advisoryKey); err != nil {
			return state, errors.New("cannot acquire deployment session lock")
		}
		if r.hooks.locked != nil {
			r.hooks.locked()
		}
	}
	state, err = r.readState(ctx, conn)
	if err != nil {
		return state, err
	}
	if mode == "status" {
		return state, nil
	}
	if mode == "verify" {
		if len(state.Pending) > 0 {
			return state, ErrIncomplete
		}
		return state, nil
	}
	if len(state.Pending) == 0 {
		return state, nil
	}
	// clean-v1 supports fresh/resumed schema construction, not adoption or data
	// upgrades. Refuse business rows before executing any remaining legacy DML.
	if err = r.requireNoBusinessRows(ctx, conn); err != nil {
		return state, err
	}
	if !state.Initialized {
		tx, beginErr := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
		if beginErr != nil {
			return state, errors.New("cannot start ledger initialization")
		}
		_, err = tx.Exec(ctx, `CREATE TABLE `+r.ledger()+`(
 version INTEGER CONSTRAINT schema_migrations_pkey PRIMARY KEY CONSTRAINT ledger_version_positive CHECK(version>0),
 name TEXT NOT NULL CONSTRAINT ledger_name_unique UNIQUE,
 checksum TEXT NOT NULL CONSTRAINT ledger_checksum_hex CHECK(checksum ~ '^[0-9a-f]{64}$'),
 profile TEXT NOT NULL CONSTRAINT ledger_profile_clean CHECK(profile='`+r.target.Profile+`'),
 ordinal INTEGER NOT NULL CONSTRAINT ledger_order_unique UNIQUE CONSTRAINT ledger_order_positive CHECK(ordinal>0),
 applied_at TIMESTAMPTZ NOT NULL DEFAULT pg_catalog.clock_timestamp()
); COMMENT ON TABLE `+r.ledger()+` IS '`+r.marker()+`'`)
		if err != nil {
			rollback(tx)
			return state, errors.New("cannot initialize owned ledger")
		}
		if err = tx.Commit(ctx); err != nil {
			return r.resolveCommit(conn, id, 0)
		}
		state.Initialized = true
	}
	for i := len(state.Applied); i < len(r.manifest.migrations); i++ {
		migration := r.manifest.migrations[i]
		uncertain, applyErr := r.apply(ctx, conn, migration)
		if uncertain {
			return r.resolveCommit(conn, id, migration.Order)
		}
		if applyErr != nil {
			return state, applyErr
		}
		state, err = r.readState(ctx, conn)
		if err != nil {
			return state, err
		}
		if len(state.Applied) != i+1 {
			return state, fmt.Errorf("%w: committed ledger did not advance exactly once", ErrUnsafe)
		}
	}
	return state, nil
}

func (r *Runner) apply(ctx context.Context, conn *pgx.Conn, m migration) (bool, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	if err != nil {
		return false, errors.New("cannot start migration transaction")
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{r.target.Schema}.Sanitize()+",pg_catalog"); err != nil {
		return false, errors.New("cannot select migration transaction schema")
	}
	// This option sends the intact validated multi-statement body through the
	// simple protocol. It is not split or reserialized, including PL/pgSQL bodies.
	_, err = tx.Exec(ctx, m.body, pgx.QueryExecModeSimpleProtocol)
	if err == nil && r.hooks.afterSQL != nil {
		err = r.hooks.afterSQL(m.Version, tx)
	}
	if err == nil {
		var inserted pgconn.CommandTag
		inserted, err = tx.Exec(ctx, `INSERT INTO `+r.ledger()+`(version,name,checksum,profile,ordinal) VALUES($1,$2,$3,$4,$5)`, m.Version, m.Name, m.SHA256, r.target.Profile, m.Order)
		if err == nil && inserted.RowsAffected() != 1 {
			err = errors.New("ledger insertion was suppressed")
		}
	}
	if err == nil && r.hooks.afterLedger != nil {
		err = r.hooks.afterLedger(m.Version, tx)
	}
	if err == nil {
		var present bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+r.ledger()+` WHERE version=$1 AND name=$2 AND checksum=$3 AND profile=$4 AND ordinal=$5)`, m.Version, m.Name, m.SHA256, r.target.Profile, m.Order).Scan(&present)
		if err == nil && !present {
			err = errors.New("ledger row missing before commit")
		}
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return false, fmt.Errorf("migration %03d rolled back (SQLSTATE %s)", m.Version, pgErr.Code)
		}
		return false, fmt.Errorf("migration %03d rolled back", m.Version)
	}
	if r.hooks.commit != nil {
		err = r.hooks.commit(ctx, m.Version, tx)
	} else {
		err = tx.Commit(ctx)
	}
	return err != nil, nil
}

// Never automatically execute SQL again after an uncertain COMMIT. A new
// read-only connection can prove presence, but absence may still race the old
// backend's COMMIT. In that case report unresolved, not "rolled back". A later
// apply takes the session lock and reads the durable prefix before any writes.
func (r *Runner) resolveCommit(old *pgx.Conn, id identity, order int) (State, error) {
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	_ = old.Close(closeCtx)
	cancelClose()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, fresh, err := r.target.connect(ctx)
	if err != nil {
		return State{}, ErrCommitUnknown
	}
	defer closeConnection(conn)
	if fresh.DatabaseOID != id.DatabaseOID {
		return State{}, ErrCommitUnknown
	}
	state, err := r.readState(ctx, conn)
	if err != nil || !state.Initialized || len(state.Applied) < order {
		return state, ErrCommitUnknown
	}
	state.CommitResolved = true
	if len(state.Pending) > 0 {
		return state, ErrResume
	}
	return state, nil
}

// Both connections and transactions can inspect the same owned ledger.
type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *Runner) readState(ctx context.Context, conn queryer) (State, error) {
	state := State{Profile: r.target.Profile, Applied: []Applied{}, Pending: append([]Entry(nil), r.manifest.plan.Migrations...)}
	var oid int64
	var kind, marker string
	var rls, force bool
	err := conn.QueryRow(ctx, `SELECT c.oid::bigint,c.relkind::text,COALESCE(pg_catalog.obj_description(c.oid,'pg_class'),''),c.relrowsecurity,c.relforcerowsecurity FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='schema_migrations'`, r.target.Schema).Scan(&oid, &kind, &marker, &rls, &force)
	if errors.Is(err, pgx.ErrNoRows) {
		var occupied bool
		err = conn.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM pg_catalog.pg_depend d JOIN pg_catalog.pg_namespace n ON d.refclassid='pg_catalog.pg_namespace'::regclass AND d.refobjid=n.oid WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema'
 UNION ALL SELECT 1 FROM pg_catalog.pg_extension WHERE extname<>'plpgsql')`).Scan(&occupied)
		if err != nil || occupied {
			return state, fmt.Errorf("%w: nonempty database without an owned ledger", ErrUnsafe)
		}
		return state, nil
	}
	if err != nil {
		return state, errors.New("cannot inspect ledger identity")
	}
	if kind != "r" || marker != r.marker() || rls || force {
		return state, fmt.Errorf("%w: unrecognized ledger", ErrUnsafe)
	}
	if err = r.checkLedgerShape(ctx, conn, oid); err != nil {
		return state, err
	}
	rows, err := conn.Query(ctx, `SELECT version,name,checksum,profile,ordinal,applied_at FROM `+r.ledger()+` ORDER BY ordinal`)
	if err != nil {
		return state, errors.New("cannot read ledger")
	}
	defer rows.Close()
	for rows.Next() {
		var applied Applied
		if rows.Scan(&applied.Version, &applied.Name, &applied.SHA256, &applied.Profile, &applied.Order, &applied.AppliedAt) != nil {
			return state, errors.New("cannot decode ledger")
		}
		i := len(state.Applied)
		if i >= len(r.manifest.migrations) || applied.Entry != r.manifest.migrations[i].Entry || applied.Profile != r.target.Profile {
			return state, fmt.Errorf("%w: ledger version/name/checksum/profile/order conflict", ErrUnsafe)
		}
		state.Applied = append(state.Applied, applied)
	}
	if rows.Err() != nil {
		return state, errors.New("cannot finish ledger read")
	}
	state.Initialized = true
	state.Pending = state.Pending[len(state.Applied):]
	return state, nil
}

func (r *Runner) checkLedgerShape(ctx context.Context, conn queryer, oid int64) error {
	rows, err := conn.Query(ctx, `SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attgenerated::text,a.attidentity::text FROM pg_catalog.pg_attribute a WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return errors.New("cannot inspect ledger columns")
	}
	want := [][2]string{{"version", "integer"}, {"name", "text"}, {"checksum", "text"}, {"profile", "text"}, {"ordinal", "integer"}, {"applied_at", "timestamp with time zone"}}
	i := 0
	for rows.Next() {
		var name, typ, generated, identity string
		var notNull bool
		if rows.Scan(&name, &typ, &notNull, &generated, &identity) != nil || i >= len(want) || name != want[i][0] || typ != want[i][1] || !notNull || generated != "" || identity != "" {
			rows.Close()
			return fmt.Errorf("%w: ledger column shape", ErrUnsafe)
		}
		i++
	}
	err = rows.Err()
	rows.Close()
	if err != nil || i != len(want) {
		return fmt.Errorf("%w: incomplete ledger columns", ErrUnsafe)
	}
	var valid bool
	err = conn.QueryRow(ctx, `SELECT
	 count(*) FILTER(WHERE contype<>'n')=7 AND count(*) FILTER(WHERE contype='p' AND conkey=ARRAY[1]::smallint[])=1
 AND count(*) FILTER(WHERE contype='u' AND conkey=ARRAY[2]::smallint[])=1
 AND count(*) FILTER(WHERE contype='u' AND conkey=ARRAY[5]::smallint[])=1
 AND count(*) FILTER(WHERE contype='c' AND conname IN ('ledger_version_positive','ledger_checksum_hex','ledger_profile_clean','ledger_order_positive'))=4
 FROM pg_catalog.pg_constraint WHERE conrelid=$1`, oid).Scan(&valid)
	if err != nil || !valid {
		return fmt.Errorf("%w: ledger constraint shape", ErrUnsafe)
	}
	err = conn.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_catalog.pg_trigger WHERE tgrelid=$1 AND NOT tgisinternal) AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_rewrite WHERE ev_class=$1)`, oid).Scan(&valid)
	if err != nil || !valid {
		return fmt.Errorf("%w: custom ledger triggers or rules", ErrUnsafe)
	}
	return nil
}

func (r *Runner) requireNoBusinessRows(ctx context.Context, conn *pgx.Conn) error {
	rows, err := conn.Query(ctx, `SELECT n.nspname,c.relname,c.relkind::text,c.relrowsecurity FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema' AND c.relkind IN ('r','p','f','m') ORDER BY n.nspname,c.relname`)
	if err != nil {
		return errors.New("cannot prove data-free migration target")
	}
	var names [][2]string
	for rows.Next() {
		var schema, name, kind string
		var rls bool
		if rows.Scan(&schema, &name, &kind, &rls) != nil {
			rows.Close()
			return errors.New("cannot inspect existing data")
		}
		if schema == r.target.Schema && name == "schema_migrations" {
			continue
		}
		if schema != r.target.Schema || kind != "r" || rls {
			rows.Close()
			return fmt.Errorf("%w: unexpected table outside clean target", ErrUnsafe)
		}
		names = append(names, [2]string{schema, name})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return errors.New("cannot finish data inspection")
	}
	for _, name := range names {
		var occupied bool
		if conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+pgx.Identifier{name[0], name[1]}.Sanitize()+` LIMIT 1)`).Scan(&occupied) != nil || occupied {
			return fmt.Errorf("%w: clean construction cannot continue over business data", ErrUnsafe)
		}
	}
	return nil
}
