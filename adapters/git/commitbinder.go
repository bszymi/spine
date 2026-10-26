package git

// SHA-bound governed merges (INIT-023 TASK-005; audit F2 merge half,
// P3-37). These CLIClient methods implement engine.CommitBinder, the
// optional capability the engine needs to merge exactly the commit that
// passed validation and review instead of whatever a run branch points at
// when the merge runs.
//
// Every method takes object ids, not names, wherever it can, and checks
// them before any git process starts:
//
//   - an object id is a full LOWERCASE hex id (40 or 64 digits). The ref
//     grammar's IsFullObjectName also accepts uppercase, because git reads
//     either as the object; a binding is compared byte-for-byte, so only
//     the spelling git itself prints is accepted here;
//   - an id is accepted as a commit only when `rev-parse --verify
//     <id>^{commit}` prints the id back. git reads a hex string as an
//     object name only when its length matches the repository's object
//     format: in a SHA-1 repository a 64-digit value is resolved as a ref
//     NAME (refs/tags/<64 hex> and friends), and its peeled output then
//     differs from the input. The same comparison refuses tree, blob and
//     annotated-tag ids (TASK-005 DC-4);
//   - a branch is looked up exactly (lookupBranch, TASK-002), so a pushed
//     refs/tags/<branch> never stands in for it;
//   - the push source is "<id>^{commit}" with an explicit
//     refs/heads/<n> destination and NO force of any kind (DC-1): git
//     accepts it only as a fast-forward of the remote branch.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

// canonicalObjectName is a full lowercase hex object id: what git prints.
var canonicalObjectName = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// IsCanonicalObjectName reports whether s is a full lowercase hex object
// id (40 digits for SHA-1, 64 for SHA-256) — the only spelling SHA-bound
// merges accept (INIT-023 TASK-005).
func IsCanonicalObjectName(s string) bool { return canonicalObjectName.MatchString(s) }

// errNotCommit is wrapped by every "this id does not name a commit"
// refusal (missing object, tree, blob, annotated tag, a 64-digit name in
// a SHA-1 repository).
var errNotCommit = errors.New("does not name a commit object")

// checkObjectName is phase 1 for an object-id argument.
func checkObjectName(op, what, sha string) error {
	if !IsCanonicalObjectName(sha) {
		return invalidRef(op, what, sha, errors.New("not a full lowercase hex object id"))
	}
	return nil
}

// runOpts carries what run() does not support: extra environment
// variables and stdin.
type runOpts struct {
	env   []string
	stdin io.Reader
}

