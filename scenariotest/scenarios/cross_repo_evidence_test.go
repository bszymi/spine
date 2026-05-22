//go:build scenario

package scenarios_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/evidence"
	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/repository"
	scenarioEngine "github.com/bszymi/spine/scenariotest/engine"
	"github.com/bszymi/spine/scenariotest/harness"
	"github.com/bszymi/spine/core/validation"
	"gopkg.in/yaml.v3"
)

// This file is the scenario-level AC anchor for INIT-014/EPIC-006/TASK-006
// (cross-repo evidence scenario tests). Each test in this file maps 1:1 to
// a deliverable bullet from the task body and is named accordingly.
//
// What this file proves end to end (real on-disk repos, real Git CLI, real
// per-repo orchestrator wiring, real evidence YAML committed to canonical
// path /.spine/runs/<run_id>/evidence/<repo>.yaml on the run branch):
//
//   A. Multi-repo task with all required checks passing → validation
//      result has Status="passed", Querier surfaces RunSummary with
//      Status=passed and per-repo CheckSummaries.
//   B. Missing evidence (one repo did not commit its file) → EV-001
//      emits a present=false finding for that repo, validation result
//      Status="failed", Querier surfaces Status=missing.
//   C. Failed blocking check → EV-004 emits a blocking error finding,
//      validation result Status="failed", Querier surfaces Status=failed.
//   D. Warning-only policy with a failing check → EV-004 emits the
//      finding at Severity="warning", validation result Status="warnings"
//      (NOT "failed"), publish would proceed.
//   E. Stale evidence (HeadCommit ≠ live branch tip) → EV-005 emits
//      an error, validation result Status="failed".
//   F. Run inspection visibility → Querier output is grouped by repo,
//      raw logs are referenced (not embedded), and missing evidence is
//      visible before publish.
//
// Why scenarios over rule-level unit tests: the EV-* rule-level tests
// (internal/validation/rules_evidence_test.go) prove the rule logic
// against in-memory mock resolvers. They do NOT prove that a real
// evidence YAML round-trips through the on-disk loader, that the
// Querier's per-ref fallback resolves against live branch refs, or that
// the validation engine + Querier observe consistent state when reading
// from the same Git tree. This file consolidates those by:
//
//   - committing real evidence YAML at the canonical path on the run
//     branch (ref = run.BranchName, picked up by RefsForRun's
//     non-terminal ordering);
//   - wiring the validation engine with resolvers that read through
//     the real evidence.Load + real *git.CLIClient.RefSHA paths,
//     against the same repo state the Querier reads from;
//   - exercising the engine's StartRun (multi-repo branch fanout,
//     baseline capture, AffectedRepositories assembly) so the run
//     state under validation reflects real production wiring.
//
// What this file does NOT cover (out of scope for TASK-006):
//
//   - The merge step's evidence-write path (where producers commit
//     evidence into the primary repo). That belongs to the runner /
//     check-runner integration and lands in EPIC-007.
//   - Production wiring of the EV-* resolvers into the validation
//     engine via the gateway. The gateway today builds the engine via
//     validation.NewEngine(store) without resolvers; this file
//     constructs an engine with resolvers locally so the EV-* rules
//     can fire. Production wiring is a separate task.
//   - The orchestrator's `cross_artifact_valid` precondition path.
//     The unit-level coverage in internal/engine/step_test.go already
//     proves the precondition routes a failed validation result into
//     a step block. This file invokes the validator directly so
//     "publish blocked / publish allowed" is observable as the
//     ValidationResult.Status that drives the precondition.

// evidenceWorkflowYAML is the workflow these scenarios run against. The
// shape mirrors task-default.yaml (entry → review → publish) so the
// engine's StartRun fans out branches across affected repositories
// (per ADR-015 / INIT-014 EPIC-004). The publish step is explicit so
// the run is in a state where evidence-side validation MAKES SENSE
// (a runner has produced evidence, and the spine engine is about to
// gate on it). Steps below `publish` are not executed by these
// scenarios — we call validator.Validate directly to surface the
// publish-block decision rather than driving the orchestrator through
// the full workflow.
const evidenceWorkflowYAML = `id: task-default
name: Cross-Repo Evidence Test Workflow
version: "1.0"
status: Active
description: Cross-repo evidence scenario workflow.
applies_to:
  - Task
entry_step: build
steps:
  - id: build
    name: Build in code repo
    repository: billing
    type: manual
    outcomes:
      - id: completed
        name: Build complete
        next_step: review
    timeout: "4h"

  - id: review
    name: Review in spine
    type: review
    outcomes:
      - id: accepted
        name: Accepted
        next_step: publish
        commit:
          status: Completed
    timeout: "24h"

  - id: publish
    name: Publish Accepted Outcome
    type: internal
    execution:
      mode: spine_only
      handler: merge
    outcomes:
      - id: published
        name: Outcome Published
        next_step: end
      - id: merge_failed
        name: Merge Failed
        next_step: build
    retry:
      limit: 3
      backoff: exponential
`

// evidenceTaskFrontmatter is the multi-repo task frontmatter every
// scenario seeds. The repositories list opts billing+shipping into the
// run, so AffectedRepositories on the started run is
// [spine, billing, shipping] — the primary repo is always implicit per
// ADR-015. EV-001 then expects an evidence file for every entry.
const evidenceTaskFrontmatter = `---
id: TASK-001
type: Task
title: "Cross-Repo Evidence Task"
status: Pending
epic: /initiatives/init-902/epics/epic-902/epic.md
initiative: /initiatives/init-902/initiative.md
repositories:
  - billing
  - shipping
links:
  - type: parent
    target: /initiatives/init-902/epics/epic-902/epic.md
---

# Cross-Repo Evidence Task
`

