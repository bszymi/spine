package branchprotect_test

// INIT-023 TASK-006 (ADR-016): the governance-path guard's path matcher
// and its single decision. AC 10 (path matching), AC 33 (entry coverage)
// and the decision table of design §2.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
)

func defaultPaths(t *testing.T) branchprotect.GovernancePaths {
	t.Helper()
	g, err := branchprotect.NewGovernancePaths(branchprotect.DefaultGovernancePaths)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGovernancePaths_Match(t *testing.T) {
	g := defaultPaths(t)
	for _, tc := range []struct {
		path string
		want bool
	}{
		{".spine/branch-protection.yaml", true},
		{".spine", true}, // a file or symlink named like the directory
		{".SPINE/agents/x.yaml", true},
		{".spine.yaml", true},
		{".spine.yaml/x", true}, // AC 33: an entry also covers what lies beneath it
		{"workflows/a.yaml", true},
		{"workflows/steps/validate.yaml", true},
		{"Workflows/A.yaml", true},
		{"workflows", true}, // AC 33: workflows as a file
		{"/workflows/a.yaml", true},
		{"wor\u212aflows/a.yaml", true}, // U+212A KELVIN SIGN folds onto k
		{".\u017fpine/x", true},         // U+017F LONG S folds onto s
		{`"workflows/\303\244.yaml"`, true},
		{`"workflows/unterminated`, true},
		{`"docs/bad\escape"`, true},
		{"docs/a\x01b.md", true}, // a control byte
		{"docs/\xff.md", true},   // invalid UTF-8
		{"docs/a\u202eb.md", true},
		{"", true},
		{"workflowsx/a.yaml", false},
		{"docs/workflows/a.yaml", false},
		{"spine/x", false},
		{"my.spine.yaml", false},
		{".spinex/a", false},
		{"src/main.go", false},
		{`"docs/caf\303\251.md"`, false}, // decodes to an ungoverned path
		{"docs/café.md", false},
	} {
		if got := g.Match(tc.path); got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestGovernancePaths_ZeroValueGuardsDefaults(t *testing.T) {
	var zero branchprotect.GovernancePaths
	for _, p := range []string{".spine/x", ".spine.yaml", "workflows/a.yaml"} {
		if !zero.Match(p) {
			t.Errorf("zero GovernancePaths.Match(%q) = false: the zero value must guard the defaults", p)
		}
	}
	if zero.Match("src/a.go") {
		t.Error("zero GovernancePaths matched an ungoverned path")
	}
}

func TestNewGovernancePaths_Normalisation(t *testing.T) {
	g, err := branchprotect.NewGovernancePaths([]string{" /Spine/Workflows/** ", "workflows/", "WORKFLOWS"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(g.Entries(), ",")
	if got != "spine/workflows,workflows" {
		t.Fatalf("entries = %q, want spine/workflows,workflows (trim, leading /, /**, case fold, dedupe)", got)
	}
	if !g.Match("spine/workflows/task-default.yaml") || g.Match("spine/docs/a.md") {
		t.Fatal("normalised entries match wrongly")
	}
	empty, err := branchprotect.NewGovernancePaths(nil)
	if err != nil || strings.Join(empty.Entries(), ",") != ".spine,.spine.yaml,workflows" {
		t.Fatalf("NewGovernancePaths(nil) = %v, %v; want the defaults", empty.Entries(), err)
	}
}

func TestNewGovernancePaths_RejectsInvalidEntries(t *testing.T) {
	for _, e := range []string{"", "/", "  ", "../x", "a/../b", "./x", "a//b", "work*", "w?rk", "a[b]", `a\b`, `"a`, "a\x00", "a\tb", "a\x7f", "\xff", "**"} {
		if _, err := branchprotect.NewGovernancePaths([]string{".spine/", e}); err == nil {
			t.Errorf("NewGovernancePaths(%q) accepted an invalid entry", e)
		} else if !errors.Is(err, branchprotect.ErrInvalidGovernancePath) {
			t.Errorf("NewGovernancePaths(%q) error %v does not wrap ErrInvalidGovernancePath", e, err)
		}
	}
}

func TestGovernanceCheck_Touched(t *testing.T) {
	c := &branchprotect.GovernanceCheck{
		Paths: defaultPaths(t),
		// The engine appends Path and OldPath of every diff entry: a rename
		// out of a governed tree (workflows/a.yaml -> docs/a.yaml) and a
		// deletion both arrive here as the governed path.
		ChangedPaths: []string{
			"docs/a.yaml", "workflows/a.yaml", // rename, via OldPath
			".spine/branch-protection.yaml", // deletion
			"src/x.go",
			"workflows/foo.yaml",
			".spine/runs/r1/evidence/spine.yaml",
			".spine/branch-protection.yaml", // duplicate
		},
		ExemptPaths: []string{"workflows/foo.yaml", ".spine/runs/r1/evidence/spine.yaml", "Workflows/A.yaml"},
	}
	got := strings.Join(c.Touched(), ",")
	if got != ".spine/branch-protection.yaml,workflows/a.yaml" {
		t.Fatalf("Touched() = %q", got)
	}
	// Exemptions compare byte-exactly: an exemption spelled in another
	// case does not excuse the changed path.
	c2 := &branchprotect.GovernanceCheck{Paths: defaultPaths(t),
		ChangedPaths: []string{"workflows/a.yaml"}, ExemptPaths: []string{"Workflows/a.yaml"}}
	if len(c2.Touched()) != 1 {
		t.Fatal("a case-folded exemption excused a path")
	}
	var nilCheck *branchprotect.GovernanceCheck
	if nilCheck.Touched() != nil {
		t.Fatal("nil check touched something")
	}
}

func TestEvaluateGovernance(t *testing.T) {
	bound := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	op := domain.Actor{ActorID: "op-1", Role: domain.RoleOperator}
	approval := func(sha string, a domain.Actor) *branchprotect.GovernanceApproval {
		return &branchprotect.GovernanceApproval{SHA: sha, Actor: a, ApprovedAt: time.Now()}
	}
	governed := []string{"workflows/task-default.yaml", ".spine/branch-protection.yaml"}
	for _, tc := range []struct {
		name     string
		check    *branchprotect.GovernanceCheck
		decision branchprotect.Decision
		code     branchprotect.ReasonCode
		msg      []string
	}{
		{"nil check is v1", nil, branchprotect.DecisionAllow, branchprotect.ReasonGovernedMerge, nil},
		{"no bound commit", &branchprotect.GovernanceCheck{ChangedPaths: governed},
			branchprotect.DecisionDeny, branchprotect.ReasonGovernanceUnevaluable, nil},
		{"malformed bound commit", &branchprotect.GovernanceCheck{BoundSHA: "abc", ChangedPaths: governed},
			branchprotect.DecisionDeny, branchprotect.ReasonGovernanceUnevaluable, nil},
		{"nothing governed", &branchprotect.GovernanceCheck{BoundSHA: bound, ChangedPaths: []string{"src/a.go"}},
			branchprotect.DecisionAllow, branchprotect.ReasonGovernedMerge, nil},
		{"held", &branchprotect.GovernanceCheck{BoundSHA: bound, ChangedPaths: governed},
			branchprotect.DecisionDeny, branchprotect.ReasonGovernanceApprovalRequired,
			[]string{"2 governance path(s)", bound, `".spine/branch-protection.yaml"`}},
		{"operator approval", &branchprotect.GovernanceCheck{BoundSHA: bound, ChangedPaths: governed, Approval: approval(bound, op)},
			branchprotect.DecisionAllow, branchprotect.ReasonGovernanceApproved, nil},
		{"admin approval", &branchprotect.GovernanceCheck{BoundSHA: bound, ChangedPaths: governed,
			Approval: approval(bound, domain.Actor{ActorID: "adm", Role: domain.RoleAdmin})},
			branchprotect.DecisionAllow, branchprotect.ReasonGovernanceApproved, nil},
		{"reviewer approval", &branchprotect.GovernanceCheck{BoundSHA: bound, ChangedPaths: governed,
			Approval: approval(bound, domain.Actor{ActorID: "rev", Role: domain.RoleReviewer})},
			branchprotect.DecisionDeny, branchprotect.ReasonGovernanceApprovalRequired, nil},
		{"approval without actor", &branchprotect.GovernanceCheck{BoundSHA: bound, ChangedPaths: governed,
			Approval: approval(bound, domain.Actor{Role: domain.RoleAdmin})},
			branchprotect.DecisionDeny, branchprotect.ReasonGovernanceApprovalRequired, nil},
		{"approval of another commit", &branchprotect.GovernanceCheck{BoundSHA: bound, ChangedPaths: governed, Approval: approval(other, op)},
			branchprotect.DecisionDeny, branchprotect.ReasonGovernanceApprovalRequired,
			[]string{"approval recorded for " + other + ", merge is bound to " + bound}},
		{"tombstoned approval", &branchprotect.GovernanceCheck{BoundSHA: bound, ChangedPaths: governed, Approval: approval("", op)},
			branchprotect.DecisionDeny, branchprotect.ReasonGovernanceApprovalRequired, nil},
		{"many paths are capped", &branchprotect.GovernanceCheck{BoundSHA: bound,
			ChangedPaths: []string{".spine/a", ".spine/b", ".spine/c", ".spine/d", ".spine/e", ".spine/f", ".spine/g"}},
			branchprotect.DecisionDeny, branchprotect.ReasonGovernanceApprovalRequired, []string{"7 governance path(s)", ", …"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, reasons := branchprotect.EvaluateGovernance(tc.check)
			if d != tc.decision || len(reasons) != 1 || reasons[0].Code != tc.code {
				t.Fatalf("EvaluateGovernance = %s %+v, want %s %s", d, reasons, tc.decision, tc.code)
			}
			for _, m := range tc.msg {
				if !strings.Contains(reasons[0].Message, m) {
					t.Errorf("message %q lacks %q", reasons[0].Message, m)
				}
			}
			if tc.name == "many paths are capped" && strings.Contains(reasons[0].Message, ".spine/f") {
				t.Errorf("message names more than 5 paths: %q", reasons[0].Message)
			}
		})
	}
}

// TestPolicy_GovernedMergeWithGovernance: Policy.Evaluate defers to
// EvaluateGovernance when the request carries governance facts, and keeps
// the v1 answer otherwise — even over a rule source that fails (governance
// evaluation loads no rules).
func TestPolicy_GovernedMergeWithGovernance(t *testing.T) {
	pol := branchprotect.New(failingSource{})
	ctx := context.Background()
	d, reasons, err := pol.Evaluate(ctx, branchprotect.Request{Branch: "main", Kind: branchprotect.OpGovernedMerge})
	if err != nil || d != branchprotect.DecisionAllow || reasons[0].Code != branchprotect.ReasonGovernedMerge {
		t.Fatalf("v1 governed merge = %s %+v %v", d, reasons, err)
	}
	d, reasons, err = pol.Evaluate(ctx, branchprotect.Request{Branch: "main", Kind: branchprotect.OpGovernedMerge,
		Governance: &branchprotect.GovernanceCheck{BoundSHA: strings.Repeat("c", 40), ChangedPaths: []string{".spine.yaml"}}})
	if err != nil || d != branchprotect.DecisionDeny || reasons[0].Code != branchprotect.ReasonGovernanceApprovalRequired {
		t.Fatalf("governed merge with governance facts = %s %+v %v", d, reasons, err)
	}
	// The control-byte refusal still comes first.
	if d, _, err := pol.Evaluate(ctx, branchprotect.Request{Branch: "ma\x00in", Kind: branchprotect.OpGovernedMerge,
		Governance: &branchprotect.GovernanceCheck{}}); err == nil || d != branchprotect.DecisionDeny {
		t.Fatalf("control byte with governance = %s %v, want deny + error", d, err)
	}
}

type failingSource struct{}

func (failingSource) Rules(context.Context) ([]config.Rule, error) {
	return nil, errors.New("rule source down")
}
