package git

// One ref grammar for the git CLI client (INIT-023 TASK-002; audit F10,
// P3-32, P3-36). This file is the only place that decides how a caller's
// ref string becomes git argv: CLIClient and EnterBranch build every ref
// argument through the helpers below and nowhere else. The class table
// and the argv rules are documented on the CLIClient type (cli.go).
//
// Why it exists. git resolves a bare name <n> by DWIM, in order:
// $GIT_DIR/<n>, refs/<n>, refs/tags/<n>, refs/heads/<n>, refs/remotes/<n>,
// refs/remotes/<n>/HEAD. Branch protection allows every push outside
// refs/heads/ (ADR-009 §6), so a pushed tag or ref named after a run
// branch (or "main") used to beat the branch: the governed merge
// published the tag's unreviewed commit under the trusted merge message,
// and a tag named "main" made every governed push fail. A qualified
// "refs/heads/<n>" is not safe either: when the branch is missing, git
// DWIM-resolves it to refs/tags/refs/heads/<n> and its siblings. So:
//
//   - a branch is looked up EXACTLY (show-ref --verify, which never
//     applies DWIM), and the object id it names, not its name, reaches
//     git;
//   - a push source is "<oid>^{commit}", which no refname can match ("^"
//     is illegal in refnames), with an explicit refs/heads/<n>
//     destination; a CreateBranch start point is "<oid>^{commit}" or
//     "HEAD^{commit}" for the same reason (`git branch` re-resolves its
//     start point by DWIM, see CreateBranch);
//   - every ref-like argument is syntax-checked before any git process
//     starts (two phases: validate all, then resolve), and reaches git
//     after --end-of-options, so a leading "-" can never become an
//     option.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/bszymi/spine/adapters/git/refname"
	"github.com/bszymi/spine/core/domain"
)

// ErrInvalidRef is wrapped by every grammar refusal. A refusal is a
// *GitError with Kind ErrKindPermanent whose Err is ErrInvalidRef, so
// errors.Is(err, ErrInvalidRef) identifies it and the engine never
// retries it (a plain error on the push path is treated as transient).
var ErrInvalidRef = errors.New("invalid git ref")

const headsPrefix = "refs/heads/"

