package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
)

// WriteResult contains the result of an artifact write operation.
type WriteResult struct {
	Artifact  *domain.Artifact
	CommitSHA string
}

// Service implements artifact CRUD operations backed by Git.
type Service struct {
	git          git.GitClient
	events       event.EventRouter
	repo         string               // repository root path
	artifactsDir string               // artifacts directory relative to repo (empty or "/" means repo root)
	policy       branchprotect.Policy // branch-protection guard for direct writes; required in production (ADR-009 §3)
	defaultRef   string               // branch to report when no WriteContext is set; defaults to "main"
}

// NewService creates a new Artifact Service.
//
// Production construction MUST follow up with WithPolicy to install the
// branch-protection guard (ADR-009 §3). A Service without a policy is
// fail-closed at the write boundary — writeAndCommit refuses to advance
// any ref until a policy is wired. Tests that do not care about branch
// protection can install a permissive policy via
// branchprotect.New(branchprotect.StaticRules(nil-slice → bootstrap,
// []config.Rule{} → no protection)).
func NewService(gitClient git.GitClient, events event.EventRouter, repoPath string) *Service {
	return &Service{
		git:          gitClient,
		events:       events,
		repo:         repoPath,
		artifactsDir: "/",
		defaultRef:   "main",
	}
}

// WithArtifactsDir sets the artifacts directory for path resolution.
// When set to a non-root value (e.g., "spine"), all artifact paths
// are prefixed with this directory for file I/O and git operations.
func (s *Service) WithArtifactsDir(dir string) {
	s.artifactsDir = dir
}

// WithPolicy installs the branch-protection policy consulted by every
// ref-advancing helper (ADR-009 §3). Callers in production wire the
// projection-backed policy built in cmd/spine; tests inject a mock or a
// permissive policy. Passing nil is equivalent to never calling
// WithPolicy — the Service remains fail-closed.
func (s *Service) WithPolicy(p branchprotect.Policy) {
	s.policy = p
}

// WithDefaultRef overrides the branch reported to branch-protection when
// no WriteContext is set on the request. The default is "main" — matching
// the bootstrap authoritative-branch name used throughout Spine. Tests
// that check-out a different authoritative branch point this at that
// name so the policy evaluator sees the real target.
func (s *Service) WithDefaultRef(ref string) {
	if ref != "" {
		s.defaultRef = ref
	}
}

// repoRelativePath converts an artifact-relative path to a repo-relative path.
// When artifactsDir is "/" (root), paths pass through unchanged.
// When artifactsDir is "spine", "governance/charter.md" becomes "spine/governance/charter.md".
func (s *Service) repoRelativePath(artifactPath string) string {
	artifactPath = strings.TrimPrefix(artifactPath, "/")
	if s.artifactsDir == "/" || s.artifactsDir == "" {
		return artifactPath
	}
	return filepath.Join(s.artifactsDir, artifactPath)
}

// safePath validates and resolves a path against the main repo root.
func (s *Service) safePath(path string) (string, error) {
	return s.safePathIn(s.repo, path)
}

// safePathIn validates and resolves a path, ensuring it stays within root.
// The input path is artifact-relative; it is first converted to a repo-relative
// path via repoRelativePath before being joined with root.
// root may be the main repo or an isolated worktree directory.
func (s *Service) safePathIn(root, path string) (string, error) {
	// Reject absolute paths before any processing.
	if filepath.IsAbs(path) {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("path must be relative: %s", path))
	}

	repoPath := s.repoRelativePath(path)
	fullPath := filepath.Join(root, repoPath)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		// Log the underlying error server-side; do not leak the repo
		// root path back to the caller.
		slog.Default().Warn("safePath: abs(root) failed", "error", err, "root", root)
		return "", domain.NewError(domain.ErrInvalidParams, "invalid path")
	}

	// Resolve symlinks on the root
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		slog.Default().Warn("safePath: evalSymlinks(root) failed", "error", err, "root", absRoot)
		return "", domain.NewError(domain.ErrInvalidParams, "invalid path")
	}

	// Resolve the target path — for new files, resolve the parent directory
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		slog.Default().Warn("safePath: abs(fullPath) failed", "error", err, "path", path)
		return "", domain.NewError(domain.ErrInvalidParams, "invalid path")
	}

	// Resolve symlinks by walking up to the nearest existing ancestor.
	// This prevents escaping via symlinked directories with missing descendants.
	realPath, err := resolveToExistingAncestor(absPath)
	if err != nil {
		slog.Default().Warn("safePath: resolveToExistingAncestor failed", "error", err, "path", path)
		return "", domain.NewError(domain.ErrInvalidParams, "invalid path")
	}

	if !strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) && realPath != realRoot {
		return "", domain.NewError(domain.ErrInvalidParams, "path escapes repository")
	}
	return absPath, nil
}

