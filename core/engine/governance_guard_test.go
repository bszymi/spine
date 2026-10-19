package engine

// INIT-023 TASK-006 (ADR-016) regression tests on a real repository with
// merge binding on: the audit's run-branch-slippage PoC
// (poc/verify-r1-2/poc_r12_engine_test.go), inverted — what it showed
// landing on main must not land without an operator's approval of the
// bound commit — plus the approve and reject flows and the forged-key
// case.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

// slipFiles is what the attacker commits on the run branch next to the
// real deliverable: protection removed, review removed.
var slipFiles = map[string]string{
	"src/feature.txt":               "legit deliverable\n",
	".spine/branch-protection.yaml": govRelaxed,
	"workflows/task-default.yaml":   govReviewLess,
}

func (g *govRepo) requireMainGoverned() {
	g.t.Helper()
	if got := g.mainFile(".spine/branch-protection.yaml"); got != govProtected {
		g.t.Fatalf("main:.spine/branch-protection.yaml = %q, want the protected main rule", got)
	}
	if got := g.mainFile("workflows/task-default.yaml"); !strings.Contains(got, "id: review") {
		g.t.Fatalf("main:workflows/task-default.yaml lost its review step: %q", got)
	}
}

// TestGovernanceGuard_ZeroValueKeepsV1Behaviour is AC 2: without
// WithGovernanceGuard a run touching .spine/branch-protection.yaml and
// workflows/ merges with no approval and no governance event, with
// binding off (the embedded library's zero value) and on.
func TestGovernanceGuard_ZeroValueKeepsV1Behaviour(t *testing.T) {
	for _, tc := range []struct {
		name      string
		noBinding bool
	}{{"binding off", true}, {"binding on", false}} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGovRepo(t, govRepoOpts{runFiles: slipFiles, noBinding: tc.noBinding})
			if g.orch.GovernanceGuardEnabled() {
				t.Fatal("guard enabled without WithGovernanceGuard")
			}
			g.toMerge()
			if r := g.store.runs[govRunID]; r.Status != domain.RunStatusCompleted {
				t.Fatalf("run status = %s, want completed (v1 merges governance changes)", r.Status)
			}
			if got := g.mainFile(".spine/branch-protection.yaml"); got != govRelaxed {
				t.Fatalf("main config = %q, want the run's relaxed config (v1)", got)
			}
			for _, e := range g.events.events {
				if strings.HasPrefix(string(e.Type), "governance.") || e.Type == domain.EventBranchProtectionRemovalRefused {
					t.Fatalf("v1 emitted %s", e.Type)
				}
			}
			if keys := governanceKeys(g.store.runs[govRunID].CommitMeta); len(keys) != 0 {
				t.Fatalf("v1 wrote governance keys %v", keys)
			}
		})
	}
}