// state-key constants. Scenarios stash run state via these names so a
// step-3 evidence writer can read run_id+branch_name+baselines that
// step-2 startEvidenceRun captured.
const (
	stateBillingGit  = "billing_git"
	stateShippingGit = "shipping_git"
	stateRunID       = "run_id"
	stateBranchName  = "branch_name"
	stateTaskPath    = "task_path"
	stateBaselines   = "baselines"
	stateRun         = "run"
)

// setupEvidenceMultiRepo wires two real on-disk code repositories
// (billing, shipping) onto sc.Runtime.Orchestrator via
// harness.WithCodeRepos. Without this step the scenario harness's
// default orchestrator has no per-repo wiring and a multi-repo run
// would silently degrade to primary-only.
//
// State keys set:
//   - stateBillingGit  — git.GitClient for the billing code repo
//   - stateShippingGit — git.GitClient for the shipping code repo
func setupEvidenceMultiRepo() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-evidence-multi-repo",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repos := harness.WithCodeRepos(sc.ParentT, sc.Runtime.Orchestrator, sc.Repo,
				harness.CodeRepoSpec{ID: "billing"},
				harness.CodeRepoSpec{ID: "shipping"},
			)
			sc.Set(stateBillingGit, repos["billing"].Client())
			sc.Set(stateShippingGit, repos["shipping"].Client())
			return nil
		},
	}
}

// seedEvidenceTaskHierarchy seeds initiative + epic + multi-repo task.
// The hierarchy uses init-902/epic-902 (separate from
// multi_repo_run_lifecycle_test.go's init-901) so the scenarios can run
// in the same package without artifact-path collisions when go test
// schedules them in the same process.
func seedEvidenceTaskHierarchy() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "seed-evidence-task-hierarchy",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			scenarioEngine.FixtureInitiative(sc, "initiatives/init-902/initiative.md", scenarioEngine.ArtifactOpts{ID: "INIT-902"})
			scenarioEngine.FixtureEpic(sc, "initiatives/init-902/epics/epic-902/epic.md", scenarioEngine.ArtifactOpts{
				ID:   "EPIC-902",
				Init: "/initiatives/init-902/initiative.md",
			})
			taskPath := "initiatives/init-902/epics/epic-902/tasks/task-001.md"
			if _, err := sc.Runtime.Artifacts.Create(sc.Ctx, taskPath, evidenceTaskFrontmatter); err != nil {
				return fmt.Errorf("create evidence task: %w", err)
			}
			sc.Set(stateTaskPath, taskPath)
			return nil
		},
	}
}

// startEvidenceRun calls Orchestrator.StartRun and stashes the resulting
// run state for the evidence-writing step. After this step the primary
// repo, billing, and shipping all have a refs/heads/<run.BranchName>;
// AffectedRepositories is [spine, billing, shipping]; baselines per
// repo are recorded. Sanity checks on those properties anchor the
// "scenario tests use temporary primary and code repositories" AC.
func startEvidenceRun() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "start-evidence-run",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			taskPath := sc.MustGet(stateTaskPath).(string)
			result, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, taskPath)
			if err != nil {
				return fmt.Errorf("StartRun: %w", err)
			}
			run := result.Run
			wantAffected := []string{repository.PrimaryRepositoryID, "billing", "shipping"}
			if !sameStringSlice(run.AffectedRepositories, wantAffected) {
				return fmt.Errorf("AffectedRepositories: got %v, want %v", run.AffectedRepositories, wantAffected)
			}
			for _, id := range wantAffected {
				if sha := run.RepositoryBaselines[id]; sha == "" {
					return fmt.Errorf("RepositoryBaselines[%q]: empty (got %v)", id, run.RepositoryBaselines)
				}
			}
			sc.Set(stateRunID, run.RunID)
			sc.Set(stateBranchName, run.BranchName)
			sc.Set(stateBaselines, run.RepositoryBaselines)
			sc.Set(stateRun, run)
			return nil
		},
	}
}

// buildEvidence returns a canonical-and-validated ExecutionEvidence
// fixture for (runID, repoID) with the given branch/base/head. The
// `apply` callback can override fields after the defaults are set —
// scenarios use it to drop checks (missing-required), flip a check
// status (failing-blocking), or stash a stale HeadCommit.
//
// The returned value is byte-stable: Canonicalize sorts list fields
// and normalizes timestamps to UTC. Tests that snapshot YAML/JSON of
// this output do not need to additionally normalize.
func buildEvidence(t *testing.T, runID, taskPath, repoID, branch, baseSHA, headSHA string, apply func(*domain.ExecutionEvidence)) *domain.ExecutionEvidence {
	t.Helper()
	ev := &domain.ExecutionEvidence{
		SchemaVersion: domain.ExecutionEvidenceSchemaVersion,
		RunID:         runID,
		TaskPath:      taskPath,
		RepositoryID:  repoID,
		BranchName:    branch,
		BaseCommit:    baseSHA,
		HeadCommit:    headSHA,
		ChangedPaths: domain.ChangedPathsSummary{
			FilesChanged: 1,
			Insertions:   1,
			Deletions:    0,
			Paths:        []string{"src/dummy.go"},
		},
		RequiredChecks: []string{"unit-tests"},
		CheckResults: []domain.CheckResult{
			{
				CheckID:     "unit-tests",
				Status:      domain.CheckStatusPassed,
				Producer:    domain.CheckProducerAutomated,
				ProducedBy:  "ci/test",
				Summary:     "all green",
				EvidenceURI: "https://ci.example.com/runs/" + runID + "/" + repoID,
			},
		},
		Actor:       "ci/test",
		TraceID:     "trace-" + runID,
		Status:      domain.EvidenceStatusPassed,
		GeneratedAt: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
	}
	if apply != nil {
		apply(ev)
	}
	ev.Canonicalize()
	if err := ev.Validate(); err != nil {
		t.Fatalf("buildEvidence (%s): validate: %v", repoID, err)
	}
	return ev
}

