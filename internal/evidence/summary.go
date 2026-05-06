package evidence

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

// RunEvidenceStatus is the run-level rollup of per-repository
// EvidenceStatus values. Distinct from [domain.EvidenceStatus] because
// the on-disk record only knows {pending, passed, failed} for itself;
// the run-level rollup must also surface "missing" — at least one
// affected repository has no evidence file at all. Operators consume
// this as the headline status; the per-repository details below carry
// the full picture.
type RunEvidenceStatus string

const (
	// RunEvidenceMissing — at least one affected repository has no
	// evidence record yet. Dominates failed/pending/passed because a
	// missing required artifact is a higher-impact gap than a
	// failed-but-present one (operators must create the file before
	// publication can proceed; failed-but-present already says exactly
	// what to fix).
	RunEvidenceMissing RunEvidenceStatus = "missing"

	// RunEvidenceFailed — every affected repo has evidence; at least
	// one repo's stored Status is failed.
	RunEvidenceFailed RunEvidenceStatus = "failed"

	// RunEvidencePending — every affected repo has evidence; at least
	// one is pending; none is failed.
	RunEvidencePending RunEvidenceStatus = "pending"

	// RunEvidencePassed — every affected repo has evidence and every
	// stored Status is passed.
	RunEvidencePassed RunEvidenceStatus = "passed"

	// RunEvidenceUnknown — the run has no affected repositories the
	// summary could enumerate. Returned for an empty AffectedRepositories
	// list so the headline is not silently "passed" when the rollup
	// is undefined.
	RunEvidenceUnknown RunEvidenceStatus = "unknown"
)