// runWith executes git in the repository with extra env and stdin, and
// returns stdout, stderr and the classified error (its Err is the
// *exec.ExitError, so callers can read the exit status). It never applies
// push credentials: none of its callers talk to a remote.
//
// It keeps TASK-003's read bounds for every command it runs (INIT-023
// wave 3 integration; before it, these commands were buffered whole and
// ran in the host's locale):
//
//   - the C locale (LC_ALL=C, LANGUAGE=), as for read commands and the
//     exact branch lookup: classifyGitError's patterns are English, and the
//     engine decides on the kind — VerifyCommit's transient failure is
//     retried, any other refuses the binding — so a translated
//     "Unable to create '….lock'" would turn a lock race into a failed run.
//     None of the parsed stdout depends on the locale (object ids, raw
//     dates, -z listings, a ref name), and commit-tree writes the message
//     bytes as given, so CommitFilesOnto's commit ids are unchanged;
//   - stdout fails past the per-command limit (c.outputLimit(), never
//     below 4 KiB) instead of growing without bound. Every command here
//     prints at most a few object ids or one ls-tree record per argument,
//     so the limit is never reached by a working git;
//   - stderr keeps its head and tail (stderrSink, 64 KiB).
//
// Not applied: GIT_ALLOC_LIMIT and the pinned delta-base cache. ResetBranch's
// `read-tree -m -u` materialises the working tree like checkout (opMutate),
// and the read-only commands here parse the commit being merged, which the
// merge itself then reads without an allocation limit (KNOWN-LIMITATIONS,
// "Git read bounds").
//
// Every command it runs is local, and several materialise the working
// tree (`read-tree -m -u`, `read-tree --reset -u`, `reset --hard`,
// `switch`, `merge --abort`), so all of them run under the client's
// mutate deadline (INIT-024 TASK-003, WithMutateTimeout); none when the
// client has none.
func (c *CLIClient) runWith(ctx context.Context, op string, o runOpts, args ...string) (string, string, error) {
	parent := ctx
	deadline := c.CheckoutLimits().MutateTimeout
	ctx, cancelDeadline := withMutateDeadline(ctx, deadline)
	defer cancelDeadline()
	cmd := newGitCmd(ctx, args...)
	cmd.Dir = c.repoPath
	env := append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
	env = append(env, o.env...)
	cmd.Env = env
	if o.stdin != nil {
		cmd.Stdin = o.stdin
	}
	limit := c.outputLimit()
	stdout := newStdoutSink(limit, 0, true, true)
	stderr := &stderrSink{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	errText := stderr.String()
	if err != nil && mutateDeadlineHit(parent, ctx, deadline) {
		return stdout.String(), errText, mutateTimeout(op, deadline, err)
	}
	if stdout.overflow {
		return "", errText, &GitError{Kind: ErrKindPermanent, Op: op, Message: "output exceeds read limit",
			Err: &OutputTooLargeError{Op: op, Limit: limit}}
	}
	if err != nil {
		gitErr := classifyGitError(op, strings.TrimSpace(errText))
		gitErr.Err = err
		return stdout.String(), errText, gitErr
	}
	return stdout.String(), errText, nil
}

// verifyCommitOID returns nil only when sha is a canonical object id that
// names a commit object present in the repository (see the file comment
// for why the output must equal the input).
func (c *CLIClient) verifyCommitOID(ctx context.Context, op, what, sha string) error {
	if err := checkObjectName(op, what, sha); err != nil {
		return err
	}
	out, _, err := c.runWith(ctx, op, runOpts{},
		"rev-parse", "--verify", "--quiet", "--end-of-options", sha+"^{commit}")
	if err != nil {
		// `rev-parse --verify` exits 1 for a name that does not resolve to
		// a commit (a missing object, or a tree, blob or tag that does not
		// peel to one; it may still print why). That is decided by the
		// exit status, never by stderr text. Any other failure (128: not a
		// repository, a corrupt object store) keeps its classification.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return &GitError{Kind: ErrKindNotFound, Op: op,
				Message: fmt.Sprintf("%s %s %v", what, sha, errNotCommit), Err: errNotCommit}
		}
		return err
	}
	if strings.TrimSpace(out) != sha {
		return &GitError{Kind: ErrKindNotFound, Op: op,
			Message: fmt.Sprintf("%s %s %v", what, sha, errNotCommit), Err: errNotCommit}
	}
	return nil
}

// ResolveBranchHead returns the commit refs/heads/<branch> points at in
// this repository. The branch is Branch class (validated before git runs,
// so it can never start with "-"), and it is looked up exactly: a
// refs/tags/<branch> or any other shadow never answers for it, and a
// missing branch is ErrKindNotFound rather than a DWIM guess.
func (c *CLIClient) ResolveBranchHead(ctx context.Context, branch string) (string, error) {
	const op = "resolve_branch_head"
	short, _, err := checkBranch(op, "branch", branch)
	if err != nil {
		return "", err
	}
	oid, err := c.resolveBranchOID(ctx, op, "branch", branch, short)
	if err != nil {
		return "", err
	}
	if err := c.verifyCommitOID(ctx, op, "branch head", oid); err != nil {
		return "", err
	}
	return oid, nil
}

// VerifyCommit returns nil only for a full lowercase hex object id that
// names a commit object in this repository.
func (c *CLIClient) VerifyCommit(ctx context.Context, sha string) error {
	return c.verifyCommitOID(ctx, "verify_commit", "commit", sha)
}

