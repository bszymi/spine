package workflow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/git/refname"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/worktreefs"
)

// WorkflowsDir is the canonical repo-relative directory for workflow definitions.
const WorkflowsDir = "workflows"

// idPattern restricts workflow IDs to a safe filename-friendly set. The ID is
// used directly in the repo-relative path, so path traversal and shell-unsafe
// characters must be rejected here.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-.]*$`)

// WriteResult is returned from Create and Update.
type WriteResult struct {
	Workflow  *domain.WorkflowDefinition
	Path      string
	CommitSHA string
}

// ReadResult is returned from Read.
type ReadResult struct {
	Workflow     *domain.WorkflowDefinition
	Path         string
	Body         string
	SourceCommit string
}

// Service implements workflow definition CRUD backed by Git. By default writes
// commit to the authoritative branch; when a WriteContext with a branch is
// attached to the operation context, writes are scoped to that branch via a
// git worktree (ADR-008 — workflow edits through the lifecycle workflow).
//
// Branch protection (ADR-009 §3, INIT-023 TASK-014, audit F7). With a policy
// installed (WithPolicy), every write is checked before any worktree or file
// is touched: a non-bypass write is evaluated as an OpDirectWrite on its real
// target branch — the WriteContext branch, or the authoritative branch
// (WithDefaultRef, default "main") — so the bootstrap `main: no-direct-write`
// rule now applies to workflow files too; an ADR-008 bypass write
// (WithBypass) requires an operator-or-above domain.Actor in the context and
// skips rule evaluation on the authoritative branch, because the bypass IS
// the sanctioned, trailer-audited operator direct commit.
//
// The zero value (no policy) is the pre-TASK-014 behaviour, byte for byte:
// no rule is evaluated and no role is checked, bypass or not. Unlike
// artifact.Service, a nil policy is NOT fail-closed: embedders (SMP) build
// this service with no policy and write only through actor-less bypass
// contexts, and every one of those writes would otherwise be refused.
// Standalone `spine serve` gets protection by wiring the policy, not by
// changing the zero value. An embedder that installs a policy must bind a
// domain.Actor of role >= operator into every bypass write context.
type Service struct {
	git        git.GitClient
	repo       string
	policy     branchprotect.Policy // nil = no evaluation, no bypass role check (pre-TASK-014)
	defaultRef string               // branch reported to the policy when no WriteContext branch is set
	// lifecycle, when set, refuses a body whose commit.status is not a
	// declared status of its applies_to types (INIT-023 TASK-017). nil =
	// no check (the pre-TASK-017 behaviour).
	lifecycle *domain.StatusLifecycle
}

// NewService constructs a workflow Service with no branch-protection policy
// (see Service) and "main" as the authoritative branch reported to a policy
// installed later.
func NewService(gitClient git.GitClient, repoPath string) *Service {
	return &Service{git: gitClient, repo: repoPath, defaultRef: "main"}
}

// WithPolicy installs the ADR-009 branch-protection guard on the write path
// (INIT-023 TASK-014). Passing nil restores the no-policy behaviour (see
// Service): no rule evaluation and no role check on bypass writes.
func (s *Service) WithPolicy(p branchprotect.Policy) {
	s.policy = p
}

// WithDefaultRef sets the authoritative branch reported to the policy when a
// write carries no WriteContext branch (default "main"). An empty ref is
// ignored.
func (s *Service) WithDefaultRef(ref string) {
	if ref != "" {
		s.defaultRef = ref
	}
}

// WithStatusLifecycle installs the declared artifact-status lifecycle
// (INIT-023 TASK-017, audit P3-21): Create and Update then refuse, before
// anything is written, a body whose commit.status is not a declared status
// of every applies_to type (ValidateCommitStatuses), and ValidateBody
// reports the same errors. nil (the default) turns the check off. The
// check is deliberately not part of Validate, the projection or the
// loaders (see ValidateCommitStatuses). Like the other With* setters it is
// a setup-time call.
func (s *Service) WithStatusLifecycle(l *domain.StatusLifecycle) {
	s.lifecycle = l
}

