package migrations

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (r *Runner) checkDeployment(ctx context.Context, tx pgx.Tx) error {
	if r.target.Profile != IdentityProfile {
		return nil
	}
	table := pgx.Identifier{r.target.Schema, "deployment_identity"}.Sanitize()
	var valid bool
	err := tx.QueryRow(ctx, `SELECT count(*)=1 AND bool_and(singleton AND deployment_id<>'00000000-0000-0000-0000-000000000000'::uuid AND created_at IS NOT NULL) FROM `+table).Scan(&valid)
	if err != nil || !valid {
		return errors.New("invalid deployment identity singleton")
	}
	var n int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_constraint WHERE conrelid=$1::regclass AND convalidated AND conenforced AND ((contype='p' AND conkey=ARRAY[1]::smallint[]) OR (contype='u' AND conkey=ARRAY[2]::smallint[]) OR (contype='c' AND pg_catalog.pg_get_constraintdef(oid)='CHECK (singleton)'))`, table).Scan(&n)
	if err != nil || n != 3 {
		return errors.New("invalid deployment identity constraints")
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_attribute WHERE attrelid=$1::regclass AND attnum>0 AND NOT attisdropped AND attnotnull AND ((attnum=1 AND attname='singleton' AND atttypid='pg_catalog.bool'::regtype) OR (attnum=2 AND attname='deployment_id' AND atttypid='pg_catalog.uuid'::regtype) OR (attnum=3 AND attname='created_at' AND atttypid='pg_catalog.timestamptz'::regtype))`, table).Scan(&n)
	if err != nil || n != 3 {
		return errors.New("invalid deployment identity columns")
	}
	for _, name := range []string{"deployment_identity_no_rewrite", "deployment_identity_no_truncate"} {
		var definition string
		err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_get_triggerdef(oid) FROM pg_catalog.pg_trigger WHERE tgrelid=$1::regclass AND tgname=$2 AND tgenabled='O' AND NOT tgisinternal`, table, name).Scan(&definition)
		expected := "CREATE TRIGGER " + name + " BEFORE "
		if name == "deployment_identity_no_rewrite" {
			expected += "DELETE OR UPDATE ON " + r.target.Schema + ".deployment_identity FOR EACH ROW EXECUTE FUNCTION deployment_identity_immutable()"
		} else {
			expected += "TRUNCATE ON " + r.target.Schema + ".deployment_identity FOR EACH STATEMENT EXECUTE FUNCTION deployment_identity_immutable()"
		}
		if err != nil || definition != expected {
			return errors.New("invalid deployment identity trigger")
		}
	}
	var source string
	err = tx.QueryRow(ctx, `SELECT prosrc FROM pg_catalog.pg_proc WHERE pronamespace=$1::regnamespace AND proname='deployment_identity_immutable' AND pronargs=0`, r.target.Schema).Scan(&source)
	want := "BEGIN\n    RAISE EXCEPTION 'Deployment identity is immutable; independent clones require an explicit offline provisioning procedure' USING ERRCODE = '23514';\nEND;"
	if err != nil || strings.TrimSpace(source) != want {
		return errors.New("invalid deployment identity protection function")
	}
	return nil
}
