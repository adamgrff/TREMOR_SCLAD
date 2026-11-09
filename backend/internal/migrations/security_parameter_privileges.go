package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
)

// Parameter ACL is cluster-wide. Names/ACL/effective rights are approved logical
// metadata; physical catalog-row OIDs belong only to target-bound snapshots.
// IntrinsicUserSet does NOT mean has_parameter_privilege(SET): ordinary USERSET
// changes need no delegated parameter grant at all.
type TransitionParameterPrivilege struct {
	Name                   string        `json:"name"`
	Context                string        `json:"context"`
	Owner                  string        `json:"owner"`
	Present                bool          `json:"present"`
	ACL                    TransitionACL `json:"acl"`
	IntrinsicUserSet       bool          `json:"intrinsic_user_set"`
	Set                    bool          `json:"set"`
	AlterSystem            bool          `json:"alter_system"`
	SetGrantOption         bool          `json:"set_grant_option"`
	AlterSystemGrantOption bool          `json:"alter_system_grant_option"`
}
type TransitionParameterIdentity struct {
	Name string `json:"name"`
	OID  uint32 `json:"oid"`
}

func reviewedParameterContext(name string) string {
	switch name {
	case "session_replication_role":
		return "superuser"
	case "row_security", "search_path", "statement_timeout", "lock_timeout":
		return "user"
	}
	return ""
}
func defaultParameterPrivileges(owner string, super bool) []TransitionParameterPrivilege {
	out := []TransitionParameterPrivilege{}
	for _, name := range []string{"row_security", "search_path", "session_replication_role"} {
		ctx := reviewedParameterContext(name)
		out = append(out, TransitionParameterPrivilege{name, ctx, owner, false, TransitionACL{"default", []TransitionGrant{}}, ctx == "user", super, super, super, super})
	}
	return out
}
func defaultParameterIdentities() []TransitionParameterIdentity {
	return []TransitionParameterIdentity{{"row_security", 0}, {"search_path", 0}, {"session_replication_role", 0}}
}

func validateParameterPrivileges(m TransitionMetadata, inventory []TransitionMetadata) error {
	if m.Kind != "role" {
		if len(m.ParameterPrivileges) != 0 {
			return errors.New("parameter privileges require role metadata")
		}
		return nil
	}
	if m.ParameterPrivileges == nil {
		return errors.New("parameter privileges missing")
	}
	roles := map[string]TransitionMetadata{}
	for _, o := range inventory {
		if o.Kind == "role" {
			roles[strings.TrimPrefix(o.Key, "role:")] = o
		}
	}
	inherited := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if inherited[name] {
			return
		}
		inherited[name] = true
		for _, e := range roles[name].Memberships {
			if e.Inherit {
				visit(e.Role)
			}
		}
	}
	visit(strings.TrimPrefix(m.Key, "role:"))
	previous := ""
	required := map[string]bool{"row_security": false, "search_path": false, "session_replication_role": false}
	for _, p := range m.ParameterPrivileges {
		ctx := reviewedParameterContext(p.Name)
		if p.Name <= previous || ctx == "" || p.Context != ctx || p.IntrinsicUserSet != (ctx == "user") || p.ACL.Grants == nil {
			return errors.New("invalid or unsupported parameter privilege scope")
		}
		previous = p.Name
		if _, ok := roles[p.Owner]; !ok {
			return errors.New("unknown parameter owner role")
		}
		if _, ok := required[p.Name]; ok {
			required[p.Name] = true
		}
		if (!p.Present && (p.ACL.Mode != "default" || len(p.ACL.Grants) != 0)) || (p.Present && p.ACL.Mode != "default" && p.ACL.Mode != "explicit") {
			return errors.New("invalid parameter ACL presence")
		}
		if p.Present && p.ACL.Mode == "default" {
			expected := []TransitionGrant{{p.Owner, p.Owner, "ALTER SYSTEM", false}, {p.Owner, p.Owner, "SET", false}}
			if !reflect.DeepEqual(p.ACL.Grants, expected) {
				return errors.New("parameter default ACL unresolved")
			}
		}
		set, alter, setGO, alterGO := m.RoleFlags.Superuser, m.RoleFlags.Superuser, m.RoleFlags.Superuser, m.RoleFlags.Superuser
		// PostgreSQL treats the bootstrap superuser as owner for parameter ACL masks.
		if p.Present && inherited[p.Owner] {
			setGO = true
			alterGO = true
		}
		last := ""
		for _, g := range p.ACL.Grants {
			key := g.Grantor + "/" + g.Grantee + "/" + g.Privilege
			_, grantorKnown := roles[g.Grantor]
			_, granteeKnown := roles[g.Grantee]
			if (g.Grantee == "PUBLIC" && g.GrantOption) || key <= last || !grantorKnown || (!granteeKnown && g.Grantee != "PUBLIC") || (g.Privilege != "SET" && g.Privilege != "ALTER SYSTEM") {
				return errors.New("invalid or unknown parameter ACL reference")
			}
			last = key
			if g.Grantee == "PUBLIC" || inherited[g.Grantee] {
				if g.Privilege == "SET" {
					set = true
					setGO = setGO || g.GrantOption
				} else {
					alter = true
					alterGO = alterGO || g.GrantOption
				}
			}
		}
		if p.Set != set || p.AlterSystem != alter || p.SetGrantOption != setGO || p.AlterSystemGrantOption != alterGO {
			return errors.New("parameter ACL disagrees with effective privileges")
		}
	}
	for _, present := range required {
		if !present {
			return errors.New("mandatory parameter privilege absent")
		}
	}
	for _, o := range inventory {
		if o.Kind != "role" || o.ParameterPrivileges == nil {
			continue
		}
		if len(o.ParameterPrivileges) != len(m.ParameterPrivileges) {
			return errors.New("inconsistent parameter catalog coverage")
		}
		for i, p := range m.ParameterPrivileges {
			other := o.ParameterPrivileges[i]
			if p.Name != other.Name || p.Context != other.Context || p.Owner != other.Owner || p.Present != other.Present || !reflect.DeepEqual(p.ACL, other.ACL) {
				return errors.New("contradictory shared parameter ACL")
			}
		}
	}
	return nil
}

