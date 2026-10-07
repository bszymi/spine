package workflow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/git/refname"
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
type Service struct {
	git  git.GitClient
	repo string
}

// NewService constructs a workflow Service.
func NewService(gitClient git.GitClient, repoPath string) *Service {
	return &Service{git: gitClient, repo: repoPath}
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
	if err := validateID(id); err != nil {
		return nil, err
	}
	path := filePath(id)

	wf, err := Parse(path, []byte(body))
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

	// workflows/<id>.yaml is already constrained by idPattern; Clean adds
	// the length caps and the git-dir rule, before any worktree exists.
	rel, err := worktreefs.Clean(path)
	if err != nil {
		return nil, worktreeError(ctx, path, "validate path", err)
	}

	scope, err := s.enterBranch(ctx)
	if err != nil {
		return nil, err
	}
	defer scope.Cleanup()

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
	var undo *worktreefs.Undo
	if op.requireExists {
		if _, err := tree.Lstat(rel); errors.Is(err, fs.ErrNotExist) {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("workflow not found: %s", path))
		} else if err != nil {
			return nil, worktreeError(ctx, path, "check", err)
		}
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

	commit, err := git.StageAndCommit(ctx, scope, rel, git.CommitOpts{
		Message:  op.messageFn(wf, prior),
		Trailers: workflowTrailers(ctx, op.operation),
		Author:   workflowAuthor(ctx),
	})
	if err != nil {
		// Restore the original bytes and mode (update), or remove the file
		// and every directory this call created (create).
		if op.requireExists {
			_ = tree.Replace(rel, originalBody)
		} else {
			_ = undo.Rollback()
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	return &WriteResult{Workflow: wf, Path: path, CommitSHA: commit.SHA}, nil
}

// Read returns the executable workflow definition at the given ref. Empty ref
// means HEAD on the authoritative branch.
func (s *Service) Read(ctx context.Context, id, ref string) (*ReadResult, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	if ref == "" {
		ref = "HEAD"
	}

	path := filePath(id)
	content, err := s.git.ReadFile(ctx, ref, path)
	if err != nil {
		if gitErr, ok := err.(*git.GitError); ok && gitErr.Kind == git.ErrKindNotFound {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("workflow not found: %s at ref %s", path, ref))
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
func (s *Service) ValidateBody(ctx context.Context, id, body string) domain.ValidationResult {
	path := filePath(id)
	wf, err := Parse(path, []byte(body))
	if err != nil {
		return domain.ValidationResult{
			Status: "failed",
			Errors: []domain.ValidationError{{
				RuleID:   "parse",
				Severity: "error",
				Message:  err.Error(),
			}},
		}
	}
	return Validate(wf)
}

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

func validateID(id string) error {
	if !idPattern.MatchString(id) {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("invalid workflow id %q (must match %s)", id, idPattern.String()))
	}
	return nil
}

func filePath(id string) string {
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
