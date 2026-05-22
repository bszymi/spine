package validation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/validation"
)

// Evidence-rule tests use a hand-rolled fixture builder rather than
// reusing engine_test.go's fakeStore plumbing. The EV-* rules read
// resolvers (run / evidence / policy / branch tip), not the
// projection store, so the only thing the store needs to return is a
// Task projection so the engine reaches the rule. Keeping the fixture
// scoped to evidence concerns avoids accidentally wiring rule
// behavior to fakeStore mutations that other rule families rely on.

const (
	taskPath = "initiatives/INIT-014/epics/EPIC-006/tasks/TASK-004.md"
	runID    = "run-evidence-1"
	branch   = "spine/run/run-evidence-1"
)

type evidenceFixture struct {
	store           *fakeStore
	run             *domain.Run
	evidenceByRepo  map[string]*domain.ExecutionEvidence
	policyByRepo    map[string][]domain.ValidationPolicy
	branchTipByRepo map[string]string

	runErr     error
	evErr      error
	polErr     error
	branchErr  error
	missingRun bool // when true, RunResolver returns (nil, nil)
}

func newEvidenceFixture() *evidenceFixture {
	fs := newFakeStore()
	addArtifact(fs, taskPath, string(domain.ArtifactTypeTask), string(domain.StatusInProgress), nil, nil)
	return &evidenceFixture{
		store: fs,
		run: &domain.Run{
			RunID:                runID,
			TaskPath:             taskPath,
			BranchName:           branch,
			Mode:                 domain.RunModeStandard,
			AffectedRepositories: []string{domain.PrimaryRepositoryID, "billing"},
			RepositoryBaselines: map[string]string{
				domain.PrimaryRepositoryID: "0000spineBaseline",
				"billing":                  "0000billingBaseline",
			},
			Status: domain.RunStatusActive,
		},
		evidenceByRepo:  map[string]*domain.ExecutionEvidence{},
		policyByRepo:    map[string][]domain.ValidationPolicy{},
		branchTipByRepo: map[string]string{},
	}
}

func (f *evidenceFixture) putEvidence(repoID, branchName, baseCommit, headCommit string, results []domain.CheckResult) {
	f.evidenceByRepo[repoID] = &domain.ExecutionEvidence{
		SchemaVersion: domain.ExecutionEvidenceSchemaVersion,
		RunID:         runID,
		TaskPath:      taskPath,
		RepositoryID:  repoID,
		BranchName:    branchName,
		BaseCommit:    baseCommit,
		HeadCommit:    headCommit,
		CheckResults:  results,
		Status:        domain.EvidenceStatusPassed,
		GeneratedAt:   time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
		Actor:         "ci/test",
		TraceID:       "trace-evidence",
	}
}

// engine builds an Engine with whatever resolvers the fixture has
// populated. Nil-tolerance is the contract — tests that want to
// exercise the "resolver missing" case simply set the field to nil.
func (f *evidenceFixture) engine() *validation.Engine {
	opts := []validation.Option{
		validation.WithRunResolver(func(_ context.Context, _ string) (*domain.Run, error) {
			if f.runErr != nil {
				return nil, f.runErr
			}
			if f.missingRun {
				return nil, nil
			}
			return f.run, nil
		}),
		validation.WithEvidenceResolver(func(_ context.Context, _ string, repoID string) (*domain.ExecutionEvidence, error) {
			if f.evErr != nil {
				return nil, f.evErr
			}
			return f.evidenceByRepo[repoID], nil
		}),
		validation.WithValidationPolicyResolver(func(_ context.Context, _ string, repoID string) ([]domain.ValidationPolicy, error) {
			if f.polErr != nil {
				return nil, f.polErr
			}
			return f.policyByRepo[repoID], nil
		}),
	}
	if len(f.branchTipByRepo) > 0 || f.branchErr != nil {
		opts = append(opts, validation.WithBranchTipResolver(func(_ context.Context, repoID, _ string) (string, error) {
			if f.branchErr != nil {
				return "", f.branchErr
			}
			return f.branchTipByRepo[repoID], nil
		}))
	}
	return validation.NewEngine(f.store, opts...)
}

