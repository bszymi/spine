package projection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/branchprotect"
	bpconfig "github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/yamlsafe"
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
	artifactsDir string // restricts artifact scanning to a subdirectory
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

// WithArtifactsDir configures the projection service to scope artifact
// discovery to the given directory (matching .spine.yaml artifacts_dir).
func (s *Service) WithArtifactsDir(dir string) {
	s.artifactsDir = dir
}

// FullRebuild scans the entire repository at HEAD and rebuilds all projections.
// Per Data Model §4.1.
func (s *Service) FullRebuild(ctx context.Context) error {
	log := observe.Logger(ctx)
	log.Info("starting full projection rebuild")

	head, err := s.git.Head(ctx)
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
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

	// Clear existing projections
	if err := s.store.DeleteAllProjections(ctx); err != nil {
		return fmt.Errorf("delete projections: %w", err)
	}

	// Discover all artifacts (scoped to artifacts_dir if configured)
	var discoverOpts []string
	if s.artifactsDir != "" {
		discoverOpts = append(discoverOpts, s.artifactsDir)
	}
	result, err := artifact.DiscoverAll(ctx, s.git, head, discoverOpts...)
	if err != nil {
		return fmt.Errorf("discover artifacts: %w", err)
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
	for _, wfPath := range result.Workflows {
		if err := s.projectWorkflow(ctx, wfPath, head); err != nil {
			log.Warn("failed to project workflow",
				"path", wfPath,
				"error", err,
			)
			projErrors++
		}
	}

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

	log.Info("full rebuild complete",
		"artifacts", len(result.Artifacts),
		"workflows", len(result.Workflows),
		"skipped", len(result.Skipped),
		"errors", len(result.Errors),
		"commit", head,
	)

	observe.GlobalMetrics.ProjectionSyncs.Inc()
	return nil
}

// IncrementalSync updates projections based on changes since the last sync.
// Per Data Model §4.2.
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

	head, err := s.git.Head(ctx)
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
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

	changeset, err := artifact.DiscoverChanges(ctx, s.git, state.LastSyncedCommit, head)
	if err != nil {
		return fmt.Errorf("discover changes: %w", err)
	}

	// When artifactsDir is set, strip the prefix from changeset paths so
	// they match the projection keys produced by FullRebuild.
	prefix := ""
	if s.artifactsDir != "" && s.artifactsDir != "/" {
		prefix = s.artifactsDir + "/"
	}

	var syncErrors int

	// Process created artifacts
	for _, a := range changeset.Created {
		if prefix != "" {
			a.Path = strings.TrimPrefix(a.Path, prefix)
		}
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
		if prefix != "" {
			a.Path = strings.TrimPrefix(a.Path, prefix)
		}
		if err := s.projectArtifact(ctx, a, head); err != nil {
			log.Warn("failed to project modified artifact",
				"path", a.Path,
				"error", err,
			)
			syncErrors++
		}
	}

	// Process deleted artifacts
	for _, path := range changeset.Deleted {
		if prefix != "" {
			path = strings.TrimPrefix(path, prefix)
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

	// Sync workflow + branch-protection changes from the diff.
	diffs, _ := s.git.Diff(ctx, state.LastSyncedCommit, head)

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

	for _, diff := range diffs {
		if !artifact.IsWorkflowPath(diff.Path) && (diff.OldPath == "" || !artifact.IsWorkflowPath(diff.OldPath)) {
			continue
		}
		switch diff.Status {
		case "added", "modified":
			if err := s.projectWorkflow(ctx, diff.Path, head); err != nil {
				log.Warn("failed to project workflow", "path", diff.Path, "error", err)
				syncErrors++
			}
		case "deleted":
			if err := s.store.DeleteWorkflowProjection(ctx, diff.Path); err != nil {
				log.Warn("failed to delete workflow", "path", diff.Path, "error", err)
				syncErrors++
			}
		case "renamed":
			if diff.OldPath != "" {
				_ = s.store.DeleteWorkflowProjection(ctx, diff.OldPath)
			}
			if artifact.IsWorkflowPath(diff.Path) {
				if err := s.projectWorkflow(ctx, diff.Path, head); err != nil {
					log.Warn("failed to project renamed workflow", "path", diff.Path, "error", err)
					syncErrors++
				}
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
		"from_commit": shortSHA(state.LastSyncedCommit),
		"to_commit":   shortSHA(head),
		"created":     len(changeset.Created),
		"modified":    len(changeset.Modified),
		"deleted":     len(changeset.Deleted),
		"errors":      syncErrors,
	})
	event.EmitLogged(ctx, s.events, domain.Event{
		EventID: fmt.Sprintf("sync-%s", head[:12]),
		Type:    domain.EventProjectionSynced,
		Payload: payload,
	})

	return nil
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

// projectWorkflow reads and projects a workflow YAML file.
func (s *Service) projectWorkflow(ctx context.Context, wfPath, commitSHA string) error {
	content, err := s.git.ReadFile(ctx, commitSHA, wfPath)
	if err != nil {
		return fmt.Errorf("read workflow %s: %w", wfPath, err)
	}

	// Bound size / depth / alias count once via yamlsafe so a hostile
	// workflow file can't stall projection with a billion-laughs payload,
	// then decode twice off the same parsed node: once for the typed
	// projection fields, once as `any` for the JSONB definition column.
	node, err := yamlsafe.Decode(content)
	if err != nil {
		return fmt.Errorf("parse workflow %s: %w", wfPath, err)
	}

	var wf struct {
		ID        string   `yaml:"id" json:"id"`
		Name      string   `yaml:"name" json:"name"`
		Version   string   `yaml:"version" json:"version"`
		Status    string   `yaml:"status" json:"status"`
		AppliesTo []string `yaml:"applies_to" json:"applies_to"`
	}
	if err := node.Decode(&wf); err != nil {
		return fmt.Errorf("parse workflow %s: %w", wfPath, err)
	}

	appliesTo, err := json.Marshal(wf.AppliesTo)
	if err != nil {
		return fmt.Errorf("marshal workflow applies_to: %w", err)
	}

	// Convert YAML to JSON for the JSONB definition column.
	var rawDef any
	if err := node.Decode(&rawDef); err != nil {
		return fmt.Errorf("parse workflow raw definition %s: %w", wfPath, err)
	}
	definition, err := json.Marshal(rawDef)
	if err != nil {
		return fmt.Errorf("marshal workflow definition: %w", err)
	}

	proj := &store.WorkflowProjection{
		WorkflowPath: wfPath,
		WorkflowID:   wf.ID,
		Name:         wf.Name,
		Version:      wf.Version,
		Status:       wf.Status,
		AppliesTo:    appliesTo,
		Definition:   definition,
		SourceCommit: commitSHA,
	}

	return s.store.UpsertWorkflowProjection(ctx, proj)
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
