package projection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	bpconfig "github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
)

// BranchProtectionConfigPath is the fixed repo path the projection
// watches. Not configurable (ADR-009 §2.2).
const BranchProtectionConfigPath = ".spine/branch-protection.yaml"

// Service implements the Projection Service — syncs Git state to PostgreSQL.
type Service struct {
	git          git.GitClient
	store        store.Store
	events       event.EventRouter
	pollInterval time.Duration
	// scope is the one path classifier both entry points use
	// (artifacts_dir; INIT-023 TASK-011, audit P3-23).
	scope pathScope
	// authoritativeBranch, when set, pins every sync to
	// refs/heads/<branch> instead of the working tree's HEAD (P3-22).
	authoritativeBranch string
	// strictWorkflowValidation turns a workflow.Validate failure into a
	// rejection; off by default (owner question 7).
	strictWorkflowValidation bool
	// workflowsVerified records that this instance has reconciled every
	// stored workflow row against the sync head (the sweep, §6). Atomic
	// because the sync loop and the event handlers run IncrementalSync
	// concurrently; sweepMu makes the sweep single-flight.
	workflowsVerified atomic.Bool
	sweepMu           sync.Mutex
}

// NewService creates a new Projection Service.
func NewService(gitClient git.GitClient, s store.Store, events event.EventRouter, pollInterval time.Duration) *Service {
	if pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}
	return &Service{
		git:          gitClient,
		store:        s,
		events:       events,
		pollInterval: pollInterval,
	}
}

// WithArtifactsDir configures the projection to scope artifact discovery
// to the given directory (matching .spine.yaml artifacts_dir). Both
// FullRebuild and IncrementalSync apply it through one classifier:
// artifacts are exactly <dir>/**.md minus templates/, keyed by the path
// relative to <dir>; workflows are the direct *.yaml / *.yml children of
// the root workflows/ and of <dir>/workflows/. "", ".", "./" and "/"
// mean the repository root.
func (s *Service) WithArtifactsDir(dir string) {
	s.scope = newPathScope(dir)
}

// workflowPlan is FullRebuild's decision for one workflow path, taken
// before the delete-all window so nothing it needs is lost to the wipe.
type workflowPlan struct {
	path string
	row  *store.WorkflowProjection // accepted row to upsert, or the previous row to restore
	rej  *WorkflowRejection        // non-nil for a content rejection
	// action for a rejection: kept (row is the verified previous row,
	// restored quarantined), removed or not_projected (row is nil).
	action string
	// infraErr is a git read failure for this path. A verified previous
	// row (in row) is restored QUARANTINED: kept for runs pinned to its
	// source_commit and visible to the next sync's sweep, which retries
	// the file (and restores the row's status when the read succeeds),
	// but not bindable while the file at head cannot be checked (I6).
	infraErr error
}