// commitEvidenceBundle writes the supplied evidence map to canonical
// paths under the primary repo's run branch and commits them in a
// single deterministic commit. The primary repo's working tree is
// left checked out on the run branch so subsequent reads via
// sc.Repo.Git.ReadFile(ref=branch, ...) succeed without further
// checkouts.
//
// Each map entry maps repository_id to the evidence record that the
// real producer would have written for that repo. Callers omit a key
// to leave that repo's evidence "missing" — EV-001's failure mode.
func commitEvidenceBundle(t *testing.T, sc *scenarioEngine.ScenarioContext, byRepo map[string]*domain.ExecutionEvidence) {
	t.Helper()
	branchName := sc.MustGet(stateBranchName).(string)
	sc.Repo.CheckoutBranch(t, branchName)
	for _, repoID := range sortedRepoKeys(byRepo) {
		ev := byRepo[repoID]
		if ev == nil {
			continue
		}
		raw, err := yaml.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal evidence for %s: %v", repoID, err)
		}
		path := evidence.EvidencePath(ev.RunID, repoID)
		sc.Repo.WriteArtifact(t, path, string(raw))
	}
	sc.Repo.CommitAll(t, "Commit cross-repo evidence")
}

// sortedRepoKeys returns map keys lexically so commitEvidenceBundle
// stages files in a deterministic order. Test failures that capture
// the staged file list stay byte-identical across runs.
func sortedRepoKeys(m map[string]*domain.ExecutionEvidence) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// buildEvidenceValidator constructs a validation.Engine wired with the
// four evidence resolvers: RunResolver pulls from the projection store
// (the production run-by-task lookup), EvidenceResolver reads via the
// real evidence.Load against the primary repo's CLIClient on the
// active run branch (Querier-style ref selection), PolicyResolver
// returns a per-(taskPath, repoID) lookup the test fixture supplies,
// and BranchTipResolver resolves refs/heads/<branch> via the matching
// per-repo CLIClient.
//
// The resolvers mirror what production wiring would do once EPIC-007
// lands the gateway integration. Building them here lets the scenario
// prove the rules-fire-against-real-files path without that wiring
// being in place yet.
func buildEvidenceValidator(
	sc *scenarioEngine.ScenarioContext,
	policiesByRepo map[string][]domain.ValidationPolicy,
	tipsByRepo map[string]string,
) *validation.Engine {
	primaryGit := sc.Repo.Git
	billingGit := sc.MustGet(stateBillingGit).(git.GitClient)
	shippingGit := sc.MustGet(stateShippingGit).(git.GitClient)

	clientByRepo := map[string]git.GitClient{
		repository.PrimaryRepositoryID: primaryGit,
		"billing":                      billingGit,
		"shipping":                     shippingGit,
	}

	runResolver := func(ctx context.Context, taskPath string) (*domain.Run, error) {
		runs, err := sc.Runtime.Store.ListRunsByTask(ctx, taskPath)
		if err != nil {
			return nil, err
		}
		// ListRunsByTask returns most-recent first (ORDER BY created_at
		// DESC). Pick the first non-terminal entry; production wiring
		// would do the same so the validator gates on the live run.
		for i := range runs {
			if runs[i].Status.IsTerminal() {
				continue
			}
			return &runs[i], nil
		}
		return nil, nil
	}

	evidenceResolver := func(ctx context.Context, runID, repoID string) (*domain.ExecutionEvidence, error) {
		// All evidence lives in the primary repo. Read from the run
		// branch first — non-terminal run priority per
		// evidence.Querier.RefsForRun. Falling back to "main" is
		// deliberately omitted because in scenarios the run never
		// merges; a read miss on the run branch IS the
		// evidence-not-committed signal EV-001 wants.
		branchName := sc.MustGet(stateBranchName).(string)
		res, err := evidence.Load(ctx, primaryGit, branchName, runID, repoID)
		if err != nil {
			return nil, err
		}
		if !res.Found {
			return nil, nil
		}
		return res.Evidence, nil
	}

	policyResolver := func(_ context.Context, _ string, repoID string) ([]domain.ValidationPolicy, error) {
		return policiesByRepo[repoID], nil
	}

	branchTipResolver := func(ctx context.Context, repoID, branchName string) (string, error) {
		// Test override takes priority — scenarios set tipsByRepo[repoID]
		// to an SHA different from the live tip to exercise EV-005
		// without having to mutate the actual branch.
		if tip, ok := tipsByRepo[repoID]; ok {
			return tip, nil
		}
		c, ok := clientByRepo[repoID]
		if !ok {
			return "", nil
		}
		return c.RefSHA(ctx, "refs/heads/"+branchName)
	}

	return validation.NewEngine(
		sc.Runtime.Store,
		validation.WithRunResolver(runResolver),
		validation.WithEvidenceResolver(evidenceResolver),
		validation.WithValidationPolicyResolver(policyResolver),
		validation.WithBranchTipResolver(branchTipResolver),
	)
}