func validateParameterIdentities(s TransitionSnapshot) error {
	if s.ParameterIdentities == nil {
		return errors.New("parameter catalog identities missing")
	}
	var params []TransitionParameterPrivilege
	for _, m := range s.Metadata {
		if m.Kind == "role" {
			params = m.ParameterPrivileges
			break
		}
	}
	if len(s.ParameterIdentities) != len(params) {
		return errors.New("incomplete parameter catalog identities")
	}
	seen := map[uint32]bool{}
	for i, id := range s.ParameterIdentities {
		if id.Name != params[i].Name || (id.OID != 0) != params[i].Present || (id.OID != 0 && seen[id.OID]) {
			return errors.New("parameter catalog identity mismatch")
		}
		if id.OID != 0 {
			seen[id.OID] = true
		}
	}
	return nil
}

const securityParameterCatalogSQL = `WITH names AS (
 SELECT parname AS name FROM pg_catalog.pg_parameter_acl
 UNION SELECT unnest($1::text[])
) SELECT pg_catalog.jsonb_build_object('oid',COALESCE(a.oid,0)::bigint,'name',n.name,
 'context',s.context,'owner',pg_catalog.pg_get_userbyid(10), 'present',a.oid IS NOT NULL,
 'acl_mode',CASE WHEN a.paracl IS NULL THEN 'default' ELSE 'explicit' END,
 'grants',COALESCE((SELECT jsonb_agg(jsonb_build_object('grantor',pg_catalog.pg_get_userbyid(x.grantor),'grantee',CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(x.grantee) END,'privilege',x.privilege_type,'grant_option',x.is_grantable))
 FROM pg_catalog.aclexplode(CASE WHEN a.oid IS NULL THEN NULL::aclitem[] ELSE COALESCE(a.paracl,pg_catalog.acldefault('p',10)) END) x),'[]'::jsonb))::text
 FROM names n LEFT JOIN pg_catalog.pg_parameter_acl a ON a.parname=n.name
 LEFT JOIN pg_catalog.pg_settings s ON s.name=n.name ORDER BY n.name COLLATE "C"`

const securityParameterEffectiveSQL = `SELECT r,p,
 pg_catalog.has_parameter_privilege(r,p,'SET'),
 pg_catalog.has_parameter_privilege(r,p,'ALTER SYSTEM'),
 pg_catalog.has_parameter_privilege(r,p,'SET WITH GRANT OPTION'),
 pg_catalog.has_parameter_privilege(r,p,'ALTER SYSTEM WITH GRANT OPTION')
 FROM unnest($1::text[]) r CROSS JOIN unnest($2::text[]) p
 ORDER BY r COLLATE "C",p COLLATE "C"`

