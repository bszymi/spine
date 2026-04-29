package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
)

// createRunBranches creates the run branch on every affected repository
// (INIT-014 EPIC-004 TASK-002). The primary branch is created from HEAD
// against the orchestrator's primary GitOperator; every non-primary
// affected repo's branch is created from that repo's default branch
// against the per-repo client returned by the RepositoryGitClients
// resolver.
//
// On success it returns the list of repository IDs that received a
// freshly-created local branch — used by the caller to roll back if a
// later step (store.CreateRun) fails. The list is in creation order so
// rollback can iterate it deterministically.
//
// On failure it returns the wrapped error including the repository ID
// that failed and rolls back already-created branches before returning.
// Comprehensive cleanup of partial multi-repo failures (remote refs,
// structured logging) is the deliverable of TASK-003; here we keep the
// rollback to local-branch deletes so the next attempt can re-create
// without `branch already exists` collisions.
func (o *Orchestrator) createRunBranches(ctx context.Context, run *domain.Run) ([]string, error) {
	created := make([]string, 0, len(run.AffectedRepositories)+1)

	// Refuse to start a multi-repo run when the per-repo wiring is
	// missing. Silently treating it as primary-only would persist the
	// run with phantom AffectedRepositories — code repos listed as
	// participating without a backing branch — which downstream
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
		return nil, domain.NewError(domain.ErrPrecondition,
			"multi-repo run requires WithRepositoryGitClients and WithRepositoryResolver wirings")
	}

	if err := o.git.CreateBranch(ctx, run.BranchName, "HEAD"); err != nil {
		return nil, fmt.Errorf("create run branch on %q: %w", repository.PrimaryRepositoryID, err)
	}
	created = append(created, repository.PrimaryRepositoryID)

	if !hasCodeRepo {
		return created, nil
	}

	for _, repoID := range run.AffectedRepositories {
		if repoID == "" || repoID == repository.PrimaryRepositoryID {
			continue
		}
		client, base, err := o.resolveCodeRepoForBranch(ctx, repoID)
		if err != nil {
			o.rollbackRunBranches(ctx, run, created)
			return nil, fmt.Errorf("create run branch on %q: %w", repoID, err)
		}
		if err := client.CreateBranch(ctx, run.BranchName, base); err != nil {
			o.rollbackRunBranches(ctx, run, created)
			return nil, fmt.Errorf("create run branch on %q: %w", repoID, err)
		}
		created = append(created, repoID)
	}
	// RepositoryBranches stays nil today: every affected repo gets the
	// same run branch name, and the field is documented as "fill in
	// only when divergent state needs to be tracked." Recovery and
	// per-repo divergence are downstream tasks (TASK-006 / future
	// recovery work).
	return created, nil
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

// rollbackRunBranches best-effort deletes the local run branch on every
// repository in created. Errors are logged but not surfaced — this is
// invoked from a path that already has a primary error to return; a
// secondary delete failure must not mask it. Comprehensive partial-
// failure cleanup (including remote refs) is TASK-003.
func (o *Orchestrator) rollbackRunBranches(ctx context.Context, run *domain.Run, created []string) {
	log := observe.Logger(ctx)
	for _, repoID := range created {
		if repoID == repository.PrimaryRepositoryID {
			if err := o.git.DeleteBranch(ctx, run.BranchName); err != nil {
				log.Warn("rollback: failed to delete primary run branch",
					"branch", run.BranchName, "error", err)
			}
			continue
		}
		client, err := o.codeRepoClient(ctx, repoID)
		if err != nil {
			log.Warn("rollback: failed to resolve code repo client",
				"repository_id", repoID, "branch", run.BranchName, "error", err)
			continue
		}
		if err := client.DeleteBranch(ctx, run.BranchName); err != nil {
			log.Warn("rollback: failed to delete run branch",
				"repository_id", repoID, "branch", run.BranchName, "error", err)
		}
	}
}

// pushRunBranches pushes the run branch to origin on every repository
// in created. Errors are logged warn-and-continue so a transient push
// failure does not break run startup — the run is already persisted
// and active by the time auto-push runs (matching the pre-multi-repo
// behavior of single-repo auto-push).
func (o *Orchestrator) pushRunBranches(ctx context.Context, run *domain.Run, created []string) {
	log := observe.Logger(ctx)
	for _, repoID := range created {
		if repoID == repository.PrimaryRepositoryID {
			if err := o.git.PushBranch(ctx, "origin", run.BranchName); err != nil {
				log.Warn("auto-push: failed to push run branch",
					"branch", run.BranchName, "error", err)
			}
			continue
		}
		client, err := o.codeRepoClient(ctx, repoID)
		if err != nil {
			log.Warn("auto-push: failed to resolve code repo client",
				"repository_id", repoID, "branch", run.BranchName, "error", err)
			continue
		}
		if err := client.PushBranch(ctx, "origin", run.BranchName); err != nil {
			log.Warn("auto-push: failed to push run branch",
				"repository_id", repoID, "branch", run.BranchName, "error", err)
		}
	}
}

// codeRepoClient returns just the client for a non-primary repo,
// adapted to the local interface. Skips the default-branch lookup
// because rollback and push do not need the base ref.
func (o *Orchestrator) codeRepoClient(ctx context.Context, repoID string) (codeRepoBranchClient, error) {
	if o.repoClients == nil {
		return nil, fmt.Errorf("repository git clients not wired")
	}
	return o.repoClients.Client(ctx, repoID)
}

// codeRepoBranchClient is the subset of git.GitClient the run-start
// path needs against a code repository: create, delete, and push the
// run branch. Defined locally so the engine does not pin the wider
// git.GitClient surface across this single use site.
type codeRepoBranchClient interface {
	CreateBranch(ctx context.Context, name, base string) error
	DeleteBranch(ctx context.Context, name string) error
	PushBranch(ctx context.Context, remote, branch string) error
}