// buildEvidenceQuerier returns the production Querier wired against the
// primary repo's CLIClient. SummarizeForRun reads from refs in
// RefsForRun-priority order; the test commits evidence to the run
// branch, so for active runs the first probe finds it.
func buildEvidenceQuerier(sc *scenarioEngine.ScenarioContext) *evidence.Querier {
	return evidence.NewQuerier(sc.Repo.Git)
}

// passingCheck builds a CheckResult fixture in passed status.
func passingCheck(checkID string) domain.CheckResult {
	return domain.CheckResult{
		CheckID:     checkID,
		Status:      domain.CheckStatusPassed,
		Producer:    domain.CheckProducerAutomated,
		ProducedBy:  "ci/test",
		Summary:     "ok",
		EvidenceURI: "https://ci.example.com/checks/" + checkID,
	}
}

// failingCheck builds a CheckResult fixture in failed status. Use for
// the EV-004 blocking-check-failure scenario and the warning-only
// scenario (the same data; only the matching policy's severity
// differs).
func failingCheck(checkID string) domain.CheckResult {
	return domain.CheckResult{
		CheckID:     checkID,
		Status:      domain.CheckStatusFailed,
		Producer:    domain.CheckProducerAutomated,
		ProducedBy:  "ci/test",
		Summary:     "lint regression",
		EvidenceURI: "https://ci.example.com/checks/" + checkID,
	}
}

// policyWith returns a single-check ValidationPolicy at the given
// severity. The selector matches the "code" repository role so it
// applies to billing+shipping but not to the primary spine repo —
// which is what production policies typically do (code-side checks
// gate on code repos).
func policyWith(policyID, checkID string, severity domain.PolicySeverity) domain.ValidationPolicy {
	return domain.ValidationPolicy{
		PolicyID: policyID,
		Version:  "1",
		Title:    policyID,
		Status:   domain.ValidationPolicyStatusActive,
		ADRPaths: []string{"/architecture/adr/ADR-test.md"},
		Selector: domain.PolicySelector{RepositoryRoles: []string{"code"}},
		Checks: []domain.PolicyCheck{{
			CheckID:        checkID,
			Name:           checkID,
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "true",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       severity,
		}},
	}
}

// errorsByRule and warningsByRule are local aliases of the helpers
// rules_evidence_test.go uses, scoped to scenarios_test so the
// scenario file does not have to reach across packages for two-line
// helpers.
func errorsByRule(result domain.ValidationResult, ruleID string) []domain.ValidationError {
	var out []domain.ValidationError
	for _, e := range result.Errors {
		if e.RuleID == ruleID {
			out = append(out, e)
		}
	}
	return out
}

func warningsByRule(result domain.ValidationResult, ruleID string) []domain.ValidationError {
	var out []domain.ValidationError
	for _, w := range result.Warnings {
		if w.RuleID == ruleID {
			out = append(out, w)
		}
	}
	return out
}

// repoSummaryByID returns the repository summary for repoID from a
// RunSummary, or nil. Used by the visibility scenario; scenarios
// that look up a single repo's status read this rather than walking
// the slice each time.
func repoSummaryByID(s *evidence.RunSummary, repoID string) *evidence.RepositorySummary {
	if s == nil {
		return nil
	}
	for i := range s.Repositories {
		if s.Repositories[i].RepositoryID == repoID {
			return &s.Repositories[i]
		}
	}
	return nil
}

// — Deliverable A: multi-repo task with all checks passing —————————

