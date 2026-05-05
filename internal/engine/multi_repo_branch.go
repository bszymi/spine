package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
)

// branchCreationResult tracks which repos got which kinds of refs
// during multi-repo branch creation. Two slices in creation order so
// rollback can iterate deterministically and clean up the exact set
// of refs that exist when a later step (another repo's create, the
// caller's store.CreateRun) fails.
type branchCreationResult struct {
	local  []string // repo IDs that received a local branch
	remote []string // repo IDs that received a remote branch via auto-push
}

// createRunBranches creates the run branch on every affected repository
// (INIT-014 EPIC-004 TASK-002 + TASK-003). The primary branch is cut
// from HEAD against the orchestrator's primary GitOperator; every
// non-primary affected repo's branch is cut from that repo's default
// branch against the per-repo client returned by RepositoryGitClients.
// When auto-push is enabled, each repo's local branch is pushed
// immediately after creation so a later failure rolls back symmetric
// refs: every remote ref we placed gets a DeleteRemoteBranch alongside
// its local DeleteBranch.
//
// As each branch is created, the parent SHA (the tip the new ref is cut
// from) is captured into run.RepositoryBaselines so the assignment
// payload can surface it to runners as `commit_baseline` per
// [Multi-Repository Integration §5](/architecture/multi-repository-integration.md)
// and INIT-014 EPIC-004 TASK-005. Baseline lookup is best-effort: a
// failed Head() call logs a warning and proceeds without a baseline
// for that repo (the assignment payload then omits commit_baseline,
// matching the omitempty contract on AssignmentContext.CommitBaseline).
//
// On success it returns the branchCreationResult listing repos that
// received local and remote refs respectively. Callers pass it to
// rollbackRunBranches if a later step (e.g. store.CreateRun) fails.
//
// On failure it returns the wrapped error including the repository ID
// that failed and rolls back already-created refs before returning.
// Push errors during creation are warn-and-continue (matching the
// pre-multi-repo single-repo semantic — push is best-effort and the
// run still proceeds without a remote ref); only local CreateBranch
// failures abort the chain.
func (o *Orchestrator) createRunBranches(ctx context.Context, run *domain.Run) (branchCreationResult, error) {
	var result branchCreationResult

	// Refuse a multi-repo run when per-repo wiring is missing. Silent
	// degradation would persist a run with phantom AffectedRepositories
	// — code repos listed without a backing branch — which downstream
	// execution and cleanup would then operate on. The legacy
	// primary-only path (no wiring + no declared code repos) is
	// unaffected.
	hasCodeRepo := false
	for _, repoID := range run.AffectedRepositories {
		if repoID != "" && repoID != repository.PrimaryRepositoryID {
			hasCodeRepo = true
			break
		}
	}
	if hasCodeRepo && (o.repoClients == nil || o.repositories == nil) {
		return result, domain.NewError(domain.ErrPrecondition,
			"multi-repo run requires WithRepositoryGitClients and WithRepositoryResolver wirings")
	}

	log := observe.Logger(ctx)
	pushOn := autoPushEnabled()

	// Capture the primary repo's HEAD SHA *before* CreateBranch advances
	// it, so the recorded baseline is the cut-from tip rather than the
	// post-create tip. Best-effort: a Head() error is logged and the
	// baseline is left unset for spine.
	if sha, err := o.git.Head(ctx); err == nil && sha != "" {
		captureRepoBaseline(run, repository.PrimaryRepositoryID, sha)
	} else if err != nil {
		log.Warn("baseline capture: failed to read primary HEAD",
			"phase", "create",
			"run_id", run.RunID,
			"repository_id", repository.PrimaryRepositoryID,
			"error", err)
	}

	if err := o.git.CreateBranch(ctx, run.BranchName, "HEAD"); err != nil {
		return result, fmt.Errorf("create run branch on %q: %w", repository.PrimaryRepositoryID, err)
	}
	result.local = append(result.local, repository.PrimaryRepositoryID)
	if pushOn {
		if err := o.git.PushBranch(ctx, "origin", run.BranchName); err != nil {
			log.Warn("auto-push: failed to push run branch",
				"phase", "create",
				"run_id", run.RunID,
				"repository_id", repository.PrimaryRepositoryID,
				"branch", run.BranchName,
				"error", err)
		} else {
			result.remote = append(result.remote, repository.PrimaryRepositoryID)
		}
	}

	if !hasCodeRepo {
		return result, nil
	}

	for _, repoID := range run.AffectedRepositories {
		if repoID == "" || repoID == repository.PrimaryRepositoryID {
			continue
		}
		client, base, err := o.resolveCodeRepoForBranch(ctx, repoID)
		if err != nil {
			o.rollbackRunBranches(ctx, run, result)
			return result, fmt.Errorf("create run branch on %q: %w", repoID, err)
		}
		// Baseline capture for the code repo's run branch. Resolve the
		// configured `base` (the repo's default branch) to a SHA so
		// the recorded baseline matches whatever `client.CreateBranch
		// (ctx, run.BranchName, base)` cuts from in the next call —
		// independent of where the cached repo's HEAD currently
		// points. Falling back to Head() would mis-record the baseline
		// when the cached repo is checked out to a non-default branch
		// (e.g. a reused worktree from a prior run). Best-effort: a
		// RefSHA() error is logged and the baseline left unset.
		if sha, err := client.RefSHA(ctx, base); err == nil && sha != "" {
			captureRepoBaseline(run, repoID, sha)
		} else if err != nil {
			log.Warn("baseline capture: failed to resolve code repo base ref",
				"phase", "create",
				"run_id", run.RunID,
				"repository_id", repoID,
				"base", base,
				"error", err)
		}
		if err := client.CreateBranch(ctx, run.BranchName, base); err != nil {
			o.rollbackRunBranches(ctx, run, result)
			return result, fmt.Errorf("create run branch on %q: %w", repoID, err)
		}
		result.local = append(result.local, repoID)
		if pushOn {
			if err := client.PushBranch(ctx, "origin", run.BranchName); err != nil {
				log.Warn("auto-push: failed to push run branch",
					"phase", "create",
					"run_id", run.RunID,
					"repository_id", repoID,
					"branch", run.BranchName,
					"error", err)
			} else {
				result.remote = append(result.remote, repoID)
			}
		}
	}
	// RepositoryBranches stays nil today: every affected repo gets the
	// same run branch name, and the field is documented as "fill in
	// only when divergent state needs to be tracked." Recovery and
	// per-repo divergence are downstream tasks.
	return result, nil
}