func collectParameterPrivileges(ctx context.Context, q queryer, s *TransitionSnapshot, scope TransitionScope) error {
	rows, err := q.Query(ctx, securityParameterCatalogSQL, scope.Parameters)
	if err != nil {
		return errors.New("parameter ACL catalog unreadable")
	}
	params := []TransitionParameterPrivilege{}
	ids := []TransitionParameterIdentity{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return errors.New("parameter ACL row unreadable")
		}
		var record struct {
			OID     uint32            `json:"oid"`
			Name    string            `json:"name"`
			Context string            `json:"context"`
			Owner   string            `json:"owner"`
			Present bool              `json:"present"`
			Mode    string            `json:"acl_mode"`
			Grants  []TransitionGrant `json:"grants"`
		}
		if json.Unmarshal([]byte(raw), &record) != nil || reviewedParameterContext(record.Name) == "" || record.Context != reviewedParameterContext(record.Name) || record.Grants == nil {
			rows.Close()
			return errors.New("unsupported parameter ACL record")
		}
		sortCollectedGrants(record.Grants)
		params = append(params, TransitionParameterPrivilege{Name: record.Name, Context: record.Context, Owner: record.Owner, Present: record.Present, ACL: TransitionACL{record.Mode, record.Grants}, IntrinsicUserSet: record.Context == "user"})
		ids = append(ids, TransitionParameterIdentity{record.Name, record.OID})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return errors.New("incomplete parameter ACL read")
	}
	actors := []string{}
	indices := map[string]int{}
	names := []string{}
	for _, p := range params {
		names = append(names, p.Name)
	}
	for i, m := range s.Metadata {
		if m.Kind == "role" {
			name := strings.TrimPrefix(m.Key, "role:")
			actors = append(actors, name)
			indices[name] = i
			s.Metadata[i].ParameterPrivileges = transitionParameterCopy(params)
		}
	}
	sort.Strings(actors)
	rows, err = q.Query(ctx, securityParameterEffectiveSQL, actors, names)
	if err != nil {
		return errors.New("effective parameter privileges unreadable")
	}
	seen := map[string]bool{}
	for rows.Next() {
		var actor, name string
		var set, alter, setGO, alterGO bool
		if err = rows.Scan(&actor, &name, &set, &alter, &setGO, &alterGO); err != nil {
			rows.Close()
			return errors.New("effective parameter privilege unreadable")
		}
		i, ok := indices[actor]
		j := sort.SearchStrings(names, name)
		key := actor + "/" + name
		if !ok || j == len(names) || names[j] != name || seen[key] {
			rows.Close()
			return errors.New("unknown or duplicate parameter privilege result")
		}
		seen[key] = true
		p := &s.Metadata[i].ParameterPrivileges[j]
		p.Set = set
		p.AlterSystem = alter
		p.SetGrantOption = setGO
		p.AlterSystemGrantOption = alterGO
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(seen) != len(actors)*len(params) {
		return errors.New("incomplete effective parameter privileges")
	}
	s.ParameterIdentities = ids
	if err = validateParameterIdentities(*s); err != nil {
		return err
	}
	for _, i := range indices {
		if err = validateParameterPrivileges(s.Metadata[i], s.Metadata); err != nil {
			return err
		}
	}
	return nil
}
func transitionParameterCopy(in []TransitionParameterPrivilege) []TransitionParameterPrivilege {
	raw, _ := json.Marshal(in)
	var out []TransitionParameterPrivilege
	_ = json.Unmarshal(raw, &out)
	return out
}

func preservedParameterIdentities(before, after []TransitionParameterIdentity) bool {
	ids := map[string]uint32{}
	for _, p := range after {
		ids[p.Name] = p.OID
	}
	for _, p := range before {
		if oid, ok := ids[p.Name]; ok && p.OID != 0 && oid != 0 && p.OID != oid {
			return false
		}
	}
	return true
}

func validateParameterScope(names []string, objects []TransitionMetadata) error {
	if names == nil {
		return errors.New("approved parameter scope missing")
	}
	required := map[string]bool{"row_security": false, "search_path": false, "session_replication_role": false}
	previous := ""
	for _, n := range names {
		if n <= previous || reviewedParameterContext(n) == "" {
			return errors.New("invalid approved parameter scope")
		}
		previous = n
		if _, ok := required[n]; ok {
			required[n] = true
		}
	}
	for _, ok := range required {
		if !ok {
			return errors.New("mandatory parameter scope omitted")
		}
	}
	for _, m := range objects {
		if m.Kind != "role" {
			continue
		}
		if len(m.ParameterPrivileges) != len(names) {
			return errors.New("parameter inventory differs from approved scope")
		}
		for i, p := range m.ParameterPrivileges {
			if p.Name != names[i] {
				return errors.New("parameter inventory differs from approved scope")
			}
		}
	}
	return nil
}