func TestCrossRepoEvidence_AllChecksPassing_AllowsPublish_AnchorsTASK006_DeliverableA(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "evidence-all-checks-passing",
		Description: "Deliverable A: every affected repo has passing required-check evidence; validator returns passed; querier groups by repo",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupEvidenceMultiRepo(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				evidenceWorkflowYAML,
				"seed evidence-scenario task workflow",
			),
			seedEvidenceTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			startEvidenceRun(),
			{
				Name: "all-pass-commit-evidence-and-validate",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet(stateRunID).(string)
					branchName := sc.MustGet(stateBranchName).(string)
					taskPath := sc.MustGet(stateTaskPath).(string)
					baselines := sc.MustGet(stateBaselines).(map[string]string)

					// One evidence file per affected repo with a
					// passing required check. Per-repo head SHA is set
					// to a deterministic value the BranchTipResolver
					// override will return so EV-005 sees a match.
					evidences := map[string]*domain.ExecutionEvidence{
						repository.PrimaryRepositoryID: buildEvidence(
							t, runID, "/"+taskPath, repository.PrimaryRepositoryID,
							branchName, baselines[repository.PrimaryRepositoryID],
							strings.Repeat("a", 40), nil),
						"billing": buildEvidence(
							t, runID, "/"+taskPath, "billing",
							branchName, baselines["billing"],
							strings.Repeat("b", 40), nil),
						"shipping": buildEvidence(
							t, runID, "/"+taskPath, "shipping",
							branchName, baselines["shipping"],
							strings.Repeat("s", 40), nil),
					}
					commitEvidenceBundle(t, sc, evidences)

					// Policies live on code repos; primary "spine"
					// has no policy, so EV-003/004 will not fire for
					// it. Both code policies declare a blocking check
					// matching evidence.RequiredChecks.
					policiesByRepo := map[string][]domain.ValidationPolicy{
						"billing":  {policyWith("billing-pol", "unit-tests", domain.PolicySeverityBlocking)},
						"shipping": {policyWith("shipping-pol", "unit-tests", domain.PolicySeverityBlocking)},
					}
					// BranchTipResolver: stub each repo's tip to match
					// the evidence's HeadCommit so EV-005 is silent.
					tipsByRepo := map[string]string{
						repository.PrimaryRepositoryID: strings.Repeat("a", 40),
						"billing":                      strings.Repeat("b", 40),
						"shipping":                     strings.Repeat("s", 40),
					}
					validator := buildEvidenceValidator(sc, policiesByRepo, tipsByRepo)

					result := validator.Validate(sc.Ctx, taskPath)
					if result.Status != "passed" {
						return fmt.Errorf("validation status: got %q want %q (errors=%v warnings=%v)",
							result.Status, "passed", result.Errors, result.Warnings)
					}
					if len(result.Errors) != 0 {
						return fmt.Errorf("expected no errors, got %v", result.Errors)
					}
					if len(result.Warnings) != 0 {
						return fmt.Errorf("expected no warnings, got %v", result.Warnings)
					}

					// Cross-check the Querier: same repo state should
					// produce a passed RunSummary with three present
					// repo summaries.
					q := buildEvidenceQuerier(sc)
					run := sc.MustGet(stateRun).(*domain.Run)
					summary, err := q.SummarizeForRun(sc.Ctx, run)
					if err != nil {
						return fmt.Errorf("SummarizeForRun: %w", err)
					}
					if summary == nil {
						return fmt.Errorf("Querier returned nil summary for non-planning run")
					}
					if summary.Status != evidence.RunEvidencePassed {
						return fmt.Errorf("RunSummary.Status: got %q want %q", summary.Status, evidence.RunEvidencePassed)
					}
					if len(summary.Repositories) != 3 {
						return fmt.Errorf("RunSummary.Repositories: got %d entries, want 3 (%v)", len(summary.Repositories), summary.Repositories)
					}
					for _, repoID := range []string{repository.PrimaryRepositoryID, "billing", "shipping"} {
						rs := repoSummaryByID(summary, repoID)
						if rs == nil {
							return fmt.Errorf("RunSummary missing repo %q", repoID)
						}
						if !rs.Present {
							return fmt.Errorf("repo %q: Present=false (Reason=%q)", repoID, rs.Reason)
						}
					}
					return nil
				},
			},
		},
	})
}

// — Deliverable B: missing evidence blocks publish ——————————————————

func TestCrossRepoEvidence_MissingEvidence_BlocksPublish_AnchorsTASK006_DeliverableB(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "evidence-missing-blocks-publish",
		Description: "Deliverable B: shipping repo has no evidence file → EV-001 emits error; querier marks shipping missing",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupEvidenceMultiRepo(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				evidenceWorkflowYAML,
				"seed evidence-scenario task workflow",
			),
			seedEvidenceTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			startEvidenceRun(),
			{
				Name: "missing-evidence-shipping-blocks-publish",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet(stateRunID).(string)
					branchName := sc.MustGet(stateBranchName).(string)
					taskPath := sc.MustGet(stateTaskPath).(string)
					baselines := sc.MustGet(stateBaselines).(map[string]string)

					// Spine + billing committed; shipping intentionally
					// absent. Producer crash, runner timeout, partial
					// outage — all roll up to "evidence missing for
					// repository X" in EV-001's emission.
					evidences := map[string]*domain.ExecutionEvidence{
						repository.PrimaryRepositoryID: buildEvidence(
							t, runID, "/"+taskPath, repository.PrimaryRepositoryID,
							branchName, baselines[repository.PrimaryRepositoryID],
							strings.Repeat("a", 40), nil),
						"billing": buildEvidence(
							t, runID, "/"+taskPath, "billing",
							branchName, baselines["billing"],
							strings.Repeat("b", 40), nil),
					}
					commitEvidenceBundle(t, sc, evidences)

					policiesByRepo := map[string][]domain.ValidationPolicy{
						"billing":  {policyWith("billing-pol", "unit-tests", domain.PolicySeverityBlocking)},
						"shipping": {policyWith("shipping-pol", "unit-tests", domain.PolicySeverityBlocking)},
					}
					tipsByRepo := map[string]string{
						repository.PrimaryRepositoryID: strings.Repeat("a", 40),
						"billing":                      strings.Repeat("b", 40),
					}
					validator := buildEvidenceValidator(sc, policiesByRepo, tipsByRepo)

					result := validator.Validate(sc.Ctx, taskPath)
					if result.Status != "failed" {
						return fmt.Errorf("validation status: got %q want %q", result.Status, "failed")
					}
					missingErrs := errorsByRule(result, validation.RuleEvidenceMissing)
					if len(missingErrs) != 1 {
						return fmt.Errorf("EV-001 errors: got %d, want 1 (errors=%v)", len(missingErrs), result.Errors)
					}
					if missingErrs[0].RepositoryID != "shipping" {
						return fmt.Errorf("EV-001 finding repo: got %q want %q", missingErrs[0].RepositoryID, "shipping")
					}

					// Querier surfaces the same missing-state via per-
					// repo Present=false + run-level Status=missing.
					q := buildEvidenceQuerier(sc)
					run := sc.MustGet(stateRun).(*domain.Run)
					summary, err := q.SummarizeForRun(sc.Ctx, run)
					if err != nil {
						return fmt.Errorf("SummarizeForRun: %w", err)
					}
					if summary.Status != evidence.RunEvidenceMissing {
						return fmt.Errorf("RunSummary.Status: got %q want %q", summary.Status, evidence.RunEvidenceMissing)
					}
					shipping := repoSummaryByID(summary, "shipping")
					if shipping == nil {
						return fmt.Errorf("RunSummary missing shipping entry: %v", summary.Repositories)
					}
					if shipping.Present {
						return fmt.Errorf("shipping: Present=true, want false")
					}
					if !containsString(summary.MissingRepositories, "shipping") {
						return fmt.Errorf("MissingRepositories should include shipping; got %v", summary.MissingRepositories)
					}
					return nil
				},
			},
		},
	})
}

