package workspace

// INIT-024 TASK-013: pgx v5.11.0 parses connection URIs with a
// libpq-compatible parser instead of net/url, and its keyword/value parser
// changed with it. These tests pin what that means for the workspace
// connection strings Spine parses (BuildPgxpoolConfig) and for its
// guarantee that no error carries a workspace credential.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// clearPGEnv empties every libpq environment variable pgx merges into a
// parsed config (an empty value is ignored), so the expectations below
// depend on the connection string alone.
func clearPGEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE",
		"PGSERVICE", "PGSERVICEFILE", "PGSSLMODE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLPASSWORD",
		"PGAPPNAME", "PGCONNECT_TIMEOUT", "PGTARGETSESSIONATTRS", "PGOPTIONS"} {
		t.Setenv(k, "")
	}
}

// A malformed string makes pgx v5.11.0 quote part of it in the reason
// connStringFreeError keeps: the text after an unquoted space or an
// unencoded '&' in a password, or a password tail an unencoded '@' moved
// into the host. None of it may reach the error; the reason's own words
// do.
func TestBuildPgxpoolConfig_ReasonQuotesNoPasswordFragment(t *testing.T) {
	clearPGEnv(t)
	for _, tc := range []struct {
		name, dsn, fragment, want string
	}{
		{"key/value password with an unquoted space", "host=127.0.0.1 port=1 user=u password=TOP SECRETK6 connect_timeout=no", "SECRETK6", `missing "=" after`},
		{"query password with an unencoded &", "postgres://u@127.0.0.1:1/db?password=TOP&SECRETU1&connect_timeout=2", "SECRETU1", "missing key/value separator"},
		{"userinfo password with an unencoded @, bad escape after it", "postgres://u:TOP@SECRETU2%zz@127.0.0.1:1/db?sslmode=disable", "SECRETU2", "invalid percent-encoded token"},
		{"userinfo password with an unencoded @, space after it", "postgres://u:TOP@SEC RETU3@127.0.0.1:1/db?sslmode=disable", "RETU3", "unexpected spaces found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildPgxpoolConfig(tc.dsn, PoolPolicy{})
			if err == nil {
				t.Fatal("parse succeeded; the fixture must be refused")
			}
			if strings.Contains(err.Error(), tc.fragment) || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("error carries part of the connection string: %v", err)
			}
			if !errors.Is(err, ErrInvalidDatabaseURL) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want ErrInvalidDatabaseURL naming %q", err, tc.want)
			}
		})
	}

	// A quoted fragment that is a parameter name the parser read as a key
	// is not a credential and is kept: it names what to fix.
	_, err := BuildPgxpoolConfig("postgres://u@127.0.0.1:1/db?sslmode=disable&password=TOPSECRET-Q1=", PoolPolicy{})
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") || !strings.Contains(err.Error(), `query parameter: "password"`) {
		t.Fatalf("error = %v, want the key named and the password withheld", err)
	}
}

// pgx v5.11.0 ends the userinfo at the first '@', as libpq does; net/url
// (and pgx v5.9.2) took the last. A password with an unencoded '@' is
// therefore split: its tail becomes part of the host, and the connect
// error names the host. connStringFreeError masks every piece.
func TestNewWorkspaceDBPool_RawAtInPasswordLeaksNoPiece(t *testing.T) {
	clearPGEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, dsn := range []string{
		"postgres://u:TOPSECRET-B3@SECRETTAIL-B3@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		"postgres://u:TOPSECRET-B4@SECRET%41TAIL-B4@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		// The tail is a host list: pgx splits it at ',' (also a decoded
		// %2C) and names each host (codex review round 1).
		"postgres://u:TOPSECRET-B5@SECRETTAIL-B5a,SECRETTAIL-B5b@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		"postgres://u:TOPSECRET-B6@SECRETTAIL-B6a%2CSECRETTAIL-B6b@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
	} {
		_, err := NewWorkspaceDBPool(ctx, "ws-x", dsn, PoolPolicy{})
		if err == nil {
			t.Fatalf("%s: connected; the fixture must fail", dsn)
		}
		for _, s := range []string{"TOPSECRET", "SECRETTAIL", "SECRETATAIL"} {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%s: connect error carries a piece of the password (%q): %v", dsn, s, err)
				break
			}
		}
	}
}