// Create creates a new artifact, validates it, writes the file, commits to Git,
// and emits an artifact_created event.
func (s *Service) Create(ctx context.Context, path, content string) (*WriteResult, error) {
	return s.writeAndCommit(ctx, path, content, writeOp{
		requireExists: false,
		messageVerb:   "Create",
		operation:     "artifact.create",
		eventType:     domain.EventArtifactCreated,
		logAction:     "artifact created",
	})
}

// writeOp encodes the three axes that distinguish Create from Update: the
// pre-check semantics, the commit-message verb + operation trailer, and the
// emitted event type. Both paths share the remaining 10-step skeleton
// (parse → validate → enterBranch → safePath → pre-check → write → commit
// → rollback-on-error → autoPush → emit).
type writeOp struct {
	requireExists bool // false: must not exist (Create); true: must exist (Update)
	messageVerb   string
	operation     string
	eventType     domain.EventType
	logAction     string
}

func (s *Service) writeAndCommit(ctx context.Context, path, content string, op writeOp) (*WriteResult, error) {
	log := observe.Logger(ctx)

	artifact, err := Parse(path, []byte(content))
	if err != nil {
		return nil, domain.NewError(domain.ErrValidationFailed, err.Error())
	}

	result := Validate(artifact)
	if result.Status == "failed" {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"artifact validation failed", result.Errors)
	}

	// Branch-protection check (ADR-009 §3). Run before enterBranch so a
	// denied write never creates a worktree. The Artifact Service writes
	// commits but never merges — every writeAndCommit is an OpDirectWrite.
	// OpGovernedMerge lives on the Orchestrator (EPIC-003 TASK-002).
	bpResult, err := s.checkBranchProtection(ctx, op.operation)
	if err != nil {
		return nil, err
	}

	scope, err := s.enterBranch(ctx)
	if err != nil {
		return nil, err
	}
	defer scope.Cleanup()

	fullPath, err := s.safePathIn(scope.RepoDir, path)
	if err != nil {
		return nil, err
	}

	var originalContent []byte
	if op.requireExists {
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("artifact not found: %s", path))
		}
		var readErr error
		originalContent, readErr = os.ReadFile(fullPath)
		if readErr != nil {
			return nil, domain.NewError(domain.ErrInternal,
				fmt.Sprintf("read original %s: %v", path, readErr))
		}
	} else {
		if _, err := os.Stat(fullPath); err == nil {
			return nil, domain.NewError(domain.ErrAlreadyExists,
				fmt.Sprintf("artifact already exists at path: %s", path))
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return nil, domain.NewError(domain.ErrInternal,
				fmt.Sprintf("create directory %s: %v", filepath.Dir(fullPath), err))
		}
	}

	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("write file %s: %v", path, err))
	}

	repoPath := s.repoRelativePath(path)
	trailers := observe.TrailersFromContext(ctx, op.operation)
	if bpResult.overrideHonoured {
		// ADR-009 §4: API-path overrides carry a Branch-Protection-Override
		// trailer. Git-push overrides do not (handler does not rewrite
		// pushed commits). The trailer is a convenience for `git log`
		// inspection; the governance event below is the authoritative
		// audit record.
		trailers["Branch-Protection-Override"] = "true"
	}
	commitResult, err := git.StageAndCommit(ctx, scope, repoPath, git.CommitOpts{
		Message:  fmt.Sprintf("%s %s: %s", op.messageVerb, artifact.Type, artifact.Title),
		Trailers: trailers,
		Author: git.Author{
			Name:  observe.ActorID(ctx),
			Email: observe.ActorID(ctx) + "@spine.local",
		},
	})
	if err != nil {
		// Rollback. StageAndCommit already unstaged on failure, so we only
		// have to restore file state here.
		if op.requireExists {
			_ = os.WriteFile(fullPath, originalContent, 0o644) //nolint:gosec // G703: fullPath came from safePath above; 0644 required for git tracking
		} else {
			_ = os.Remove(fullPath)
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	s.autoPush(ctx, scope.RepoDir)

	log.Info(op.logAction,
		"path", path,
		"type", artifact.Type,
		"commit", commitResult.SHA,
	)

	s.emitEvent(ctx, op.eventType, artifact, commitResult.SHA)

	if bpResult.overrideHonoured {
		s.emitBranchProtectionOverride(ctx, op.operation, bpResult.branch, bpResult.ruleKinds, commitResult.SHA)
	}

	return &WriteResult{Artifact: artifact, CommitSHA: commitResult.SHA}, nil
}

// Read reads an artifact from Git at the specified ref (or HEAD if empty).
func (s *Service) Read(ctx context.Context, path, ref string) (*domain.Artifact, error) {
	if ref == "" {
		ref = "HEAD"
	}

	repoPath := s.repoRelativePath(path)
	content, err := s.git.ReadFile(ctx, ref, repoPath)
	if err != nil {
		if gitErr, ok := err.(*git.GitError); ok && gitErr.Kind == git.ErrKindNotFound {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("artifact not found: %s at ref %s", path, ref))
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	artifact, err := Parse(path, content)
	if err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("parse artifact %s: %v", path, err))
	}

	return artifact, nil
}