// — Deliverable C: failed blocking check blocks publish ——————————————

func TestCrossRepoEvidence_FailedBlockingCheck_BlocksPublish_AnchorsTASK006_DeliverableC(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "evidence-failed-blocking-blocks-publish",
		Description: "Deliverable C: billing's required check is failed → EV-004 emits error severity; publish blocked",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupEvidenceMultiRepo(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				evidenceWorkflowYAML,
				"seed evidence-scenario task workflow",
			),
			seedEvidenceTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			startEvidenceRun(),
			{
				Name: "failed-blocking-check-blocks-publish",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet(stateRunID).(string)
					branchName := sc.MustGet(stateBranchName).(string)
					taskPath := sc.MustGet(stateTaskPath).(string)
					baselines := sc.MustGet(stateBaselines).(map[string]string)

					// Billing's check is failed; spine + shipping pass.
					failingBilling := buildEvidence(
						t, runID, "/"+taskPath, "billing",
						branchName, baselines["billing"],
						strings.Repeat("b", 40),
						func(ev *domain.ExecutionEvidence) {
							ev.CheckResults = []domain.CheckResult{failingCheck("unit-tests")}
							ev.Status = domain.EvidenceStatusFailed
						})
					evidences := map[string]*domain.ExecutionEvidence{
						repository.PrimaryRepositoryID: buildEvidence(
							t, runID, "/"+taskPath, repository.PrimaryRepositoryID,
							branchName, baselines[repository.PrimaryRepositoryID],
							strings.Repeat("a", 40), nil),
						"billing": failingBilling,
						"shipping": buildEvidence(
							t, runID, "/"+taskPath, "shipping",
							branchName, baselines["shipping"],
							strings.Repeat("s", 40), nil),
					}
					commitEvidenceBundle(t, sc, evidences)

					policiesByRepo := map[string][]domain.ValidationPolicy{
						"billing":  {policyWith("billing-pol", "unit-tests", domain.PolicySeverityBlocking)},
						"shipping": {policyWith("shipping-pol", "unit-tests", domain.PolicySeverityBlocking)},
					}
					tipsByRepo := map[string]string{
						repository.PrimaryRepositoryID: strings.Repeat("a", 40),
						"billing":                      strings.Repeat("b", 40),
						"shipping":                     strings.Repeat("s", 40),
					}
					validator := buildEvidenceValidator(sc, policiesByRepo, tipsByRepo)

					result := validator.Validate(sc.Ctx, taskPath)
					if result.Status != "failed" {
						return fmt.Errorf("validation status: got %q want %q (errors=%v)", result.Status, "failed", result.Errors)
					}
					blockErrs := errorsByRule(result, validation.RuleEvidenceBlockingChecksPass)
					if len(blockErrs) != 1 {
						return fmt.Errorf("EV-004 errors: got %d, want 1 (errors=%v)", len(blockErrs), result.Errors)
					}
					ev := blockErrs[0]
					if ev.RepositoryID != "billing" || ev.PolicyID != "billing-pol" || ev.CheckID != "unit-tests" {
						return fmt.Errorf("EV-004 finding metadata: got repo=%q policy=%q check=%q",
							ev.RepositoryID, ev.PolicyID, ev.CheckID)
					}

					q := buildEvidenceQuerier(sc)
					run := sc.MustGet(stateRun).(*domain.Run)
					summary, err := q.SummarizeForRun(sc.Ctx, run)
					if err != nil {
						return fmt.Errorf("SummarizeForRun: %w", err)
					}
					if summary.Status != evidence.RunEvidenceFailed {
						return fmt.Errorf("RunSummary.Status: got %q want %q", summary.Status, evidence.RunEvidenceFailed)
					}
					if !containsString(summary.FailingRepositories, "billing") {
						return fmt.Errorf("FailingRepositories should include billing; got %v", summary.FailingRepositories)
					}
					return nil
				},
			},
		},
	})
}

// — Deliverable D: warning-only policy allows publish ————————————————