// IsAncestor reports whether ancestor is reachable from descendant (a
// commit is its own ancestor). Both must name commits (VerifyCommit's
// check), so a missing object is an error, never "false".
func (c *CLIClient) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	const op = "is_ancestor"
	if err := checkObjectName(op, "ancestor", ancestor); err != nil {
		return false, err
	}
	if err := checkObjectName(op, "descendant", descendant); err != nil {
		return false, err
	}
	if err := c.verifyCommitOID(ctx, op, "ancestor", ancestor); err != nil {
		return false, err
	}
	if err := c.verifyCommitOID(ctx, op, "descendant", descendant); err != nil {
		return false, err
	}
	_, _, err := c.runWith(ctx, op, runOpts{},
		"merge-base", "--is-ancestor", "--end-of-options", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// CommitParents returns the full ids of sha's parents, in order: two for a
// merge commit, one for a normal commit, none (an empty, non-nil slice)
// for a root commit.
func (c *CLIClient) CommitParents(ctx context.Context, sha string) ([]string, error) {
	const op = "commit_parents"
	if err := checkObjectName(op, "commit", sha); err != nil {
		return nil, err
	}
	out, _, err := c.runWith(ctx, op, runOpts{},
		"rev-list", "--parents", "-n", "1", "--end-of-options", sha)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(out)
	// rev-list prints the commit itself first. A different first id means
	// git resolved the argument as something else (a 64-digit ref name in
	// a SHA-1 repository, an annotated tag): not the commit asked for.
	if len(fields) == 0 || fields[0] != sha {
		return nil, &GitError{Kind: ErrKindNotFound, Op: op,
			Message: fmt.Sprintf("commit %s %v", sha, errNotCommit), Err: errNotCommit}
	}
	parents := make([]string, 0, len(fields)-1)
	for _, p := range fields[1:] {
		if !IsCanonicalObjectName(p) {
			return nil, &GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("unexpected rev-list output for %s", sha)}
		}
		parents = append(parents, p)
	}
	return parents, nil
}

