package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type securityCatalogObject struct {
	OID         uint32                 `json:"oid"`
	SubID       int32                  `json:"sub_id"`
	Owner       string                 `json:"owner"`
	ACLNull     bool                   `json:"acl_null"`
	ACL         string                 `json:"acl"`
	Definition  json.RawMessage        `json:"definition"`
	SD          bool                   `json:"sd"`
	Config      []string               `json:"config"`
	Flags       TransitionRoleFlags    `json:"flags"`
	Memberships []TransitionMembership `json:"memberships"`
	Kind        string                 `json:"kind"`
}

func collectSecurityObject(ctx context.Context, q queryer, r TransitionRequirement) (TransitionMetadata, TransitionObjectIdentity, bool, error) {
	var sql string
	var args []any
	switch r.Kind {
	case "role":
		sql = securityRoleSQL
		args = []any{strings.TrimPrefix(r.Key, "role:")}
	case "schema":
		sql = securitySchemaSQL
		args = []any{r.Schema}
	case "table", "view", "sequence":
		sql = securityRelationSQL
		args = []any{r.Schema, strings.TrimPrefix(r.Key, r.Schema+".")}
	case "column":
		sql = securityColumnSQL
		args = []any{r.Schema, strings.TrimPrefix(r.Parent, r.Schema+"."), strings.TrimPrefix(r.Key, r.Parent+".")}
	case "function":
		sql = securityFunctionSQL
		args = []any{r.Schema, strings.TrimPrefix(r.Key, r.Schema+".")}
	case "default_acl":
		parts := strings.Split(r.Key, ":")
		class := map[string]string{"tables": "r", "functions": "f", "sequences": "S"}[parts[3]]
		sql = securityDefaultACLSQL
		args = []any{parts[2], r.Schema, class}
	default:
		return TransitionMetadata{}, TransitionObjectIdentity{}, false, errors.New("unsupported catalog class")
	}
	var raw string
	err := q.QueryRow(ctx, sql, args...).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return TransitionMetadata{}, TransitionObjectIdentity{}, false, nil
	}
	if err != nil {
		return TransitionMetadata{}, TransitionObjectIdentity{}, false, errors.New("required catalog record unreadable")
	}
	var o securityCatalogObject
	if err = json.Unmarshal([]byte(raw), &o); err != nil || o.Definition == nil || o.Kind != r.Kind {
		return TransitionMetadata{}, TransitionObjectIdentity{}, false, errors.New("catalog object kind/definition invalid")
	}
	m := TransitionMetadata{SessionDefaults: []TransitionSessionLayer{}, Key: r.Key, Kind: r.Kind, Owner: o.Owner, ACL: TransitionACL{Mode: "explicit", Grants: []TransitionGrant{}}, DefinitionSHA: "", SecurityDefiner: o.SD, SearchPath: []string{}, Protected: r.Kind == "schema", RoleFlags: o.Flags, Memberships: []TransitionMembership{}}
	resolved := []TransitionGrant{}
	if r.Kind != "role" {
		rows, e := q.Query(ctx, securityACLGrantsSQL, o.ACL)
		if e != nil {
			return m, TransitionObjectIdentity{}, false, e
		}
		for rows.Next() {
			var g TransitionGrant
			if e = rows.Scan(&g.Grantor, &g.Grantee, &g.Privilege, &g.GrantOption); e != nil {
				rows.Close()
				return m, TransitionObjectIdentity{}, false, e
			}
			resolved = append(resolved, g)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return m, TransitionObjectIdentity{}, false, e
		}
		sortCollectedGrants(resolved)
		if o.ACLNull {
			m.ACL.Mode = "default"
		} else {
			m.ACL.Grants = resolved
		}
	}
	if r.Kind == "role" {
		m.Memberships = o.Memberships
		sort.Slice(m.Memberships, func(i, j int) bool {
			a, b := m.Memberships[i], m.Memberships[j]
			return a.Role+"/"+a.Grantor < b.Role+"/"+b.Grantor
		})
		if m.Memberships == nil {
			return m, TransitionObjectIdentity{}, false, errors.New("membership inventory unavailable")
		}
	}
	if r.Kind == "function" {
		m.SearchPath, err = securityFunctionPath(o.Config)
		if err != nil {
			return m, TransitionObjectIdentity{}, false, err
		}
	}
	// Include resolved NULL ACL semantics, not only raw ACL, in the definition.
	m.DefinitionSHA = transitionDigest([]any{o.Definition, resolved})
	return m, TransitionObjectIdentity{Key: r.Key, OID: o.OID, SubID: o.SubID}, true, nil
}

