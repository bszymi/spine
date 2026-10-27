package git

// Neutral checkout attributes (INIT-024 TASK-003, review round 2).
//
// The checkout budget sums the blob bytes git stores. What a checkout
// writes can be more: attributes rewrite content on the way out — `ident`,
// `text` / `eol=crlf` (and the host's core.autocrlf / core.eol, which act
// through `text`), `working-tree-encoding`, a `filter=` smudge driver —
// and a conflicted merge writes markers `conflict-marker-size` long. Those
// attributes come from the tree's own .gitattributes, its macros, the
// host's core.attributesFile and the system attributes file. Predicting
// git's attribute parser from outside proved brittle (an attribute scanner
// kept yielding inputs git and it read differently), so a budgeted
// repository turns them off instead:
//
//	* -text -ident -filter !eol !working-tree-encoding !conflict-marker-size
//
// as the last line of $GIT_COMMON_DIR/info/attributes. That file has the
// highest precedence of every attribute source, and within a file a later
// line wins, so the line overrides every other setting of these
// attributes, macros included, for every checkout, merge, restore and
// linked worktree of the repository (worktrees share the common dir). A
// checkout then writes exactly the bytes the listing sums, and a conflict
// marker is 7 characters. `merge` is deliberately left alone: `!merge`
// would change what `binary` and `merge=union` do to a merge.
//
// What it changes for the repository (CHANGELOG, "Changed (operators)"):
// checkouts write stored bytes (an LFS pointer stays a pointer, CRLF is
// not added), `git add` no longer normalises (CRLF content stays CRLF,
// `$Id: … $` is not collapsed), conflict markers are always 7 characters,
// and a tree checked out with conversions before the line would look
// modified, so EnsureNeutralWorkTree brings it to the stored bytes
// (retried until that succeeds).
//
// The writer is opt-in like the budget: `spine serve` calls
// EnsureNeutralWorkTree for each repository it serves, a budgeted Clone
// writes the line before its checkout, and a
// budgeted measure refuses (fail closed) on a repository whose attributes
// file does not end in the line (requireNeutralCheckoutAttributes).
// Operator lines in the file are kept.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bszymi/spine/core/observe"
)

// NeutralCheckoutAttributes is the attributes line a budgeted repository
// carries last in $GIT_COMMON_DIR/info/attributes. It is the one source
// of the line: an embedder that writes it with its own writer should use
// this constant (and must not manage the same file with a second writer).
const NeutralCheckoutAttributes = "* -text -ident -filter !eol !working-tree-encoding !conflict-marker-size"

// ErrNeutralAttributesMissing is what a budgeted measure on a repository
// without the neutral attributes line matches with errors.Is.
var ErrNeutralAttributesMissing = errors.New("git: the repository's info/attributes does not end in the neutral checkout attributes line")

// NeutralAttributesMissingMessage is the GitError.Message of that refusal.
const NeutralAttributesMissingMessage = "the checkout budget needs the neutral checkout attributes as the last line of info/attributes"

// ErrRenormalisePending is matched (errors.Is) by an EnsureNeutralWorkTree
// error returned after the line is in place: the work tree is still
// marked, not yet at its stored bytes, and the next call retries. The
// wrapped cause (a budget refusal, an obstruction, a cancelled request)
// is matched too. A caller that only needs the line — the git HTTP
// endpoint's push measure — can go on; one that works in the tree cannot.
var ErrRenormalisePending = errors.New("git: the neutral checkout attributes are in place, but the work tree is not yet brought to its stored bytes (the next call retries)")

// maxInfoAttributesBytes bounds the read of an existing info/attributes.
const maxInfoAttributesBytes = 1 << 20

// neutralWriteMu serialises this process's writers of info/attributes.
var neutralWriteMu sync.Mutex

// infoAttributesPath returns $GIT_COMMON_DIR/info/attributes of repoDir.
func infoAttributesPath(ctx context.Context, repoDir string) (string, error) {
	cmd := newGitCmd(ctx, "rev-parse", "--git-common-dir")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
	out, err := cmd.Output()
	if err != nil {
		return "", &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot locate the git directory", Err: err}
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoDir, common)
	}
	return filepath.Join(common, "info", "attributes"), nil
}