// CommitFilesOnto creates a commit whose only parent is base and whose
// tree is base's tree with files replaced (path → full new content). It
// uses plumbing only — a private temporary index, hash-object, write-tree
// and commit-tree — so it moves no ref and touches neither HEAD, the real
// index nor the working tree: nothing on disk outside the object store
// changes, which also takes the working-tree symlink (F5) and race (F9)
// surfaces out of the status cascade.
//
// Paths must be clean repository-relative file paths: no leading "/", no
// "." or ".." component, no empty component, no component that is ".git"
// in any letter case, and no NUL or control byte. An existing entry keeps
// its mode (100644 or 100755); a new one is 100644. A path that is a
// symlink, a gitlink or a directory in base is refused. Every path is
// checked before git starts.
//
// The result is deterministic: the author and committer dates are pinned
// to base's committer date, the identity is the one CLIClient.Commit uses
// (repository/user config, or opts.Author), signing is disabled and the
// message is built with sorted trailers, so the same base, files and
// options always give the same commit (TASK-005 DC-2). A governed-merge
// retry therefore recomputes the cascade to an object main already holds,
// and its merge is "already up to date".
//
// With no files, AllowEmpty must be set; a commit whose tree equals
// base's tree is likewise refused unless AllowEmpty is set.
func (c *CLIClient) CommitFilesOnto(ctx context.Context, base string, files map[string]string, opts CommitOpts) (CommitResult, error) {
	const op = "commit_files_onto"
	if err := checkObjectName(op, "base", base); err != nil {
		return CommitResult{}, err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		if err := checkTreePath(p); err != nil {
			return CommitResult{}, &GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("refused path %q: %v", p, err), Err: ErrInvalidRef}
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) == 0 && !opts.AllowEmpty {
		return CommitResult{}, &GitError{Kind: ErrKindPermanent, Op: op, Message: "no files to commit"}
	}
	if err := c.verifyCommitOID(ctx, op, "base", base); err != nil {
		return CommitResult{}, err
	}

	tmp, err := os.MkdirTemp("", "spine-commit-onto-*")
	if err != nil {
		return CommitResult{}, fmt.Errorf("commit onto: temp index: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	indexEnv := []string{"GIT_INDEX_FILE=" + tmp + "/index"}

	if _, _, err := c.runWith(ctx, op, runOpts{env: indexEnv}, "read-tree", "--end-of-options", base); err != nil {
		return CommitResult{}, err
	}

	var indexInfo bytes.Buffer
	for _, p := range paths {
		mode, err := c.ontoEntryMode(ctx, op, base, p)
		if err != nil {
			return CommitResult{}, err
		}
		out, _, err := c.runWith(ctx, op, runOpts{stdin: strings.NewReader(files[p])},
			"hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return CommitResult{}, err
		}
		blob := strings.TrimSpace(out)
		if !IsCanonicalObjectName(blob) {
			return CommitResult{}, &GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("hash-object returned no object id for %q", p)}
		}
		// --index-info -z: "<mode> SP <sha> TAB <path> NUL", so a path is
		// never split or unquoted.
		fmt.Fprintf(&indexInfo, "%s %s\t%s\x00", mode, blob, p)
	}
	if indexInfo.Len() > 0 {
		if _, _, err := c.runWith(ctx, op, runOpts{env: indexEnv, stdin: &indexInfo},
			"update-index", "-z", "--index-info"); err != nil {
			return CommitResult{}, err
		}
	}

	treeOut, _, err := c.runWith(ctx, op, runOpts{env: indexEnv}, "write-tree")
	if err != nil {
		return CommitResult{}, err
	}
	tree := strings.TrimSpace(treeOut)
	if !IsCanonicalObjectName(tree) {
		return CommitResult{}, &GitError{Kind: ErrKindPermanent, Op: op, Message: "write-tree returned no object id"}
	}
	if !opts.AllowEmpty {
		baseTree, _, err := c.runWith(ctx, op, runOpts{},
			"rev-parse", "--verify", "--end-of-options", base+"^{tree}")
		if err != nil {
			return CommitResult{}, err
		}
		if strings.TrimSpace(baseTree) == tree {
			return CommitResult{}, &GitError{Kind: ErrKindPermanent, Op: op, Message: "nothing to commit: files match base"}
		}
	}

	dateOut, _, err := c.runWith(ctx, op, runOpts{},
		"log", "-1", "--date=raw", "--format=%cd", "--end-of-options", base, "--")
	if err != nil {
		return CommitResult{}, err
	}
	date := strings.TrimSpace(dateOut)
	if date == "" {
		return CommitResult{}, &GitError{Kind: ErrKindPermanent, Op: op, Message: "base commit has no committer date"}
	}
	env := []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	if opts.Author.Name != "" && opts.Author.Email != "" {
		env = append(env, "GIT_AUTHOR_NAME="+opts.Author.Name, "GIT_AUTHOR_EMAIL="+opts.Author.Email)
	}
	msg := buildCommitMessage(opts) + "\n"
	out, _, err := c.runWith(ctx, op, runOpts{env: env, stdin: strings.NewReader(msg)},
		"commit-tree", "--no-gpg-sign", "-p", base, "-F", "-", tree)
	if err != nil {
		return CommitResult{}, err
	}
	sha := strings.TrimSpace(out)
	if !IsCanonicalObjectName(sha) {
		return CommitResult{}, &GitError{Kind: ErrKindPermanent, Op: op, Message: "commit-tree returned no object id"}
	}
	return CommitResult{SHA: sha}, nil
}

// ontoEntryMode returns the mode CommitFilesOnto writes for p: the
// existing regular-file mode, or 100644 for a new path. A symlink,
// gitlink or directory entry is refused: the cascade rewrites artifact
// files, and replacing any of those with a blob would change what the
// path is, not just its content. Every parent component must be a
// directory (or absent): `update-index --index-info` would otherwise
// silently replace a symlink or file component with a directory.
func (c *CLIClient) ontoEntryMode(ctx context.Context, op, base, p string) (string, error) {
	comps := strings.Split(p, "/")
	args := []string{"--literal-pathspecs", "ls-tree", "-z", "--full-tree", "--end-of-options", base}
	parents := make(map[string]bool, len(comps)-1)
	for i := 1; i < len(comps); i++ {
		parent := strings.Join(comps[:i], "/")
		parents[parent] = true
		args = append(args, parent)
	}
	args = append(args, p)
	out, _, err := c.runWith(ctx, op, runOpts{}, args...)
	if err != nil {
		return "", err
	}
	mode := "100644"
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, got, ok := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return "", &GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("unexpected ls-tree record for %q", p)}
		}
		switch {
		case got == p:
			switch fields[0] {
			case "100644", "100755":
				mode = fields[0]
			default:
				return "", &GitError{Kind: ErrKindPermanent, Op: op,
					Message: fmt.Sprintf("refused path %q: entry mode %s is not a regular file", p, fields[0]), Err: ErrInvalidRef}
			}
		case parents[got]:
			if fields[0] != "040000" {
				return "", &GitError{Kind: ErrKindPermanent, Op: op,
					Message: fmt.Sprintf("refused path %q: parent %q is not a directory (mode %s)", p, got, fields[0]), Err: ErrInvalidRef}
			}
		}
	}
	return mode, nil
}