// Update updates an existing artifact, validates the new content, commits to Git,
// and emits an artifact_updated event.
func (s *Service) Update(ctx context.Context, path, content string) (*WriteResult, error) {
	return s.writeAndCommit(ctx, path, content, writeOp{
		requireExists: true,
		messageVerb:   "Update",
		operation:     "artifact.update",
		eventType:     domain.EventArtifactUpdated,
		logAction:     "artifact updated",
	})
}

// List scans the repository for all artifacts.
func (s *Service) List(ctx context.Context, ref string) ([]*domain.Artifact, error) {
	if ref == "" {
		ref = "HEAD"
	}

	// Scope listing to the artifacts directory.
	pattern := "*.md"
	if s.artifactsDir != "/" && s.artifactsDir != "" {
		pattern = s.artifactsDir + "/"
	}

	files, err := s.git.ListFiles(ctx, ref, pattern)
	if err != nil {
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	var artifacts []*domain.Artifact
	for _, file := range files {
		// Only include .md files when using directory prefix filter.
		if !strings.HasSuffix(file, ".md") {
			continue
		}

		content, err := s.git.ReadFile(ctx, ref, file)
		if err != nil {
			continue // skip unreadable files
		}

		if !IsArtifact(content) {
			continue
		}

		// Strip the artifacts_dir prefix so paths remain artifacts-relative.
		artifactPath := s.stripArtifactsDir(file)
		a, err := Parse(artifactPath, content)
		if err != nil {
			continue // skip unparseable artifacts
		}

		artifacts = append(artifacts, a)
	}

	return artifacts, nil
}

// checkBranchProtection consults the installed branch-protection policy
// (ADR-009 §3) before the Service advances any ref. The Artifact Service
// only produces direct commits — merges are the Orchestrator's job — so
// every write is classified as OpDirectWrite. A nil policy is fail-closed:
// the Service refuses to write until WithPolicy has installed one.
//
// Branch resolution: a non-empty WriteContext.Branch is the destination
// (typically a "spine/run/*" branch that matches no protection rule). A
// missing WriteContext means the write would land on the main repo's
// authoritative branch — s.defaultRef. That is the case the policy exists
// to gate; a contributor who omits WriteContext and targets "main" is
// exactly what "no-direct-write" is for.
//
// Override is threaded from WriteContext.Override (set by the gateway's
// handleArtifact* path from write_context.override in the request body).
// The policy evaluator gates effective use on Actor.Role ≥ operator; a
// contributor who sets the flag sees a distinct "override not authorised"
// reason rather than a silent "rule denies".
//
// Returns (overrideHonoured, err). The caller uses overrideHonoured to
// stamp the commit trailer and emit the governance event (ADR-009 §4);
// neither is produced when the flag was set but did not actually bypass
// a rule (e.g. a branch with no matching rule — the write would have
// succeeded either way).
type branchProtectResult struct {
	overrideHonoured bool
	branch           string
	ruleKinds        []string
}

func (s *Service) checkBranchProtection(ctx context.Context, operation string) (branchProtectResult, error) {
	if s.policy == nil {
		return branchProtectResult{}, domain.NewError(domain.ErrUnavailable,
			"artifact service: branch-protection policy not configured (production must call WithPolicy; tests may install a permissive policy)")
	}

	branch := s.defaultRef
	override := false
	if wc := GetWriteContext(ctx); wc != nil {
		if wc.Branch != "" {
			branch = wc.Branch
		}
		override = wc.Override
	}

	req := branchprotect.Request{
		Branch:   branch,
		Kind:     branchprotect.OpDirectWrite,
		Actor:    actorForRequest(ctx),
		Override: override,
		RunID:    observe.RunID(ctx),
		TraceID:  observe.TraceID(ctx),
	}

	decision, reasons, evalErr := s.policy.Evaluate(ctx, req)
	if evalErr != nil {
		// Policy error is fail-closed — the evaluator itself signals Deny
		// with a non-nil error when the rule source is unreachable, and
		// we surface that as ErrInternal so the gateway renders a 5xx
		// rather than a 403 that implies "this is forbidden by rule".
		observe.Logger(ctx).Error("branch-protection evaluation failed",
			"operation", operation,
			"branch", branch,
			"error", evalErr.Error(),
		)
		return branchProtectResult{}, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("branch-protection evaluation failed: %v", evalErr))
	}

	if decision == branchprotect.DecisionDeny {
		observe.Logger(ctx).Info("branch-protection denied write",
			"operation", operation,
			"branch", branch,
			"reasons", reasonCodes(reasons),
		)
		return branchProtectResult{}, domain.NewErrorWithDetail(domain.ErrForbidden,
			firstDenyMessage(reasons, branch),
			map[string]any{
				"branch":  branch,
				"reasons": reasonsToDetail(reasons),
			})
	}

	honoured, kinds := overrideHonoured(reasons)
	return branchProtectResult{
		overrideHonoured: honoured,
		branch:           branch,
		ruleKinds:        kinds,
	}, nil
}

