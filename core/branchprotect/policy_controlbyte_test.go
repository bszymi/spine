package branchprotect

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
)

// TestEvaluate_ControlByteBranchFailsClosed pins the defence in depth
// for audit F1 (INIT-023 TASK-001): a branch carrying NUL or another
// ASCII control byte is (Deny, nil, error) for every operation kind and
// every rule set — never Allow(no_matching_rule), even with protection
// switched off (`rules: []`), for a governed merge, or for a
// non-branch ref that would otherwise take the out-of-scope allow.
func TestEvaluate_ControlByteBranchFailsClosed(t *testing.T) {
	ruleSets := []struct {
		name string
		src  RuleSource
	}{
		{"main rule", StaticRules(mainOnlyRules)},
		{"bootstrap", StaticRules(nil)},
		{"explicit rules:[]", StaticRules([]config.Rule{})},
	}
	branches := []string{
		"refs/heads/main\x00", "main\x00", "main\x00report-status", "ma\x00in",
		"main\r", "main\n", "main\x1f", "main\x7f", "refs/tags/v1\x00",
	}
	kinds := []OperationKind{OpDirectWrite, OpDelete, OpGovernedMerge}
	for _, rs := range ruleSets {
		p := New(rs.src)
		for _, b := range branches {
			for _, k := range kinds {
				for _, override := range []bool{false, true} {
					d, reasons, err := p.Evaluate(context.Background(), Request{
						Branch: b, Kind: k, Actor: actor(domain.RoleAdmin), Override: override,
					})
					if err == nil || d != DecisionDeny || reasons != nil {
						t.Errorf("%s / %q / %s / override=%v: got (%v, %+v, %v), want (Deny, nil, error)",
							rs.name, b, k, override, d, reasons, err)
						continue
					}
					if !strings.Contains(err.Error(), "control byte") {
						t.Errorf("%q: error %q does not name the control byte", b, err)
					}
					if strings.ContainsAny(err.Error(), "\x00\r\n\x1f\x7f") {
						t.Errorf("%q: error carries a raw control byte: %q", b, err)
					}
				}
			}
		}
	}
}

// TestEvaluate_WellFormedBranchesUnchanged guards the other direction:
// names without control bytes — including non-ASCII ones — keep their
// pre-fix decisions.
func TestEvaluate_WellFormedBranchesUnchanged(t *testing.T) {
	p := New(StaticRules(append(append([]config.Rule{}, mainOnlyRules...),
		config.Rule{Branch: "release/*", Protections: []config.RuleKind{config.KindNoDelete}})))
	cases := []struct {
		branch string
		kind   OperationKind
		want   Decision
	}{
		{"main", OpDirectWrite, DecisionDeny},
		{"refs/heads/main", OpDirectWrite, DecisionDeny},
		{"refs/heads/main", OpDelete, DecisionDeny},
		{"release/1.0", OpDelete, DecisionDeny},
		{"release/1.0", OpDirectWrite, DecisionAllow},
		{"feat/é", OpDirectWrite, DecisionAllow},
		{"feat/a\u00a0b", OpDirectWrite, DecisionAllow}, // NBSP, escaped
		{"refs/tags/v1", OpDelete, DecisionAllow},
		{"main", OpGovernedMerge, DecisionAllow},
	}
	for _, tc := range cases {
		d, _, err := p.Evaluate(context.Background(), Request{Branch: tc.branch, Kind: tc.kind, Actor: actor(domain.RoleContributor)})
		if err != nil || d != tc.want {
			t.Errorf("%q / %s: got (%v, %v), want %v", tc.branch, tc.kind, d, err, tc.want)
		}
	}
	// The non-ASCII rows prove the guard is byte-wise: bytes >= 0x80 are
	// not control bytes. The NBSP is an escape, never a literal, because
	// an editor that folded it into an ASCII space would leave the row
	// passing (a space is allowed here) while the coverage silently
	// vanished. Pin that both rows are still in the table.
	for _, want := range []string{"feat/é", "feat/a b"} {
		found := false
		for _, tc := range cases {
			found = found || tc.branch == want
		}
		if !found {
			t.Errorf("table lost its non-ASCII row %q", want)
		}
	}
}

// TestIndexControlByte pins the byte classification the guard uses: the
// C0 range and DEL are control bytes; space, printable ASCII and every
// byte >= 0x80 (including invalid UTF-8) are not.
func TestIndexControlByte(t *testing.T) {
	for b := 0; b < 256; b++ {
		s := "ab" + string([]byte{byte(b)})
		want := -1
		if b < 0x20 || b == 0x7f {
			want = 2
		}
		if got := indexControlByte(s); got != want {
			t.Errorf("byte 0x%02x: indexControlByte = %d, want %d", b, got, want)
		}
	}
	if got := indexControlByte(""); got != -1 {
		t.Errorf("empty: %d", got)
	}
}