// CheckSummary projects a [domain.CheckResult] plus its required-vs-advisory
// classification. Required is computed at projection time (not stored
// on CheckResult) because it depends on the parent record's
// RequiredChecks/AdvisoryChecks lists. EvidenceURI is preserved so the
// query API satisfies AC #3 — raw logs are referenced, not embedded.
type CheckSummary struct {
	CheckID     string                   `json:"check_id"             yaml:"check_id"`
	Name        string                   `json:"name,omitempty"       yaml:"name,omitempty"`
	Status      domain.CheckStatus       `json:"status"               yaml:"status"`
	Required    bool                     `json:"required"             yaml:"required"`
	Producer    domain.CheckProducerKind `json:"producer,omitempty"   yaml:"producer,omitempty"`
	ProducedBy  string                   `json:"produced_by,omitempty" yaml:"produced_by,omitempty"`
	Summary     string                   `json:"summary,omitempty"    yaml:"summary,omitempty"`
	EvidenceURI string                   `json:"evidence_uri,omitempty" yaml:"evidence_uri,omitempty"`
	StartedAt   *time.Time               `json:"started_at,omitempty"  yaml:"started_at,omitempty"`
	CompletedAt *time.Time               `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
}

// RepositorySummary is the per-repo entry in [RunSummary]. Present
// distinguishes "evidence file committed" from "evidence file
// missing"; Reason carries an operator-readable explanation when
// Present is false (load failure, file not committed, etc).
//
// Required/Failing/Pending/Missing CheckIDs are exposed as flat string
// slices so dashboards can pin a known check ID without iterating the
// row list. They are deterministically sorted (lexical) for stable
// snapshots.
type RepositorySummary struct {
	RepositoryID string `json:"repository_id" yaml:"repository_id"`

	// Present reports whether an evidence record was located for this
	// (run, repository) pair. False covers both "file not committed
	// yet" and "load failed" — Reason carries the distinction.
	Present bool   `json:"present" yaml:"present"`
	Reason  string `json:"reason,omitempty" yaml:"reason,omitempty"`

	Status      domain.EvidenceStatus `json:"status,omitempty"      yaml:"status,omitempty"`
	BranchName  string                `json:"branch_name,omitempty" yaml:"branch_name,omitempty"`
	BaseCommit  string                `json:"base_commit,omitempty" yaml:"base_commit,omitempty"`
	HeadCommit  string                `json:"head_commit,omitempty" yaml:"head_commit,omitempty"`
	Actor       string                `json:"actor,omitempty"       yaml:"actor,omitempty"`
	GeneratedAt *time.Time            `json:"generated_at,omitempty" yaml:"generated_at,omitempty"`

	Checks         []CheckSummary               `json:"checks,omitempty"          yaml:"checks,omitempty"`
	FailingChecks  []string                     `json:"failing_checks,omitempty"  yaml:"failing_checks,omitempty"`
	PendingChecks  []string                     `json:"pending_checks,omitempty"  yaml:"pending_checks,omitempty"`
	MissingChecks  []string                     `json:"missing_checks,omitempty"  yaml:"missing_checks,omitempty"`
	Policies       []domain.ValidationPolicyRef `json:"policies,omitempty"        yaml:"policies,omitempty"`
	ChangedPaths   *domain.ChangedPathsSummary  `json:"changed_paths,omitempty"   yaml:"changed_paths,omitempty"`
}

// RunSummary is the top-level evidence summary for a Run, returned by
// the run inspect API. Repositories is sorted lexically by RepositoryID
// so the JSON/YAML response is deterministic for snapshot consumers
// and runbook dashboards.
type RunSummary struct {
	RunID    string            `json:"run_id"    yaml:"run_id"`
	TaskPath string            `json:"task_path" yaml:"task_path"`
	Status   RunEvidenceStatus `json:"status"    yaml:"status"`

	Repositories        []RepositorySummary `json:"repositories"                    yaml:"repositories"`
	MissingRepositories []string            `json:"missing_repositories,omitempty"  yaml:"missing_repositories,omitempty"`
	FailingRepositories []string            `json:"failing_repositories,omitempty"  yaml:"failing_repositories,omitempty"`
	PendingRepositories []string            `json:"pending_repositories,omitempty"  yaml:"pending_repositories,omitempty"`
}

// BuildSummary loads evidence for every affected repository on the
// given Run and returns the deterministic summary. The refs slice is
// the priority-ordered list of Git refs to consult for each repo;
// the first ref that yields a found+valid evidence file wins (see
// [RefsForRun] for the policy production wires). An empty refs list
// collapses to [PrimaryAuthoritativeBranchDefault] (a named branch,
// never the symbolic `HEAD`) so direct callers cannot accidentally
// probe the working tree's mutable current branch — that
// invariant is the codex-pass-6 / pass-8 finding for iter 22.
//
// Per-repository errors (load failure, parse failure, validation
// failure) are recorded as Present=false / Reason="..." rather than
// returned as errors — operators must see partial state (the AC #4
// mandate) even when one repo's evidence file is corrupt. The
// function returns an error only for argument-shape problems (nil
// run); the per-repo error tolerance is intentional and matches the
// iter-21 EV-001 design where a missing record IS the finding rather
// than a precondition for evaluation.
//
// Determinism: AffectedRepositories may be in declared order
// (primary first) but the summary sorts repositories lexically. This
// matches the loop's "sort emitted findings deterministically" rule
// (loop-state.md, iter 21) and keeps the response byte-stable across
// runs that touch the same repo set.
func BuildSummary(ctx context.Context, reader GitReader, refs []string, run *domain.Run) (*RunSummary, error) {
	if run == nil {
		return nil, fmt.Errorf("evidence: nil run")
	}
	if len(refs) == 0 {
		refs = []string{PrimaryAuthoritativeBranchDefault}
	}

	repos := append([]string(nil), run.AffectedRepositories...)
	sort.Strings(repos)

	summary := &RunSummary{
		RunID:        run.RunID,
		TaskPath:     run.TaskPath,
		Repositories: make([]RepositorySummary, 0, len(repos)),
	}

	for _, repoID := range repos {
		summary.Repositories = append(summary.Repositories, summarizeRepository(ctx, reader, refs, run, repoID))
	}

	classify(summary)
	return summary, nil
}

// summarizeRepository computes one repository's slice of the summary.
// Tries each ref in priority order; the first ref that yields a
// found+valid evidence file wins. If every ref returns Found=false
// (or errors), Present=false with a Reason that names what was
// tried so operators can debug a producer-side path mismatch.
//
// Load failures (read errors, parse errors, validation errors) on
// any one ref do not abort the loop — a transient outage on the run
// branch followed by a successful read on HEAD is a recovery, not a
// failure. The first error encountered is preserved as a fallback
// Reason for the case where every ref returned an error.
func summarizeRepository(ctx context.Context, reader GitReader, refs []string, run *domain.Run, repoID string) RepositorySummary {
	out := RepositorySummary{RepositoryID: repoID}

	var firstErr error
	for _, ref := range refs {
		res, err := Load(ctx, reader, ref, run.RunID, repoID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !res.Found {
			continue
		}
		populateFromEvidence(&out, res.Evidence)
		return out
	}

	out.Present = false
	switch {
	case firstErr != nil:
		out.Reason = "evidence load failed: " + firstErr.Error()
	case len(refs) == 1:
		out.Reason = fmt.Sprintf("evidence file not committed at %s@%s", EvidencePath(run.RunID, repoID), refs[0])
	default:
		out.Reason = fmt.Sprintf("evidence file not committed at %s on any of refs %v", EvidencePath(run.RunID, repoID), refs)
	}
	return out
}

// populateFromEvidence fills the present-evidence fields on out from
// a loaded ExecutionEvidence. Split out so the load loop's
// successful-find branch is short and obviously single-purpose.
func populateFromEvidence(out *RepositorySummary, ev *domain.ExecutionEvidence) {
	out.Present = true
	out.Status = ev.Status
	out.BranchName = ev.BranchName
	out.BaseCommit = ev.BaseCommit
	out.HeadCommit = ev.HeadCommit
	out.Actor = ev.Actor
	if !ev.GeneratedAt.IsZero() {
		t := ev.GeneratedAt
		out.GeneratedAt = &t
	}
	if len(ev.ValidationPolicies) > 0 {
		out.Policies = append(out.Policies, ev.ValidationPolicies...)
	}
	cp := ev.ChangedPaths
	out.ChangedPaths = &cp

	required := stringSet(ev.RequiredChecks)
	resultByID := make(map[string]domain.CheckResult, len(ev.CheckResults))
	for _, r := range ev.CheckResults {
		resultByID[r.CheckID] = r
	}

	out.Checks = projectChecks(ev, required, resultByID)
	out.FailingChecks, out.PendingChecks, out.MissingChecks = classifyCheckIDs(ev.RequiredChecks, resultByID)
}

// projectChecks builds the per-check projection list. The list spans
// every CheckResult row (covering both required and advisory) so
// dashboards can render the full producer history; required vs
// advisory is exposed via the Required flag rather than a separate
// list, so a single iteration over Checks suffices.
func projectChecks(ev *domain.ExecutionEvidence, required map[string]struct{}, resultByID map[string]domain.CheckResult) []CheckSummary {
	if len(ev.CheckResults) == 0 {
		return nil
	}
	out := make([]CheckSummary, 0, len(ev.CheckResults))
	ids := make([]string, 0, len(resultByID))
	for id := range resultByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := resultByID[id]
		c := CheckSummary{
			CheckID:     r.CheckID,
			Name:        r.Name,
			Status:      r.Status,
			Producer:    r.Producer,
			ProducedBy:  r.ProducedBy,
			Summary:     r.Summary,
			EvidenceURI: r.EvidenceURI,
			StartedAt:   r.StartedAt,
			CompletedAt: r.CompletedAt,
		}
		if _, ok := required[id]; ok {
			c.Required = true
		}
		out = append(out, c)
	}
	return out
}

// classifyCheckIDs groups required-check IDs by their current outcome
// state. Failing covers any required check whose row is in failed or
// error; pending covers any non-terminal row (pending/running);
// missing covers required checks with no row at all. All slices are
// lexically sorted; nil when empty so the JSON/YAML output uses
// `omitempty` properly.
func classifyCheckIDs(required []string, resultByID map[string]domain.CheckResult) (failing, pending, missing []string) {
	for _, id := range required {
		r, ok := resultByID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		switch r.Status {
		case domain.CheckStatusFailed, domain.CheckStatusError:
			failing = append(failing, id)
		case domain.CheckStatusPending, domain.CheckStatusRunning:
			pending = append(pending, id)
		}
	}
	sort.Strings(failing)
	sort.Strings(pending)
	sort.Strings(missing)
	return
}

// classify computes the run-level rollup from the per-repository
// statuses and populates the parallel Missing/Failing/Pending repo ID
// lists. Priority: missing > failing > pending > passed (see
// [RunEvidenceMissing] for rationale).
func classify(s *RunSummary) {
	if len(s.Repositories) == 0 {
		s.Status = RunEvidenceUnknown
		return
	}
	for _, r := range s.Repositories {
		switch {
		case !r.Present:
			s.MissingRepositories = append(s.MissingRepositories, r.RepositoryID)
		case r.Status == domain.EvidenceStatusFailed:
			s.FailingRepositories = append(s.FailingRepositories, r.RepositoryID)
		case r.Status == domain.EvidenceStatusPending:
			s.PendingRepositories = append(s.PendingRepositories, r.RepositoryID)
		}
	}
	switch {
	case len(s.MissingRepositories) > 0:
		s.Status = RunEvidenceMissing
	case len(s.FailingRepositories) > 0:
		s.Status = RunEvidenceFailed
	case len(s.PendingRepositories) > 0:
		s.Status = RunEvidencePending
	default:
		s.Status = RunEvidencePassed
	}
}

func stringSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, s := range items {
		out[s] = struct{}{}
	}
	return out
}