func securityFunctionPath(config []string) ([]string, error) {
	path := []string{}
	seen := false
	for _, setting := range config {
		if strings.HasPrefix(setting, "search_path=") {
			if seen {
				return nil, errors.New("duplicate function search_path")
			}
			seen = true
			for _, part := range strings.Split(strings.TrimPrefix(setting, "search_path="), ",") {
				part = strings.TrimSpace(part)
				if !schemaName.MatchString(part) {
					return nil, errors.New("unsupported function search_path syntax")
				}
				path = append(path, part)
			}
		}
	}
	return path, nil
}

// Class and identifiers are always selected from fixed queries. No descriptor
// payload/definition text is evaluated. ORDER BY is explicit for every inventory.
const securityACLGrantsSQL = `SELECT pg_catalog.pg_get_userbyid(grantor),CASE WHEN grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(grantee) END,privilege_type,is_grantable FROM pg_catalog.aclexplode(CASE WHEN cardinality($1::aclitem[])>0 THEN $1::aclitem[] ELSE NULL::aclitem[] END) ORDER BY grantor,grantee,privilege_type`

const securityRoleSQL = `SELECT pg_catalog.jsonb_build_object('kind','role','oid',r.oid::bigint,'owner','','definition',to_jsonb(r)-'oid'-'rolpassword','flags',jsonb_build_object('login',rolcanlogin,'superuser',rolsuper,'create_db',rolcreatedb,'create_role',rolcreaterole,'replication',rolreplication,'bypass_rls',rolbypassrls,'inherit',rolinherit),'memberships',COALESCE((SELECT jsonb_agg(jsonb_build_object('role',pg_catalog.pg_get_userbyid(a.roleid),'grantor',pg_catalog.pg_get_userbyid(a.grantor),'admin',a.admin_option,'inherit',a.inherit_option,'set',a.set_option) ORDER BY pg_catalog.pg_get_userbyid(a.roleid) COLLATE "C",pg_catalog.pg_get_userbyid(a.grantor) COLLATE "C") FROM pg_catalog.pg_auth_members a WHERE a.member=r.oid),'[]'::jsonb))::text FROM pg_catalog.pg_roles r WHERE rolname=$1`

const securitySchemaSQL = `SELECT jsonb_build_object('kind','schema','oid',oid::bigint,'owner',pg_catalog.pg_get_userbyid(nspowner),'acl_null',nspacl IS NULL,'acl',COALESCE(nspacl,pg_catalog.acldefault('n',nspowner))::text,'definition',jsonb_build_array('schema',nspname))::text FROM pg_catalog.pg_namespace WHERE nspname=$1`

