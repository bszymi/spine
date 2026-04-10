package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CLIClient implements GitClient by shelling out to the git CLI.
type CLIClient struct {
	repoPath         string
	credentialHelper string   // path to external credential helper script
	pushEnv          []string // extra env vars for push operations (e.g., SMP_WORKSPACE_ID=xxx)
}

// CLIOption configures a CLIClient.
type CLIOption func(*CLIClient)

// WithCredentialHelper configures the Git client to use an external credential
// helper for push authentication. The helper path must point to an existing,
// executable file. An empty path is a no-op (credential helper not configured).
func WithCredentialHelper(path string) CLIOption {
	return func(c *CLIClient) {
		c.credentialHelper = path
	}
}

// WithPushEnv adds environment variables to push operations.
// Each entry should be in KEY=VALUE format.
func WithPushEnv(env ...string) CLIOption {
	return func(c *CLIClient) {
		c.pushEnv = append(c.pushEnv, env...)
	}
}

// NewCLIClient creates a new Git client for the repository at the given path.
func NewCLIClient(repoPath string, opts ...CLIOption) *CLIClient {
	c := &CLIClient{repoPath: repoPath}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ConfigureCredentialHelper sets credential.helper in the repo-local git config.
// Must be called after the repository exists on disk (after Clone or init).
// No-op if no credential helper is configured on the client.
func (c *CLIClient) ConfigureCredentialHelper(ctx context.Context) error {
	if c.credentialHelper == "" {
		return nil
	}
	_, err := c.run(ctx, "config", "config", "--local", "credential.helper", c.credentialHelper)
	return err
}

// ValidateCredentialHelper checks that the configured credential helper path
// exists and is executable. Returns nil if no helper is configured.
func ValidateCredentialHelper(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("credential helper %q: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("credential helper %q: is a directory", path)
	}
	if info.Mode()&0111 == 0 {
		return fmt.Errorf("credential helper %q: not executable", path)
	}
	return nil
}

// Clone clones a remote repository to a local path.
// Unlike other operations, Clone does not run inside repoPath since the
// target directory may not exist yet.
func (c *CLIClient) Clone(ctx context.Context, url, path string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", url, path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		gitErr := classifyGitError("clone", strings.TrimSpace(stderr.String()))
		gitErr.Err = err
		return gitErr
	}
	return nil
}

// Commit creates a new commit with the staged changes.
// Commit messages include structured trailers per Git Integration §5.1.
func (c *CLIClient) Commit(ctx context.Context, opts CommitOpts) (CommitResult, error) {
	msg := buildCommitMessage(opts)

	args := []string{"commit", "-m", msg}
	if opts.AllowEmpty {
		args = append(args, "--allow-empty")
	}
	if opts.Author.Name != "" && opts.Author.Email != "" {
		args = append(args, "--author", fmt.Sprintf("%s <%s>", opts.Author.Name, opts.Author.Email))
	}

	if _, err := c.run(ctx, "commit", args...); err != nil {
		return CommitResult{}, err
	}

	sha, err := c.run(ctx, "rev-parse", "rev-parse", "HEAD")
	if err != nil {
		return CommitResult{}, err
	}

	return CommitResult{SHA: strings.TrimSpace(sha)}, nil
}

// Merge merges a source branch into the current branch (or target branch).
func (c *CLIClient) Merge(ctx context.Context, opts MergeOpts) (MergeResult, error) {
	if opts.Target != "" {
		if _, err := c.run(ctx, "checkout", "checkout", opts.Target); err != nil {
			return MergeResult{}, err
		}
	}

	args := []string{"merge"}
	switch opts.Strategy {
	case "fast-forward":
		args = append(args, "--ff-only")
	case "merge-commit":
		args = append(args, "--no-ff")
		if opts.Message != "" {
			msg := opts.Message
			if len(opts.Trailers) > 0 {
				msg += "\n"
				for k, v := range opts.Trailers {
					msg += fmt.Sprintf("\n%s: %s", k, v)
				}
			}
			args = append(args, "-m", msg)
		}
	}
	args = append(args, opts.Source)

	output, err := c.run(ctx, "merge", args...)
	if err != nil {
		return MergeResult{}, err
	}

	sha, err := c.run(ctx, "rev-parse", "rev-parse", "HEAD")
	if err != nil {
		return MergeResult{}, err
	}

	ff := strings.Contains(output, "Fast-forward")
	return MergeResult{SHA: strings.TrimSpace(sha), FastForward: ff}, nil
}