// captureRepoBaseline records the parent SHA the run's branch was cut
// from in repoID. Allocates run.RepositoryBaselines lazily so primary-
// only single-repo runs do not pay for an empty map.
func captureRepoBaseline(run *domain.Run, repoID, sha string) {
	if run.RepositoryBaselines == nil {
		run.RepositoryBaselines = make(map[string]string, len(run.AffectedRepositories))
	}
	run.RepositoryBaselines[repoID] = sha
}

// resolveCodeRepoForBranch returns the per-repo client and that repo's
// default branch — the two values needed to create the run branch on a
// code repository. Caller has already gated on the wiring being
// present.
func (o *Orchestrator) resolveCodeRepoForBranch(ctx context.Context, repoID string) (codeRepoBranchClient, string, error) {
	repo, err := o.repositories.Lookup(ctx, repoID)
	if err != nil {
		return nil, "", err
	}
	if repo.DefaultBranch == "" {
		return nil, "", domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository %q has no default_branch configured", repoID))
	}
	client, err := o.repoClients.Client(ctx, repoID)
	if err != nil {
		return nil, "", err
	}
	return client, repo.DefaultBranch, nil
}

// rollbackRunBranches best-effort deletes refs created during
// createRunBranches. Remote refs go first so a partial rollback never
// leaves a remote ref orphaned past its local branch — easier to
// re-create with a clean slate next attempt. Errors are logged with
// structured fields but never surfaced: the caller already has the
// original startup error to return, and a secondary delete failure
// must not mask it.
func (o *Orchestrator) rollbackRunBranches(ctx context.Context, run *domain.Run, result branchCreationResult) {
	log := observe.Logger(ctx)

	for _, repoID := range result.remote {
		o.deleteRollbackRemote(ctx, log, run, repoID)
	}
	for _, repoID := range result.local {
		o.deleteRollbackLocal(ctx, log, run, repoID)
	}
}