// TestGovernanceGuard_SelfReviewedRunCannotSlipGovernanceFiles is AC 3,
// the inverted PoC: one actor executes and self-reviews a run whose
// branch carries `rules: []` and a review-less task-default; the publish
// step's merge is held, main keeps both governance files, the hold is
// recorded on the publish step, and one governance.merge_held event names
// both paths. Mutation: EvaluateGovernance always allowing merges the run.
func TestGovernanceGuard_SelfReviewedRunCannotSlipGovernanceFiles(t *testing.T) {
	g := newGovRepo(t, govRepoOpts{runFiles: slipFiles, guard: defaultGuard()})
	g.complete("execute", "mallory", "completed")
	g.complete("review", "mallory", "accepted") // self-review (no SoD)

	r := g.store.runs[govRunID]
	if r.Status != domain.RunStatusCommitting {
		t.Fatalf("run status = %s, want committing (held)", r.Status)
	}
	g.requireMainGoverned()
	if g.mainFile("src/feature.txt") != "" {
		t.Fatal("the deliverable reached main while the merge was held")
	}
	pub := g.latest("publish")
	if pub == nil || pub.Status.IsTerminal() {
		t.Fatalf("publish step = %+v, want a live execution", pub)
	}
	if pub.ErrorDetail == nil || pub.ErrorDetail.Classification != domain.FailureGovernanceApprovalRequired ||
		pub.ErrorDetail.RuleID != GovernanceHoldRuleID {
		t.Fatalf("publish ErrorDetail = %+v, want governance_approval_required", pub.ErrorDetail)
	}
	held := g.eventsOf(domain.EventGovernanceMergeHeld)
	if len(held) != 1 {
		t.Fatalf("governance.merge_held events = %d, want 1", len(held))
	}
	var p struct {
		Paths     []string `json:"paths"`
		PathCount int      `json:"path_count"`
		BoundSHA  string   `json:"bound_sha"`
		PostMerge bool     `json:"post_merge"`
	}
	if err := json.Unmarshal(held[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Paths, ",") != ".spine/branch-protection.yaml,workflows/task-default.yaml" || p.PathCount != 2 ||
		p.BoundSHA != g.runHead() || p.PostMerge {
		t.Fatalf("merge_held payload = %+v", p)
	}
	if len(g.store.mergeOutcomes) != 0 {
		t.Fatalf("a held merge recorded outcomes %+v", g.store.mergeOutcomes)
	}
}

// TestGovernanceGuard_ApproveMerges is AC 4: in the same fixture an
// operator's approval of the bound commit merges the run, completes the
// publish step as published, records the six approval keys (merge_sha =
// the primary outcome's merge commit) and emits one decision event.
func TestGovernanceGuard_ApproveMerges(t *testing.T) {
	g := newGovRepo(t, govRepoOpts{runFiles: slipFiles, guard: defaultGuard()})
	g.toMerge()
	bound := g.runHead()
	before := g.mainHead()

	res, err := g.decide(GovernanceApprove, strings.ToUpper(bound), govOperator, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !res.MergeAttempted || res.MergeError != "" || res.RunStatus != domain.RunStatusCompleted || res.PathCount != 2 {
		t.Fatalf("result = %+v", res)
	}
	if got := g.mainFile(".spine/branch-protection.yaml"); got != govRelaxed {
		t.Fatalf("main config = %q, want the approved change", got)
	}
	if g.mainFile("src/feature.txt") == "" {
		t.Fatal("deliverable not merged")
	}
	if pub := g.latest("publish"); pub.Status != domain.StepStatusCompleted || pub.OutcomeID != "published" || pub.ErrorDetail != nil {
		t.Fatalf("publish = %s/%s detail %+v, want completed/published with the hold cleared", pub.Status, pub.OutcomeID, pub.ErrorDetail)
	}
	keys := governanceKeys(g.store.runs[govRunID].CommitMeta)
	out := g.primaryOutcome()
	if out == nil || out.Status != domain.RepositoryMergeStatusMerged {
		t.Fatalf("primary outcome = %+v", out)
	}
	if len(keys) != 6 || keys[domain.CommitMetaGovernanceApprovalSHA] != bound ||
		keys[domain.CommitMetaGovernanceApprovalActorID] != "op-1" ||
		keys[domain.CommitMetaGovernanceApprovalActorRole] != "operator" ||
		keys[domain.CommitMetaGovernanceApprovalApprovedAt] == "" ||
		keys[domain.CommitMetaGovernanceApprovalMergeSHA] != out.MergeCommitSHA ||
		keys[domain.CommitMetaGovernanceApprovalBaseSHA] != before {
		t.Fatalf("approval keys = %v (merge %s, base %s)", keys, out.MergeCommitSHA, before)
	}
	decided := g.eventsOf(domain.EventGovernanceMergeDecided)
	if len(decided) != 1 || !strings.Contains(string(decided[0].Payload), `"decision":"approved"`) ||
		!strings.Contains(string(decided[0].Payload), `"actor_id":"op-1"`) {
		t.Fatalf("decision events = %+v", decided)
	}
	// Approving again is idempotent and acts as a retry: the run is no
	// longer committing.
	if _, err := g.decide(GovernanceApprove, bound, govOperator, ""); err == nil {
		t.Fatal("a decision on a completed run succeeded")
	} else {
		requireSpineCode(t, err, domain.ErrConflict)
	}
}

// TestGovernanceGuard_RejectRoutesBack is AC 5.
func TestGovernanceGuard_RejectRoutesBack(t *testing.T) {
	t.Run("task-default routes back to execute", func(t *testing.T) {
		g := newGovRepo(t, govRepoOpts{runFiles: slipFiles, guard: defaultGuard()})
		g.toMerge()
		before := g.mainHead()
		res, err := g.decide(GovernanceReject, g.runHead(), govAdmin, "protection must stay")
		if err != nil {
			t.Fatalf("reject: %v", err)
		}
		r := g.store.runs[govRunID]
		if r.Status != domain.RunStatusActive || r.CurrentStepID != "execute" || res.RunStatus != domain.RunStatusActive {
			t.Fatalf("run = %s at %s (result %s), want active at execute", r.Status, r.CurrentStepID, res.RunStatus)
		}
		if pub := g.latest("publish"); pub.Status != domain.StepStatusCompleted || pub.OutcomeID != "merge_failed" {
			t.Fatalf("publish = %s/%s, want completed/merge_failed", pub.Status, pub.OutcomeID)
		}
		if g.mainHead() != before {
			t.Fatal("main moved on reject")
		}
		g.requireMainGoverned()
		if keys := governanceKeys(r.CommitMeta); keys[domain.CommitMetaGovernanceApprovalSHA] != "" {
			t.Fatalf("approval not revoked: %v", keys)
		}
		if d := g.eventsOf(domain.EventGovernanceMergeDecided); len(d) != 1 || !strings.Contains(string(d[0].Payload), `"decision":"rejected"`) {
			t.Fatalf("decision events = %+v", d)
		}
	})
	t.Run("no publish step fails the run and keeps the branch", func(t *testing.T) {
		g := newGovRepo(t, govRepoOpts{wf: govLegacyWF(), runFiles: slipFiles, guard: defaultGuard()})
		g.toMerge()
		if r := g.store.runs[govRunID]; r.Status != domain.RunStatusCommitting {
			t.Fatalf("legacy run = %s, want committing (held)", r.Status)
		}
		rev := g.latest("review")
		if rev.ErrorDetail == nil || rev.ErrorDetail.Classification != domain.FailureGovernanceApprovalRequired {
			t.Fatalf("hold not recorded on the last completed step: %+v", rev.ErrorDetail)
		}
		before := g.mainHead()
		head := g.runHead()
		if _, err := g.decide(GovernanceReject, head, govOperator, "no"); err != nil {
			t.Fatalf("reject: %v", err)
		}
		if r := g.store.runs[govRunID]; r.Status != domain.RunStatusFailed {
			t.Fatalf("run = %s, want failed", r.Status)
		}
		if g.runHead() != head {
			t.Fatal("run branch not preserved")
		}
		if g.mainHead() != before {
			t.Fatal("main moved on reject")
		}
		if n := len(g.eventsOf(domain.EventRunFailed)); n != 1 {
			t.Fatalf("run_failed events = %d, want 1", n)
		}
	})
}

// TestGovernanceGuard_ForgedApprovalIsStripped is AC 9: a workflow outcome
// that plants approval keys for the bound commit (with an admin role) in
// its commit map is held all the same — TASK-005's strip removes every
// reserved key before it is stored — and the validator names the key.
// Mutation: removing the strip lets the forged approval merge the run.
func TestGovernanceGuard_ForgedApprovalIsStripped(t *testing.T) {
	wf := govLegacyWF()
	g := newGovRepo(t, govRepoOpts{wf: wf, runFiles: slipFiles, guard: defaultGuard()})
	forged := map[string]string{
		"status":                                      "Done",
		domain.CommitMetaGovernanceApprovalSHA:        g.runHead(),
		domain.CommitMetaGovernanceApprovalActorID:    "mallory",
		domain.CommitMetaGovernanceApprovalActorRole:  "admin",
		domain.CommitMetaGovernanceApprovalApprovedAt: "2026-10-14T00:00:00Z",
	}
	wf.Steps[1].Outcomes[0].Commit = forged

	g.toMerge()
	r := g.store.runs[govRunID]
	if r.Status != domain.RunStatusCommitting {
		t.Fatalf("run = %s, want committing (held): the forged approval was honoured", r.Status)
	}
	g.requireMainGoverned()
	if r.CommitMeta["status"] != "Done" {
		t.Fatalf("commit_meta lost the workflow's status key: %v", r.CommitMeta)
	}
	if keys := governanceKeys(r.CommitMeta); len(keys) != 0 {
		t.Fatalf("forged approval keys stored: %v", keys)
	}

	res := workflow.Validate(wf)
	found := false
	for _, e := range res.Errors {
		if e.Field == "steps[1].outcomes[0].commit" && strings.Contains(e.Message, domain.CommitMetaGovernanceApprovalSHA) {
			found = true
		}
	}
	if !found {
		t.Fatalf("Validate errors %+v do not name %s at steps[1].outcomes[0].commit", res.Errors, domain.CommitMetaGovernanceApprovalSHA)
	}
}

// TestGovernanceGuard_PermissivePolicyCannotDropTheGuard pins D4: the
// engine enforces EvaluateGovernance itself, so the scenario harness's
// permissive policy (or any custom Policy) still holds the merge.
func TestGovernanceGuard_PermissivePolicyCannotDropTheGuard(t *testing.T) {
	g := newGovRepo(t, govRepoOpts{runFiles: slipFiles, guard: defaultGuard(), policy: allowAllPolicy{}})
	g.toMerge()
	if r := g.store.runs[govRunID]; r.Status != domain.RunStatusCommitting {
		t.Fatalf("run = %s, want held under an allow-all policy", r.Status)
	}
	g.requireMainGoverned()
}

type allowAllPolicy struct{}

func (allowAllPolicy) Evaluate(context.Context, branchprotect.Request) (branchprotect.Decision, []branchprotect.Reason, error) {
	return branchprotect.DecisionAllow, []branchprotect.Reason{{Code: branchprotect.ReasonGovernedMerge}}, nil
}