// overrideHonoured reports whether the policy's allow-decision reasons
// indicate that an override actually bypassed a matching rule, and
// returns the bypassed rule kinds. A write that allowed without invoking
// override (e.g. no matching rule) returns (false, nil) — the caller
// skips the governance event and commit trailer, per ADR-009 §4.
func overrideHonoured(reasons []branchprotect.Reason) (bool, []string) {
	var kinds []string
	for _, r := range reasons {
		if r.Code == branchprotect.ReasonOverrideHonoured {
			kinds = append(kinds, string(r.RuleKind))
		}
	}
	return len(kinds) > 0, kinds
}

// actorForRequest resolves the authenticated actor from ctx for use in a
// branchprotect.Request. Falls back to a zero-role actor identified only
// by observe.ActorID(ctx) when no full actor is bound — the evaluator
// then treats Override requests as unauthorised (correct fail-closed
// behaviour for the unauthenticated / test paths).
func actorForRequest(ctx context.Context) domain.Actor {
	if a := domain.ActorFromContext(ctx); a != nil {
		return *a
	}
	return domain.Actor{ActorID: observe.ActorID(ctx)}
}

func reasonCodes(reasons []branchprotect.Reason) []string {
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = string(r.Code)
	}
	return out
}

func reasonsToDetail(reasons []branchprotect.Reason) []map[string]string {
	out := make([]map[string]string, len(reasons))
	for i, r := range reasons {
		entry := map[string]string{
			"code":    string(r.Code),
			"message": r.Message,
		}
		if r.RuleKind != "" {
			entry["rule_kind"] = string(r.RuleKind)
		}
		out[i] = entry
	}
	return out
}

func firstDenyMessage(reasons []branchprotect.Reason, branch string) string {
	if len(reasons) == 0 {
		return fmt.Sprintf("branch %q is protected", branch)
	}
	return reasons[0].Message
}

// enterBranch prepares an isolated working directory for branch-scoped writes.
// When a WriteContext specifies a branch, a git worktree is created so that
// file I/O and commits target the branch without changing the main working
// tree. Returns a *git.WriteScope whose RepoDir should be used for all file
// and git operations, and whose Cleanup must be deferred.
func (s *Service) enterBranch(ctx context.Context) (*git.WriteScope, error) {
	wc := GetWriteContext(ctx)
	branch := ""
	if wc != nil {
		branch = wc.Branch
	}
	scope, err := git.EnterBranch(ctx, s.repo, branch, validateGitRefName)
	if err != nil {
		// Preserve typed domain error so the gateway surfaces git_error, not
		// internal_error, on the realistic "branch missing or already
		// checked out" failure path. validateGitRefName already returns a
		// typed error; passthrough only the untyped worktree failure.
		if _, ok := err.(*domain.SpineError); ok {
			return nil, err
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}
	return scope, nil
}

// resolveToExistingAncestor resolves symlinks by walking up the path
// to the nearest existing ancestor, then appending the remaining components.
// This prevents symlink escapes via missing subdirectories.
func resolveToExistingAncestor(absPath string) (string, error) {
	// Try the full path first
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return resolved, nil
	}

	// Walk up to find the nearest existing ancestor
	current := absPath
	var remainder []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root without finding an existing path
			return absPath, nil
		}
		remainder = append([]string{filepath.Base(current)}, remainder...)
		current = parent

		if ancestorReal, err := filepath.EvalSymlinks(current); err == nil {
			// Found an existing ancestor — reconstruct the path
			resolved := ancestorReal
			for _, part := range remainder {
				resolved = filepath.Join(resolved, part)
			}
			return resolved, nil
		}
	}
}