// CreateBranch creates a new branch from the specified base.
func (c *CLIClient) CreateBranch(ctx context.Context, name, base string) error {
	_, err := c.run(ctx, "branch", "branch", name, base)
	return err
}

// DeleteBranch deletes a local branch.
func (c *CLIClient) DeleteBranch(ctx context.Context, name string) error {
	_, err := c.run(ctx, "branch", "branch", "-D", name)
	return err
}

// Diff returns the file differences between two refs.
func (c *CLIClient) Diff(ctx context.Context, from, to string) ([]FileDiff, error) {
	output, err := c.run(ctx, "diff", "diff", "--name-status", from, to)
	if err != nil {
		return nil, err
	}

	var diffs []FileDiff
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		statusCode := parts[0]
		var fd FileDiff

		switch {
		case statusCode == "A":
			fd = FileDiff{Path: parts[1], Status: "added"}
		case statusCode == "M":
			fd = FileDiff{Path: parts[1], Status: "modified"}
		case statusCode == "D":
			fd = FileDiff{Path: parts[1], Status: "deleted"}
		case strings.HasPrefix(statusCode, "R"):
			// Rename: R<score>\t<old>\t<new>
			fd = FileDiff{Status: "renamed"}
			if len(parts) >= 3 {
				fd.OldPath = parts[1]
				fd.Path = parts[2]
			} else {
				fd.Path = parts[1]
			}
		default:
			fd = FileDiff{Path: parts[1], Status: "modified"}
		}

		diffs = append(diffs, fd)
	}
	return diffs, nil
}

// Log returns commit history.
func (c *CLIClient) Log(ctx context.Context, opts LogOpts) ([]CommitInfo, error) {
	args := []string{"log", "--format=%H%n%an%n%ae%n%aI%n%B%n---END---"}

	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", opts.Limit))
	}
	if opts.Since != "" {
		args = append(args, fmt.Sprintf("%s..HEAD", opts.Since))
	}
	if opts.Path != "" {
		args = append(args, "--", opts.Path)
	}

	output, err := c.run(ctx, "log", args...)
	if err != nil {
		return nil, err
	}

	return parseLogOutput(output), nil
}

// ReadFile reads a file at a specific Git ref.
func (c *CLIClient) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	output, err := c.run(ctx, "read_file", "show", fmt.Sprintf("%s:%s", ref, path))
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

// ListFiles lists files matching a pattern at a specific Git ref.
func (c *CLIClient) ListFiles(ctx context.Context, ref, pattern string) ([]string, error) {
	output, err := c.run(ctx, "list_files", "ls-tree", "-r", "--name-only", ref)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		if pattern == "" || matchSimpleGlob(pattern, line) {
			files = append(files, line)
		}
	}
	return files, nil
}

// Head returns the current HEAD commit SHA.
func (c *CLIClient) Head(ctx context.Context) (string, error) {
	output, err := c.run(ctx, "head", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// HasCommitWithTrailer checks if a commit with the given trailer value exists.
// Used for idempotent commit detection per Git Integration §5.6.
func (c *CLIClient) HasCommitWithTrailer(ctx context.Context, key, value string) (string, bool, error) {
	output, err := c.run(ctx, "log", "log", "--all", "--format=%H %B---END---")
	if err != nil {
		return "", false, err
	}

	trailer := fmt.Sprintf("%s: %s", key, value)
	for _, entry := range strings.Split(output, "---END---") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, trailer) {
			sha := strings.SplitN(entry, " ", 2)[0]
			return sha, true, nil
		}
	}
	return "", false, nil
}

