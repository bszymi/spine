package validation

import (
	"context"
	"fmt"
	"sort"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// Rule IDs for the EV-* execution-evidence rule family.
//
// The five rules are split along the deliverable boundary in
// /initiatives/INIT-014.../EPIC-006/TASK-004 so each line of the
// "Validation output names repo ID, policy ID, and failing check"
// AC #5 maps to a specific rule. Operators / dashboards filter by
// rule_id; ValidationError.RepositoryID / .PolicyID / .CheckID carry
// the structured naming so consumers do not have to text-parse
// Message.
//
// Rules emit at most one ValidationError per (repository_id,
// policy_id, check_id) triple — never a single message that bundles
// multiple repos or checks. This keeps output stable for diffs and
// tractable for incident-response runbooks: "EV-004 failures for
// repo=billing" is a real query, not a Message regex.
const (
	// RuleEvidenceMissing — every affected repository must produce
	// evidence. Missing evidence blocks publication (TASK-004 AC #1).
	RuleEvidenceMissing = "EV-001"

	// RuleEvidenceBranchCommitMatch — evidence's claimed branch and
	// base commit must agree with the run's recorded values for that
	// repository. Wrong branch or wrong base commit blocks
	// publication (TASK-004 AC #2).
	RuleEvidenceBranchCommitMatch = "EV-002"

	// RuleEvidenceRequiredChecks — every blocking check declared by an
	// applicable policy must appear in evidence.CheckResults. A
	// missing required check is treated as a failed prerequisite, not
	// a skipped check (TASK-004 AC #3).
	RuleEvidenceRequiredChecks = "EV-003"

	// RuleEvidenceBlockingChecksPass — every blocking check's recorded
	// result must be a successful terminal status (passed or
	// skipped). Failed / errored / still-pending blocking checks
	// block publication (TASK-004 AC #3, AC #4 inverse).
	// Warning-severity check failures are emitted as warnings (Severity
	// "warning") so they appear in ValidationResult.Warnings but do
	// NOT block — TASK-004 AC #4: "Warning-only policy checks do not
	// block publish."
	RuleEvidenceBlockingChecksPass = "EV-004"

	// RuleEvidenceStale — evidence must reflect the current state of
	// the run branch. When a BranchTipResolver is wired and the live
	// branch tip differs from evidence.HeadCommit, the evidence is
	// stale (new commits landed since the producer wrote it). Stale
	// evidence blocks publication; the runner must regenerate.
	RuleEvidenceStale = "EV-005"
)

// evidenceRules returns the execution-evidence rules registered with
// the validation engine. The rules are constructed regardless of
// whether the resolvers are wired; each rule short-circuits cleanly
// when its required resolver is absent so a workspace not yet wired
// for evidence (e.g. during the EPIC-006 rollout window) sees no
// emissions rather than spurious failures.
func evidenceRules(
	runResolver RunResolver,
	evidenceResolver EvidenceResolver,
	policyResolver ValidationPolicyResolver,
	branchTipResolver BranchTipResolver,
) []Rule {
	deps := evidenceRuleDeps{
		run:       runResolver,
		evidence:  evidenceResolver,
		policy:    policyResolver,
		branchTip: branchTipResolver,
	}
	return []Rule{
		&ruleEV001{deps: deps},
		&ruleEV002{deps: deps},
		&ruleEV003{deps: deps},
		&ruleEV004{deps: deps},
		&ruleEV005{deps: deps},
	}
}

// evidenceRuleDeps bundles the resolvers shared by every EV-* rule so
// each rule struct can stay terse. The rules read but never write
// these fields; values are captured at engine construction.
type evidenceRuleDeps struct {
	run       RunResolver
	evidence  EvidenceResolver
	policy    ValidationPolicyResolver
	branchTip BranchTipResolver
}

// resolveRun returns (run, ok). It encapsulates the precondition every
// EV-* rule shares: the artifact under validation must be a Task with
// an attached non-planning Run, and the RunResolver must be wired.
// Resolver errors are treated as "no run for now" — the orchestrator's
// existing precondition path surfaces engine errors separately, and
// emitting no findings here avoids blocking publish on a transient
// resolver bug.
func (d evidenceRuleDeps) resolveRun(ctx context.Context, proj *store.ArtifactProjection) (*domain.Run, bool) {
	if d.run == nil {
		return nil, false
	}
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeTask {
		return nil, false
	}
	run, err := d.run(ctx, proj.ArtifactPath)
	if err != nil {
		observe.Logger(ctx).Warn("evidence rule: run resolver returned error",
			"task_path", proj.ArtifactPath, "error", err)
		return nil, false
	}
	if run == nil {
		return nil, false
	}
	// Planning runs only create artifacts on a branch; they do not
	// produce execution evidence (per execution-evidence.md §2.2 and
	// EPIC-006 AC #1, evidence applies to standard runs that change
	// affected repositories). Skip silently rather than emit
	// false-positive missing-evidence findings.
	if run.Mode == domain.RunModePlanning {
		return nil, false
	}
	if len(run.AffectedRepositories) == 0 {
		return nil, false
	}
	return run, true
}

// expectedBranch returns the branch name evidence is expected to claim
// for a given repository. Run.RepositoryBranches optionally records a
// per-repo override (used in partial-cleanup recovery scenarios per
// run.go); fall back to Run.BranchName otherwise.
func expectedBranch(run *domain.Run, repositoryID string) string {
	if branch, ok := run.RepositoryBranches[repositoryID]; ok && branch != "" {
		return branch
	}
	return run.BranchName
}

// sortedRepoIDs returns Run.AffectedRepositories with stable lexical
// ordering so evidence-rule outputs are deterministic across calls.
// Run.AffectedRepositories preserves declared order today (primary
// first, code repos in their task-frontmatter order); rules that emit
// per-repo findings sort to keep YAML/JSON diffs in
// ValidationResult.Errors stable for snapshot consumers.
func sortedRepoIDs(run *domain.Run) []string {
	out := append([]string(nil), run.AffectedRepositories...)
	sort.Strings(out)
	return out
}

// ruleEV001 — every affected repository must produce evidence.
type ruleEV001 struct {
	deps evidenceRuleDeps
}

func (r *ruleEV001) ID() string { return RuleEvidenceMissing }
func (r *ruleEV001) Classification() domain.ViolationClassification {
	return domain.ViolationExecutionEvidence
}

func (r *ruleEV001) Evaluate(ctx context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	run, ok := r.deps.resolveRun(ctx, proj)
	if !ok {
		return nil
	}
	if r.deps.evidence == nil {
		return nil
	}
	var errs []domain.ValidationError
	for _, repoID := range sortedRepoIDs(run) {
		ev, err := r.deps.evidence(ctx, run.RunID, repoID)
		if err != nil {
			// Treat resolver error as "evidence missing" — the runner
			// either failed to publish or the resolver is broken.
			// Either way, validation cannot let publication proceed
			// without a verifiable record. The error text is logged
			// via observe; emitted Message stays operator-readable.
			observe.Logger(ctx).Warn("evidence resolver error treated as missing",
				"run_id", run.RunID, "repository_id", repoID, "error", err)
			errs = append(errs, domain.ValidationError{
				RuleID:       r.ID(),
				Severity:     "error",
				RepositoryID: repoID,
				Message:      fmt.Sprintf("execution evidence missing for repository %q on run %q (resolver error)", repoID, run.RunID),
			})
			continue
		}
		if ev == nil {
			errs = append(errs, domain.ValidationError{
				RuleID:       r.ID(),
				Severity:     "error",
				RepositoryID: repoID,
				Message:      fmt.Sprintf("execution evidence missing for repository %q on run %q", repoID, run.RunID),
			})
		}
	}
	return errs
}

// ruleEV002 — evidence must claim the same branch and base commit as
// the Run. Wrong-branch or wrong-base-commit evidence is from a
// different run incarnation and cannot be trusted.
type ruleEV002 struct {
	deps evidenceRuleDeps
}

func (r *ruleEV002) ID() string { return RuleEvidenceBranchCommitMatch }
func (r *ruleEV002) Classification() domain.ViolationClassification {
	return domain.ViolationExecutionEvidence
}

func (r *ruleEV002) Evaluate(ctx context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	run, ok := r.deps.resolveRun(ctx, proj)
	if !ok {
		return nil
	}
	if r.deps.evidence == nil {
		return nil
	}
	var errs []domain.ValidationError
	for _, repoID := range sortedRepoIDs(run) {
		ev, err := r.deps.evidence(ctx, run.RunID, repoID)
		if err != nil || ev == nil {
			// Missing evidence is EV-001's responsibility; emitting a
			// branch-mismatch error for an absent record would be
			// noisy and confusing. EV-002 only validates evidence that
			// is present.
			continue
		}
		if want := expectedBranch(run, repoID); want != "" && ev.BranchName != want {
			errs = append(errs, domain.ValidationError{
				RuleID:       r.ID(),
				Severity:     "error",
				RepositoryID: repoID,
				Field:        "branch_name",
				Message: fmt.Sprintf(
					"evidence for repository %q claims branch %q but run is on branch %q",
					repoID, ev.BranchName, want,
				),
			})
		}
		if baseline, has := run.RepositoryBaselines[repoID]; has && baseline != "" && ev.BaseCommit != baseline {
			errs = append(errs, domain.ValidationError{
				RuleID:       r.ID(),
				Severity:     "error",
				RepositoryID: repoID,
				Field:        "base_commit",
				Message: fmt.Sprintf(
					"evidence for repository %q claims base commit %q but run baseline is %q",
					repoID, ev.BaseCommit, baseline,
				),
			})
		}
	}
	return errs
}

// ruleEV003 — every blocking check declared by an applicable policy
// must appear in evidence.CheckResults. A missing entry is treated as
// a failed prerequisite, not a skipped check.
type ruleEV003 struct {
	deps evidenceRuleDeps
}

func (r *ruleEV003) ID() string { return RuleEvidenceRequiredChecks }
func (r *ruleEV003) Classification() domain.ViolationClassification {
	return domain.ViolationExecutionEvidence
}

func (r *ruleEV003) Evaluate(ctx context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	run, ok := r.deps.resolveRun(ctx, proj)
	if !ok {
		return nil
	}
	if r.deps.evidence == nil || r.deps.policy == nil {
		return nil
	}
	var errs []domain.ValidationError
	for _, repoID := range sortedRepoIDs(run) {
		policies, err := r.deps.policy(ctx, proj.ArtifactPath, repoID)
		if err != nil {
			observe.Logger(ctx).Warn("policy resolver error",
				"task_path", proj.ArtifactPath, "repository_id", repoID, "error", err)
			continue
		}
		if len(policies) == 0 {
			continue
		}
		ev, err := r.deps.evidence(ctx, run.RunID, repoID)
		if err != nil || ev == nil {
			// EV-001 already covers missing-evidence; do not double-emit.
			continue
		}
		present := checkResultIndex(ev.CheckResults)
		for _, policy := range policies {
			for _, check := range policy.Checks {
				if !check.IsBlocking() {
					continue
				}
				if _, ok := present[check.CheckID]; ok {
					continue
				}
				errs = append(errs, domain.ValidationError{
					RuleID:       r.ID(),
					Severity:     "error",
					RepositoryID: repoID,
					PolicyID:     policy.PolicyID,
					CheckID:      check.CheckID,
					Field:        "check_results",
					Message: fmt.Sprintf(
						"required check %q (policy %q, repository %q) has no result row in evidence",
						check.CheckID, policy.PolicyID, repoID,
					),
				})
			}
		}
	}
	return errs
}

// ruleEV004 — every blocking check's recorded result must be a
// successful terminal status (passed or skipped). Warning-severity
// check failures are emitted as warnings so dashboards see them but
// publish is not blocked.
type ruleEV004 struct {
	deps evidenceRuleDeps
}

func (r *ruleEV004) ID() string { return RuleEvidenceBlockingChecksPass }
func (r *ruleEV004) Classification() domain.ViolationClassification {
	return domain.ViolationExecutionEvidence
}

func (r *ruleEV004) Evaluate(ctx context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	run, ok := r.deps.resolveRun(ctx, proj)
	if !ok {
		return nil
	}
	if r.deps.evidence == nil || r.deps.policy == nil {
		return nil
	}
	var findings []domain.ValidationError
	for _, repoID := range sortedRepoIDs(run) {
		policies, err := r.deps.policy(ctx, proj.ArtifactPath, repoID)
		if err != nil {
			continue
		}
		if len(policies) == 0 {
			continue
		}
		ev, err := r.deps.evidence(ctx, run.RunID, repoID)
		if err != nil || ev == nil {
			continue
		}
		results := checkResultIndex(ev.CheckResults)
		for _, policy := range policies {
			for _, check := range policy.Checks {
				result, ok := results[check.CheckID]
				if !ok {
					// EV-003 catches missing required checks. For
					// missing warning-severity checks, do not emit:
					// the policy author asked for visibility, not
					// presence-as-prerequisite.
					continue
				}
				if isSuccessfulCheckStatus(result.Status) {
					continue
				}
				severity := "error"
				if !check.IsBlocking() {
					severity = "warning"
				}
				findings = append(findings, domain.ValidationError{
					RuleID:       r.ID(),
					Severity:     severity,
					RepositoryID: repoID,
					PolicyID:     policy.PolicyID,
					CheckID:      check.CheckID,
					Field:        "check_results",
					Message: fmt.Sprintf(
						"%s check %q (policy %q, repository %q) terminated with status %q",
						check.Severity, check.CheckID, policy.PolicyID, repoID, result.Status,
					),
				})
			}
		}
	}
	return findings
}

// ruleEV005 — evidence must not be stale. When a BranchTipResolver is
// wired, EV-005 fetches the current head SHA of the evidence's branch
// and emits an error if it differs from evidence.HeadCommit. Without
// the resolver, the rule is a no-op.
type ruleEV005 struct {
	deps evidenceRuleDeps
}

func (r *ruleEV005) ID() string { return RuleEvidenceStale }
func (r *ruleEV005) Classification() domain.ViolationClassification {
	return domain.ViolationExecutionEvidence
}

func (r *ruleEV005) Evaluate(ctx context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	run, ok := r.deps.resolveRun(ctx, proj)
	if !ok {
		return nil
	}
	if r.deps.evidence == nil || r.deps.branchTip == nil {
		return nil
	}
	var errs []domain.ValidationError
	for _, repoID := range sortedRepoIDs(run) {
		ev, err := r.deps.evidence(ctx, run.RunID, repoID)
		if err != nil || ev == nil {
			continue
		}
		branch := expectedBranch(run, repoID)
		if branch == "" {
			continue
		}
		tip, err := r.deps.branchTip(ctx, repoID, branch)
		if err != nil || tip == "" {
			// Resolver couldn't determine current tip — treat as
			// "staleness undecidable". EV-002 still catches branch
			// mismatch with the run; EV-005 just goes silent here.
			continue
		}
		if tip != ev.HeadCommit {
			errs = append(errs, domain.ValidationError{
				RuleID:       r.ID(),
				Severity:     "error",
				RepositoryID: repoID,
				Field:        "head_commit",
				Message: fmt.Sprintf(
					"evidence for repository %q is stale: claims head %q but branch %q is at %q",
					repoID, ev.HeadCommit, branch, tip,
				),
			})
		}
	}
	return errs
}

// checkResultIndex returns a CheckID-keyed view of CheckResults so
// rules can look up by CheckID in O(1) instead of O(n²) over every
// (policy_check, evidence_row) combination.
func checkResultIndex(rows []domain.CheckResult) map[string]domain.CheckResult {
	out := make(map[string]domain.CheckResult, len(rows))
	for _, row := range rows {
		out[row.CheckID] = row
	}
	return out
}

// isSuccessfulCheckStatus mirrors the "counts as success" column of
// execution-evidence.md §4.3.1: passed and skipped both clear the
// blocking-checks gate; pending/running/failed/error do not.
//
// Skipped is a deliberate success: a declared-and-not-applicable check
// is a satisfied requirement, not a missing one. Error is a deliberate
// failure: a runner crash means we have no verdict, so the policy
// cannot be cleared.
func isSuccessfulCheckStatus(status domain.CheckStatus) bool {
	return status == domain.CheckStatusPassed || status == domain.CheckStatusSkipped
}
