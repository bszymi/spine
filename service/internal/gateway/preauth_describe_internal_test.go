package gateway

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// INIT-024 TASK-013: describePreauthErr withholds every connection-string
// parse error, not only the ones whose password pgconn fails to mask. The
// guarantee is Spine's: pgconn's masking is best effort (pgx v5.11.0 masks
// query-parameter passwords that v5.9.2 printed, and still misses other
// shapes), so these parse errors carry the password in their reason, which
// pgconn never masks, whatever its connection-string masking does.
func TestDescribePreauthErr_WithholdsEveryParseError(t *testing.T) {
	const secret = "TOPSECRET-P1"
	dsn := "postgres://u:" + secret + "@127.0.0.1:1/db?sslmode=bogus"
	parseErr := pgconn.NewParseConfigError(dsn, "invalid value near "+secret, errors.New("inner "+secret))
	cases := map[string]error{
		"bare":           parseErr,
		"wrapped":        fmt.Errorf("parse database URL: %w", parseErr),
		"wrapped twice":  fmt.Errorf("connect to workspace database: %w", fmt.Errorf("parse database URL: %w", parseErr)),
		"joined":         errors.Join(errors.New("build failed"), parseErr),
		"real, password": realParseError(t, "host=127.0.0.1 port=1 user=u password = "+secret+" connect_timeout=no"),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(err.Error(), secret) {
				t.Fatalf("precondition: the error must carry the password, or the case is vacuous: %v", err)
			}
			got := describePreauthErr(err)
			if strings.Contains(got, secret) || strings.Contains(got, "127.0.0.1") {
				t.Fatalf("describePreauthErr = %q, carries the connection string", got)
			}
			if !strings.Contains(got, "details withheld") {
				t.Fatalf("describePreauthErr = %q, want the withheld-cause message", got)
			}
		})
	}

	// Every other cause is logged as it is, so an outage stays diagnosable.
	other := fmt.Errorf("connect to workspace database: %w", errors.New("dial tcp 127.0.0.1:1: connection refused"))
	if got := describePreauthErr(other); got != other.Error() {
		t.Fatalf("describePreauthErr(non-parse error) = %q, want it unchanged", got)
	}
}

// realParseError returns pgconn's own parse error for dsn, failing the test
// when dsn parses.
func realParseError(t *testing.T, dsn string) error {
	t.Helper()
	_, err := pgconn.ParseConfig(dsn)
	if err == nil {
		t.Fatalf("fixture %q parses; it must be refused", dsn)
	}
	return err
}
