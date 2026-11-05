package main

// INIT-024 TASK-013: pgx v5.11.0 parses connection URIs like libpq. It
// trims spaces around a query key and value, the last repeated parameter
// wins, and the userinfo ends at the first '@'. These tests pin what that
// means for the sslmode=disable startup gate and for describeDBError.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Every spelling pgx reads as sslmode=disable is refused, and each one
// really connects without TLS in the pgx Spine builds with, so the gate
// and the driver agree. Strings that keep TLS pass as before.
func TestRequireSecureDBURL_EverySpellingOfDisable(t *testing.T) {
	t.Setenv("SPINE_INSECURE_LOCAL", "")
	t.Setenv("PGSSLMODE", "")
	for _, tc := range []struct {
		name, dsn string
		refused   bool
	}{
		{"literal", "postgres://u:p@h:5432/db?sslmode=disable", true},
		{"space after =", "postgres://u:p@h:5432/db?sslmode= disable", true},
		{"space before =", "postgres://u:p@h:5432/db?sslmode =disable", true},
		{"trailing space", "postgres://u:p@h:5432/db?sslmode=disable ", true},
		{"last of two wins", "postgres://u:p@h:5432/db?sslmode=require&sslmode= disable", true},
		{"percent-encoded value", "postgres://u:p@h:5432/db?sslmode=%64isable", true},
		{"percent-encoded key", "postgres://u:p@h:5432/db?ssl%6Dode=disable", true},
		{"postgresql scheme, spaced", "postgresql://u:p@h:5432/db?sslmode = disable", true},
		{"key/value, spaces", "host=h user=u password=p sslmode = disable", true},
		{"key/value, quoted", "host=h user=u password=p sslmode='disable'", true},
		{"key/value, escaped", `host=h user=u password=p sslmode=dis\able`, true},
		{"key/value, tab", "host=h user=u password=p sslmode\t=\tdisable", true},

		{"require", "postgres://u:p@h:5432/db?sslmode=require", false},
		{"verify-full", "postgres://u:p@h:5432/db?sslmode=verify-full", false},
		{"no sslmode (prefer)", "postgres://u:p@h:5432/db", false},
		{"disable as another parameter's value", "postgres://u:p@h:5432/db?sslmode=require&application_name=disable", false},
		{"key/value require", "host=h user=u password=p sslmode=require application_name=disable", false},
		{"key/value, disable in another value", "host=h user=u password=p application_name='sslmode = disable' sslmode=require", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireSecureDBURL(tc.dsn)
			if (err != nil) != tc.refused {
				t.Fatalf("requireSecureDBURL(%q) = %v, want refused=%v", tc.dsn, err, tc.refused)
			}
			if err != nil && !strings.Contains(err.Error(), "sslmode=disable") {
				t.Fatalf("error should mention sslmode=disable: %v", err)
			}
			// The driver's view: a refused string dials without TLS, an
			// accepted one with it (on the primary attempt or a fallback).
			cfg, perr := pgconn.ParseConfig(tc.dsn)
			if perr != nil {
				t.Fatalf("pgconn refuses the fixture; the gate's verdict is untested: %v", perr)
			}
			tls := cfg.TLSConfig != nil
			for _, fb := range cfg.Fallbacks {
				tls = tls || fb.TLSConfig != nil
			}
			if tls == tc.refused {
				t.Fatalf("pgx dials %q with tls=%v, but the gate says refused=%v", tc.dsn, tls, tc.refused)
			}
		})
	}
	t.Setenv("SPINE_INSECURE_LOCAL", "1")
	if err := requireSecureDBURL("postgres://u:p@h:5432/db?sslmode= disable"); err != nil {
		t.Fatalf("SPINE_INSECURE_LOCAL=1 must still acknowledge it: %v", err)
	}
}

// pgx v5.11.0 ends the userinfo at the first '@': a password with an
// unencoded '@' is split, its tail becomes part of the host, and the
// connect error names it. describeDBError masks every piece.
func TestDescribeDBError_RawAtInPasswordLeaksNoPiece(t *testing.T) {
	t.Setenv("PGPASSWORD", "")
	const dsn = "postgres://u:TOPSECRET-C1@SECRETTAIL-C1@127.0.0.1:1/db?sslmode=disable&connect_timeout=2"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pgx.Connect(ctx, dsn)
	if err == nil || !strings.Contains(err.Error(), "SECRETTAIL-C1") {
		t.Fatalf("precondition: the connect error names the password's tail as the host, or the case is vacuous: %v", err)
	}
	if got := describeDBError(err, dsn); strings.Contains(got, "TOPSECRET") || strings.Contains(got, "SECRETTAIL") {
		t.Fatalf("describeDBError carries a piece of the password: %q", got)
	}
}