const securityRelationSQL = `SELECT jsonb_build_object('kind',CASE c.relkind WHEN 'r' THEN 'table' WHEN 'v' THEN 'view' WHEN 'S' THEN 'sequence' ELSE 'unsupported' END,'oid',c.oid::bigint,'owner',pg_catalog.pg_get_userbyid(c.relowner),'acl_null',c.relacl IS NULL,'acl',COALESCE(c.relacl,pg_catalog.acldefault(CASE WHEN c.relkind='S' THEN 'S' ELSE 'r' END::"char",c.relowner))::text,'definition',jsonb_build_object(
 'persistence',c.relpersistence,'rls',c.relrowsecurity,'force_rls',c.relforcerowsecurity,'options',c.reloptions,'replica_identity',c.relreplident,
 'view',CASE WHEN c.relkind='v' THEN pg_catalog.pg_get_viewdef(c.oid,true) END,
 'columns',(SELECT COALESCE(jsonb_agg(jsonb_build_array(a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attidentity,a.attgenerated,pg_catalog.pg_get_expr(d.adbin,d.adrelid)) ORDER BY a.attnum),'[]'::jsonb) FROM pg_catalog.pg_attribute a LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped),
 'constraints',(SELECT COALESCE(jsonb_agg(jsonb_build_array(k.conname,pg_catalog.pg_get_constraintdef(k.oid),k.convalidated,k.conenforced) ORDER BY k.conname COLLATE "C"),'[]'::jsonb) FROM pg_catalog.pg_constraint k WHERE k.conrelid=c.oid),
 'indexes',(SELECT COALESCE(jsonb_agg(jsonb_build_array(pg_catalog.pg_get_indexdef(i.indexrelid),i.indisvalid,i.indisready) ORDER BY pg_catalog.pg_get_indexdef(i.indexrelid) COLLATE "C"),'[]'::jsonb) FROM pg_catalog.pg_index i WHERE i.indrelid=c.oid),
 'triggers',(SELECT COALESCE(jsonb_agg(jsonb_build_array(t.tgname,pg_catalog.pg_get_triggerdef(t.oid),t.tgenabled) ORDER BY t.tgname COLLATE "C"),'[]'::jsonb) FROM pg_catalog.pg_trigger t WHERE t.tgrelid=c.oid),
 'rules',(SELECT COALESCE(jsonb_agg(pg_catalog.pg_get_ruledef(w.oid) ORDER BY w.rulename COLLATE "C"),'[]'::jsonb) FROM pg_catalog.pg_rewrite w WHERE w.ev_class=c.oid),
 'policies',(SELECT COALESCE(jsonb_agg(jsonb_build_array(p.polname,p.polpermissive,p.polcmd,ARRAY(SELECT CASE WHEN x=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(x) END FROM unnest(p.polroles) x ORDER BY 1),pg_catalog.pg_get_expr(p.polqual,p.polrelid),pg_catalog.pg_get_expr(p.polwithcheck,p.polrelid)) ORDER BY p.polname COLLATE "C"),'[]'::jsonb) FROM pg_catalog.pg_policy p WHERE p.polrelid=c.oid),
 'sequence',(SELECT jsonb_build_array(s.seqtypid::regtype::text,s.seqstart,s.seqincrement,s.seqmax,s.seqmin,s.seqcache,s.seqcycle) FROM pg_catalog.pg_sequence s WHERE s.seqrelid=c.oid),
 'dependencies',(SELECT COALESCE(jsonb_agg(pg_catalog.pg_describe_object(d.refclassid,d.refobjid,d.refobjsubid) ORDER BY pg_catalog.pg_describe_object(d.refclassid,d.refobjid,d.refobjsubid) COLLATE "C"),'[]'::jsonb) FROM pg_catalog.pg_depend d WHERE d.classid='pg_catalog.pg_class'::regclass AND d.objid=c.oid)
 ))::text FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`

const securityColumnSQL = `SELECT jsonb_build_object('kind','column','oid',c.oid::bigint,'sub_id',a.attnum,'owner',pg_catalog.pg_get_userbyid(c.relowner),'acl_null',a.attacl IS NULL,'acl',COALESCE(a.attacl,'{}'::aclitem[])::text,'definition',jsonb_build_array(a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attidentity,a.attgenerated,pg_catalog.pg_get_expr(d.adbin,d.adrelid)))::text FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE n.nspname=$1 AND c.relname=$2 AND a.attname=$3 AND a.attnum>0 AND NOT a.attisdropped`

const securityFunctionSQL = `SELECT jsonb_build_object('kind','function','oid',p.oid::bigint,'owner',pg_catalog.pg_get_userbyid(p.proowner),'acl_null',p.proacl IS NULL,'acl',COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner))::text,'sd',p.prosecdef,'config',COALESCE(to_jsonb(p.proconfig),'[]'::jsonb),'definition',jsonb_build_array(pg_catalog.pg_get_functiondef(p.oid),(SELECT COALESCE(jsonb_agg(pg_catalog.pg_describe_object(d.refclassid,d.refobjid,d.refobjsubid) ORDER BY pg_catalog.pg_describe_object(d.refclassid,d.refobjid,d.refobjsubid) COLLATE "C"),'[]'::jsonb) FROM pg_catalog.pg_depend d WHERE d.classid='pg_catalog.pg_proc'::regclass AND d.objid=p.oid)))::text FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.prokind IN ('f','p') AND p.proname||'('||pg_catalog.pg_get_function_identity_arguments(p.oid)||')'=$2`

const securityDefaultACLSQL = `SELECT jsonb_build_object('kind','default_acl','owner',r.rolname,'acl_null',d.oid IS NULL,'acl',COALESCE(d.defaclacl,CASE WHEN $2='' THEN pg_catalog.acldefault($3::"char",r.oid) ELSE '{}'::aclitem[] END)::text,'definition',jsonb_build_array($2::text,$3::text,d.oid IS NOT NULL))::text FROM pg_catalog.pg_roles r LEFT JOIN pg_catalog.pg_namespace n ON n.nspname=$2 LEFT JOIN pg_catalog.pg_default_acl d ON d.defaclrole=r.oid AND d.defaclnamespace=CASE WHEN $2='' THEN 0 ELSE n.oid END AND d.defaclobjtype=$3::"char" WHERE r.rolname=$1 AND ($2='' OR n.oid IS NOT NULL)`