// enterBranch prepares an isolated working directory when a WriteContext
// branch is set on the context. Callers must defer the returned scope's
// Cleanup. Untyped worktree failures are wrapped as ErrGit so the gateway
// preserves the git_error response contract.
func (s *Service) enterBranch(ctx context.Context) (*git.WriteScope, error) {
	wc := GetWriteContext(ctx)
	branch := ""
	if wc != nil {
		branch = wc.Branch
	}
	scope, err := git.EnterBranch(ctx, s.repo, branch, refname.Validate)
	if err != nil {
		if _, ok := err.(*domain.SpineError); ok {
			return nil, err
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}
	return scope, nil
}

// Create writes a new workflow definition, running the full workflow
// validation suite before commit.
func (s *Service) Create(ctx context.Context, id, body string) (*WriteResult, error) {
	return s.writeAndCommit(ctx, id, body, wfWriteOp{
		requireExists: false,
		operation:     "workflow.create",
		messageFn: func(wf, _ *domain.WorkflowDefinition) string {
			return fmt.Sprintf("Create workflow %s (%s)", wf.ID, wf.Version)
		},
	})
}

// Update rewrites an existing workflow definition. The new body must declare a
// different version from the prior definition; the full validation suite runs
// before commit.
func (s *Service) Update(ctx context.Context, id, body string) (*WriteResult, error) {
	return s.writeAndCommit(ctx, id, body, wfWriteOp{
		requireExists: true,
		operation:     "workflow.update",
		messageFn: func(wf, prior *domain.WorkflowDefinition) string {
			return fmt.Sprintf("Update workflow %s (%s -> %s)", wf.ID, prior.Version, wf.Version)
		},
		priorCheck: func(wf, prior *domain.WorkflowDefinition) error {
			if wf.Version == prior.Version {
				return domain.NewError(domain.ErrValidationFailed,
					fmt.Sprintf("workflow.update requires a version bump (prior=%s, submitted=%s)",
						prior.Version, wf.Version))
			}
			return nil
		},
	})
}

// wfWriteOp encodes the axes that distinguish Create from Update: pre-check
// semantics, commit-message verb, operation trailer, and the optional
// prior-workflow check (Update's version-bump rule). Both paths share the
// parse → validate → enterBranch → pre-check → write → commit →
// rollback-on-error skeleton.
type wfWriteOp struct {
	requireExists bool
	operation     string
	messageFn     func(wf, prior *domain.WorkflowDefinition) string
	priorCheck    func(wf, prior *domain.WorkflowDefinition) error
}

func (s *Service) writeAndCommit(ctx context.Context, id, body string, op wfWriteOp) (*WriteResult, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	path := FilePath(id)

	// ProjectionDefinition, not bare Parse: a body the projection would
	// refuse (strict-parse failure, or a definition whose stored JSON
	// reads back differently, e.g. `version: 2026-10-06`) is refused here
	// with the same ErrValidationFailed a parse failure returns, so an
	// author learns at create/update time instead of finding a silently
	// kept or quarantined row (INIT-023 TASK-011 §7).
	wf, _, err := ProjectionDefinition(path, []byte(body))
	if err != nil {
		return nil, domain.NewError(domain.ErrValidationFailed, err.Error())
	}
	if wf.ID != id {
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("body id %q does not match path id %q", wf.ID, id))
	}
	if result := Validate(wf); result.Status == "failed" {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"workflow validation failed", result.Errors)
	}
	// Declared commit.status (INIT-023 TASK-017): refused before the
	// branch-protection check and enterBranch, so nothing is written or
	// committed.
	if errs := ValidateCommitStatuses(wf, s.lifecycle); len(errs) > 0 {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"workflow validation failed", errs)
	}

	// workflows/<id>.yaml is already constrained by idPattern; Clean adds
	// the length caps and the git-dir rule, before any worktree exists.
	rel, err := worktreefs.Clean(path)
	if err != nil {
		return nil, worktreeError(ctx, path, "validate path", err)
	}

	// Branch protection runs before enterBranch, the same placement as
	// artifact.Service: a refused write never creates a worktree or
	// touches a file (INIT-023 TASK-014 I3).
	if err := s.checkBranchProtection(ctx, op.operation); err != nil {
		return nil, err
	}

	scope, err := s.enterBranch(ctx)
	if err != nil {
		return nil, err
	}
	defer scope.Cleanup()
	// Every git call inside the scope uses sctx (INIT-023 TASK-012; see
	// artifact.Service.writeAndCommit). workflow.Service emits no events.
	sctx := scope.Context(ctx)

	// All file I/O below goes through worktreefs (INIT-023 TASK-004, audit
	// F5). On 61994fe this joined scope.RepoDir + path and called os.* with
	// no containment check at all: a committed workflows/<id>.yaml link
	// made Create write outside the repository and Update overwrite (and
	// read without bound — a link to /dev/zero) whatever it pointed at.
	tree, err := worktreefs.Open(scope.RepoDir)
	if err != nil {
		return nil, worktreeError(ctx, path, "open worktree for", err)
	}
	defer func() { _ = tree.Close() }()

	var prior *domain.WorkflowDefinition
	var originalBody []byte
	var originalMode fs.FileMode
	var undo *worktreefs.Undo
	if op.requireExists {
		fi, err := tree.Lstat(rel)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("workflow not found: %s", path))
		} else if err != nil {
			return nil, worktreeError(ctx, path, "check", err)
		}
		originalMode = fi.Mode().Perm()
		// One confined, capped read serves both the prior-version check
		// and the rollback copy.
		original, readErr := tree.ReadFile(rel)
		if readErr != nil {
			return nil, worktreeError(ctx, path, "read existing workflow", readErr)
		}
		p, parseErr := parseExisting(path, original)
		if parseErr != nil {
			return nil, parseErr
		}
		prior = p
		if op.priorCheck != nil {
			if err := op.priorCheck(wf, prior); err != nil {
				return nil, err
			}
		}
		originalBody = original
		if err := tree.Replace(rel, []byte(body)); err != nil {
			return nil, worktreeError(ctx, path, "write file", err)
		}
	} else {
		undo, err = tree.Create(rel, []byte(body), 0o644)
		if errors.Is(err, fs.ErrExist) {
			return nil, domain.NewError(domain.ErrAlreadyExists,
				fmt.Sprintf("workflow already exists: %s", path))
		}
		if err != nil {
			return nil, worktreeError(ctx, path, "write file", err)
		}
		defer undo.Release()
	}

	commit, err := git.StageAndCommit(sctx, scope, rel, git.CommitOpts{
		Message:  op.messageFn(wf, prior),
		Trailers: workflowTrailers(ctx, op.operation),
		Author:   workflowAuthor(ctx),
	})
	if err != nil {
		// Restore the original bytes and mode (update; WriteFile also
		// re-creates a file a failing hook deleted, and still refuses a
		// link in its place), or remove the file and every directory this
		// call created (create) — including a file a failing hook put in
		// its place by a rename, when the path is still untracked in HEAD
		// (claimUntracked).
		if op.requireExists {
			_ = tree.WriteFile(rel, originalBody, originalMode)
		} else {
			_ = undo.RollbackClaiming(claimUntracked(sctx, scope.RepoDir, rel))
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	return &WriteResult{Workflow: wf, Path: path, CommitSHA: commit.SHA}, nil
}

// claimUntracked is the worktreefs.Undo.RollbackClaiming callback for a
// create whose commit failed; see the artifact package's claimUntracked,
// which it mirrors (INIT-023 TASK-004 review round 2). A file a failing
// hook left in place of the one Create made is removed only while rel is
// untracked in HEAD; a tracked path or any git failure keeps it.
func claimUntracked(ctx context.Context, repoDir, rel string) func() bool {
	return func() bool {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		tracked, err := git.TrackedInHEAD(cctx, repoDir, rel)
		if err != nil {
			observe.Logger(ctx).Warn("workflow rollback: cannot tell whether the replaced file is tracked; keeping it",
				"path", rel, "error", err)
			return false
		}
		return !tracked
	}
}

// Read returns the executable workflow definition at the given ref. Empty ref
// means HEAD on the authoritative branch.
func (s *Service) Read(ctx context.Context, id, ref string) (*ReadResult, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	if ref == "" {
		ref = "HEAD"
	}

	path := FilePath(id)
	content, err := s.git.ReadFile(ctx, ref, path)
	if err != nil {
		if gitErr, ok := err.(*git.GitError); ok && gitErr.Kind == git.ErrKindNotFound {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("workflow not found: %s at ref %s", path, ref))
		}
		// A blob over the git client's read limit (INIT-023 TASK-003) is
		// 413, like an oversized worktree read (TASK-004).
		if errors.Is(err, git.ErrBlobTooLarge) {
			// The cause's own text names the limit that refused it: the
			// per-blob read limit, or git's allocation limit for a file
			// stored as a delta on a large base.
			return nil, domain.NewErrorWithCause(domain.ErrPayloadTooLarge,
				fmt.Sprintf("workflow %s at ref %s is refused by the read limits: %v", path, ref, err), err)
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	wf, err := Parse(path, content)
	if err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("parse workflow %s: %v", path, err))
	}

	head, _ := s.git.Head(ctx)
	return &ReadResult{Workflow: wf, Path: path, Body: string(content), SourceCommit: head}, nil
}