func (o *Orchestrator) deleteRollbackRemote(ctx context.Context, log *slog.Logger, run *domain.Run, repoID string) {
	if repoID == repository.PrimaryRepositoryID {
		if err := o.git.DeleteRemoteBranch(ctx, "origin", run.BranchName); err != nil {
			log.Warn("rollback: failed to delete remote run branch",
				"phase", "rollback",
				"scope", "remote",
				"run_id", run.RunID,
				"repository_id", repository.PrimaryRepositoryID,
				"branch", run.BranchName,
				"error", err)
		}
		return
	}
	client, err := o.codeRepoClient(ctx, repoID)
	if err != nil {
		log.Warn("rollback: failed to resolve code repo client",
			"phase", "rollback",
			"scope", "remote",
			"run_id", run.RunID,
			"repository_id", repoID,
			"branch", run.BranchName,
			"error", err)
		return
	}
	if err := client.DeleteRemoteBranch(ctx, "origin", run.BranchName); err != nil {
		log.Warn("rollback: failed to delete remote run branch",
			"phase", "rollback",
			"scope", "remote",
			"run_id", run.RunID,
			"repository_id", repoID,
			"branch", run.BranchName,
			"error", err)
	}
}

func (o *Orchestrator) deleteRollbackLocal(ctx context.Context, log *slog.Logger, run *domain.Run, repoID string) {
	if repoID == repository.PrimaryRepositoryID {
		if err := o.git.DeleteBranch(ctx, run.BranchName); err != nil {
			log.Warn("rollback: failed to delete local run branch",
				"phase", "rollback",
				"scope", "local",
				"run_id", run.RunID,
				"repository_id", repository.PrimaryRepositoryID,
				"branch", run.BranchName,
				"error", err)
		}
		return
	}
	client, err := o.codeRepoClient(ctx, repoID)
	if err != nil {
		log.Warn("rollback: failed to resolve code repo client",
			"phase", "rollback",
			"scope", "local",
			"run_id", run.RunID,
			"repository_id", repoID,
			"branch", run.BranchName,
			"error", err)
		return
	}
	if err := client.DeleteBranch(ctx, run.BranchName); err != nil {
		log.Warn("rollback: failed to delete local run branch",
			"phase", "rollback",
			"scope", "local",
			"run_id", run.RunID,
			"repository_id", repoID,
			"branch", run.BranchName,
			"error", err)
	}
}

// codeRepoClient returns just the client for a non-primary repo,
// adapted to the local interface.
func (o *Orchestrator) codeRepoClient(ctx context.Context, repoID string) (codeRepoBranchClient, error) {
	if o.repoClients == nil {
		return nil, fmt.Errorf("repository git clients not wired")
	}
	return o.repoClients.Client(ctx, repoID)
}

// codeRepoBranchClient is the subset of git.GitClient the run-start
// path needs against a code repository: resolve a ref (for baseline
// capture against the cut-from base, independent of the working-tree
// HEAD), create, delete, push, and remote-delete the run branch.
// Defined locally so the engine does not pin the wider git.GitClient
// surface across this single use site.
type codeRepoBranchClient interface {
	RefSHA(ctx context.Context, ref string) (string, error)
	CreateBranch(ctx context.Context, name, base string) error
	DeleteBranch(ctx context.Context, name string) error
	PushBranch(ctx context.Context, remote, branch string) error
	DeleteRemoteBranch(ctx context.Context, remote, branch string) error
}
