package migrations

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Target never reads .env. Password is supplied separately from flags/DSNs.
type Target struct {
	Host, Database, User, Profile, Schema         string
	Port                                          int
	ServerAddress, ServerID, SSLMode, SSLRootCert string
	Password                                      string `json:"-"`
}

func (Target) String() string     { return "MigrationTarget{credentials:redacted}" }
func (t Target) GoString() string { return t.String() }

var simpleName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`)
var schemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

func (t Target) Validate() error {
	if strings.EqualFold(t.Database, "tremor_sklad") || strings.EqualFold(t.Database, "postgres") || strings.EqualFold(t.Database, "template0") || strings.EqualFold(t.Database, "template1") {
		return errors.New("protected database is forbidden")
	}
	if t.Host == "" || strings.TrimSpace(t.Host) != t.Host || strings.ContainsAny(t.Host, "/\\,@ \t\r\n") || t.Port < 1 || t.Port > 65535 || !simpleName.MatchString(t.Database) || !simpleName.MatchString(t.User) || (t.Profile != Profile && t.Profile != IdentityProfile) || !schemaName.MatchString(t.Schema) || strings.HasPrefix(t.Schema, "pg_") || t.Schema == "information_schema" {
		return errors.New("explicit valid host/port/database/user/profile/schema required")
	}
	address, err := netip.ParseAddr(t.ServerAddress)
	if err != nil || address.Zone() != "" || address.IsUnspecified() || !regexp.MustCompile(`^[1-9][0-9]{0,19}$`).MatchString(t.ServerID) {
		return errors.New("expected server address and system identifier required")
	}
	if t.SSLMode != "verify-full" && t.SSLMode != "disable" {
		return errors.New("sslmode must be explicitly verify-full or loopback-only disable")
	}
	if t.SSLMode == "disable" {
		host, err := netip.ParseAddr(t.Host)
		if err != nil || !host.IsLoopback() || !address.IsLoopback() || host.Unmap() != address.Unmap() {
			return errors.New("unencrypted connections require an exact loopback IP target")
		}
	} else if t.SSLRootCert == "" {
		return errors.New("explicit TLS root certificate required")
	}
	return nil
}

func (t Target) connect(ctx context.Context) (*pgx.Conn, identity, error) {
	var id identity
	if err := t.Validate(); err != nil {
		return nil, id, err
	}
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(t.Host, strconv.Itoa(t.Port)), Path: "/" + t.Database, User: url.UserPassword(t.User, t.Password)}
	query := url.Values{"sslmode": {t.SSLMode}}
	if t.SSLMode == "verify-full" {
		query.Set("sslrootcert", t.SSLRootCert)
	}
	u.RawQuery = query.Encode()
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, id, errors.New("invalid connection configuration")
	}
	// Override inherited PGOPTIONS, PGDATABASE, PGPASSWORD, service settings and
	// fallback targets. The startup session is read-only until an explicit tx.
	cfg.Host = t.Host
	cfg.Port = uint16(t.Port)
	cfg.Database = t.Database
	cfg.User = t.User
	cfg.Password = t.Password
	cfg.Fallbacks = nil
	cfg.RuntimeParams = map[string]string{"application_name": "tremor-migrate", "client_encoding": "UTF8", "standard_conforming_strings": "on", "search_path": "pg_catalog", "default_transaction_read_only": "on", "default_transaction_isolation": "read committed"}
	cfg.ConnectTimeout = 10 * time.Second
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, id, errors.New("cannot connect to the explicit migration target")
	}
	id, err = t.inspect(ctx, conn)
	if err != nil {
		closeConnection(conn)
		return nil, identity{}, err
	}
	return conn, id, nil
}

type identity struct{ DatabaseOID int64 }

func (t Target) inspect(ctx context.Context, conn *pgx.Conn) (identity, error) {
	var id identity
	var database, user, address string
	var port, version int
	var recovery bool
	err := conn.QueryRow(ctx, `SELECT pg_catalog.current_database(),current_user,pg_catalog.host(pg_catalog.inet_server_addr()),pg_catalog.inet_server_port(),pg_catalog.current_setting('server_version_num')::int,pg_catalog.pg_is_in_recovery()`).Scan(&database, &user, &address, &port, &version, &recovery)
	if err != nil {
		return id, errors.New("cannot verify actual connection identity")
	}
	actual, e1 := netip.ParseAddr(address)
	expected, e2 := netip.ParseAddr(t.ServerAddress)
	if strings.EqualFold(database, "tremor_sklad") || database != t.Database || user != t.User || e1 != nil || e2 != nil || actual.Unmap() != expected.Unmap() || port != t.Port || recovery || version/10000 != 18 {
		return id, errors.New("actual database/server/user/version does not match approved target")
	}
	var serverID, encoding, provider, locale, client, stringsMode, isolation, readOnly string
	var template bool
	err = conn.QueryRow(ctx, `SELECT (SELECT system_identifier::text FROM pg_catalog.pg_control_system()),oid::bigint,pg_catalog.pg_encoding_to_char(encoding),datlocprovider::text,COALESCE(datlocale,''),datistemplate,pg_catalog.current_setting('client_encoding'),pg_catalog.current_setting('standard_conforming_strings'),pg_catalog.current_setting('default_transaction_isolation'),pg_catalog.current_setting('default_transaction_read_only') FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()`).Scan(&serverID, &id.DatabaseOID, &encoding, &provider, &locale, &template, &client, &stringsMode, &isolation, &readOnly)
	if err != nil {
		return id, errors.New("cannot prove cluster identity and database parameters; no writes allowed")
	}
	if serverID != t.ServerID || encoding != "UTF8" || provider != "b" || locale != "C.UTF-8" || template || client != "UTF8" || stringsMode != "on" || isolation != "read committed" || readOnly != "on" {
		return id, errors.New("cluster identity or required clean-v1 database parameters mismatch")
	}
	var exists bool
	if conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)`, t.Schema).Scan(&exists) != nil || !exists {
		return id, errors.New("explicit target schema must already exist")
	}
	return id, nil
}

func closeConnection(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