func TestCrossRepoEvidence_WarningOnlyPolicy_AllowsPublishWithWarnings_AnchorsTASK006_DeliverableD(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "evidence-warning-only-allows-publish",
		Description: "Deliverable D: warning-severity policy + failed check → EV-004 emits warning; publish allowed",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupEvidenceMultiRepo(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				evidenceWorkflowYAML,
				"seed evidence-scenario task workflow",
			),
			seedEvidenceTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			startEvidenceRun(),
			{
				Name: "warning-only-policy-failed-check-allows-publish",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet(stateRunID).(string)
					branchName := sc.MustGet(stateBranchName).(string)
					taskPath := sc.MustGet(stateTaskPath).(string)
					baselines := sc.MustGet(stateBaselines).(map[string]string)

					// Billing's lint check is failed but its policy
					// declares severity=warning, so EV-004 must emit
					// the finding as a warning rather than an error.
					// Required check (`unit-tests`) is still passing
					// so EV-001/EV-002/EV-003 stay silent.
					softFailBilling := buildEvidence(
						t, runID, "/"+taskPath, "billing",
						branchName, baselines["billing"],
						strings.Repeat("b", 40),
						func(ev *domain.ExecutionEvidence) {
							// `lint` is a warning-severity check, so it
							// belongs in advisory_checks (the schema
							// requires every check_results entry to be
							// declared in either required_checks or
							// advisory_checks; a warning-severity check
							// failing must not block publish, so the
							// advisory bucket is the right home).
							ev.AdvisoryChecks = []string{"lint"}
							ev.CheckResults = []domain.CheckResult{
								passingCheck("unit-tests"),
								failingCheck("lint"),
							}
						})
					evidences := map[string]*domain.ExecutionEvidence{
						repository.PrimaryRepositoryID: buildEvidence(
							t, runID, "/"+taskPath, repository.PrimaryRepositoryID,
							branchName, baselines[repository.PrimaryRepositoryID],
							strings.Repeat("a", 40), nil),
						"billing": softFailBilling,
						"shipping": buildEvidence(
							t, runID, "/"+taskPath, "shipping",
							branchName, baselines["shipping"],
							strings.Repeat("s", 40), nil),
					}
					commitEvidenceBundle(t, sc, evidences)

					policiesByRepo := map[string][]domain.ValidationPolicy{
						"billing": {
							policyWith("billing-blocking", "unit-tests", domain.PolicySeverityBlocking),
							policyWith("billing-warning", "lint", domain.PolicySeverityWarning),
						},
						"shipping": {policyWith("shipping-pol", "unit-tests", domain.PolicySeverityBlocking)},
					}
					tipsByRepo := map[string]string{
						repository.PrimaryRepositoryID: strings.Repeat("a", 40),
						"billing":                      strings.Repeat("b", 40),
						"shipping":                     strings.Repeat("s", 40),
					}
					validator := buildEvidenceValidator(sc, policiesByRepo, tipsByRepo)

					result := validator.Validate(sc.Ctx, taskPath)
					if result.Status != "warnings" {
						return fmt.Errorf("validation status: got %q want %q (errors=%v warnings=%v)",
							result.Status, "warnings", result.Errors, result.Warnings)
					}
					if len(result.Errors) != 0 {
						return fmt.Errorf("expected zero errors (publish allowed); got %v", result.Errors)
					}
					blockWarns := warningsByRule(result, validation.RuleEvidenceBlockingChecksPass)
					if len(blockWarns) != 1 {
						return fmt.Errorf("EV-004 warnings: got %d, want 1 (warnings=%v)", len(blockWarns), result.Warnings)
					}
					if blockWarns[0].Severity != "warning" {
						return fmt.Errorf("EV-004 severity: got %q want %q", blockWarns[0].Severity, "warning")
					}
					if blockWarns[0].PolicyID != "billing-warning" || blockWarns[0].CheckID != "lint" {
						return fmt.Errorf("EV-004 finding metadata: got policy=%q check=%q",
							blockWarns[0].PolicyID, blockWarns[0].CheckID)
					}
					return nil
				},
			},
		},
	})
}

// — Deliverable E: stale evidence blocks publish ————————————————————

func TestCrossRepoEvidence_StaleEvidence_BlocksPublish_AnchorsTASK006_DeliverableE(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "evidence-stale-blocks-publish",
		Description: "Deliverable E: live branch tip ≠ evidence.HeadCommit → EV-005 emits error; publish blocked",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupEvidenceMultiRepo(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				evidenceWorkflowYAML,
				"seed evidence-scenario task workflow",
			),
			seedEvidenceTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			startEvidenceRun(),
			{
				Name: "stale-evidence-shipping-blocks-publish",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet(stateRunID).(string)
					branchName := sc.MustGet(stateBranchName).(string)
					taskPath := sc.MustGet(stateTaskPath).(string)
					baselines := sc.MustGet(stateBaselines).(map[string]string)

					evidences := map[string]*domain.ExecutionEvidence{
						repository.PrimaryRepositoryID: buildEvidence(
							t, runID, "/"+taskPath, repository.PrimaryRepositoryID,
							branchName, baselines[repository.PrimaryRepositoryID],
							strings.Repeat("a", 40), nil),
						"billing": buildEvidence(
							t, runID, "/"+taskPath, "billing",
							branchName, baselines["billing"],
							strings.Repeat("b", 40), nil),
						"shipping": buildEvidence(
							t, runID, "/"+taskPath, "shipping",
							branchName, baselines["shipping"],
							strings.Repeat("s", 40), nil),
					}
					commitEvidenceBundle(t, sc, evidences)

					policiesByRepo := map[string][]domain.ValidationPolicy{
						"billing":  {policyWith("billing-pol", "unit-tests", domain.PolicySeverityBlocking)},
						"shipping": {policyWith("shipping-pol", "unit-tests", domain.PolicySeverityBlocking)},
					}
					// Spine and billing tip-resolvers report the SHA
					// the evidence claims (ev silent). Shipping's
					// resolver returns a DIFFERENT SHA so EV-005 sees
					// the staleness — same shape a producer would
					// observe if a new commit landed on shipping's
					// run branch after it wrote evidence.
					tipsByRepo := map[string]string{
						repository.PrimaryRepositoryID: strings.Repeat("a", 40),
						"billing":                      strings.Repeat("b", 40),
						"shipping":                     strings.Repeat("d", 40), // != "ssss..."
					}
					validator := buildEvidenceValidator(sc, policiesByRepo, tipsByRepo)

					result := validator.Validate(sc.Ctx, taskPath)
					if result.Status != "failed" {
						return fmt.Errorf("validation status: got %q want %q (errors=%v)", result.Status, "failed", result.Errors)
					}
					staleErrs := errorsByRule(result, validation.RuleEvidenceStale)
					if len(staleErrs) != 1 {
						return fmt.Errorf("EV-005 errors: got %d, want 1 (errors=%v)", len(staleErrs), result.Errors)
					}
					if staleErrs[0].RepositoryID != "shipping" {
						return fmt.Errorf("EV-005 finding repo: got %q want %q", staleErrs[0].RepositoryID, "shipping")
					}
					if staleErrs[0].Field != "head_commit" {
						return fmt.Errorf("EV-005 finding field: got %q want %q", staleErrs[0].Field, "head_commit")
					}
					return nil
				},
			},
		},
	})
}