// stripArtifactsDir removes the artifacts directory prefix from a repo-relative path.
func (s *Service) stripArtifactsDir(repoRelPath string) string {
	if s.artifactsDir == "/" || s.artifactsDir == "" {
		return repoRelPath
	}
	prefix := s.artifactsDir + "/"
	return strings.TrimPrefix(repoRelPath, prefix)
}

// autoPush pushes the current branch to origin after a commit.
// repoDir is the working directory (main repo or worktree) used to determine
// the current branch. The push itself goes through the main git client since
// worktrees share the same object store.
// Push failures are logged as warnings but do not fail the operation.
// Disabled when SPINE_GIT_AUTO_PUSH is set to "false".
func (s *Service) autoPush(ctx context.Context, repoDir string) {
	if strings.EqualFold(os.Getenv("SPINE_GIT_AUTO_PUSH"), "false") {
		return
	}

	log := observe.Logger(ctx)

	branch, err := git.CurrentBranch(ctx, repoDir)
	if err != nil {
		log.Warn("auto-push: failed to determine current branch", "error", err)
		return
	}

	if err := s.git.Push(ctx, "origin", branch); err != nil {
		log.Warn("auto-push: push failed", "branch", branch, "error", err)
	}
}

// emitEvent publishes a domain event for artifact changes.
func (s *Service) emitEvent(ctx context.Context, eventType domain.EventType, a *domain.Artifact, commitSHA string) {
	if s.events == nil {
		return
	}

	eventID, _ := observe.GenerateTraceID()
	evt := domain.Event{
		EventID:      eventID,
		Type:         eventType,
		Timestamp:    time.Now(),
		ActorID:      observe.ActorID(ctx),
		RunID:        observe.RunID(ctx),
		ArtifactPath: a.Path,
		TraceID:      observe.TraceID(ctx),
		Payload: mustJSON(map[string]string{
			"commit_sha":    commitSHA,
			"artifact_id":   a.ID,
			"artifact_type": string(a.Type),
			"status":        string(a.Status),
		}),
	}

	// Fire and forget — event delivery is async, errors logged by EmitLogged.
	event.EmitLogged(ctx, s.events, evt)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// emitBranchProtectionOverride emits the governance event required by
// ADR-009 §4 for every honored override on the Spine write path. The
// payload captures the actor, branch, bypassed rule kinds, operation,
// trace/run identifiers, and the resulting commit SHA — commitSHA is
// empty on the Delete path (not yet exposed on the API), which the event
// renders as null via omitempty.
func (s *Service) emitBranchProtectionOverride(ctx context.Context, operation, branch string, ruleKinds []string, commitSHA string) {
	if s.events == nil {
		return
	}
	// Prefer the authenticated domain.Actor's ID for the governance
	// audit event — observe.ActorID is a free-floating trace field that
	// tests may leave unset, but the policy gate fired because of the
	// actor bound to the request context.
	actorID := observe.ActorID(ctx)
	if a := domain.ActorFromContext(ctx); a != nil && a.ActorID != "" {
		actorID = a.ActorID
	}
	eventID, _ := observe.GenerateTraceID()
	evt := domain.Event{
		EventID:   eventID,
		Type:      domain.EventBranchProtectionOverride,
		Timestamp: time.Now(),
		ActorID:   actorID,
		RunID:     observe.RunID(ctx),
		TraceID:   observe.TraceID(ctx),
		Payload: mustJSON(map[string]any{
			"branch":     branch,
			"rule_kinds": ruleKinds,
			"operation":  operation,
			"commit_sha": nullableSHA(commitSHA),
		}),
	}
	event.EmitLogged(ctx, s.events, evt)
}

// nullableSHA returns nil for an empty SHA so JSON marshals to null,
// matching the Git-path event shape for override-deletes (ADR-009 §4).
func nullableSHA(sha string) any {
	if sha == "" {
		return nil
	}
	return sha
}
