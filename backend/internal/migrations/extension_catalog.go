package migrations

import (
	"context"
	"fmt"
)

// pg18-catalog-v2 intentionally supports a bounded set of schema object classes.
// Reject unsupported classes even if an operator computed their current hash.
// This is object admission, not a verifier of arbitrary SQL function semantics.
func checkExtensionCatalogSupport(ctx context.Context, q queryer) error {
	var supported bool
	err := q.QueryRow(ctx, `WITH ns AS (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname !~ '^pg_' AND nspname<>'information_schema')
 SELECT NOT EXISTS(
 SELECT 1 FROM pg_catalog.pg_class c JOIN ns ON ns.oid=c.relnamespace WHERE c.relkind NOT IN ('r','i','S','v','c')
 UNION ALL SELECT 1 FROM pg_catalog.pg_proc p JOIN ns ON ns.oid=p.pronamespace WHERE p.prokind='a'
 UNION ALL SELECT 1 FROM pg_catalog.pg_type t JOIN ns ON ns.oid=t.typnamespace WHERE t.typtype NOT IN ('b','c','d','e') OR (t.typtype='b' AND t.typelem=0)
 UNION ALL SELECT 1 FROM pg_catalog.pg_extension WHERE extname<>'plpgsql'
 UNION ALL SELECT 1 FROM pg_catalog.pg_depend d JOIN ns ON d.refclassid='pg_catalog.pg_namespace'::regclass AND d.refobjid=ns.oid WHERE d.classid NOT IN ('pg_catalog.pg_class'::regclass,'pg_catalog.pg_proc'::regclass,'pg_catalog.pg_type'::regclass,'pg_catalog.pg_constraint'::regclass,'pg_catalog.pg_default_acl'::regclass)
 UNION ALL SELECT 1 FROM pg_catalog.pg_cast c WHERE c.castsource IN (SELECT t.oid FROM pg_catalog.pg_type t JOIN ns ON ns.oid=t.typnamespace) OR c.casttarget IN (SELECT t.oid FROM pg_catalog.pg_type t JOIN ns ON ns.oid=t.typnamespace) OR c.castfunc IN (SELECT p.oid FROM pg_catalog.pg_proc p JOIN ns ON ns.oid=p.pronamespace)
 )`).Scan(&supported)
	if err != nil || !supported {
		return fmt.Errorf("%w: unsupported object class for %s", ErrUnsafe, ExtensionReadinessFormat)
	}
	return nil
}