// — Deliverable F: evidence visible in run inspection output —————————

func TestCrossRepoEvidence_VisibleInRunInspect_AnchorsTASK006_DeliverableF(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "evidence-visible-in-run-inspect",
		Description: "Deliverable F: Querier output is grouped by repo, raw logs are referenced not embedded, and missing evidence is visible before publish",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupEvidenceMultiRepo(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				evidenceWorkflowYAML,
				"seed evidence-scenario task workflow",
			),
			seedEvidenceTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			startEvidenceRun(),
			{
				Name: "querier-surfaces-grouped-by-repo-and-omits-logs",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet(stateRunID).(string)
					branchName := sc.MustGet(stateBranchName).(string)
					taskPath := sc.MustGet(stateTaskPath).(string)
					baselines := sc.MustGet(stateBaselines).(map[string]string)

					// Mixed-state world: spine + billing committed,
					// shipping intentionally missing. The Querier's
					// per-repo summary should report two present and
					// one missing entry — the "missing evidence is
					// visible BEFORE publish" AC anchor.
					evidences := map[string]*domain.ExecutionEvidence{
						repository.PrimaryRepositoryID: buildEvidence(
							t, runID, "/"+taskPath, repository.PrimaryRepositoryID,
							branchName, baselines[repository.PrimaryRepositoryID],
							strings.Repeat("a", 40), nil),
						"billing": buildEvidence(
							t, runID, "/"+taskPath, "billing",
							branchName, baselines["billing"],
							strings.Repeat("b", 40), nil),
					}
					commitEvidenceBundle(t, sc, evidences)

					q := buildEvidenceQuerier(sc)
					run := sc.MustGet(stateRun).(*domain.Run)
					summary, err := q.SummarizeForRun(sc.Ctx, run)
					if err != nil {
						return fmt.Errorf("SummarizeForRun: %w", err)
					}
					if summary == nil {
						return fmt.Errorf("Querier returned nil summary for non-planning run")
					}

					// Grouped by repo: every affected repo gets one
					// RepositorySummary in lexical order.
					if len(summary.Repositories) != 3 {
						return fmt.Errorf("Repositories: got %d entries, want 3 (%v)", len(summary.Repositories), summary.Repositories)
					}
					gotIDs := make([]string, len(summary.Repositories))
					for i := range summary.Repositories {
						gotIDs[i] = summary.Repositories[i].RepositoryID
					}
					wantIDs := []string{"billing", "shipping", "spine"} // lexical
					if !sameStringSlice(gotIDs, wantIDs) {
						return fmt.Errorf("repo order: got %v want %v (lexical)", gotIDs, wantIDs)
					}

					// Per-repo presence + check details.
					billing := repoSummaryByID(summary, "billing")
					if billing == nil || !billing.Present {
						return fmt.Errorf("billing: missing or not present (%+v)", billing)
					}
					if len(billing.Checks) == 0 {
						return fmt.Errorf("billing: Checks is empty")
					}
					shipping := repoSummaryByID(summary, "shipping")
					if shipping == nil || shipping.Present {
						return fmt.Errorf("shipping: should be present=false, got %+v", shipping)
					}
					if shipping.Reason == "" {
						return fmt.Errorf("shipping.Reason should be non-empty for missing evidence")
					}
					if !containsString(summary.MissingRepositories, "shipping") {
						return fmt.Errorf("MissingRepositories should include shipping; got %v", summary.MissingRepositories)
					}
					if summary.Status != evidence.RunEvidenceMissing {
						return fmt.Errorf("RunSummary.Status: got %q want %q", summary.Status, evidence.RunEvidenceMissing)
					}

					// Wire-format banned-key check: the JSON-marshalled
					// summary MUST NOT contain field names that imply
					// embedded raw log content. The CheckSummary schema
					// holds an EvidenceURI reference, never the log
					// payload itself.
					raw, err := json.Marshal(summary)
					if err != nil {
						return fmt.Errorf("marshal summary: %w", err)
					}
					banned := []string{`"stdout"`, `"stderr"`, `"output"`, `"logs"`, `"raw"`}
					for _, key := range banned {
						if strings.Contains(string(raw), key) {
							return fmt.Errorf("RunSummary JSON contains banned key %s (raw logs must be referenced via evidence_uri, not embedded)", key)
						}
					}

					// Sanity-check the EvidenceURI referenced (not embedded)
					// for at least one check on the present billing repo.
					hasURI := false
					for _, ck := range billing.Checks {
						if ck.EvidenceURI != "" {
							hasURI = true
							break
						}
					}
					if !hasURI {
						return fmt.Errorf("billing checks have no evidence_uri references; raw logs must be referenced via uri")
					}

					return nil
				},
			},
		},
	})
}

// containsString reports whether haystack contains needle. Local helper
// rather than slices.Contains so the scenario stays buildable without a
// package-level slices import; sameStringSlice is the matching whole-
// slice helper, defined alongside in multi_repo_run_lifecycle_test.go.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