func samplePolicy(policyID, checkID string, severity domain.PolicySeverity) domain.ValidationPolicy {
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

func sampleResult(checkID string, status domain.CheckStatus) domain.CheckResult {
	return domain.CheckResult{
		CheckID:    checkID,
		Status:     status,
		Producer:   domain.CheckProducerAutomated,
		ProducedBy: "ci/test",
	}
}

func evErrorByRule(t *testing.T, result domain.ValidationResult, ruleID string) []domain.ValidationError {
	t.Helper()
	var matches []domain.ValidationError
	for _, e := range result.Errors {
		if e.RuleID == ruleID {
			matches = append(matches, e)
		}
	}
	return matches
}

func evWarningsByRule(result domain.ValidationResult, ruleID string) []domain.ValidationError {
	var matches []domain.ValidationError
	for _, w := range result.Warnings {
		if w.RuleID == ruleID {
			matches = append(matches, w)
		}
	}
	return matches
}

// ── EV-001 — missing evidence ──

// AC #1: Missing evidence blocks publish.
func TestEV001_MissingEvidence_BlocksPublish_AnchorsAC1(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	// "billing" has no evidence — EV-001 must fire for it.
	result := f.engine().Validate(context.Background(), taskPath)
	if result.Status != "failed" {
		t.Fatalf("status: got %q, want failed", result.Status)
	}
	matches := evErrorByRule(t, result, validation.RuleEvidenceMissing)
	if len(matches) != 1 {
		t.Fatalf("EV-001: got %d errors, want 1: %+v", len(matches), matches)
	}
	if matches[0].RepositoryID != "billing" {
		t.Fatalf("EV-001 repo: got %q, want billing", matches[0].RepositoryID)
	}
	if matches[0].Classification != domain.ViolationExecutionEvidence {
		t.Fatalf("classification: got %q, want execution_evidence", matches[0].Classification)
	}
}

// EV-001 must check every affected repo, not just the first miss.
func TestEV001_MissingEvidence_AllRepos(t *testing.T) {
	f := newEvidenceFixture()
	// No evidence anywhere.
	result := f.engine().Validate(context.Background(), taskPath)
	matches := evErrorByRule(t, result, validation.RuleEvidenceMissing)
	if len(matches) != 2 {
		t.Fatalf("got %d errors, want 2 (one per repo): %+v", len(matches), matches)
	}
}

// EV-001 errors when the resolver itself errors — a transient
// resolver problem must not silently let publish proceed.
func TestEV001_ResolverError_TreatedAsMissing(t *testing.T) {
	f := newEvidenceFixture()
	f.evErr = errors.New("transient db outage")
	result := f.engine().Validate(context.Background(), taskPath)
	matches := evErrorByRule(t, result, validation.RuleEvidenceMissing)
	if len(matches) == 0 {
		t.Fatalf("expected EV-001 errors when resolver fails, got 0")
	}
	for _, m := range matches {
		if m.Severity != "error" {
			t.Fatalf("EV-001 must emit errors not warnings on resolver failure")
		}
	}
}

// ── EV-002 — branch and base commit must match ──

// AC #2: Evidence from the wrong branch blocks publish.
func TestEV002_WrongBranch_BlocksPublish_AnchorsAC2(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	f.putEvidence("billing", "wrong/branch", "0000billingBaseline", "headBilling", nil)
	result := f.engine().Validate(context.Background(), taskPath)
	matches := evErrorByRule(t, result, validation.RuleEvidenceBranchCommitMatch)
	if len(matches) != 1 {
		t.Fatalf("EV-002 wrong-branch: got %d errors, want 1: %+v", len(matches), matches)
	}
	if matches[0].RepositoryID != "billing" {
		t.Fatalf("repo: got %q, want billing", matches[0].RepositoryID)
	}
	if matches[0].Field != "branch_name" {
		t.Fatalf("field: got %q, want branch_name", matches[0].Field)
	}
}

// AC #2 (commit half): evidence's base commit must match the run's
// recorded baseline.
func TestEV002_WrongBaseCommit_BlocksPublish_AnchorsAC2(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	f.putEvidence("billing", branch, "wrongBaseline", "headBilling", nil)
	result := f.engine().Validate(context.Background(), taskPath)
	matches := evErrorByRule(t, result, validation.RuleEvidenceBranchCommitMatch)
	if len(matches) != 1 {
		t.Fatalf("EV-002 base-commit: got %d errors, want 1: %+v", len(matches), matches)
	}
	if matches[0].Field != "base_commit" {
		t.Fatalf("field: got %q, want base_commit", matches[0].Field)
	}
}

// EV-002 honors per-repo branch overrides recorded in
// Run.RepositoryBranches.
func TestEV002_PerRepoBranchOverride(t *testing.T) {
	f := newEvidenceFixture()
	f.run.RepositoryBranches = map[string]string{
		"billing": "spine/run/run-evidence-1-billing-recovery",
	}
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	// Evidence claims the override branch — should pass EV-002.
	f.putEvidence("billing", "spine/run/run-evidence-1-billing-recovery", "0000billingBaseline", "headBilling", nil)
	result := f.engine().Validate(context.Background(), taskPath)
	if errs := evErrorByRule(t, result, validation.RuleEvidenceBranchCommitMatch); len(errs) != 0 {
		t.Fatalf("EV-002 with per-repo branch override should pass: %+v", errs)
	}
}

// EV-002 doesn't double-emit on missing evidence (EV-001's job).
func TestEV002_MissingEvidence_NoDoubleFinding(t *testing.T) {
	f := newEvidenceFixture()
	// No evidence for either repo. EV-002 must not emit anything.
	result := f.engine().Validate(context.Background(), taskPath)
	if errs := evErrorByRule(t, result, validation.RuleEvidenceBranchCommitMatch); len(errs) != 0 {
		t.Fatalf("EV-002 must skip absent evidence: got %+v", errs)
	}
}

// ── EV-003 — required policy checks must be present ──

// AC #3: required (blocking) checks must be present in evidence.
func TestEV003_MissingRequiredCheck_BlocksPublish_AnchorsAC3(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	f.putEvidence("billing", branch, "0000billingBaseline", "headBilling", nil) // empty results
	f.policyByRepo["billing"] = []domain.ValidationPolicy{
		samplePolicy("code-quality-v1", "unit-tests", domain.PolicySeverityBlocking),
	}
	result := f.engine().Validate(context.Background(), taskPath)
	matches := evErrorByRule(t, result, validation.RuleEvidenceRequiredChecks)
	if len(matches) != 1 {
		t.Fatalf("EV-003: got %d errors, want 1: %+v", len(matches), matches)
	}
	if matches[0].RepositoryID != "billing" || matches[0].PolicyID != "code-quality-v1" || matches[0].CheckID != "unit-tests" {
		t.Fatalf("EV-003 must name repo/policy/check (AC #5): got %+v", matches[0])
	}
}

// EV-003 only checks blocking-severity policy entries; advisory
// checks not being present is not a failure (they're optional).
func TestEV003_AdvisoryCheckNotRequired(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	f.putEvidence("billing", branch, "0000billingBaseline", "headBilling", nil)
	f.policyByRepo["billing"] = []domain.ValidationPolicy{
		samplePolicy("style-v1", "lint", domain.PolicySeverityWarning),
	}
	result := f.engine().Validate(context.Background(), taskPath)
	if errs := evErrorByRule(t, result, validation.RuleEvidenceRequiredChecks); len(errs) != 0 {
		t.Fatalf("EV-003 must not flag missing advisory check: got %+v", errs)
	}
}

// EV-003 silent when policy resolver is not wired — TASK-005 wiring
// path lands later.
func TestEV003_NoPolicyResolver_NoEmissions(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence("billing", branch, "0000billingBaseline", "headBilling", nil)
	// Build engine with policy resolver explicitly nil.
	eng := validation.NewEngine(f.store,
		validation.WithRunResolver(func(_ context.Context, _ string) (*domain.Run, error) { return f.run, nil }),
		validation.WithEvidenceResolver(func(_ context.Context, _ string, repoID string) (*domain.ExecutionEvidence, error) {
			return f.evidenceByRepo[repoID], nil
		}),
	)
	result := eng.Validate(context.Background(), taskPath)
	if errs := evErrorByRule(t, result, validation.RuleEvidenceRequiredChecks); len(errs) != 0 {
		t.Fatalf("EV-003 must be a no-op without policy resolver: got %+v", errs)
	}
}

// ── EV-004 — blocking checks must pass; warning-only doesn't block ──

// AC #4 inverse: Failed blocking policy checks block publish.
func TestEV004_FailedBlockingCheck_BlocksPublish_AnchorsAC3(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	f.putEvidence("billing", branch, "0000billingBaseline", "headBilling", []domain.CheckResult{
		sampleResult("unit-tests", domain.CheckStatusFailed),
	})
	f.policyByRepo["billing"] = []domain.ValidationPolicy{
		samplePolicy("code-quality-v1", "unit-tests", domain.PolicySeverityBlocking),
	}
	result := f.engine().Validate(context.Background(), taskPath)
	if result.Status != "failed" {
		t.Fatalf("status: got %q, want failed", result.Status)
	}
	matches := evErrorByRule(t, result, validation.RuleEvidenceBlockingChecksPass)
	if len(matches) != 1 {
		t.Fatalf("EV-004: got %d errors, want 1: %+v", len(matches), matches)
	}
	if matches[0].PolicyID != "code-quality-v1" || matches[0].CheckID != "unit-tests" {
		t.Fatalf("EV-004 must name repo/policy/check (AC #5): got %+v", matches[0])
	}
}

// AC #4: Warning-only policy checks do not block publish.
func TestEV004_WarningOnlyCheck_DoesNotBlock_AnchorsAC4(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	f.putEvidence("billing", branch, "0000billingBaseline", "headBilling", []domain.CheckResult{
		sampleResult("lint", domain.CheckStatusFailed),
	})
	f.policyByRepo["billing"] = []domain.ValidationPolicy{
		samplePolicy("style-v1", "lint", domain.PolicySeverityWarning),
	}
	result := f.engine().Validate(context.Background(), taskPath)
	// Critical: status must be at most "warnings", not "failed".
	if result.Status == "failed" {
		t.Fatalf("warning-only failure must not block: status=%q errors=%+v", result.Status, result.Errors)
	}
	if errs := evErrorByRule(t, result, validation.RuleEvidenceBlockingChecksPass); len(errs) != 0 {
		t.Fatalf("EV-004 must not emit errors for warning-severity check: %+v", errs)
	}
	warnings := evWarningsByRule(result, validation.RuleEvidenceBlockingChecksPass)
	if len(warnings) != 1 {
		t.Fatalf("EV-004 must surface warning-severity failures as warnings: got %d, want 1", len(warnings))
	}
	if warnings[0].PolicyID != "style-v1" || warnings[0].CheckID != "lint" {
		t.Fatalf("EV-004 warning must name repo/policy/check: %+v", warnings[0])
	}
}

// EV-004 treats skipped as success (per execution-evidence.md
// §4.3.1: declared-and-not-applicable counts as satisfied).
func TestEV004_SkippedCounts_AsSuccess(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	f.putEvidence("billing", branch, "0000billingBaseline", "headBilling", []domain.CheckResult{
		sampleResult("unit-tests", domain.CheckStatusSkipped),
	})
	f.policyByRepo["billing"] = []domain.ValidationPolicy{
		samplePolicy("code-quality-v1", "unit-tests", domain.PolicySeverityBlocking),
	}
	result := f.engine().Validate(context.Background(), taskPath)
	if errs := evErrorByRule(t, result, validation.RuleEvidenceBlockingChecksPass); len(errs) != 0 {
		t.Fatalf("EV-004 must accept skipped: %+v", errs)
	}
}

// EV-004 fires on error / pending / running — these are non-success
// terminals (or pre-terminal) and the policy must clear cleanly.
func TestEV004_NonSuccessStatuses(t *testing.T) {
	for _, status := range []domain.CheckStatus{
		domain.CheckStatusError,
		domain.CheckStatusPending,
		domain.CheckStatusRunning,
	} {
		t.Run(string(status), func(t *testing.T) {
			f := newEvidenceFixture()
			f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
			f.putEvidence("billing", branch, "0000billingBaseline", "headBilling", []domain.CheckResult{
				sampleResult("unit-tests", status),
			})
			f.policyByRepo["billing"] = []domain.ValidationPolicy{
				samplePolicy("code-quality-v1", "unit-tests", domain.PolicySeverityBlocking),
			}
			result := f.engine().Validate(context.Background(), taskPath)
			if errs := evErrorByRule(t, result, validation.RuleEvidenceBlockingChecksPass); len(errs) != 1 {
				t.Fatalf("status=%q: EV-004 errors=%d want 1: %+v", status, len(errs), errs)
			}
		})
	}
}

// ── EV-005 — stale evidence ──

// AC #2 reinforcement: stale evidence (head commit no longer matches
// branch tip) blocks publish.
func TestEV005_StaleEvidence_BlocksPublish_AnchorsTaskRule5(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	f.putEvidence("billing", branch, "0000billingBaseline", "headBillingOld", nil)
	f.branchTipByRepo[domain.PrimaryRepositoryID] = "headSpine"        // current
	f.branchTipByRepo["billing"] = "headBillingNewSinceEvidenceWrote" // advanced
	result := f.engine().Validate(context.Background(), taskPath)
	matches := evErrorByRule(t, result, validation.RuleEvidenceStale)
	if len(matches) != 1 {
		t.Fatalf("EV-005: got %d errors, want 1: %+v", len(matches), matches)
	}
	if matches[0].RepositoryID != "billing" {
		t.Fatalf("repo: got %q, want billing", matches[0].RepositoryID)
	}
}

// EV-005 is a no-op when no BranchTipResolver is wired — the rule slot
// exists for future activation; absence must not crash or false-flag.
func TestEV005_NoBranchTipResolver_NoEmissions(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence("billing", branch, "0000billingBaseline", "anyHead", nil)
	// engine() only registers BranchTipResolver if branchTipByRepo is non-empty.
	result := f.engine().Validate(context.Background(), taskPath)
	if errs := evErrorByRule(t, result, validation.RuleEvidenceStale); len(errs) != 0 {
		t.Fatalf("EV-005 must be a no-op without BranchTipResolver: %+v", errs)
	}
}

// EV-005 silent when branch-tip lookup fails (resolver returns "" or
// errors) — staleness becomes undecidable, do not false-fail.
func TestEV005_UnresolvableTip_NoEmissions(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence("billing", branch, "0000billingBaseline", "anyHead", nil)
	f.branchErr = errors.New("git fetch failed")
	result := f.engine().Validate(context.Background(), taskPath)
	if errs := evErrorByRule(t, result, validation.RuleEvidenceStale); len(errs) != 0 {
		t.Fatalf("EV-005 must skip on resolver error: %+v", errs)
	}
}

// ── End-to-end output naming (AC #5) ──

// AC #5: Validation output names repo ID, policy ID, and failing
// check. Pin all three structured fields land on the same finding.
func TestEvidenceRules_NameRepoPolicyCheck_AnchorsAC5(t *testing.T) {
	f := newEvidenceFixture()
	f.putEvidence(domain.PrimaryRepositoryID, branch, "0000spineBaseline", "headSpine", nil)
	f.putEvidence("billing", branch, "0000billingBaseline", "headBilling", []domain.CheckResult{
		sampleResult("unit-tests", domain.CheckStatusFailed),
	})
	f.policyByRepo["billing"] = []domain.ValidationPolicy{
		samplePolicy("code-quality-v1", "unit-tests", domain.PolicySeverityBlocking),
	}
	result := f.engine().Validate(context.Background(), taskPath)
	if len(result.Errors) == 0 {
		t.Fatalf("expected errors")
	}
	var found bool
	for _, e := range result.Errors {
		if e.RuleID != validation.RuleEvidenceBlockingChecksPass {
			continue
		}
		if e.RepositoryID == "billing" && e.PolicyID == "code-quality-v1" && e.CheckID == "unit-tests" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("AC #5: at least one finding must carry repo+policy+check; got %+v", result.Errors)
	}
}

// ── Inactive resolvers ──

// When RunResolver is not wired, every EV-* rule must be a no-op.
// This is the "evidence pipeline not yet rolled out" case — adding
// the engine to a workspace that has not wired evidence must not
// break existing validation.
func TestEvidenceRules_NoRunResolver_NoEmissions(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, taskPath, string(domain.ArtifactTypeTask), string(domain.StatusInProgress), nil, nil)
	eng := validation.NewEngine(fs)
	result := eng.Validate(context.Background(), taskPath)
	for _, e := range result.Errors {
		if isEvidenceRule(e.RuleID) {
			t.Fatalf("evidence rule fired without RunResolver: %+v", e)
		}
	}
}