var (
	// fullObjectName is a complete SHA-1 or SHA-256 object id. git's
	// revision parser takes a full hex id as the object itself before
	// any ref lookup (a same-named ref only draws a warning), so rev-parse,
	// show, ls-tree, diff, merge-base, log, merge and worktree add get it
	// unchanged. `git branch` is the exception: it re-resolves its start
	// point by DWIM after parsing it, so CreateBranch appends ^{commit}
	// (verified on git 2.39.5 and 2.54). Abbreviated ids are NOT object
	// names here: they are indistinguishable from branch names such as
	// "cafe".
	fullObjectName = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

	// remoteNamePattern is the Remote class. Every caller passes
	// "origin"; the pattern refuses URLs, "ext::" transports and a
	// leading "-", which ValidateRef would let through.
	remoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:/[A-Za-z0-9._-]+)*$`)
)

// IsFullObjectName reports whether s is a complete hex object id
// (40 hex digits for SHA-1, 64 for SHA-256).
func IsFullObjectName(s string) bool { return fullObjectName.MatchString(s) }

// QualifyBranch is the Branch class and the only producer of
// "refs/heads/<n>" argv. name is a short branch name or "refs/heads/<n>";
// it returns the short name and the fully qualified ref. The short name
// must pass refname.Validate (git's branch grammar plus Spine's stricter
// rules; HEAD, "@" and anything under refs/ are refused) and must not be
// a full object id, which git would read as the object rather than a
// branch. The error is the *domain.SpineError (ErrInvalidParams) from
// refname.Validate, so API callers can surface invalid_params.
func QualifyBranch(name string) (short, full string, err error) {
	short = strings.TrimPrefix(name, headsPrefix)
	if err := refname.Validate(short); err != nil {
		return "", "", err
	}
	if IsFullObjectName(short) {
		return "", "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("branch name %q is a full object id, not a branch name", short))
	}
	return short, headsPrefix + short, nil
}

// ValidateBranchName reports whether name (short, or "refs/heads/<n>")
// is acceptable as a branch argument. It is QualifyBranch without the
// results, for input guards such as a repository's default_branch.
func ValidateBranchName(name string) error {
	_, _, err := QualifyBranch(name)
	return err
}

// ValidateShortBranchName is ValidateBranchName for stored
// configuration such as a repository's default_branch: name must be the
// SHORT branch name. "refs/heads/<n>" is refused rather than accepted
// and stored verbatim, so one branch has one spelling (it would
// otherwise surface as RepositoryMergeOutcome.TargetBranch
// "refs/heads/main" and never compare equal to "main").
func ValidateShortBranchName(name string) error {
	if strings.HasPrefix(name, headsPrefix) {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("branch name %q must be the short name, without %s", name, headsPrefix))
	}
	return ValidateBranchName(name)
}

// ValidateRemoteName reports whether name is acceptable as a remote
// argument: a configured remote's name, never a URL or an option.
func ValidateRemoteName(name string) error {
	if !remoteNamePattern.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("remote %q must be a configured remote name", name)
	}
	return nil
}

// invalidRef builds the refusal every grammar check returns. %q keeps
// control bytes out of logs and error text.
func invalidRef(op, what, value string, cause error) *GitError {
	return &GitError{
		Kind:    ErrKindPermanent,
		Op:      op,
		Message: fmt.Sprintf("invalid git ref: %s %q: %v", what, value, cause),
		Err:     ErrInvalidRef,
	}
}

// --- Phase 1: syntax checks. No git process; every method runs all of
// its checks before any phase-2 lookup or command. ---

// checkBranch is phase 1 for a Branch-class argument.
func checkBranch(op, what, name string) (short, full string, err error) {
	short, full, verr := QualifyBranch(name)
	if verr != nil {
		return "", "", invalidRef(op, what, name, verr)
	}
	return short, full, nil
}

// checkCommit is phase 1 for a Commit-class argument (merge source,
// CreateBranch base): a full object id, "HEAD" when allowHEAD, or a
// branch.
func checkCommit(op, what, v string, allowHEAD bool) error {
	if IsFullObjectName(v) || (allowHEAD && v == "HEAD") {
		return nil
	}
	_, _, err := checkBranch(op, what, v)
	return err
}

// checkRev is phase 1 for a Rev-class argument: non-empty and accepted
// by ValidateRef (no leading "-", no ~ ^ : \ space or control bytes, no
// "..").
func checkRev(op, what, rev string) error {
	if rev == "" {
		return invalidRef(op, what, rev, errors.New("empty revision"))
	}
	if err := ValidateRef(rev); err != nil {
		return invalidRef(op, what, rev, err)
	}
	return nil
}

// checkRemote is phase 1 for a Remote-class argument.
func checkRemote(op, remote string) error {
	if err := ValidateRemoteName(remote); err != nil {
		return invalidRef(op, "remote", remote, err)
	}
	return nil
}

// --- Phase 2: resolution. ---

// lookupBranch looks up refs/heads/<short> exactly. git show-ref
// --verify never applies DWIM: a missing branch is never substituted by
// refs/tags/refs/heads/<short> or any other name, which is what
// `rev-parse --verify refs/heads/<short>` does. Returns the object id
// and true when the branch exists; ("", false, nil) when it does not
// (exit status non-zero AND git's "not a valid ref" marker); any other
// failure is the classified *GitError, and the caller fails closed on
// it — there is no DWIM fallback.
func lookupBranch(ctx context.Context, dir, op, short string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--hash", headsPrefix+short)
	cmd.Dir = dir
	// "not a valid ref" is a translatable message: force the C locale so
	// a host with a translated git still reports a missing branch as
	// missing (a localized message would otherwise fail closed and break
	// every read of a tag, abbreviated id or remote-tracking name). The
	// later duplicates override the inherited values.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		oid := strings.TrimSpace(stdout.String())
		if !IsFullObjectName(oid) {
			return "", false, &GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("branch lookup for %q returned no object id", short)}
		}
		return oid, true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && strings.Contains(stderr.String(), "not a valid ref") {
		return "", false, nil
	}
	gitErr := classifyGitError(op, strings.TrimSpace(stderr.String()))
	gitErr.Err = err
	return "", false, gitErr
}

// branchNotFound is the refusal for a branch argument that names no
// existing branch. Not retryable: retrying cannot make the branch exist,
// and falling back to DWIM is exactly the shadowing this grammar closes.
func branchNotFound(op, what, value string) *GitError {
	return &GitError{Kind: ErrKindNotFound, Op: op,
		Message: fmt.Sprintf("%s: branch %q does not exist", what, value)}
}

// resolveBranchOID resolves a branch argument (already through phase 1)
// to the object id of refs/heads/<short>, or a NotFound *GitError.
func (c *CLIClient) resolveBranchOID(ctx context.Context, op, what, value, short string) (string, error) {
	oid, ok, err := lookupBranch(ctx, c.repoPath, op, short)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", branchNotFound(op, what, value)
	}
	return oid, nil
}

// resolveCommit is phase 2 for a Commit-class argument: an object id and
// "HEAD" pass through; a branch becomes its exact object id. The result
// is safe as a revision for every command except `git branch`, whose
// caller (CreateBranch) appends ^{commit}.
func (c *CLIClient) resolveCommit(ctx context.Context, op, what, v string) (string, error) {
	if IsFullObjectName(v) || v == "HEAD" {
		return v, nil
	}
	short, _, err := QualifyBranch(v)
	if err != nil { // unreachable after checkCommit; fail closed anyway
		return "", invalidRef(op, what, v, err)
	}
	return c.resolveBranchOID(ctx, op, what, v, short)
}

// resolveRev is phase 2 for a Rev-class argument. Branch-first:
//
//  1. a full object id, or exactly "HEAD", passes through. (Only HEAD:
//     a "^[A-Z_]*HEAD$" pass-through would let a branch such as
//     HOTFIXHEAD skip resolution and be shadowed by a same-named tag.)
//  2. "refs/heads/<n>" for a valid branch name is looked up exactly: the
//     object id when it exists, ErrKindNotFound when it does not — never
//     a DWIM fallback to refs/tags/refs/heads/<n>.
//  3. anything else under refs/, or a bare name that is not a valid
//     branch name (main@{1}), passes through unchanged.
//  4. a bare branch name is looked up exactly: the object id when the
//     branch exists; otherwise the name passes through, so tags,
//     abbreviated ids and "origin/main" resolve exactly as before.
//
// A lookup that fails for any other reason returns the classified error.
func (c *CLIClient) resolveRev(ctx context.Context, op, what, rev string) (string, error) {
	if IsFullObjectName(rev) || rev == "HEAD" {
		return rev, nil
	}
	if strings.HasPrefix(rev, headsPrefix) {
		short, _, err := QualifyBranch(rev)
		if err != nil {
			return rev, nil // e.g. refs/heads/x@{1}: a rev expression, rule 3
		}
		return c.resolveBranchOID(ctx, op, what, rev, short)
	}
	if strings.HasPrefix(rev, "refs/") {
		return rev, nil
	}
	short, _, err := QualifyBranch(rev)
	if err != nil {
		return rev, nil
	}
	oid, ok, err := lookupBranch(ctx, c.repoPath, op, short)
	if err != nil {
		return "", err
	}
	if ok {
		return oid, nil
	}
	return rev, nil
}

// --- Git version floor. ---

// MinGitVersion is the oldest git the client's argv supports:
// `rev-parse --end-of-options` arrived in git 2.30.0 (parse-options and
// setup_revisions support in 2.24, `git switch` in 2.23). Debian 11 and
// Ubuntu 22.04 ship newer; every shipped image runs 2.39.5 or later.
const MinGitVersion = "2.30"

const minGitMajor, minGitMinor = 2, 30

// gitVersionPattern parses `git version X.Y[.Z][...]`: "2.39.5",
// "2.54.0 (Apple Git-157)" and "2.45.1.windows.1" all match.
var gitVersionPattern = regexp.MustCompile(`^git version (\d+)\.(\d+)`)

// CheckGitVersion runs `git version` and returns an error when git is
// missing, unparseable or older than MinGitVersion. Hosts call it once at
// startup and refuse to start on error: below the floor the client's
// --end-of-options argv fails closed on the first read with a confusing
// message, and there is deliberately no fallback argv without the
// terminator.
func CheckGitVersion(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "git", "version").Output()
	if err != nil {
		return fmt.Errorf("git version check: cannot run git: %w", err)
	}
	return checkGitVersionOutput(strings.TrimSpace(string(out)))
}

// checkGitVersionOutput is CheckGitVersion's parser, split out so the
// accepted formats are testable without a git binary.
func checkGitVersionOutput(line string) error {
	m := gitVersionPattern.FindStringSubmatch(line)
	if m == nil {
		return fmt.Errorf("git version check: unrecognised output %q", line)
	}
	major, errMajor := strconv.Atoi(m[1])
	minor, errMinor := strconv.Atoi(m[2])
	if errMajor != nil || errMinor != nil {
		return fmt.Errorf("git version check: unrecognised output %q", line)
	}
	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return fmt.Errorf("git version check: %q is older than the minimum supported git %s", line, MinGitVersion)
	}
	return nil
}