// checkTreePath is CommitFilesOnto's path rule (see its doc comment).
func checkTreePath(p string) error {
	if p == "" {
		return errors.New("empty path")
	}
	for i := 0; i < len(p); i++ {
		if b := p[i]; b < 0x20 || b == 0x7f {
			return errors.New("path contains a NUL or control byte")
		}
	}
	if strings.HasPrefix(p, "/") {
		return errors.New("path is absolute")
	}
	if path.Clean(p) != p {
		return errors.New("path is not clean")
	}
	for _, comp := range strings.Split(p, "/") {
		switch {
		case comp == "" || comp == "." || comp == "..":
			return errors.New("path has an empty, . or .. component")
		case strings.EqualFold(comp, ".git"):
			return errors.New("path has a .git component")
		}
	}
	return nil
}

// MergeBound merges exactly opts.SourceSHA (required, a commit) into
// opts.Target (required, Branch class) and reports MergeResult.BaseSHA,
// the target tip the merge was computed on. "Already up to date" — the
// replay of a merge that already landed — returns SHA == BaseSHA.
func (c *CLIClient) MergeBound(ctx context.Context, opts MergeOpts) (MergeResult, error) {
	if opts.SourceSHA == "" {
		return MergeResult{}, invalidRef("merge", "source sha", "", errors.New("MergeBound requires SourceSHA"))
	}
	if opts.Target == "" {
		return MergeResult{}, invalidRef("merge", "target", "", errors.New("MergeBound requires Target"))
	}
	return c.mergeSHA(ctx, opts)
}

// mergeSHA is the SourceSHA path shared by Merge and MergeBound: every
// argument is checked, the commit is verified, the target (when given)
// is switched to and its tip recorded as BaseSHA, then exactly SourceSHA
// is merged. Source is never resolved. Everything after the argument
// checks holds the working tree (INIT-023 TASK-012), so no other writer
// can move the target between the base read and the merge in-process.
func (c *CLIClient) mergeSHA(ctx context.Context, opts MergeOpts) (MergeResult, error) {
	if err := checkObjectName("merge", "source sha", opts.SourceSHA); err != nil {
		return MergeResult{}, err
	}
	var target string
	if opts.Target != "" {
		short, _, err := checkBranch("merge", "target", opts.Target)
		if err != nil {
			return MergeResult{}, err
		}
		target = short
	}
	h, err := c.lockTree(ctx)
	if err != nil {
		return MergeResult{}, err
	}
	defer h.release()
	ctx = h.ctx
	if err := c.verifyCommitOID(ctx, "merge", "source sha", opts.SourceSHA); err != nil {
		return MergeResult{}, err
	}
	// The checkout budget (INIT-024 TASK-003): the merge's result tree on
	// the target tip (and the target's tree when the switch is a
	// checkout), before the switch — over it, nothing is switched or
	// merged and the target does not move. No budget: no process.
	ours, err := c.checkMergeBudget(ctx, opts.Target, target, opts.SourceSHA)
	if err != nil {
		return MergeResult{}, err
	}
	var base string
	if target != "" {
		if err := c.switchTo(ctx, target); err != nil {
			return MergeResult{}, err
		}
		oid, err := c.resolveBranchOID(ctx, "merge", "target", opts.Target, target)
		if err != nil {
			return MergeResult{}, err
		}
		base = oid
	} else {
		head, err := c.run(ctx, "merge", "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return MergeResult{}, err
		}
		base = strings.TrimSpace(head)
	}
	// The merge must run on the tip whose result was measured.
	if ours != "" && base != ours {
		return MergeResult{}, errMergeTargetMoved()
	}
	res, err := c.runMerge(ctx, opts, opts.SourceSHA)
	if err != nil {
		return MergeResult{}, err
	}
	res.BaseSHA = base
	return res, nil
}

