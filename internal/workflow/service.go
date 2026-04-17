package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
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

// branchScope captures the working directory for a single write operation.
// When a WriteContext specifies a branch, a worktree is created so file I/O
// and commits target the branch without disturbing the main working tree;
// otherwise the main repo is used directly and cleanup is a no-op.
type branchScope struct {
	repoDir string
	cleanup func()
}

// enterBranch prepares an isolated working directory when a WriteContext
// branch is set on the context, mirroring internal/artifact.Service.enterBranch.
// Callers must defer the returned cleanup.
func (s *Service) enterBranch(ctx context.Context) (*branchScope, error) {
	wc := GetWriteContext(ctx)
	if wc == nil || wc.Branch == "" {
		return &branchScope{repoDir: s.repo, cleanup: func() {}}, nil
	}

	if err := validateGitRefName(wc.Branch); err != nil {
		return nil, err
	}

	parent, err := os.MkdirTemp("", "spine-wf-wt-*")
	if err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("create worktree parent: %v", err))
	}
	worktreeDir := filepath.Join(parent, "wt")

	cmd := exec.CommandContext(ctx, "git", "worktree", "add", worktreeDir, wc.Branch)
	cmd.Dir = s.repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(parent)
		return nil, domain.NewError(domain.ErrGit,
			fmt.Sprintf("add worktree for branch %s: %s", wc.Branch, strings.TrimSpace(string(out))))
	}

	return &branchScope{
		repoDir: worktreeDir,
		cleanup: func() {
			rmCmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", worktreeDir)
			rmCmd.Dir = s.repo
			_, _ = rmCmd.CombinedOutput()
			os.RemoveAll(parent)
		},
	}, nil
}

// Create writes a new workflow definition, running the full workflow
// validation suite before commit.
func (s *Service) Create(ctx context.Context, id, body string) (*WriteResult, error) {
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

	scope, err := s.enterBranch(ctx)
	if err != nil {
		return nil, err
	}
	defer scope.cleanup()

	absPath := filepath.Join(scope.repoDir, path)

	if _, err := os.Stat(absPath); err == nil {
		return nil, domain.NewError(domain.ErrAlreadyExists,
			fmt.Sprintf("workflow already exists: %s", path))
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, domain.NewError(domain.ErrInternal, fmt.Sprintf("create dir: %v", err))
	}
	if err := os.WriteFile(absPath, []byte(body), 0o644); err != nil {
		return nil, domain.NewError(domain.ErrInternal, fmt.Sprintf("write file: %v", err))
	}

	sha, err := stageAndCommit(ctx, scope.repoDir, path,
		fmt.Sprintf("Create workflow %s (%s)", wf.ID, wf.Version),
		observe.TrailersFromContext(ctx, "workflow.create"))
	if err != nil {
		_ = os.Remove(absPath)
		_ = gitReset(ctx, scope.repoDir, path)
		return nil, err
	}

	return &WriteResult{Workflow: wf, Path: path, CommitSHA: sha}, nil
}

// Update rewrites an existing workflow definition. The new body must declare a
// different version from the prior definition; the full validation suite runs
// before commit.
func (s *Service) Update(ctx context.Context, id, body string) (*WriteResult, error) {
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

	scope, err := s.enterBranch(ctx)
	if err != nil {
		return nil, err
	}
	defer scope.cleanup()

	absPath := filepath.Join(scope.repoDir, path)

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return nil, domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("workflow not found: %s", path))
	}

	prior, err := s.readFromDisk(absPath, path)
	if err != nil {
		return nil, err
	}
	if wf.Version == prior.Version {
		return nil, domain.NewError(domain.ErrValidationFailed,
			fmt.Sprintf("workflow.update requires a version bump (prior=%s, submitted=%s)",
				prior.Version, wf.Version))
	}

	originalBody, readErr := os.ReadFile(absPath)
	if readErr != nil {
		return nil, domain.NewError(domain.ErrInternal, fmt.Sprintf("read original: %v", readErr))
	}

	if err := os.WriteFile(absPath, []byte(body), 0o644); err != nil {
		return nil, domain.NewError(domain.ErrInternal, fmt.Sprintf("write file: %v", err))
	}

	sha, err := stageAndCommit(ctx, scope.repoDir, path,
		fmt.Sprintf("Update workflow %s (%s -> %s)", wf.ID, prior.Version, wf.Version),
		observe.TrailersFromContext(ctx, "workflow.update"))
	if err != nil {
		_ = os.WriteFile(absPath, originalBody, 0o644)
		return nil, err
	}

	return &WriteResult{Workflow: wf, Path: path, CommitSHA: sha}, nil
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

// readFromDisk is a cheap read used internally (no git round-trip).
func (s *Service) readFromDisk(absPath, path string) (*domain.WorkflowDefinition, error) {
	content, err := os.ReadFile(absPath)
	if err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("read existing workflow: %v", err))
	}
	wf, err := Parse(path, content)
	if err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("parse existing workflow: %v", err))
	}
	return wf, nil
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

// stageAndCommit stages a single file and commits it with the given message
// and structured trailers. Mirrors the pattern in internal/artifact but
// avoids the worktree machinery since workflow writes target the
// authoritative branch only.
func stageAndCommit(ctx context.Context, repoDir, path, message string, trailers map[string]string) (string, error) {
	msg := message
	if len(trailers) > 0 {
		msg += "\n"
		for _, key := range []string{"Trace-ID", "Actor-ID", "Run-ID", "Operation"} {
			if val, ok := trailers[key]; ok {
				msg += "\n" + key + ": " + val
			}
		}
	}

	if err := runGit(ctx, repoDir, "add", "--", path); err != nil {
		return "", domain.NewError(domain.ErrGit, fmt.Sprintf("stage %s: %v", path, err))
	}

	args := []string{"commit", "-m", msg}
	actor := observe.ActorID(ctx)
	if actor != "" {
		args = append(args, "--author", fmt.Sprintf("%s <%s@spine.local>", actor, actor))
	}
	args = append(args, "--", path)
	if err := runGit(ctx, repoDir, args...); err != nil {
		_ = gitReset(ctx, repoDir, path)
		return "", domain.NewError(domain.ErrGit, fmt.Sprintf("commit %s: %v", path, err))
	}

	out, err := runGitOutput(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", domain.NewError(domain.ErrGit, fmt.Sprintf("rev-parse: %v", err))
	}
	return strings.TrimSpace(out), nil
}

func gitReset(ctx context.Context, repoDir, path string) error {
	return runGit(ctx, repoDir, "reset", "HEAD", "--", path)
}

// execGit is exposed as a variable so tests can stub git invocations.
var execGit = func(ctx context.Context, repoDir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	return cmd.CombinedOutput()
}

func runGit(ctx context.Context, repoDir string, args ...string) error {
	out, err := execGit(ctx, repoDir, args...)
	if err != nil {
		return fmt.Errorf("%s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func runGitOutput(ctx context.Context, repoDir string, args ...string) (string, error) {
	out, err := execGit(ctx, repoDir, args...)
	if err != nil {
		return "", fmt.Errorf("%s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