// readInfoAttributes reads path ("" content when it does not exist). It
// refuses anything but a regular file: a link is never written through.
func readInfoAttributes(path string) ([]byte, bool, error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	case !fi.Mode().IsRegular():
		return nil, true, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path) //nolint:gosec // G304: the repository's own git-administrative file
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxInfoAttributesBytes+1))
	if err != nil {
		return nil, true, err
	}
	if len(b) > maxInfoAttributesBytes {
		return nil, true, fmt.Errorf("%s is larger than %d bytes", path, maxInfoAttributesBytes)
	}
	return b, true, nil
}

// gitBlanks are the bytes git's attribute parser skips around a line's
// fields (attr.c): space, tab and CR (and the LF that ends the line). Any
// other byte — a vertical tab, a form feed, a no-break space — is part of
// a pattern or an attribute name to git, so it must be to us too (review
// round 3): a line led by one is a pattern line, not a blank or a
// comment, and the neutral line with one around it is not the neutral
// line.
const gitBlanks = " \t\r"

// isNeutralLine reports whether line is exactly the neutral line, git's
// blanks around it aside.
func isNeutralLine(line []byte) bool {
	return string(bytes.Trim(line, gitBlanks+"\n")) == NeutralCheckoutAttributes
}

// endsNeutral reports whether the last attribute line of content — the
// last line that, past git's blanks, is neither empty nor a comment (a
// '#' right after the blanks) — is the neutral line.
func endsNeutral(content []byte) bool {
	lines := bytes.Split(content, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		l := bytes.TrimLeft(lines[i], gitBlanks)
		if len(l) == 0 || l[0] == '#' {
			continue
		}
		return isNeutralLine(l)
	}
	return false
}

// EnsureNeutralCheckoutAttributes makes NeutralCheckoutAttributes the last
// line of repoDir's $GIT_COMMON_DIR/info/attributes. Every other line is
// kept in order; an earlier copy of the line is moved to the end. It is
// idempotent — a file that already ends in the line is not rewritten — and
// writes atomically (a temporary file renamed into place). A non-regular
// file at the path is refused, never followed.
//
// repoDir must be the top of a repository — its work tree's root, or a
// bare repository's directory — never a directory that merely lies
// inside one (review round 3): the file written is that repository's.
func EnsureNeutralCheckoutAttributes(ctx context.Context, repoDir string) error {
	return ensureNeutral(ctx, repoDir, "")
}

