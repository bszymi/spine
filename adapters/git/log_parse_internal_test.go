package git

// INIT-024 TASK-004 AC 7 (review finding N-11): Log's parser fails closed.
// A record whose first field is not a full object id — or that is cut
// short, or text before the first boundary — is an error for the whole
// call, never a skipped or reshaped entry.

import (
	"errors"
	"strings"
	"testing"
)

func TestParseLogOutput_RejectsMalformedRecord(t *testing.T) {
	const (
		b    = "spine-log-00112233445566778899aabbccddeeff"
		sha1 = "0123456789abcdef0123456789abcdef01234567"
		sha2 = "89abcdef0123456789abcdef0123456789abcdef"
		date = "2026-10-27T12:00:00+00:00"
	)
	rec := func(sha, body string) string { return b + "\n" + sha + "\nT\nt@t\n" + date + "\n" + body + "\n" }

	good := rec(sha1, "first\n\nTrace-ID: tr-1\n") + rec(sha2, "")
	commits, err := parseLogOutput(good, b)
	if err != nil || len(commits) != 2 {
		t.Fatalf("well-formed output: %d commits, %v", len(commits), err)
	}
	if commits[0].SHA != sha1 || commits[0].Message != "first\n\nTrace-ID: tr-1" || commits[0].Trailers["Trace-ID"] != "tr-1" ||
		commits[0].Author != (Author{Name: "T", Email: "t@t"}) || commits[0].Timestamp.IsZero() {
		t.Errorf("first commit = %+v", commits[0])
	}
	if commits[1].SHA != sha2 || commits[1].Message != "" || len(commits[1].Trailers) != 0 {
		t.Errorf("second commit (empty message) = %+v", commits[1])
	}
	if got, err := parseLogOutput("", b); err != nil || len(got) != 0 {
		t.Errorf("empty output: %v, %v; want no commits, no error", got, err)
	}

	for _, tc := range []struct{ name, out string }{
		{"first field not an object id", rec("not-a-sha", "x")},
		{"abbreviated id", rec(sha1[:12], "x")},
		{"id with trailing space", rec(sha1+" ", "x")},
		{"a later record not an id", rec(sha1, "x") + rec("zzzz", "y")},
		{"too few fields", b + "\n" + sha1 + "\nT\nt@t"},
		{"text before the first boundary", "junk\n" + rec(sha1, "x")},
		{"record not on its own line", b + sha1 + "\nT\nt@t\n" + date + "\nx\n"},
		{"the old fixed separator only", sha1 + "\nT\nt@t\n" + date + "\nx\n---END---\n"},
	} {
		got, err := parseLogOutput(tc.out, b)
		if err == nil || !errors.Is(err, errMalformedLog) || got != nil {
			t.Errorf("%s: %d commits, %v; want an error and no commits", tc.name, len(got), err)
		}
		if err != nil && strings.Contains(err.Error(), "not-a-sha") {
			t.Errorf("%s: the error echoes the output: %v", tc.name, err)
		}
	}

	// The boundary is fresh per call.
	b1, err1 := newLogBoundary()
	b2, err2 := newLogBoundary()
	if err1 != nil || err2 != nil || b1 == b2 || !strings.HasPrefix(b1, logBoundaryPrefix) || len(b1) != len(logBoundaryPrefix)+32 {
		t.Errorf("newLogBoundary = %q, %q (%v, %v); want two distinct %s+32-hex values", b1, b2, err1, err2, logBoundaryPrefix)
	}
	if unparseableLog(errMalformedLog).Kind != ErrKindPermanent {
		t.Error("an unparseable log is not a permanent error")
	}
}