// FullRebuild scans the entire repository at the sync head and rebuilds
// all projections. Per Data Model §4.1.
//
// Workflow rows obey the same rejection rule as IncrementalSync
// (INIT-023 TASK-011): every workflow row and the previous row of every
// rejected file are captured BEFORE the delete-all window, and a
// verified previous row is restored quarantined with its own
// source_commit. Discovery also runs before the wipe, so a git failure
// there no longer leaves an empty projection.
func (s *Service) FullRebuild(ctx context.Context) error {
	log := observe.Logger(ctx)
	log.Info("starting full projection rebuild")

	// Single-flight against the sweep: a rebuild re-derives every
	// workflow row, so it must not interleave with a sweep on this
	// instance.
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()

	head, err := s.resolveHead(ctx)
	if err != nil {
		return fmt.Errorf("resolve sync target: %w", err)
	}

	// Mark as rebuilding but keep previous commit until rebuild succeeds
	existingState, _ := s.store.GetSyncState(ctx)
	prevCommit := ""
	if existingState != nil {
		prevCommit = existingState.LastSyncedCommit
	}
	_ = s.store.UpdateSyncState(ctx, &store.SyncState{
		LastSyncedCommit: prevCommit,
		Status:           "rebuilding",
	})

	// Discover all artifacts (scoped to artifacts_dir if configured) and
	// plan every workflow row before the wipe.
	result, err := artifact.DiscoverAll(ctx, s.git, head, s.scope.dir)
	if err != nil {
		return fmt.Errorf("discover artifacts: %w", err)
	}
	wfPaths, err := s.workflowPathsAt(ctx, head, result.Workflows)
	if err != nil {
		return fmt.Errorf("discover workflows: %w", err)
	}
	plans, err := s.planWorkflowRows(ctx, wfPaths, head)
	if err != nil {
		return err
	}

	// Clear existing projections
	if err := s.store.DeleteAllProjections(ctx); err != nil {
		return fmt.Errorf("delete projections: %w", err)
	}

	// Count discovery errors as projection failures
	var projErrors int
	projErrors += len(result.Errors)

	// Project artifacts
	for _, a := range result.Artifacts {
		if err := s.projectArtifact(ctx, a, head); err != nil {
			log.Warn("failed to project artifact",
				"path", a.Path,
				"error", err,
			)
			projErrors++
			continue
		}
	}

	// Project workflows
	wfInfraErrors, rejected := s.applyWorkflowPlans(ctx, plans)
	projErrors += wfInfraErrors

	// Project branch-protection config (ADR-009 §1). Always runs on
	// rebuild so bootstrap defaults land even when the file is absent.
	if err := s.projectBranchProtection(ctx, head); err != nil {
		log.Warn("failed to project branch-protection config",
			"path", BranchProtectionConfigPath,
			"error", err,
		)
		projErrors++
	}

	// Update sync state — only mark idle if no errors
	if projErrors > 0 {
		if err := s.store.UpdateSyncState(ctx, &store.SyncState{
			LastSyncedCommit: head, // for rebuild, keep HEAD to allow retry at same commit
			Status:           "error",
			ErrorDetail:      fmt.Sprintf("%d projection errors during rebuild", projErrors),
		}); err != nil {
			return fmt.Errorf("update sync state: %w", err)
		}
	} else {
		if err := s.store.UpdateSyncState(ctx, &store.SyncState{
			LastSyncedCommit: head,
			Status:           "idle",
		}); err != nil {
			return fmt.Errorf("update sync state: %w", err)
		}
	}
	// A rebuild re-derived every workflow row from head, which is what
	// the sweep would do — unless a workflow read or write failed, in
	// which case the next IncrementalSync sweeps again.
	s.workflowsVerified.Store(wfInfraErrors == 0)

	log.Info("full rebuild complete",
		"artifacts", len(result.Artifacts),
		"workflows", len(wfPaths),
		"workflows_rejected", rejected,
		"skipped", len(result.Skipped),
		"errors", len(result.Errors),
		"commit", head,
	)

	observe.GlobalMetrics.ProjectionSyncs.Inc()
	return nil
}