// ListOptions narrows the result of List.
type ListOptions struct {
	AppliesTo string // filter: applies_to contains this artifact type
	Status    string // filter: exact status match
	Mode      string // filter: exact mode match
}

// List returns summaries for all workflow definitions on the authoritative
// branch. The result contains the full parsed definition — callers that only
// need summary fields should discard the body.
func (s *Service) List(ctx context.Context, opts ListOptions) ([]*domain.WorkflowDefinition, error) {
	files, err := s.git.ListFiles(ctx, "HEAD", WorkflowsDir+"/")
	if err != nil {
		return nil, domain.NewError(domain.ErrGit, fmt.Sprintf("list workflows: %v", err))
	}

	var out []*domain.WorkflowDefinition
	for _, f := range files {
		if !strings.HasSuffix(f, ".yaml") && !strings.HasSuffix(f, ".yml") {
			continue
		}
		// A path the projection refuses (INIT-023 TASK-003) stays out of
		// the listing, as it was while git C-quoted it.
		if domain.UnsafeRepositoryPath(f) {
			continue
		}
		content, err := s.git.ReadFile(ctx, "HEAD", f)
		if err != nil {
			continue
		}
		wf, err := Parse(f, content)
		if err != nil {
			continue
		}
		if opts.Status != "" && string(wf.Status) != opts.Status {
			continue
		}
		if opts.Mode != "" && wf.Mode != opts.Mode {
			continue
		}
		if opts.AppliesTo != "" && !contains(wf.AppliesTo, opts.AppliesTo) {
			continue
		}
		out = append(out, wf)
	}
	return out, nil
}

