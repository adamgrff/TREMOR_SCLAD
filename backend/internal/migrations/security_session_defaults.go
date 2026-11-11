package migrations

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// TransitionSessionLayer describes catalog login defaults, never live session
// settings. Database/role identity is the enclosing target/role metadata and its
// separately validated physical OID. All four levels are mandatory, even absent
// or overridden levels. Their fixed order is PostgreSQL login precedence.
type TransitionSessionLayer struct {
	Level    string                     `json:"level"`
	Present  bool                       `json:"present"`
	Settings []TransitionSessionSetting `json:"settings"`
}
type TransitionSessionSetting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Prevent accidental formatting of GUC values into diagnostics. JSON is the
// approved canonical payload, and must not be published as an operational log.
func (s TransitionSessionSetting) String() string { return s.Name + "=<redacted>" }

func emptySessionLayers() []TransitionSessionLayer {
	out := []TransitionSessionLayer{}
	for _, level := range []string{"global", "database", "role", "role_database"} {
		out = append(out, TransitionSessionLayer{level, false, []TransitionSessionSetting{}})
	}
	return out
}

var sessionDuration = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(ms|s|min)?$`)

// v1 deliberately supports a small reviewed set. Unsupported settings (including
// custom GUCs and library-loading/debug/logging settings) fail closed without
// echoing their value. Even supported changes require exact release approval.
func validateSessionSetting(s TransitionSessionSetting, objects []TransitionMetadata) error {
	valid := false
	switch s.Name {
	case "session_replication_role":
		valid = s.Value == "origin"
	case "row_security", "check_function_bodies", "standard_conforming_strings", "escape_string_warning":
		valid = s.Value == "on"
	case "transform_null_equals":
		valid = s.Value == "off"
	case "client_encoding":
		valid = s.Value == "UTF8"
	case "backslash_quote":
		valid = s.Value == "off" || s.Value == "safe_encoding"
	case "default_transaction_read_only", "default_transaction_deferrable":
		valid = s.Value == "on" || s.Value == "off"
	case "default_transaction_isolation":
		valid = s.Value == "read committed" || s.Value == "repeatable read" || s.Value == "serializable"
	case "statement_timeout", "lock_timeout", "idle_in_transaction_session_timeout", "idle_session_timeout", "transaction_timeout":
		valid = sessionDuration.MatchString(s.Value)
	case "search_path":
		parts := strings.Split(s.Value, ",")
		valid = len(parts) >= 2 && strings.TrimSpace(parts[0]) == "pg_catalog" && strings.TrimSpace(parts[len(parts)-1]) == "pg_temp"
		seen := map[string]bool{}
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if seen[p] {
				valid = false
			}
			seen[p] = true
			if p == "pg_catalog" || p == "pg_temp" {
				continue
			}
			found := false
			for _, o := range objects {
				if o.Kind == "schema" && o.Key == "schema:"+p && o.Protected {
					found = true
				}
			}
			if !found {
				valid = false
			}
		}
	}
	if !valid {
		return errors.New("unsupported or unsafe catalog session default")
	}
	return nil
}

func validateSessionLayers(m TransitionMetadata, objects []TransitionMetadata) error {
	if m.Kind != "role" {
		if len(m.SessionDefaults) != 0 {
			return errors.New("session defaults require role metadata")
		}
		return nil
	}
	expected := emptySessionLayers()
	if len(m.SessionDefaults) != len(expected) {
		return errors.New("incomplete catalog session defaults")
	}
	for i, l := range m.SessionDefaults {
		if l.Level != expected[i].Level || l.Settings == nil || (!l.Present && len(l.Settings) != 0) {
			return errors.New("invalid catalog session default layer")
		}
		previous := ""
		for _, s := range l.Settings {
			if s.Name <= previous {
				return errors.New("duplicate or unordered catalog session default")
			}
			previous = s.Name
			if err := validateSessionSetting(s, objects); err != nil {
				return err
			}
		}
	}
	// Global/database rows are single catalog facts, not independent role claims.
	for _, other := range objects {
		if other.Kind == "role" && len(other.SessionDefaults) == 4 {
			if !reflect.DeepEqual(m.SessionDefaults[:2], other.SessionDefaults[:2]) {
				return errors.New("contradictory shared session defaults")
			}
		}
	}
	return nil
}

// CatalogLoginOverrides returns ONLY catalog-derived overrides for a fresh login.
// It does not include server configuration, connection options, or later SET;
// memberships and SET ROLE do not reapply login defaults.
func CatalogLoginOverrides(role TransitionMetadata, inventory []TransitionMetadata) ([]TransitionSessionSetting, error) {
	if role.Kind != "role" {
		return nil, errors.New("role required")
	}
	if err := validateSessionLayers(role, inventory); err != nil {
		return nil, err
	}
	settings := map[string]string{}
	for _, l := range role.SessionDefaults {
		for _, s := range l.Settings {
			settings[s.Name] = s.Value
		}
	}
	out := []TransitionSessionSetting{}
	for n, v := range settings {
		out = append(out, TransitionSessionSetting{n, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func parseSessionConfig(raw []string, inventory []TransitionMetadata) ([]TransitionSessionSetting, error) {
	out := []TransitionSessionSetting{}
	for _, entry := range raw {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, errors.New("malformed catalog session default")
		}
		setting := TransitionSessionSetting{name, value}
		if err := validateSessionSetting(setting, inventory); err != nil {
			return nil, err
		}
		out = append(out, setting)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	for i := 1; i < len(out); i++ {
		if out[i-1].Name == out[i].Name {
			return nil, errors.New("duplicate catalog session default")
		}
	}
	return out, nil
}

const securitySessionDefaultsSQL = `SELECT setdatabase::bigint,setrole::bigint,setconfig
FROM pg_catalog.pg_db_role_setting
WHERE setdatabase IN (0,$1::oid) AND (setrole=0 OR setrole=ANY($2::oid[]))
ORDER BY setdatabase,setrole`

func collectSessionDefaults(ctx context.Context, q queryer, s *TransitionSnapshot) error {
	roles := map[uint32]int{}
	oids := []uint32{}
	for i, m := range s.Metadata {
		if m.Kind != "role" {
			continue
		}
		var oid uint32
		for _, id := range s.ObjectIdentities {
			if id.Key == m.Key {
				oid = id.OID
			}
		}
		if oid == 0 {
			return errors.New("session default role identity missing")
		}
		if _, ok := roles[oid]; ok {
			return errors.New("duplicate session default role identity")
		}
		roles[oid] = i
		oids = append(oids, oid)
		s.Metadata[i].SessionDefaults = emptySessionLayers()
	}
	if len(roles) == 0 || s.DatabaseOID == 0 {
		return errors.New("session default scope missing")
	}
	rows, err := q.Query(ctx, securitySessionDefaultsSQL, s.DatabaseOID, oids)
	if err != nil {
		return errors.New("catalog session defaults unreadable")
	}
	seen := map[[2]uint32]bool{}
	for rows.Next() {
		var db, role uint32
		var config []string
		if err = rows.Scan(&db, &role, &config); err != nil {
			rows.Close()
			return errors.New("catalog session defaults unreadable")
		}
		key := [2]uint32{db, role}
		_, roleKnown := roles[role]
		if seen[key] || (db != 0 && db != s.DatabaseOID) || (role != 0 && !roleKnown) || config == nil {
			rows.Close()
			return errors.New("contradictory catalog session default identity")
		}
		seen[key] = true
		settings, e := parseSessionConfig(config, s.Metadata)
		if e != nil {
			rows.Close()
			return e
		}
		layer := 0
		if db != 0 {
			layer = 1
		}
		if role != 0 {
			layer += 2
		}
		// Map (db,role) -> global=0,database=1,role=2,role_database=3.
		for oid, i := range roles {
			if role != 0 && role != oid {
				continue
			}
			s.Metadata[i].SessionDefaults[layer].Present = true
			s.Metadata[i].SessionDefaults[layer].Settings = append([]TransitionSessionSetting{}, settings...)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return errors.New("catalog session defaults unreadable")
	}
	// pg_roles exposes only setdatabase=0 role defaults. Independently reconcile
	// that view with its exact pg_db_role_setting source, including NULL/absence.
	rows, err = q.Query(ctx, `SELECT oid::bigint,rolname,rolconfig FROM pg_catalog.pg_roles WHERE oid=ANY($1::oid[]) ORDER BY oid`, oids)
	if err != nil {
		return errors.New("role defaults unreadable")
	}
	observed := map[uint32]bool{}
	for rows.Next() {
		var oid uint32
		var name string
		var raw []string
		if err = rows.Scan(&oid, &name, &raw); err != nil {
			rows.Close()
			return errors.New("role defaults unreadable")
		}
		i, ok := roles[oid]
		if !ok || observed[oid] || s.Metadata[i].Key != "role:"+name {
			rows.Close()
			return errors.New("role default reference mismatch")
		}
		observed[oid] = true
		parsed, e := parseSessionConfig(raw, s.Metadata)
		layer := s.Metadata[i].SessionDefaults[2]
		if e != nil || layer.Present != (raw != nil) || !reflect.DeepEqual(parsed, layer.Settings) {
			rows.Close()
			return errors.New("rolconfig disagrees with catalog session defaults")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(observed) != len(roles) {
		return errors.New("incomplete role defaults")
	}
	for _, i := range roles {
		if err = validateSessionLayers(s.Metadata[i], s.Metadata); err != nil {
			return err
		}
	}
	return nil
}
