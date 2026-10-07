package projection

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bszymi/spine/core/domain"
)

// §8 (review round 1, finding N): rejection text echoes attacker-chosen
// YAML keys and values, so what reaches the Warn line, the event payload
// and sync_state.error_detail is one bounded line with no control or
// format characters.
func TestSanitizeForLog(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain", "line 3: field steps not found", "line 3: field steps not found"},
		{"newline joined", "yaml: unmarshal errors:\n  line 8: field ſteps not found", "yaml: unmarshal errors:;   line 8: field ſteps not found"},
		{"crlf and blank lines collapse", "a\r\n\r\nb", "a; b"},
		{"unicode line separators", "a\u2028b\u2029c", "a; b; c"},
		{"leading break dropped", "\nx", "x"},
		{"trailing break dropped", "x\n", "x"},
		{"escape sequence", "red\x1b[31mtext", "red\uFFFD[31mtext"},
		{"nul and del", "a\x00b\x7fc", "a\uFFFDb\uFFFDc"},
		{"bidi override", "evil\u202Eexe.md", "evil\uFFFDexe.md"},
		{"zero-width joiner and bom", "a\u200Db\uFEFFc", "a\uFFFDb\uFFFDc"},
		{"invalid utf-8", "a\xffb", "a\uFFFDb"},
		{"tab is a control", "a\tb", "a\uFFFDb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeForLog(tc.in); got != tc.want {
				t.Fatalf("sanitizeForLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	t.Run("capped at 512 bytes on a rune boundary", func(t *testing.T) {
		got := sanitizeForLog(strings.Repeat("ſ", 1000)) // 2 bytes each
		if len(got) > maxLoggedErrorBytes || !utf8.ValidString(got) {
			t.Fatalf("len=%d valid=%v", len(got), utf8.ValidString(got))
		}
		if len(got) < maxLoggedErrorBytes-1 {
			t.Fatalf("cap cut too early: %d bytes", len(got))
		}
		long := sanitizeForLog(strings.Repeat("a\n", 600))
		if len(long) > maxLoggedErrorBytes || strings.HasSuffix(long, ";") || strings.HasSuffix(long, " ") {
			t.Fatalf("joined text over the cap or with a dangling separator: %d %q", len(long), long[len(long)-4:])
		}
	})

	t.Run("output never holds a control or format character", func(t *testing.T) {
		var b strings.Builder
		for r := rune(0); r < 0x3000; r++ {
			b.WriteRune(r)
		}
		for _, r := range sanitizeForLog(b.String()) {
			if r != utf8.RuneError && (r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 || r == 0x202e || r == 0x200d) {
				t.Fatalf("control or format character %U survived", r)
			}
		}
	})
}

// The rejection event carries the sanitised text, and its id is
// deterministic over (path, commit, action) — so a sweep that re-reports
// on the next instance start de-duplicates — and differs when any of the
// three differs.
func TestReportWorkflowRejection_SanitisedAndDeterministicID(t *testing.T) {
	router := &recordingRouter{}
	svc := NewService(nil, nil, router, 0)
	rej := func(path, commit string) *WorkflowRejection {
		return &WorkflowRejection{Path: path, Commit: commit, Check: checkParse,
			Err: errors.New("line 2: field \u202Eſteps\x1b[0m not found\nsecond line")}
	}
	report := func(path, commit, action string) domain.Event {
		router.reset()
		svc.reportWorkflowRejection(context.Background(), rej(path, commit), action, nil)
		ev := router.ofType(domain.EventProjectionWorkflowRejected)
		if len(ev) != 1 {
			t.Fatalf("expected one event, got %d", len(ev))
		}
		return ev[0]
	}

	first := report(wfPath, shaC1, actionRemoved)
	again := report(wfPath, shaC1, actionRemoved)
	if first.EventID != again.EventID || !strings.HasPrefix(first.EventID, "wfrej-") || len(first.EventID) != len("wfrej-")+16 {
		t.Fatalf("event id not deterministic or malformed: %q vs %q", first.EventID, again.EventID)
	}
	seen := map[string]string{first.EventID: "base"}
	for name, ev := range map[string]domain.Event{
		"path":   report("workflows/other.yaml", shaC1, actionRemoved),
		"commit": report(wfPath, shaC2, actionRemoved),
		"action": report(wfPath, shaC1, actionNotProjected),
	} {
		if prev, dup := seen[ev.EventID]; dup {
			t.Fatalf("changing the %s reused the id of %s: %q", name, prev, ev.EventID)
		}
		seen[ev.EventID] = name
	}

	payload := string(first.Payload)
	for _, bad := range []string{"\u202E", "\\u202e", "\x1b", "\\u001b", "\\n"} {
		if strings.Contains(strings.ToLower(payload), strings.ToLower(bad)) {
			t.Fatalf("payload carries %q unsanitised: %s", bad, payload)
		}
	}
	if !strings.Contains(payload, "not found; second line") {
		t.Fatalf("payload error text not joined to one line: %s", payload)
	}
}