// ValidateBody parses and validates a candidate body without persisting.
//
// It mirrors Create/Update: the body must pass ProjectionDefinition. A
// strict-parse failure reports RuleID "parse"; a body that parses but
// would not project faithfully reports RuleID "projection_round_trip"
// (INIT-023 TASK-011 §7).
func (s *Service) ValidateBody(ctx context.Context, id, body string) domain.ValidationResult {
	path := FilePath(id)
	wf, _, err := ProjectionDefinition(path, []byte(body))
	if err != nil {
		ruleID := "parse"
		if errors.Is(err, ErrProjectionRoundTrip) {
			ruleID = ProjectionRoundTripRuleID
		}
		return domain.ValidationResult{
			Status: "failed",
			Errors: []domain.ValidationError{{
				RuleID:   ruleID,
				Severity: "error",
				Message:  err.Error(),
			}},
		}
	}
	result := Validate(wf)
	// The same commit.status errors Create/Update refuse with (INIT-023
	// TASK-017), so the validate preview matches create.
	if errs := ValidateCommitStatuses(wf, s.lifecycle); len(errs) > 0 {
		result.Errors = append(result.Errors, errs...)
		result.Status = "failed"
	}
	return result
}

// ProjectionRoundTripRuleID is the validation rule id ValidateBody reports
// for a body that strict Parse accepts but the projection would refuse
// (ErrProjectionRoundTrip). Embedders that mirror ValidateBody use it so
// both surfaces report the same rule.
const ProjectionRoundTripRuleID = "projection_round_trip"