// ResetBranch takes back a merge the engine refused (INIT-023 TASK-005
// review round 1): it moves refs/heads/<branch> from expected to `to`
// only if the branch is still at expected — `update-ref <ref> <to>
// <expected>`, git's own compare-and-swap — so a branch another writer
// moved meanwhile is left alone and reported. Without it, MergeBound has
// already moved the local branch to the refused merge, and "nothing is
// pushed" holds only until the next writer builds on it: the next
// governed merge's base is that merge, its own check passes, and its push
// publishes the refused commit; with auto-push off the local branch is
// the published one.
//
// When the branch is checked out here, the index and the working tree
// follow with `read-tree -m -u <expected> <to>`, a two-tree switch that
// refuses to overwrite a local change rather than discarding it. If it
// refuses, the ref is moved forward to expected again, so the branch, the
// index and the tree never disagree: an index left at the refused merge's
// tree on a branch reset to `to` would make the next ordinary commit on
// the branch record that tree — the refused content — as a plain commit.
// No remote is touched.
//
// A failure is remembered for the tree (INIT-023 TASK-012 review round
// 1, pendingTakeBack): every later take of the tree — WithWorkingTree,
// an in-place write scope, the restore of a home-branch tree, the merge
// block's own exit restore first — retries the swap before anything else
// runs, and refuses the tree while the branch still contains the refused
// merge.
//
// Review round 2 of INIT-023 TASK-012:
//
//   - the record is armed once the arguments pass the grammar, before the
//     first call that can fail — the lock wait, the commit checks — not
//     after them: a take-back that failed at a commit check used to be
//     forgotten, and the merge block then released a tree that looked
//     healthy with the refused merge on its branch. The one failure that
//     arms nothing is proof that expected names no commit: a branch
//     cannot contain it;
//   - the git work runs on its own context, detached from the caller's
//     cancellation and bounded by treeRestoreTimeout, like the restore: a
//     take-back is a compensating action, and the governed merge calls it
//     at the end of a sequence whose own deadline may already be spent;
//   - on a tree with a home branch (standalone's shared tree, whose policy
//     is quarantine and continue) a refused two-tree switch no longer
//     fails the take-back: content no checkout makes clean — a CRLF blob
//     under a `text eol=lf` attribute, two paths that differ only in case
//     — refuses `read-tree -m` for good, so a take-back of a merge that
//     carries it could never succeed and every later take of the tree
//     failed on it. The tracked state is quarantined instead (`git stash
//     create`, as the restore does) and the index and tree are reset to
//     `to`, after the restore's untracked-obstruction check. A tree with
//     no home branch keeps the refusal: a local change there is never
//     discarded (TASK-005).
func (c *CLIClient) ResetBranch(ctx context.Context, branch, to, expected string) (retErr error) {
	const op = "reset_branch"
	short, full, err := checkBranch(op, "branch", branch)
	if err != nil {
		return err
	}
	if err := checkObjectName(op, "target", to); err != nil {
		return err
	}
	if err := checkObjectName(op, "expected", expected); err != nil {
		return err
	}
	// From here a failure may leave the branch at (or containing) the
	// merge the caller refused. The take-back is remembered for the tree,
	// and every later take of it settles it first — retries the swap, or
	// refuses while the branch still holds the merge — so the tree is
	// never handed on as healthy with the refused merge on it
	// (pendingTakeBack). A success clears the record of this take-back.
	remember := true
	defer func() {
		switch {
		case retErr == nil:
			clearPendingTakeBack(c.repoPath, short, expected)
		case remember:
			notePendingTakeBack(c.repoPath, short, to, expected)
		}
	}()
	// The ref move and the index/tree follow-up hold the working tree
	// (INIT-023 TASK-012); inside the governed merge's sequence this
	// re-enters. Only the wait honours the caller's ctx.
	h, err := c.lockTree(ctx)
	if err != nil {
		return err
	}
	defer h.release()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), treeRestoreTimeout)
	defer cancel()
	if err := c.verifyCommitOID(ctx, op, "target", to); err != nil {
		return err
	}
	if err := c.verifyCommitOID(ctx, op, "expected", expected); err != nil {
		remember = !errors.Is(err, errNotCommit)
		return err
	}
	attached, err := c.headIsBranch(ctx, op, full)
	if err != nil {
		return err
	}
	// The checkout budget (INIT-024 TASK-003): when the tree will follow
	// the ref, `to`'s tree is measured before the ref moves, so a refusal
	// leaves the branch, index and tree as they are (and the take-back
	// pending). No budget: no process.
	if attached && to != expected {
		if err := c.checkTrees(ctx, op, to); err != nil {
			return err
		}
	}
	if _, _, err := c.runWith(ctx, op, runOpts{},
		"update-ref", "-m", "spine: take back a refused bound merge", "--end-of-options", full, to, expected); err != nil {
		return err
	}
	if !attached || to == expected {
		return nil
	}
	_, _, err = c.runWith(ctx, op, runOpts{}, "read-tree", "-m", "-u", "--end-of-options", expected, to)
	if err != nil && c.treeHome() != "" {
		err = c.resetTreeOverRefusedSwitch(ctx, op, short, to, err)
	}
	if err != nil {
		// Its own short bound: the switch may have spent ctx's.
		fwdCtx, fwdCancel := context.WithTimeout(context.WithoutCancel(ctx), treeRestoreTimeout)
		defer fwdCancel()
		if _, _, fwdErr := c.runWith(fwdCtx, op, runOpts{},
			"update-ref", "-m", "spine: keep the merge (working tree not restorable)", "--end-of-options", full, expected, to); fwdErr != nil {
			return &GitError{Kind: ErrKindPermanent, Op: op,
				Message: fmt.Sprintf("%s was reset to %s but its working tree could not follow (%v), and moving it back to %s failed (%v): the branch, index and working tree disagree",
					full, to, err, expected, fwdErr)}
		}
		return err
	}
	return nil
}