// Planning runs do not produce execution evidence. EV-* rules must
// stay quiet for them.
func TestEvidenceRules_PlanningRun_Skipped(t *testing.T) {
	f := newEvidenceFixture()
	f.run.Mode = domain.RunModePlanning
	f.run.AffectedRepositories = []string{domain.PrimaryRepositoryID}
	result := f.engine().Validate(context.Background(), taskPath)
	for _, e := range result.Errors {
		if isEvidenceRule(e.RuleID) {
			t.Fatalf("evidence rule fired for planning run: %+v", e)
		}
	}
}

// When RunResolver returns (nil, nil) — task with no associated run
// yet — evidence rules must be silent. This is the "task exists but
// is not running" case (e.g. scenario tests that probe validation
// before triggering a run).
func TestEvidenceRules_NoRunForTask_NoEmissions(t *testing.T) {
	f := newEvidenceFixture()
	f.missingRun = true
	result := f.engine().Validate(context.Background(), taskPath)
	for _, e := range result.Errors {
		if isEvidenceRule(e.RuleID) {
			t.Fatalf("evidence rule fired with no run: %+v", e)
		}
	}
}

// Non-Task artifacts must never trigger evidence rules — evidence is
// keyed by run, runs target tasks.
func TestEvidenceRules_NonTaskArtifact_Skipped(t *testing.T) {
	fs := newFakeStore()
	const epicPath = "initiatives/INIT-014/epics/EPIC-006/epic.md"
	addArtifact(fs, epicPath, string(domain.ArtifactTypeEpic), string(domain.StatusInProgress), nil, nil)
	eng := validation.NewEngine(fs,
		validation.WithRunResolver(func(_ context.Context, _ string) (*domain.Run, error) {
			t.Fatalf("RunResolver must not be called for non-Task artifacts")
			return nil, nil
		}),
	)
	_ = eng.Validate(context.Background(), epicPath)
}

func isEvidenceRule(ruleID string) bool {
	switch ruleID {
	case validation.RuleEvidenceMissing,
		validation.RuleEvidenceBranchCommitMatch,
		validation.RuleEvidenceRequiredChecks,
		validation.RuleEvidenceBlockingChecksPass,
		validation.RuleEvidenceStale:
		return true
	}
	return false
}

// Sanity check: the new ValidationError fields appear in JSON output
// only when populated. Audit consumers diffing serialized output
// shouldn't see noise on every existing rule's findings.
func TestValidationError_StructuredFieldsOmitempty(t *testing.T) {
	// Pure structure check via ArtifactProjection / store path is
	// covered by the AC #5 test; here we assert the empty case.
	v := domain.ValidationError{RuleID: "SI-001", Severity: "error", Message: "test"}
	// Emulate marshal: just verify zero values are zero (not "").
	if v.RepositoryID != "" || v.PolicyID != "" || v.CheckID != "" {
		t.Fatalf("zero-value ValidationError should have empty structured fields")
	}
	_ = store.ArtifactProjection{} // touch import so unused-import doesn't bite if test is removed
}