// ensureNeutral is EnsureNeutralCheckoutAttributes. With a pendingMarker
// path, a write that ADDS the line (the file did not end in it) first
// creates that marker, so a work tree checked out under the old
// attributes stays marked for renormalising until EnsureNeutralWorkTree
// has done it (codex review round 3): the marker is on disk before the
// line is, and a failed or interrupted renormalisation is retried by the
// next call.
func ensureNeutral(ctx context.Context, repoDir, pendingMarker string) error {
	if err := requireRepositoryTop(ctx, repoDir); err != nil {
		return err
	}
	path, err := infoAttributesPath(ctx, repoDir)
	if err != nil {
		return err
	}
	neutralWriteMu.Lock()
	defer neutralWriteMu.Unlock()
	have, exists, err := readInfoAttributes(path)
	if err != nil {
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot read info/attributes", Err: err}
	}
	var want bytes.Buffer
	for _, line := range bytes.SplitAfter(have, []byte("\n")) {
		if len(line) == 0 || isNeutralLine(line) {
			continue
		}
		want.Write(line)
		if line[len(line)-1] != '\n' {
			want.WriteByte('\n')
		}
	}
	want.WriteString(NeutralCheckoutAttributes + "\n")
	if exists && bytes.Equal(have, want.Bytes()) {
		return nil
	}
	writeErr := func(msg string, err error) error {
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: msg, Err: err}
	}
	if pendingMarker != "" && !endsNeutral(have) {
		if err := markRenormalisePending(pendingMarker); err != nil {
			return writeErr("cannot mark the work tree for renormalising; info/attributes not written", err)
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return writeErr("cannot create info/", err)
	}
	tmp, err := os.CreateTemp(dir, ".attributes.spine-*")
	if err != nil {
		return writeErr("cannot write info/attributes", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op after the rename
	if _, err := tmp.Write(want.Bytes()); err != nil {
		_ = tmp.Close()
		return writeErr("cannot write info/attributes", err)
	}
	if err := tmp.Chmod(0o644); err != nil { //nolint:gosec // G302: git's own mode for info/attributes
		_ = tmp.Close()
		return writeErr("cannot write info/attributes", err)
	}
	if err := tmp.Close(); err != nil {
		return writeErr("cannot write info/attributes", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return writeErr("cannot write info/attributes", err)
	}
	return nil
}

// renormaliseMarker is the file, in a work tree's own git directory, that
// says the work tree still holds files checked out before the neutral line
// was added: created before the line is written, removed once the tree is
// at its stored bytes (or found clean).
const renormaliseMarker = "spine-renormalise-pending"

// markRenormalisePending creates the marker. One already there (a regular
// file) is kept; anything else at the path is refused, never followed
// (O_EXCL does not follow a link).
func markRenormalisePending(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a fixed name in git's own directory
	if errors.Is(err, fs.ErrExist) {
		if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode().IsRegular() {
			return nil
		}
		return errors.New("the renormalise marker is not a regular file")
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString("the work tree is brought to its stored bytes by the next neutral-attributes call\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// renormalisePending reports whether the marker is there.
func renormalisePending(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// clearRenormalisePending removes the marker (a link is removed, not
// followed).
func clearRenormalisePending(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// requireRepositoryTop refuses a repoDir that is not the top of its
// repository: a work tree's root (`rev-parse --show-toplevel`) or a bare
// repository's own directory (`rev-parse --absolute-git-dir`).
func requireRepositoryTop(ctx context.Context, repoDir string) error {
	run := func(args ...string) (string, error) {
		cmd := newGitCmd(ctx, args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	refuse := func(err error) error {
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes",
			Message: "not the top of a git repository; its info/attributes is not written", Err: err}
	}
	bare, err := run("rev-parse", "--is-bare-repository")
	if err != nil {
		return refuse(err)
	}
	top := ""
	if bare == "true" {
		top, err = run("rev-parse", "--absolute-git-dir")
	} else {
		top, err = run("rev-parse", "--show-toplevel")
	}
	if err != nil {
		return refuse(err)
	}
	same := func(a, b string) bool {
		ra, errA := filepath.EvalSymlinks(a)
		rb, errB := filepath.EvalSymlinks(b)
		return errA == nil && errB == nil && filepath.Clean(ra) == filepath.Clean(rb)
	}
	abs, err := filepath.Abs(repoDir)
	if err != nil || !same(abs, top) {
		return refuse(errors.New("the directory lies inside the repository"))
	}
	return nil
}

// EnsureNeutralWorkTree is EnsureNeutralCheckoutAttributes for an
// existing clone with a work tree (review round 3). A clone checked out
// before the line was written holds files as its old attributes or host
// config converted them (CRLF, expanded idents), which no longer match
// the blobs once conversion is off: status shows them modified and a
// merge or switch over them is refused. So when this call ADDS the line,
// it first marks the work tree (a file in its git directory), and a
// marked tree is brought to the stored bytes, holding the tree like any
// other stateful call: HEAD's tree is measured against the client's
// budget (when it has one), a tracked file standing in the way of an
// untracked one refuses, the tracked state is quarantined into an
// unreferenced commit (`git stash create`, logged at Warn, as the shared
// tree's restore does), and `reset --hard` writes HEAD's stored bytes. An
// unborn HEAD or a clean tree needs nothing more. The mark is cleared only
// then, so a renormalisation that fails or is interrupted (HEAD over the
// budget, an obstruction, a cancelled request, a crash) is retried by the
// next call — the error is returned meanwhile — rather than leaving the
// tree converted under a file that already ends in the line (codex review
// round 3). Such an error matches ErrRenormalisePending. A bare
// repository is never marked; a file that already ended in the line, with
// no mark, leaves the file and the tree alone.
//
// Only a marked tree is held (so the per-push call of the git HTTP
// endpoint never waits behind a sequence once the line is there and the
// tree is renormalised).
func (c *CLIClient) EnsureNeutralWorkTree(ctx context.Context) error {
	const op = "neutral_attributes"
	out, _, err := c.runWith(ctx, op, runOpts{}, "rev-parse", "--is-bare-repository", "--absolute-git-dir")
	if err != nil {
		return err
	}
	fields := strings.SplitN(strings.TrimSuffix(out, "\n"), "\n", 2) // a git dir may hold spaces
	if len(fields) != 2 || fields[1] == "" {
		return &GitError{Kind: ErrKindPermanent, Op: op, Message: "unexpected rev-parse output for the repository's git directory"}
	}
	marker := ""
	if fields[0] != "true" {
		marker = filepath.Join(fields[1], renormaliseMarker)
	}
	if err := ensureNeutral(ctx, c.repoPath, marker); err != nil || marker == "" {
		return err
	}
	if err := c.renormalise(ctx, op, marker); err != nil {
		return fmt.Errorf("%w: %w", ErrRenormalisePending, err)
	}
	return nil
}

// renormalise is EnsureNeutralWorkTree's second half: the marked tree
// brought to its stored bytes under the tree's hold, the mark cleared.
func (c *CLIClient) renormalise(ctx context.Context, op, marker string) error {
	if pending, err := renormalisePending(marker); err != nil || !pending {
		return err
	}
	h, err := c.lockTree(ctx)
	if err != nil {
		return err
	}
	defer h.release()
	ctx = h.ctx
	// Another call may have finished while this one waited for the tree.
	if pending, err := renormalisePending(marker); err != nil || !pending {
		return err
	}
	done := func() error {
		if err := clearRenormalisePending(marker); err != nil {
			return &GitError{Kind: ErrKindPermanent, Op: op, Message: "cannot clear the renormalise marker", Err: err}
		}
		return nil
	}
	if _, _, err := c.runWith(ctx, op, runOpts{}, "rev-parse", "-q", "--verify", "HEAD^{commit}"); err != nil {
		return done() // unborn HEAD: nothing checked out
	}
	status, _, err := c.runWith(ctx, op, runOpts{}, "status", "--porcelain=v1", "-z", "--untracked-files=no")
	if err != nil {
		return err
	}
	if len(parseTrackedStatus(status)) == 0 {
		return done()
	}
	if err := c.checkTrees(ctx, op, "HEAD"); err != nil {
		return err
	}
	if p, err := c.untrackedObstruction(ctx, op, "HEAD"); err != nil {
		return err
	} else if p != "" {
		return &GitError{Kind: ErrKindPermanent, Op: op,
			Message: fmt.Sprintf("an untracked file is in the way of a tracked path the renormalising reset would rewrite (%q); nothing was reset", p)}
	}
	sha, err := c.quarantineTracked(ctx, op)
	if err != nil {
		return err
	}
	observe.Logger(ctx).Warn("work tree checked out with conversions brought to stored bytes after the neutral checkout attributes were added",
		"repo_path", c.repoPath, "quarantine_sha", sha)
	if _, _, err := c.runWith(ctx, op, runOpts{}, "reset", "--hard", "-q"); err != nil {
		return err
	}
	return done()
}

// HasNeutralCheckoutAttributes reports whether repoDir's info/attributes
// ends in NeutralCheckoutAttributes.
func HasNeutralCheckoutAttributes(ctx context.Context, repoDir string) (bool, error) {
	path, err := infoAttributesPath(ctx, repoDir)
	if err != nil {
		return false, err
	}
	have, _, err := readInfoAttributes(path)
	if err != nil {
		return false, &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot read info/attributes", Err: err}
	}
	return endsNeutral(have), nil
}

// requireNeutralCheckoutAttributes is the measure's precondition: nil
// when the repository ends its info/attributes in the neutral line, the
// permanent ErrNeutralAttributesMissing refusal otherwise.
func (c *CLIClient) requireNeutralCheckoutAttributes(ctx context.Context, op string) error {
	ok, err := HasNeutralCheckoutAttributes(ctx, c.repoPath)
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) {
			cp := *ge
			cp.Op = op
			return &cp
		}
		return err
	}
	if !ok {
		return &GitError{Kind: ErrKindPermanent, Op: op, Message: NeutralAttributesMissingMessage, Err: ErrNeutralAttributesMissing}
	}
	return nil
}