// headIsBranch reports whether HEAD is attached to full (refs/heads/<n>).
// A detached HEAD is "not attached", not an error.
func (c *CLIClient) headIsBranch(ctx context.Context, op, full string) (bool, error) {
	out, _, err := c.runWith(ctx, op, runOpts{}, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) == full, nil
}

// PushCommit sets <remote> refs/heads/<branch> to exactly sha with a
// NON-forced push: `push --end-of-options <remote>
// <sha>^{commit}:refs/heads/<n>`. git accepts it only when the remote
// branch is an ancestor of sha (a fast-forward, which includes catching
// up after an earlier push failed) or already equals sha; any other
// remote state is refused with "! [rejected] … (fetch first)" or
// "(non-fast-forward)", which classifies as a permanent push rejection.
// There is no --force, no "+" refspec and no --force-with-lease (DC-1):
// a lease turned the normal retry after a transient push failure into a
// permanent refusal, and a matching lease would let a non-descendant
// rewrite the branch.
//
// The source is the object, not a local ref, so it does not matter where
// the local branch points; "<sha>^{commit}" can never match a ref name.
func (c *CLIClient) PushCommit(ctx context.Context, remote, branch, sha string) error {
	if err := checkRemote("push", remote); err != nil {
		return err
	}
	_, full, err := checkBranch("push", "branch", branch)
	if err != nil {
		return err
	}
	if err := checkObjectName("push", "commit", sha); err != nil {
		return err
	}
	if err := c.verifyCommitOID(ctx, "push", "commit", sha); err != nil {
		return err
	}
	_, err = c.run(ctx, "push", "push", "--end-of-options", remote, sha+"^{commit}:"+full)
	return err
}