// parseExisting parses the workflow currently on disk (read through
// worktreefs by the caller).
func parseExisting(path string, content []byte) (*domain.WorkflowDefinition, error) {
	wf, err := Parse(path, content)
	if err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("parse existing workflow: %v", err))
	}
	return wf, nil
}

// worktreeError maps a worktreefs failure onto the workflow error contract
// (the same one artifact.Service uses): a refused path is ErrInvalidParams
// with the PathError text, which names only repo-relative components; a
// file over the worktree read cap is ErrPayloadTooLarge (413); anything
// else is logged server-side and returned as a generic ErrInternal. The
// worktreefs error stays the cause, so errors.Is(err,
// worktreefs.ErrUnsafePath) / worktreefs.ErrTooLarge hold.
func worktreeError(ctx context.Context, path, action string, err error) error {
	var pe *worktreefs.PathError
	if errors.As(err, &pe) {
		return domain.NewErrorWithCause(domain.ErrInvalidParams, pe.Error(), err)
	}
	var se *worktreefs.SizeError
	if errors.As(err, &se) {
		return domain.NewErrorWithCause(domain.ErrPayloadTooLarge, se.Error(), err)
	}
	observe.Logger(ctx).Warn("workflow worktree I/O failed",
		"action", action, "path", path, "error", err)
	return domain.NewErrorWithCause(domain.ErrInternal,
		fmt.Sprintf("%s %s failed", action, path), err)
}

// ValidateID reports whether id is a valid workflow id: ErrInvalidParams
// unless it matches idPattern. The id becomes a repository path component
// (FilePath), so traversal and shell-unsafe characters are refused here.
// Planning-run starters call it before any branch or run record exists.
func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("invalid workflow id %q (must match %s)", id, idPattern.String()))
	}
	return nil
}

// FilePath is the repo-relative path of workflow id's definition:
// workflows/<id>.yaml. It is also the TaskPath of the workflow-lifecycle
// planning run that governs that definition (ADR-008). Callers validate id
// first (ValidateID).
func FilePath(id string) string {
	return WorkflowsDir + "/" + id + ".yaml"
}

func contains(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}

// workflowTrailers builds the trailer set for workflow writes, adding the
// Workflow-Bypass trailer (ADR-008 §5) when the context is marked as an
// operator-bypass write.
func workflowTrailers(ctx context.Context, operation string) map[string]string {
	trailers := observe.TrailersFromContext(ctx, operation)
	if IsBypass(ctx) {
		trailers["Workflow-Bypass"] = "true"
	}
	return trailers
}

// workflowAuthor derives the commit Author from the request actor, matching
// the pre-extraction convention "<actor> <actor@spine.local>".
func workflowAuthor(ctx context.Context) git.Author {
	actor := observe.ActorID(ctx)
	if actor == "" {
		return git.Author{}
	}
	return git.Author{Name: actor, Email: actor + "@spine.local"}
}