// The connection-string forms Spine ships and documents, and the password
// spellings operators use, parse to these settings with pgx v5.11.0. The
// "v5.9.2" note on a row records how the release Spine used before parsed
// it; every row without one parsed the same with v5.9.2 (checked
// side by side in TASK-013's Implementation Notes).
func TestBuildPgxpoolConfig_DSNForms(t *testing.T) {
	clearPGEnv(t)
	// A string without a user takes PGUSER (else the OS account).
	t.Setenv("PGUSER", "env-user")
	type want struct {
		host     string
		port     uint16
		user     string
		password string
		database string
		tls      bool
	}
	for _, tc := range []struct {
		name, dsn string
		want      want
		refused   bool
		v592      string // how pgx v5.9.2 read it, when it differs
	}{
		// Spine's own forms: compose (SPINE_DATABASE_URL, the registry),
		// the test default, the docs, and replaceDatabaseInURL's output.
		{name: "compose", dsn: "postgres://spine:spine@spine-db:5432/spine?sslmode=disable", want: want{"spine-db", 5432, "spine", "spine", "spine", false}},
		{name: "compose, tls", dsn: "postgres://spine:spine@spine-db:5432/spine?sslmode=require", want: want{"spine-db", 5432, "spine", "spine", "spine", true}},
		{name: "test default", dsn: "postgres://spine_test:spine_test@localhost:5433/spine_test?sslmode=disable", want: want{"localhost", 5433, "spine_test", "spine_test", "spine_test", false}},
		{name: "no userinfo", dsn: "postgres://localhost:5432/spine?sslmode=disable", want: want{"localhost", 5432, "env-user", "", "spine", false}},
		{name: "provisioned workspace", dsn: replaceDatabaseInURL("postgres://spine:spine@spine-db:5432/postgres?sslmode=disable", "spine_ws_acme"), want: want{"spine-db", 5432, "spine", "spine", "spine_ws_acme", false}},
		{name: "key/value", dsn: "host=spine-db port=5432 user=spine password=spine dbname=spine sslmode=disable", want: want{"spine-db", 5432, "spine", "spine", "spine", false}},

		// Userinfo passwords (compose interpolates POSTGRES_PASSWORD here
		// unencoded).
		{name: "userinfo +", dsn: "postgres://u:pa+ss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa+ss", "db", false}},
		{name: "userinfo %2B", dsn: "postgres://u:pa%2Bss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa+ss", "db", false}},
		{name: "userinfo %40", dsn: "postgres://u:pa%40ss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa@ss", "db", false}},
		{name: "userinfo raw @", dsn: "postgres://u:pa@ss@h:5432/db?sslmode=disable", want: want{"ss@h", 5432, "u", "pa", "db", false}, v592: "host h, password pa@ss"},
		{name: "userinfo :", dsn: "postgres://u:pa:ss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa:ss", "db", false}},
		{name: "userinfo %3A", dsn: "postgres://u:pa%3Ass@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa:ss", "db", false}},
		{name: "userinfo raw /", dsn: "postgres://u:pa/ss@h:5432/db?sslmode=disable", refused: true},
		{name: "userinfo %2F", dsn: "postgres://u:pa%2Fss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa/ss", "db", false}},
		{name: "userinfo raw #", dsn: "postgres://u:pa#ss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa#ss", "db", false}, v592: "refused (# began a fragment)"},
		{name: "userinfo raw ?", dsn: "postgres://u:pa?ss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa?ss", "db", false}, v592: "refused (? began the query)"},
		{name: "userinfo raw &", dsn: "postgres://u:pa&ss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa&ss", "db", false}},
		{name: "userinfo raw =", dsn: "postgres://u:pa=ss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa=ss", "db", false}},
		{name: "userinfo raw space", dsn: "postgres://u:pa ss@h:5432/db?sslmode=disable", refused: true},
		{name: "userinfo %20", dsn: "postgres://u:pa%20ss@h:5432/db?sslmode=disable", want: want{"h", 5432, "u", "pa ss", "db", false}},

		// Query-parameter passwords.
		{name: "query +", dsn: "postgres://u@h:5432/db?sslmode=disable&password=pa+ss", want: want{"h", 5432, "u", "pa+ss", "db", false}, v592: "password pa ss (+ decoded as a space)"},
		{name: "query %2B", dsn: "postgres://u@h:5432/db?sslmode=disable&password=pa%2Bss", want: want{"h", 5432, "u", "pa+ss", "db", false}},
		{name: "query raw @", dsn: "postgres://u@h:5432/db?sslmode=disable&password=pa@ss", want: want{"h", 5432, "u", "pa@ss", "db", false}},
		{name: "query :", dsn: "postgres://u@h:5432/db?sslmode=disable&password=pa:ss", want: want{"h", 5432, "u", "pa:ss", "db", false}},
		{name: "query raw /", dsn: "postgres://u@h:5432/db?sslmode=disable&password=pa/ss", want: want{"h", 5432, "u", "pa/ss", "db", false}},
		{name: "query raw =", dsn: "postgres://u@h:5432/db?sslmode=disable&password=pa=ss", refused: true, v592: "password pa=ss"},
		{name: "query %3D", dsn: "postgres://u@h:5432/db?sslmode=disable&password=pa%3Dss", want: want{"h", 5432, "u", "pa=ss", "db", false}},

		// Key/value passwords.
		{name: "key/value +", dsn: "host=h port=5432 user=u password=pa+ss dbname=db sslmode=disable", want: want{"h", 5432, "u", "pa+ss", "db", false}},
		{name: "key/value @ : /", dsn: "host=h port=5432 user=u password=p@s:s/s dbname=db sslmode=disable", want: want{"h", 5432, "u", "p@s:s/s", "db", false}},
		{name: "key/value quoted space", dsn: "host=h port=5432 user=u password='pa ss' dbname=db sslmode=disable", want: want{"h", 5432, "u", "pa ss", "db", false}},
		{name: "key/value escaped quote", dsn: `host=h port=5432 user=u password='pa\'ss' dbname=db sslmode=disable`, want: want{"h", 5432, "u", "pa'ss", "db", false}},
		{name: "key/value doubled backslash", dsn: `host=h port=5432 user=u password=pa\\ss dbname=db sslmode=disable`, want: want{"h", 5432, "u", `pa\ss`, "db", false}},
		{name: "key/value single backslash", dsn: `host=h port=5432 user=u password=pa\ss dbname=db sslmode=disable`, want: want{"h", 5432, "u", "pass", "db", false}, v592: `password pa\ss`},

		// Other changed forms.
		{name: "repeated sslmode, last wins", dsn: "postgres://u:p@h:5432/db?sslmode=require&sslmode=disable", want: want{"h", 5432, "u", "p", "db", false}, v592: "first wins: TLS"},
		{name: "repeated sslmode, last wins (tls)", dsn: "postgres://u:p@h:5432/db?sslmode=disable&sslmode=require", want: want{"h", 5432, "u", "p", "db", true}, v592: "first wins: no TLS"},
		{name: "bare IPv6", dsn: "postgres://u:p@::1/db?sslmode=disable", refused: true, v592: "host ::1"},
		{name: "bracketed IPv6", dsn: "postgres://u:p@[::1]:5432/db?sslmode=disable", want: want{"::1", 5432, "u", "p", "db", false}},
		{name: "query key without =", dsn: "postgres://u:p@h:5432/db?sslmode=disable&foo", refused: true, v592: "runtime parameter foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := BuildPgxpoolConfig(tc.dsn, PoolPolicy{})
			if tc.refused {
				if err == nil {
					t.Fatalf("parsed; want refused (pgx v5.9.2: %s)", tc.v592)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			c := cfg.ConnConfig
			got := want{c.Host, c.Port, c.User, c.Password, c.Database, c.TLSConfig != nil}
			if got != tc.want {
				t.Fatalf("parsed as %+v, want %+v (pgx v5.9.2: %s)", got, tc.want, tc.v592)
			}
		})
	}
}
