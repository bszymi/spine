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
// and a tree checked out with conversions before the line no longer
// matches its blobs, so EnsureNeutralWorkTree brings its conversion-only
// files to the stored bytes by content, leaving real uncommitted changes
// in place (retried until that succeeds).
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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

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

// neutralWriteMus serialises this process's writers of each
// info/attributes file (one mutex per file, so a slow record of one
// repository's work trees never holds another repository's writer).
//
// Lock order (review round 4): a work tree's hold (acquireTree) is taken
// BEFORE the file's mutex, never after — EnsureNeutralWorkTree takes the
// calling tree's hold and then writes the line under the mutex; nothing
// waits for a tree while holding the mutex (the record of the other work
// trees runs git there without taking their holds).
var neutralWriteMus sync.Map // cleaned info/attributes path -> *sync.Mutex

func neutralWriteMu(path string) *sync.Mutex {
	mu, _ := neutralWriteMus.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	return mu.(*sync.Mutex)
}

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
	return ensureNeutral(ctx, repoDir, nil)
}

// ensureNeutral is EnsureNeutralCheckoutAttributes. beforeAdd, when not
// nil, runs under the file's mutex when the write ADDS the line (the file
// did not end in it), before anything is written; its error leaves the
// file as it was and is returned. EnsureNeutralWorkTree uses it to take
// its hold of the tree first and to record and mark the work trees the
// line will change (review rounds 3-4): the marks are on disk before the
// line is.
func ensureNeutral(ctx context.Context, repoDir string, beforeAdd func() error) error {
	if err := requireRepositoryTop(ctx, repoDir); err != nil {
		return err
	}
	path, err := infoAttributesPath(ctx, repoDir)
	if err != nil {
		return err
	}
	mu := neutralWriteMu(path)
	mu.Lock()
	defer mu.Unlock()
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
	if beforeAdd != nil && !endsNeutral(have) {
		if err := beforeAdd(); err != nil {
			return err
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
// was added. It is written (atomically) before the line is, and holds the
// RECORD (review round 4): every tracked path that differed from HEAD or
// the index under the old attributes — the tree's real uncommitted
// changes, staged or not, which renormalising leaves as they are. It is
// removed once the tree's conversion-only paths are at their stored bytes
// (or there are none, or HEAD is unborn).
const renormaliseMarker = "spine-renormalise-pending"

// renormaliseRecordHeader opens a marker that carries a record. A marker
// without it (one written by an earlier build of this change) or with a
// damaged record has none: renormalising then falls back to quarantining
// the tracked state first.
const renormaliseRecordHeader = "spine-renormalise-record v1\n"

// maxRenormaliseRecordBytes bounds a marker, written or read.
const maxRenormaliseRecordBytes = 64 << 20

// writeRenormaliseRecord writes the marker at path with the record paths:
// the header, the count, then each path NUL-terminated. It writes a
// temporary file in the same directory and renames it into place, so a
// marker is whole or absent; a link at the path is replaced, never
// followed.
func writeRenormaliseRecord(path string, paths []string) error {
	var b bytes.Buffer
	b.WriteString(renormaliseRecordHeader)
	b.WriteString(strconv.Itoa(len(paths)) + "\n")
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte(0)
	}
	if b.Len() > maxRenormaliseRecordBytes {
		return fmt.Errorf("the record of uncommitted changes is larger than %d bytes", maxRenormaliseRecordBytes)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spine-renormalise-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op after the rename
	if _, err := tmp.Write(b.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readRenormaliseRecord reads the marker's record. ok is false when the
// marker carries none (an older marker, a damaged one, or not a regular
// file).
func readRenormaliseRecord(path string) (paths []string, ok bool, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: a fixed name in git's own directory
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxRenormaliseRecordBytes+1))
	if err != nil {
		return nil, false, err
	}
	rest, found := bytes.CutPrefix(b, []byte(renormaliseRecordHeader))
	if !found || len(b) > maxRenormaliseRecordBytes {
		return nil, false, nil
	}
	count, rest, found := bytes.Cut(rest, []byte("\n"))
	n, convErr := strconv.Atoi(string(count))
	if !found || convErr != nil || n < 0 {
		return nil, false, nil
	}
	if n == 0 {
		return nil, len(rest) == 0, nil
	}
	if len(rest) == 0 || rest[len(rest)-1] != 0 {
		return nil, false, nil
	}
	fields := strings.Split(string(rest[:len(rest)-1]), "\x00")
	if len(fields) != n {
		return nil, false, nil
	}
	return fields, true, nil
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

// forceContentCompare sets the index file's modification time to one
// second past the epoch (review round 4, P2). git trusts an index entry
// whose stat data matches its file — so a long-lived clone checked out
// with conversions looks clean under the neutral line although its bytes
// are not the stored ones — except when the entry is not older than the
// index file ("racily clean"): then git compares the content. With the
// index's time at 1 every entry is, so the next status hashes every
// tracked file under the attributes in force and reports exactly the
// files whose content differs. The index's entries, staged changes and
// flags are untouched: only its own time changes, and the status's
// refresh writes it back with fresh stat data. A missing index (nothing
// checked out) needs nothing.
func forceContentCompare(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !fi.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	}
	epoch := time.Unix(1, 0)
	return os.Chtimes(path, epoch, epoch)
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
// existing clone with a work tree (review rounds 3-4). A clone checked out
// before the line was written holds files as its old attributes or host
// config converted them (CRLF, expanded idents), which no longer match
// the blobs once conversion is off: a merge or switch over them is
// refused, and a read-modify-write through the tree would commit the
// converted bytes. So:
//
//   - When this call ADDS the line, it first takes the tree's hold (so no
//     sequence holding the tree runs on under changed attributes), then,
//     under the old attributes, records each work tree's real changes —
//     every tracked path that differs from HEAD or the index, staged or
//     not — into a marker in that work tree's git directory: this tree's
//     and every linked worktree's (`git worktree list`), the main one
//     included when this is a linked one. Only then is the line written.
//   - A marked tree (this one) is renormalised under its hold: HEAD unborn
//     — identified positively — needs nothing; otherwise git compares
//     every tracked file's content with the index under the new
//     attributes (forceContentCompare: stat data alone would call a
//     long-lived converted tree clean), and the files that differ, whose
//     index entry matches HEAD and that are not in the record, are the
//     conversion-only ones: HEAD's tree is measured against the client's
//     budget (when it has one) and they are rewritten from the index
//     (`checkout-index -f -u`). Recorded paths, staged changes and
//     untracked files are left exactly as they are (one Warn gives their
//     count). A marker without a record (written by an earlier build, or
//     damaged) falls back to quarantining the tracked state first
//     (`git stash create`, the SHA logged at Warn) and then rewriting
//     every unstaged difference.
//   - The mark is cleared only then. A renormalisation that fails or is
//     interrupted (HEAD over the budget, a torn ref, a cancelled request,
//     a crash) is retried by the next call; the error, returned
//     meanwhile, matches ErrRenormalisePending.
//
// A bare repository is never marked itself (its linked worktrees are). A
// file that already ended in the line, with no mark, leaves the file and
// the tree alone, and holds nothing (so the per-push call of the git HTTP
// endpoint never waits behind a sequence once the tree is renormalised).
// Another work tree's mark is acted on by a call on that work tree.
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
	bare, gitDir := fields[0] == "true", fields[1]
	var release func() // the hold taken to add the line
	defer func() {
		if release != nil {
			release()
		}
	}()
	for {
		err := ensureNeutral(ctx, c.repoPath, func() error {
			if !bare && release == nil {
				return errNeedTreeHold
			}
			return c.markWorkTrees(ctx, op, gitDir, bare)
		})
		if !errors.Is(err, errNeedTreeHold) {
			if err != nil {
				return err
			}
			break
		}
		h, err := c.lockTree(ctx)
		if err != nil {
			return err
		}
		release, ctx = h.release, h.ctx
	}
	if bare {
		return nil
	}
	if err := c.renormalise(ctx, op, filepath.Join(gitDir, renormaliseMarker)); err != nil {
		return fmt.Errorf("%w: %w", ErrRenormalisePending, err)
	}
	return nil
}

// errNeedTreeHold is ensureNeutral's answer, through beforeAdd, when the
// line is to be added and the caller does not hold the tree yet.
var errNeedTreeHold = errors.New("git: the tree's hold is needed before the neutral line is added")

// markWorkTrees records the real changes of each work tree the line is
// about to change, under the attributes still in force, and writes each
// record as that tree's marker: this one first (when it has a work tree;
// a failure refuses the line), then every other worktree `git worktree
// list` names (a bare or prunable entry has no tree; a failure there is
// logged and that tree left unmarked).
func (c *CLIClient) markWorkTrees(ctx context.Context, op, selfGitDir string, bare bool) error {
	if !bare {
		paths, err := c.realChanges(ctx, op, "")
		if err != nil {
			return err
		}
		if err := writeRenormaliseRecord(filepath.Join(selfGitDir, renormaliseMarker), paths); err != nil {
			return &GitError{Kind: ErrKindPermanent, Op: op, Message: "cannot record the work tree's uncommitted changes; info/attributes not written", Err: err}
		}
	}
	list, _, err := c.runWith(ctx, op, runOpts{}, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	for _, wt := range parseWorktreeList(list) {
		if wt.bare || wt.prunable {
			continue
		}
		if err := c.markOtherWorkTree(ctx, op, wt.path, selfGitDir); err != nil {
			if ctx.Err() != nil {
				return err
			}
			observe.Logger(ctx).Warn("a linked worktree could not be marked for renormalising; it keeps the bytes it was checked out with",
				"repo_path", c.repoPath, "worktree", wt.path, "error", err)
		}
	}
	return nil
}

// markOtherWorkTree records and marks the work tree at dir, unless it is
// this one (selfGitDir; git canonicalises --absolute-git-dir).
func (c *CLIClient) markOtherWorkTree(ctx context.Context, op, dir, selfGitDir string) error {
	out, _, err := c.runWith(ctx, op, runOpts{}, "-C", dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	gd := strings.TrimSuffix(out, "\n")
	if gd == "" || gd == selfGitDir {
		return nil
	}
	paths, err := c.realChanges(ctx, op, dir)
	if err != nil {
		return err
	}
	return writeRenormaliseRecord(filepath.Join(gd, renormaliseMarker), paths)
}

// realChanges lists the tracked paths of the work tree at dir ("" for
// this one) that differ from HEAD or from the index under the attributes
// in force: `git status`, renames' sources included. Called before the
// line is written, these are the tree's real uncommitted changes — a
// file checked out with the old conversions compares equal under them.
func (c *CLIClient) realChanges(ctx context.Context, op, dir string) ([]string, error) {
	args := []string{"status", "--porcelain=v1", "-z", "--untracked-files=no"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, _, err := c.runWith(ctx, op, runOpts{}, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 || f[2] != ' ' {
			continue
		}
		paths = append(paths, f[3:])
		if (f[0] == 'R' || f[0] == 'C') && i+1 < len(fields) {
			i++
			paths = append(paths, fields[i])
		}
	}
	return paths, nil
}

// worktreeEntry is one record of `git worktree list --porcelain -z`.
type worktreeEntry struct {
	path           string
	bare, prunable bool
}

// parseWorktreeList parses `git worktree list --porcelain -z`: records of
// NUL-terminated "key[ value]" fields, each record ended by an empty one.
func parseWorktreeList(out string) []worktreeEntry {
	var list []worktreeEntry
	var cur *worktreeEntry
	for _, f := range strings.Split(out, "\x00") {
		switch {
		case f == "":
			cur = nil
		case strings.HasPrefix(f, "worktree "):
			list = append(list, worktreeEntry{path: strings.TrimPrefix(f, "worktree ")})
			cur = &list[len(list)-1]
		case cur == nil:
		case f == "bare":
			cur.bare = true
		case f == "prunable" || strings.HasPrefix(f, "prunable "):
			cur.prunable = true
		}
	}
	return list
}

// renormaliseHeadProbeHook, when set (tests only), runs right before
// renormalise looks at HEAD.
var renormaliseHeadProbeHook func()

// renormalise is EnsureNeutralWorkTree's second half: the marked tree's
// conversion-only paths brought to their stored bytes under the tree's
// hold, the mark cleared.
func (c *CLIClient) renormalise(ctx context.Context, op, marker string) error {
	if pending, err := renormalisePending(marker); err != nil || !pending {
		return err
	}
	h, err := c.lockTree(ctx) // re-enters the hold taken to add the line
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
	if renormaliseHeadProbeHook != nil {
		renormaliseHeadProbeHook()
	}
	if unborn, err := c.headUnborn(ctx, op); err != nil {
		return err
	} else if unborn {
		return done() // nothing checked out
	}
	record, haveRecord, err := readRenormaliseRecord(marker)
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(record))
	for _, p := range record {
		keep[p] = true
	}
	index, _, err := c.runWith(ctx, op, runOpts{}, "rev-parse", "--git-path", "index")
	if err != nil {
		return err
	}
	index = strings.TrimSuffix(index, "\n")
	if !filepath.IsAbs(index) {
		index = filepath.Join(c.repoPath, index)
	}
	if err := forceContentCompare(index); err != nil {
		return &GitError{Kind: ErrKindPermanent, Op: op, Message: "cannot set the index up for a content comparison", Err: err}
	}
	status, _, err := c.runWith(ctx, op, runOpts{}, "-c", "core.fsmonitor=false",
		"status", "--porcelain=v1", "-z", "--untracked-files=no", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		return err
	}
	var rewrite []string
	for _, ch := range parseTrackedStatus(status) {
		// The index entry matches HEAD and the file's content differs.
		if ch.x == ' ' && ch.y == 'M' && !keep[ch.path] {
			rewrite = append(rewrite, ch.path)
		}
	}
	log := observe.Logger(ctx)
	if len(rewrite) == 0 {
		if len(record) > 0 {
			log.Warn("neutral checkout attributes added: tracked paths with uncommitted changes left as they were (they keep the bytes they were checked out with)",
				"repo_path", c.repoPath, "left_as_they_were", len(record))
		}
		return done()
	}
	if err := c.checkTrees(ctx, op, "HEAD"); err != nil {
		return err
	}
	sha := ""
	if !haveRecord {
		if sha, err = c.quarantineTracked(ctx, op); err != nil {
			return err
		}
	}
	var in strings.Builder
	for _, p := range rewrite {
		in.WriteString(p)
		in.WriteByte(0)
	}
	if _, _, err := c.runWith(ctx, op, runOpts{stdin: strings.NewReader(in.String())},
		"checkout-index", "-f", "-u", "-z", "--stdin"); err != nil {
		return err
	}
	if haveRecord {
		log.Warn("work tree checked out with conversions brought to stored bytes after the neutral checkout attributes were added",
			"repo_path", c.repoPath, "rewritten", len(rewrite), "left_as_they_were", len(record))
	} else {
		log.Warn("work tree checked out with conversions brought to stored bytes after the neutral checkout attributes were added; its marker had no record of uncommitted changes, so the tracked state was quarantined first",
			"repo_path", c.repoPath, "rewritten", len(rewrite), "quarantine_sha", sha)
	}
	return done()
}

// headUnborn reports whether HEAD is unborn, identified positively
// (review round 4): HEAD names no commit, `symbolic-ref -q HEAD` names a
// branch (refs/heads/...), `show-ref --verify --quiet` of it exits 1 with
// nothing on stderr, and the request is not cancelled. Any other failure
// is returned as an error, so the mark is kept and the next call retries:
// a torn ref (symbolic-ref refuses a branch file that holds no object id;
// show-ref exits 128 on one that names a missing object — git 2.39 and
// 2.54 alike), a detached HEAD naming no commit, a cancelled request.
func (c *CLIClient) headUnborn(ctx context.Context, op string) (bool, error) {
	_, _, err := c.runWith(ctx, op, runOpts{}, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if err == nil {
		return false, nil
	}
	notUnborn := func(msg string, cause error) (bool, error) {
		if ctx.Err() != nil {
			return false, &GitError{Kind: ErrKindTransient, Op: op, Message: "cancelled while looking at HEAD", Err: ctx.Err()}
		}
		return false, &GitError{Kind: ErrKindPermanent, Op: op, Message: msg, Err: cause}
	}
	if ctx.Err() != nil {
		return notUnborn("", err)
	}
	out, _, err := c.runWith(ctx, op, runOpts{}, "symbolic-ref", "-q", "HEAD")
	ref := strings.TrimSuffix(out, "\n")
	if err != nil || !strings.HasPrefix(ref, "refs/heads/") || strings.Contains(ref, "\n") {
		return notUnborn("HEAD names no commit and is not a branch", err)
	}
	_, stderr, err := c.runWith(ctx, op, runOpts{}, "show-ref", "--verify", "--quiet", ref)
	var exit *exec.ExitError
	if err == nil || !errors.As(err, &exit) || exit.ExitCode() != 1 || strings.TrimSpace(stderr) != "" || ctx.Err() != nil {
		return notUnborn(fmt.Sprintf("HEAD's branch %q names no commit", ref), err)
	}
	return true, nil
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