// Push pushes a ref to the specified remote.
func (c *CLIClient) Push(ctx context.Context, remote, ref string) error {
	_, err := c.run(ctx, "push", "push", remote, ref)
	return err
}

// PushBranch pushes a branch to the specified remote with upstream tracking.
func (c *CLIClient) PushBranch(ctx context.Context, remote, branch string) error {
	_, err := c.run(ctx, "push", "push", "-u", remote, branch)
	return err
}

// DeleteRemoteBranch deletes a branch on the specified remote.
func (c *CLIClient) DeleteRemoteBranch(ctx context.Context, remote, branch string) error {
	_, err := c.run(ctx, "push", "push", remote, "--delete", branch)
	return err
}

// run executes a git command and returns stdout. On error, classifies the failure.
func (c *CLIClient) run(ctx context.Context, op string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.repoPath

	// For push operations, inject extra environment variables so the
	// credential helper can read workspace context (e.g., SMP_WORKSPACE_ID).
	if op == "push" && len(c.pushEnv) > 0 {
		cmd.Env = append(os.Environ(), c.pushEnv...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		gitErr := classifyGitError(op, strings.TrimSpace(stderr.String()))
		gitErr.Err = err
		return "", gitErr
	}

	return stdout.String(), nil
}

// buildCommitMessage constructs a commit message with trailers.
func buildCommitMessage(opts CommitOpts) string {
	var b strings.Builder
	b.WriteString(opts.Message)

	if opts.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(opts.Body)
	}

	if len(opts.Trailers) > 0 {
		b.WriteString("\n")
		// Write trailers in deterministic order
		for _, key := range []string{"Trace-ID", "Actor-ID", "Run-ID", "Operation"} {
			if val, ok := opts.Trailers[key]; ok {
				b.WriteString("\n")
				b.WriteString(key)
				b.WriteString(": ")
				b.WriteString(val)
			}
		}
		// Write any remaining trailers not in the standard set
		for k, v := range opts.Trailers {
			if k == "Trace-ID" || k == "Actor-ID" || k == "Run-ID" || k == "Operation" {
				continue
			}
			b.WriteString("\n")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
		}
	}

	return b.String()
}

// parseLogOutput parses the custom-formatted git log output.
func parseLogOutput(output string) []CommitInfo {
	var commits []CommitInfo

	entries := strings.Split(output, "---END---")
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		lines := strings.SplitN(entry, "\n", 5)
		if len(lines) < 4 {
			continue
		}

		ts, _ := time.Parse(time.RFC3339, strings.TrimSpace(lines[3]))

		ci := CommitInfo{
			SHA:       strings.TrimSpace(lines[0]),
			Author:    Author{Name: strings.TrimSpace(lines[1]), Email: strings.TrimSpace(lines[2])},
			Timestamp: ts,
			Trailers:  make(map[string]string),
		}

		if len(lines) > 4 {
			ci.Message = strings.TrimSpace(lines[4])
			// Extract trailers from message
			for _, msgLine := range strings.Split(lines[4], "\n") {
				msgLine = strings.TrimSpace(msgLine)
				if idx := strings.Index(msgLine, ": "); idx > 0 {
					key := msgLine[:idx]
					if key == "Trace-ID" || key == "Actor-ID" || key == "Run-ID" || key == "Operation" {
						ci.Trailers[key] = msgLine[idx+2:]
					}
				}
			}
		}

		commits = append(commits, ci)
	}
	return commits
}

// matchSimpleGlob provides basic glob matching (supports * and ** prefix/suffix).
func matchSimpleGlob(pattern, path string) bool {
	if pattern == "*" || pattern == "" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(path, pattern[1:])
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(path, pattern[:len(pattern)-1])
	}
	return strings.Contains(path, pattern)
}