const securityCoverageSQL = `WITH ns AS (SELECT oid,nspname FROM pg_catalog.pg_namespace WHERE nspname=ANY($1::text[]))
 SELECT n.nspname||'.'||c.relname,CASE c.relkind WHEN 'r' THEN 'table' WHEN 'v' THEN 'view' WHEN 'S' THEN 'sequence' ELSE 'unsupported' END FROM pg_catalog.pg_class c JOIN ns n ON n.oid=c.relnamespace WHERE c.relkind NOT IN ('i','I','t')
 UNION ALL SELECT n.nspname||'.'||c.relname||'.'||a.attname,'column' FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN ns n ON n.oid=c.relnamespace WHERE c.relkind IN ('r','v') AND a.attnum>0 AND NOT a.attisdropped
 UNION ALL SELECT n.nspname||'.'||p.proname||'('||pg_catalog.pg_get_function_identity_arguments(p.oid)||')',CASE WHEN p.prokind IN ('f','p') THEN 'function' ELSE 'unsupported' END FROM pg_catalog.pg_proc p JOIN ns n ON n.oid=p.pronamespace`

// Kept separate for offline tests of deterministic PostgreSQL ACL interpretation.
func sortCollectedGrants(grants []TransitionGrant) {
	sort.Slice(grants, func(i, j int) bool {
		a, b := grants[i], grants[j]
		return a.Grantor+"/"+a.Grantee+"/"+a.Privilege < b.Grantor+"/"+b.Grantee+"/"+b.Privilege
	})
}

const securityRolePathsSQL = `SELECT a,b,pg_catalog.pg_has_role(a,b,'MEMBER'),pg_catalog.pg_has_role(a,b,'USAGE'),pg_catalog.pg_has_role(a,b,'SET') FROM unnest($1::text[]) a CROSS JOIN unnest($1::text[]) b ORDER BY a COLLATE "C",b COLLATE "C"`

const securityEffectiveSQL = `WITH requests AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS r(actor text,key text,oid oid,sub_id integer,kind text,privilege text))
SELECT r.actor,r.key,r.privilege,
 CASE r.kind
 WHEN 'database' THEN pg_catalog.has_database_privilege(r.actor,r.oid,r.privilege)
 WHEN 'schema' THEN pg_catalog.has_schema_privilege(r.actor,r.oid,r.privilege)
 WHEN 'table' THEN pg_catalog.has_table_privilege(r.actor,r.oid,r.privilege)
 WHEN 'view' THEN pg_catalog.has_table_privilege(r.actor,r.oid,r.privilege)
 WHEN 'column' THEN pg_catalog.has_column_privilege(r.actor,r.oid,r.sub_id::smallint,r.privilege)
 WHEN 'sequence' THEN pg_catalog.has_sequence_privilege(r.actor,r.oid,r.privilege)
 WHEN 'function' THEN pg_catalog.has_function_privilege(r.actor,r.oid,r.privilege) END,
 CASE r.kind
 WHEN 'database' THEN pg_catalog.has_database_privilege(r.actor,r.oid,r.privilege||' WITH GRANT OPTION')
 WHEN 'schema' THEN pg_catalog.has_schema_privilege(r.actor,r.oid,r.privilege||' WITH GRANT OPTION')
 WHEN 'table' THEN pg_catalog.has_table_privilege(r.actor,r.oid,r.privilege||' WITH GRANT OPTION')
 WHEN 'view' THEN pg_catalog.has_table_privilege(r.actor,r.oid,r.privilege||' WITH GRANT OPTION')
 WHEN 'column' THEN pg_catalog.has_column_privilege(r.actor,r.oid,r.sub_id::smallint,r.privilege||' WITH GRANT OPTION')
 WHEN 'sequence' THEN pg_catalog.has_sequence_privilege(r.actor,r.oid,r.privilege||' WITH GRANT OPTION')
 WHEN 'function' THEN pg_catalog.has_function_privilege(r.actor,r.oid,r.privilege||' WITH GRANT OPTION') END
FROM requests r ORDER BY r.actor COLLATE "C",r.key COLLATE "C",r.privilege COLLATE "C"`