// workflowPathsAt returns the sorted, de-duplicated workflow definition
// paths at head: DiscoverAll's workflow list (under artifacts_dir) plus,
// when artifacts_dir is a subdirectory, the root workflows/ directory
// (owner question 3), each filtered to direct children of a workflows/
// directory.
func (s *Service) workflowPathsAt(ctx context.Context, head string, discovered []string) ([]string, error) {
	seen := make(map[string]struct{}, len(discovered))
	var out []string
	add := func(paths []string) {
		for _, p := range paths {
			if !s.scope.isWorkflow(p) {
				continue
			}
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	add(discovered)
	if s.scope.dir != "" {
		root, err := artifact.DiscoverWorkflows(ctx, s.git, head)
		if err != nil {
			return nil, err
		}
		add(root)
	}
	sort.Strings(out)
	return out, nil
}

// planWorkflowRows builds every workflow row at head and captures the
// previous row of each rejected (or unreadable) file. It runs before
// FullRebuild's wipe; a store error while capturing a previous row aborts
// the rebuild before anything is deleted.
func (s *Service) planWorkflowRows(ctx context.Context, paths []string, head string) ([]workflowPlan, error) {
	plans := make([]workflowPlan, 0, len(paths))
	for _, p := range paths {
		row, rej, err := s.buildWorkflowRow(ctx, p, head)
		if err == nil && rej == nil {
			plans = append(plans, workflowPlan{path: p, row: row})
			continue
		}
		prev, getErr := s.getWorkflowRow(ctx, p)
		if getErr != nil {
			return nil, fmt.Errorf("capture previous workflow row before rebuild: %w", getErr)
		}
		verified := prev != nil && workflow.VerifyProjectionDefinition(prev.Definition) == nil
		if err != nil {
			plan := workflowPlan{path: p, infraErr: err}
			if verified {
				plan.row = prev
			}
			plans = append(plans, plan)
			continue
		}
		plan := workflowPlan{path: p, rej: rej}
		switch {
		case verified:
			plan.row, plan.action = prev, actionKept
		case prev != nil:
			plan.action = actionRemoved
		default:
			plan.action = actionNotProjected
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// applyWorkflowPlans writes FullRebuild's workflow rows after the wipe
// and reports every rejection. It returns the number of workflow
// infrastructure errors (counted as projection errors) and of content
// rejections (never counted).
func (s *Service) applyWorkflowPlans(ctx context.Context, plans []workflowPlan) (infraErrors, rejected int) {
	log := observe.Logger(ctx)
	for i := range plans {
		plan := &plans[i]
		switch {
		case plan.infraErr != nil:
			log.Warn("failed to project workflow", "path", plan.path, "error", plan.infraErr)
			infraErrors++
			if plan.row != nil {
				restored := *plan.row
				restored.Status = string(domain.WorkflowStatusQuarantined)
				if err := s.store.UpsertWorkflowProjection(ctx, &restored); err != nil {
					log.Warn("failed to restore previous workflow row", "path", plan.path, "error", err)
				}
			}
		case plan.rej == nil:
			if err := s.store.UpsertWorkflowProjection(ctx, plan.row); err != nil {
				log.Warn("failed to project workflow", "path", plan.path, "error", err)
				infraErrors++
			}
		default:
			rejected++
			if plan.action == actionKept {
				// The wipe removed the row; write the verified previous
				// row back, quarantined, with its own source_commit.
				restored := *plan.row
				restored.Status = string(domain.WorkflowStatusQuarantined)
				if err := s.store.UpsertWorkflowProjection(ctx, &restored); err != nil {
					log.Warn("failed to restore quarantined workflow row", "path", plan.path, "error", err)
					infraErrors++
					continue
				}
				s.reportWorkflowRejection(ctx, plan.rej, actionKept, &restored)
				continue
			}
			s.reportWorkflowRejection(ctx, plan.rej, plan.action, nil)
		}
	}
	return infraErrors, rejected
}

// markSyncError records an infrastructure failure: status error with the
// detail, last_synced_commit left where it was, so the next tick retries
// even when head has not moved. It returns the wrapped cause, joined with
// any error from writing the state.
func (s *Service) markSyncError(ctx context.Context, state *store.SyncState, detail string, cause error) error {
	err := fmt.Errorf("%s: %w", detail, cause)
	if upErr := s.store.UpdateSyncState(ctx, &store.SyncState{
		LastSyncedCommit: state.LastSyncedCommit,
		Status:           "error",
		ErrorDetail:      sanitizeForLog(detail),
	}); upErr != nil {
		err = errors.Join(err, fmt.Errorf("update sync state: %w", upErr))
	}
	return err
}

// IncrementalSync updates projections based on changes since the last sync.
// Per Data Model §4.2.
//
// INIT-023 TASK-011 (audit P3-14, P3-22, P3-23):
//   - one Diff per sync feeds both artifact discovery and the governance
//     (branch-protection, workflow) loop, and its error is a sync error —
//     a dropped second Diff used to skip governance changes while the
//     state still advanced to idle@head, so they were never retried;
//   - head is the authoritative ref when one is configured;
//   - artifacts and workflows go through the same scope as FullRebuild;
//   - a workflow content rejection advances the sync (reported, never
//     counted), every infrastructure error holds it (status error,
//     last_synced_commit unchanged);
//   - the first sync on an instance reconciles every stored workflow row
//     against head before the up-to-date early return.
func (s *Service) IncrementalSync(ctx context.Context) error {
	log := observe.Logger(ctx)

	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return fmt.Errorf("get sync state: %w", err)
	}

	if state == nil {
		// No sync state — do a full rebuild
		return s.FullRebuild(ctx)
	}

	head, err := s.resolveHead(ctx)
	if err != nil {
		return s.markSyncError(ctx, state, "resolve sync target", err)
	}

	// The sweep runs before the early return: a store already idle at
	// head is exactly the state an upgrade starts from.
	if err := s.ensureWorkflowsVerified(ctx, head); err != nil {
		return s.markSyncError(ctx, state, "reconcile workflow rows at "+shortSHA(head), err)
	}

	// Retry if previous sync failed, even at the same commit
	if head == state.LastSyncedCommit && state.Status == "idle" {
		return nil // already up to date and healthy
	}

	// Update sync state to syncing
	_ = s.store.UpdateSyncState(ctx, &store.SyncState{
		LastSyncedCommit: state.LastSyncedCommit,
		Status:           "syncing",
	})

	// One Diff for the whole sync.
	diffs, err := s.git.Diff(ctx, state.LastSyncedCommit, head)
	if err != nil {
		return s.markSyncError(ctx, state,
			fmt.Sprintf("diff %s..%s", shortSHA(state.LastSyncedCommit), shortSHA(head)), err)
	}

	changeset, err := artifact.DiscoverChangesFromDiffs(ctx, s.git, state.LastSyncedCommit, head, s.scope.artifactDiffs(diffs))
	if err != nil {
		return s.markSyncError(ctx, state, "discover changes", err)
	}
	readSkipped, readErr := s.classifyReadErrors(ctx, changeset.ReadErrors)
	if readErr != nil {
		return s.markSyncError(ctx, state, "read changed artifact", readErr)
	}

	var syncErrors int

	// scopeViolation guards the invariant that artifactDiffs passed only
	// in-scope paths to discovery. A path outside the scope here means a
	// filter regressed; refuse to advance rather than silently skip, so
	// the regression is loud (and the equivalence tests catch it).
	scopeViolation := func(repoPath string) {
		log.Warn("projection: changed artifact outside the projection scope; not applied",
			"path", repoPath, "artifacts_dir", s.scope.dir)
		syncErrors++
	}

	// Process created artifacts
	for _, a := range changeset.Created {
		key, ok := s.scope.artifactKey(a.Path)
		if !ok {
			scopeViolation(a.Path)
			continue
		}
		a.Path = key
		if err := s.projectArtifact(ctx, a, head); err != nil {
			log.Warn("failed to project created artifact",
				"path", a.Path,
				"error", err,
			)
			syncErrors++
		}
	}

	// Process modified artifacts
	for _, a := range changeset.Modified {
		key, ok := s.scope.artifactKey(a.Path)
		if !ok {
			scopeViolation(a.Path)
			continue
		}
		a.Path = key
		if err := s.projectArtifact(ctx, a, head); err != nil {
			log.Warn("failed to project modified artifact",
				"path", a.Path,
				"error", err,
			)
			syncErrors++
		}
	}

	// Process deleted artifacts
	for _, repoPath := range changeset.Deleted {
		path, ok := s.scope.artifactKey(repoPath)
		if !ok {
			scopeViolation(repoPath)
			continue
		}
		if err := s.store.DeleteArtifactProjection(ctx, path); err != nil {
			log.Warn("failed to delete projection",
				"path", path,
				"error", err,
			)
			syncErrors++
		}
		if err := s.store.DeleteArtifactLinks(ctx, path); err != nil {
			log.Warn("failed to delete links",
				"path", path,
				"error", err,
			)
			syncErrors++
		}
	}

	// Branch-protection.yaml: a single file at a fixed path — any diff
	// touching it triggers a re-projection. Both adds/modifies and
	// deletions re-run the projection (deletion re-seeds bootstrap
	// defaults per ADR-009 §1). If the previous sync state was "error",
	// we re-project unconditionally: a FullRebuild that failed on a
	// malformed config records LastSyncedCommit=head, which means the
	// diff window below is empty — without this force-refresh, the
	// bad config would never be retried and an error state could
	// silently transition to "idle" with stale rules.
	bpDirty := state.Status == "error"
	if !bpDirty {
		for _, diff := range diffs {
			if diff.Path == BranchProtectionConfigPath || diff.OldPath == BranchProtectionConfigPath {
				bpDirty = true
				break
			}
		}
	}
	if bpDirty {
		if err := s.projectBranchProtection(ctx, head); err != nil {
			log.Warn("failed to project branch-protection config",
				"path", BranchProtectionConfigPath,
				"error", err,
			)
			syncErrors++
		}
	}

	// Workflows: the same raw diff, classified with the shared scope.
	var workflowsRejected int
	syncWorkflow := func(path string) {
		rejected, err := s.syncWorkflowFile(ctx, path, head)
		if err != nil {
			log.Warn("failed to project workflow", "path", path, "error", err)
			syncErrors++
			return
		}
		if rejected {
			workflowsRejected++
		}
	}
	deleteWorkflow := func(path string) {
		if err := s.store.DeleteWorkflowProjection(ctx, path); err != nil {
			log.Warn("failed to delete workflow", "path", path, "error", err)
			syncErrors++
		}
	}
	for _, diff := range diffs {
		switch diff.Status {
		case "added", "modified":
			if s.scope.isWorkflow(diff.Path) {
				syncWorkflow(diff.Path)
			}
		case "deleted":
			if s.scope.isWorkflow(diff.Path) {
				deleteWorkflow(diff.Path)
			}
		case "renamed":
			// The old row's delete error is counted: dropping it left
			// the old Active row bindable while the sync advanced.
			if diff.OldPath != "" && s.scope.isWorkflow(diff.OldPath) {
				deleteWorkflow(diff.OldPath)
			}
			if s.scope.isWorkflow(diff.Path) {
				syncWorkflow(diff.Path)
			}
		}
	}

	// Update sync state — only advance commit if no errors
	if syncErrors > 0 {
		if err := s.store.UpdateSyncState(ctx, &store.SyncState{
			LastSyncedCommit: state.LastSyncedCommit, // don't advance
			Status:           "error",
			ErrorDetail:      fmt.Sprintf("%d sync errors", syncErrors),
		}); err != nil {
			return fmt.Errorf("update sync state: %w", err)
		}
	} else {
		if err := s.store.UpdateSyncState(ctx, &store.SyncState{
			LastSyncedCommit: head,
			Status:           "idle",
		}); err != nil {
			return fmt.Errorf("update sync state: %w", err)
		}
	}

	log.Info("incremental sync complete",
		"created", len(changeset.Created),
		"modified", len(changeset.Modified),
		"deleted", len(changeset.Deleted),
		"workflows_rejected", workflowsRejected,
		"read_skipped", readSkipped,
		"errors", syncErrors,
		"from", shortSHA(state.LastSyncedCommit),
		"to", shortSHA(head),
	)

	observe.GlobalMetrics.ProjectionSyncs.Inc()

	// Emit projection_synced event (if event router configured).
	if s.events == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{
		"from_commit":        shortSHA(state.LastSyncedCommit),
		"to_commit":          shortSHA(head),
		"created":            len(changeset.Created),
		"modified":           len(changeset.Modified),
		"deleted":            len(changeset.Deleted),
		"errors":             syncErrors,
		"workflows_rejected": workflowsRejected,
		"read_skipped":       readSkipped,
	})
	event.EmitLogged(ctx, s.events, domain.Event{
		EventID: fmt.Sprintf("sync-%s", head[:12]),
		Type:    domain.EventProjectionSynced,
		Payload: payload,
	})

	return nil
}

// classifyReadErrors sorts the artifact read failures discovery skipped
// past (INIT-023 TASK-011 §3). A blanket "propagate" would be unsafe: a
// gitlink entry named x.md reads as `fatal: bad object`, which the git
// client classifies as permanent, so any member could wedge the sync with
// one plantable entry. So only a failure that can heal on retry is
// infrastructure — an error that is not a *git.GitError (exec/spawn
// failure, e.g. EAGAIN under load), a transient GitError, or a cancelled
// context — and it is returned to hold the sync. Every other failure keeps
// the historical skip, now logged at Warn and counted (distinct paths) as
// read_skipped.
func (s *Service) classifyReadErrors(ctx context.Context, readErrors []artifact.DiscoveryError) (int, error) {
	skipped := make(map[string]struct{})
	for i := range readErrors {
		re := &readErrors[i]
		var gerr *git.GitError
		if ctx.Err() != nil || !errors.As(re.Err, &gerr) || gerr.Kind == git.ErrKindTransient {
			return 0, fmt.Errorf("read %s at %s: %w", re.Path, shortSHA(re.Ref), re.Err)
		}
		observe.Logger(ctx).Warn("projection: skipped unreadable changed file",
			"path", re.Path,
			"ref", shortSHA(re.Ref),
			"kind", string(gerr.Kind),
			"error", sanitizeForLog(re.Message),
		)
		skipped[re.Path] = struct{}{}
	}
	return len(skipped), nil
}

// StartSyncLoop runs incremental sync on a polling interval.
// Stops when the context is cancelled.
func (s *Service) StartSyncLoop(ctx context.Context) {
	log := observe.Logger(ctx)
	log.Info("starting projection sync loop", "interval", s.pollInterval)

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.IncrementalSync(ctx); err != nil {
				log.Error("incremental sync failed", "error", err)
			}
		case <-ctx.Done():
			log.Info("projection sync loop stopped")
			return
		}
	}
}

// RegisterEventHandlers subscribes to artifact events for real-time sync.
func (s *Service) RegisterEventHandlers(ctx context.Context) error {
	if s.events == nil {
		return nil
	}

	// React to artifact changes by triggering incremental sync
	handler := func(ctx context.Context, evt domain.Event) error {
		return s.IncrementalSync(ctx)
	}

	if err := s.events.Subscribe(ctx, domain.EventArtifactCreated, handler); err != nil {
		return err
	}
	return s.events.Subscribe(ctx, domain.EventArtifactUpdated, handler)
}

// projectArtifact upserts an artifact projection and its links.
func (s *Service) projectArtifact(ctx context.Context, a *domain.Artifact, commitSHA string) error {
	metadata, err := json.Marshal(a.Metadata)
	if err != nil {
		return fmt.Errorf("marshal artifact metadata: %w", err)
	}
	linksJSON, err := json.Marshal(a.Links)
	if err != nil {
		return fmt.Errorf("marshal artifact links: %w", err)
	}
	contentHash := hashContent(a.Content)

	proj := &store.ArtifactProjection{
		ArtifactPath: a.Path,
		ArtifactID:   a.ID,
		ArtifactType: string(a.Type),
		Title:        a.Title,
		Status:       string(a.Status),
		Metadata:     metadata,
		Content:      a.Content,
		Links:        linksJSON,
		Repositories: a.Repositories,
		SourceCommit: commitSHA,
		ContentHash:  contentHash,
	}

	if err := s.store.UpsertArtifactProjection(ctx, proj); err != nil {
		return fmt.Errorf("upsert artifact %s: %w", a.Path, err)
	}

	// Denormalize links
	var links []store.ArtifactLink
	for _, link := range a.Links {
		links = append(links, store.ArtifactLink{
			SourcePath: a.Path,
			TargetPath: link.Target,
			LinkType:   string(link.Type),
		})
	}

	if err := s.store.UpsertArtifactLinks(ctx, a.Path, links, commitSHA); err != nil {
		return fmt.Errorf("upsert links for %s: %w", a.Path, err)
	}

	// For Task artifacts, also upsert the execution projection.
	if a.Type == domain.ArtifactTypeTask {
		blocked, blockedBy := s.resolveBlockingStatus(ctx, a.Path, a.Links)
		execProj := &store.ExecutionProjection{
			TaskPath:         a.Path,
			TaskID:           a.ID,
			Title:            a.Title,
			Status:           string(a.Status),
			Blocked:          blocked,
			BlockedBy:        blockedBy,
			Repositories:     a.Repositories,
			AssignmentStatus: "unassigned",
		}
		if err := s.store.UpsertExecutionProjection(ctx, execProj); err != nil {
			observe.Logger(ctx).Warn("failed to upsert execution projection", "path", a.Path, "error", err)
		}
	}

	return nil
}

// resolveBlockingStatus checks blocked_by links and returns blocking state.
func (s *Service) resolveBlockingStatus(ctx context.Context, taskPath string, links []domain.Link) (bool, []string) {
	var blockedBy []string
	for _, link := range links {
		if link.Type != domain.LinkTypeBlockedBy {
			continue
		}
		// Normalize the target path — canonical blocked_by links use a leading
		// slash (e.g., /initiatives/...) but projection keys do not.
		target := strings.TrimPrefix(link.Target, "/")
		// Check if the blocker is in a terminal status.
		blocker, err := s.store.GetArtifactProjection(ctx, target)
		if err != nil {
			// Can't find blocker — treat as blocking (safe default).
			blockedBy = append(blockedBy, link.Target)
			continue
		}
		if !isTerminalProjectionStatus(blocker.Status) {
			blockedBy = append(blockedBy, link.Target)
		}
	}
	return len(blockedBy) > 0, blockedBy
}

func isTerminalProjectionStatus(status string) bool {
	switch domain.ArtifactStatus(status) {
	case domain.StatusCompleted, domain.StatusCancelled, domain.StatusRejected,
		domain.StatusAbandoned, domain.StatusSuperseded:
		return true
	}
	return false
}

// projectBranchProtection reads /.spine/branch-protection.yaml at the
// given commit, parses it, and atomically replaces the effective ruleset
// in projection.branch_protection_rules.
//
// Behaviour per ADR-009 §1 and EPIC-002 TASK-003:
//   - File present + parses cleanly: rows are replaced with the parsed
//     contents, stamped with commitSHA as source_commit.
//   - File present + parse error: rows left intact, the caller counts
//     this as a sync error (preventing the sync state from advancing
//     past a broken commit). Never falls back to an empty ruleset —
//     that would silently disable protection on malformed input.
//   - File absent: rows are replaced with BootstrapDefaults, stamped
//     with source_commit="bootstrap" so operators can tell the table
//     reflects defaults rather than an explicit config.
func (s *Service) projectBranchProtection(ctx context.Context, commitSHA string) error {
	// Determine existence by listing, not by ReadFile's error string —
	// git CLIs surface "file missing" with various messages, and
	// conflating every read error with "missing" would let a transient
	// git failure silently relax protection (ADR-009 §1). Only when
	// ListFiles confirms the path does not exist do we apply bootstrap
	// defaults; every other failure propagates as a sync error.
	files, listErr := s.git.ListFiles(ctx, commitSHA, BranchProtectionConfigPath)
	if listErr != nil {
		return fmt.Errorf("list branch-protection config at %s: %w", shortSHA(commitSHA), listErr)
	}
	if !containsPath(files, BranchProtectionConfigPath) {
		// Config truly absent — seed bootstrap defaults.
		return s.store.UpsertBranchProtectionRules(ctx, bootstrapRows(), "bootstrap")
	}

	content, readErr := s.git.ReadFile(ctx, commitSHA, BranchProtectionConfigPath)
	if readErr != nil {
		return fmt.Errorf("read branch-protection config at %s: %w", shortSHA(commitSHA), readErr)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		// A committed-but-empty file is treated as a malformed config,
		// not as "missing". Falling back to bootstrap here could
		// silently drop previously projected rules (e.g. `staging`,
		// `release/*`) that an earlier non-empty commit installed.
		// The parser's "file is empty" error propagates, preventing
		// sync state from advancing past this commit.
		return fmt.Errorf("parse branch-protection config at %s: file is empty", shortSHA(commitSHA))
	}

	cfg, err := bpconfig.Parse(bytes.NewReader(content))
	if err != nil {
		// Retain the existing ruleset — do NOT replace with empty.
		// The caller counts this as a sync error so the commit is not
		// advanced past the bad file.
		return fmt.Errorf("parse branch-protection config at %s: %w", shortSHA(commitSHA), err)
	}

	rows := make([]store.BranchProtectionRuleProjection, 0, len(cfg.Rules))
	for i, r := range cfg.Rules {
		protections, err := json.Marshal(ruleKindsToStrings(r.Protections))
		if err != nil {
			return fmt.Errorf("marshal protections for %q: %w", r.Branch, err)
		}
		rows = append(rows, store.BranchProtectionRuleProjection{
			BranchPattern: r.Branch,
			RuleOrder:     i,
			Protections:   protections,
		})
	}
	return s.store.UpsertBranchProtectionRules(ctx, rows, commitSHA)
}

// bootstrapRows renders branchprotect.BootstrapDefaults() into the store
// row shape so the projection handler can insert them the same way it
// inserts user-authored rules.
func bootstrapRows() []store.BranchProtectionRuleProjection {
	defaults := branchprotect.BootstrapDefaults()
	rows := make([]store.BranchProtectionRuleProjection, 0, len(defaults))
	for i, r := range defaults {
		protections, _ := json.Marshal(ruleKindsToStrings(r.Protections))
		rows = append(rows, store.BranchProtectionRuleProjection{
			BranchPattern: r.Branch,
			RuleOrder:     i,
			Protections:   protections,
		})
	}
	return rows
}

func ruleKindsToStrings(kinds []bpconfig.RuleKind) []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return out
}

func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}

func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

// shortSHA safely truncates a commit SHA to 8 characters for display.
func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}
