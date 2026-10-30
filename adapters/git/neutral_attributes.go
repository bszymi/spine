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
// RECORD: every tracked path that differed from HEAD or the index under
// the old attributes, compared by content — the tree's real uncommitted
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

// maxRenormaliseRecordEntries bounds the record's entry count, written or
// read: a record never names more paths than this (far above any tree the
// default budget admits), so a damaged count cannot make the reader
// allocate for more.
const maxRenormaliseRecordEntries = 1 << 21

// renormaliseRecord is a marker's content: the real changes.
type renormaliseRecord struct {
	changes []string
}

// writeRenormaliseRecord writes the marker at path: the header, "changes
// <n>" and n NUL-terminated paths. It writes a temporary file in the same
// directory and renames it into place, so a marker is whole or absent; a
// link at the path is replaced, never followed.
func writeRenormaliseRecord(path string, rec renormaliseRecord) error {
	if len(rec.changes) > maxRenormaliseRecordEntries {
		return fmt.Errorf("the renormalise record names more than %d paths", maxRenormaliseRecordEntries)
	}
	var b bytes.Buffer
	b.WriteString(renormaliseRecordHeader)
	b.WriteString("changes " + strconv.Itoa(len(rec.changes)) + "\n")
	for _, p := range rec.changes {
		b.WriteString(p)
		b.WriteByte(0)
	}
	if b.Len() > maxRenormaliseRecordBytes {
		return fmt.Errorf("the renormalise record is larger than %d bytes", maxRenormaliseRecordBytes)
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
// file); the caller then quarantines before it rewrites anything.
//
// The marker is untrusted input (INIT-024 integration A, codex): anything
// that can write a work tree's git directory can write it, and a crash or
// a disk error can tear it. The count is decimal digits only, at most
// maxRenormaliseRecordEntries, and no more than the fields the rest of the
// file can hold (each at least one byte and its NUL); every field is
// non-empty; nothing may follow the last one (a record of an earlier build
// that carried a second section is no record). Storage grows with the
// fields actually read, never from the count. Any violation is "no
// record", never a panic and never a partial record.
func readRenormaliseRecord(path string) (rec renormaliseRecord, ok bool, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return rec, false, err
	}
	if !fi.Mode().IsRegular() {
		return rec, false, nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: a fixed name in git's own directory
	if err != nil {
		return rec, false, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxRenormaliseRecordBytes+1))
	if err != nil {
		return rec, false, err
	}
	rest, found := bytes.CutPrefix(b, []byte(renormaliseRecordHeader))
	if !found || len(b) > maxRenormaliseRecordBytes {
		return renormaliseRecord{}, false, nil
	}
	line, after, found := bytes.Cut(rest, []byte("\n"))
	if !found {
		return renormaliseRecord{}, false, nil
	}
	label, count, found := strings.Cut(string(line), " ")
	if !found || label != "changes" {
		return renormaliseRecord{}, false, nil
	}
	n, valid := renormaliseRecordCount(count)
	// Each field takes at least two bytes (one of content, its NUL).
	if !valid || n > len(after)/2 {
		return renormaliseRecord{}, false, nil
	}
	var changes []string
	for i := 0; i < n; i++ {
		field, tail, found := bytes.Cut(after, []byte{0})
		if !found || len(field) == 0 {
			return renormaliseRecord{}, false, nil
		}
		changes = append(changes, string(field))
		after = tail
	}
	if len(after) != 0 {
		return renormaliseRecord{}, false, nil
	}
	return renormaliseRecord{changes: changes}, true, nil
}

// renormaliseRecordCount parses the record's count: decimal digits only,
// without a sign or a leading zero, at most maxRenormaliseRecordEntries.
func renormaliseRecordCount(s string) (int, bool) {
	if s == "" || len(s) > len(strconv.Itoa(maxRenormaliseRecordEntries)) || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n > maxRenormaliseRecordEntries {
		return 0, false
	}
	return n, true
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
// are not the stored ones, and a same-size edit made within the mtime
// granularity, or one a stale core.fsmonitor does not report, looks clean
// under any attributes — except when the entry is not older than the index
// file ("racily clean"): then git compares the content. With the index's
// time at 1 every entry is, so the next status (with core.fsmonitor off)
// hashes every tracked file under the attributes in force and reports
// exactly the files whose content differs. The index's entries, staged
// changes and flags are untouched: only its own time changes. A status
// that may write the index (the held tree) refreshes it with fresh stat
// data; when it did not (another worktree, read with --no-optional-locks,
// or an index.lock held elsewhere), the returned restore puts the original
// time back.
// Restoring an older time than a concurrent writer left is safe: an older
// index time only makes git compare more entries. A missing index
// (nothing checked out) needs nothing.
func forceContentCompare(path string) (restore func() error, err error) {
	none := func() error { return nil }
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return none, nil
	case err != nil:
		return none, err
	case !fi.Mode().IsRegular():
		return none, fmt.Errorf("%s is not a regular file", path)
	}
	orig := fi.ModTime()
	epoch := time.Unix(1, 0)
	if err := os.Chtimes(path, epoch, epoch); err != nil {
		return none, err
	}
	return func() error {
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || !fi.ModTime().Equal(epoch) {
			return nil // gone, replaced, or rewritten since: nothing to put back
		}
		return os.Chtimes(path, orig, orig)
	}, nil
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
// existing clone with a work tree (review rounds 3-4; the shape since
// INIT-024 integration A phase 4). A clone checked out before the line was
// written holds files as its old attributes or host config converted them
// (CRLF, expanded idents), which no longer match the blobs once conversion
// is off: a merge or switch over them is refused, and a read-modify-write
// through the tree would commit the converted bytes. So, when this call
// ADDS the line:
//
//  1. It takes the tree's hold (so no sequence holding the tree runs on
//     under changed attributes) and, still under the old attributes, runs
//     every check that can fail: HEAD unborn — identified positively —
//     needs nothing; a torn HEAD is refused; HEAD's tree is measured
//     against the client's budget (when it has one) from the stored
//     objects, which do not depend on attributes. Any failure returns its
//     error with nothing written: no line, no marker.
//  2. It records each work tree's real changes BY CONTENT
//     (forceContentCompare, core.fsmonitor off): every tracked path that
//     differs from HEAD or the index, staged or not, into a marker in that
//     work tree's git directory — this tree's and every linked worktree's
//     (`git worktree list`, read with --no-optional-locks, its index's
//     time put back). Only then is the line written.
//  3. Under the line it compares the held tree by content again and
//     rewrites from the index (`checkout-index -f -u`) every file that
//     differs, whose index entry matches HEAD and that is not recorded —
//     the conversion-only files — then clears the marker. Recorded paths,
//     staged changes and untracked files are left exactly as they are (one
//     Warn gives the counts).
//
// After the line only an I/O error or a crash can stop it; the error is
// returned wrapped in ErrRenormalisePending and the marker stays. A marker
// this call did not write and hold the tree since — a retry after such a
// failure, a linked worktree's, an old or damaged one — cannot tell an
// edit made since from a conversion, so that renormalisation checks HEAD
// again, quarantines the tracked state first (`git stash create`, the SHA
// at Warn) and then rewrites every unrecorded difference. (Round 4 took a
// fingerprint of each conversion-only file to spare a retry the
// quarantine; integration A phase 4 replaced that with these pre-line
// checks, which leave no failure but I/O after the line, on the
// reviewer's recommendation.)
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
	recorded := false // this call recorded this tree, under the hold it still has
	for {
		err := ensureNeutral(ctx, c.repoPath, func() error {
			if !bare && release == nil {
				return errNeedTreeHold
			}
			markSelf := false
			if !bare {
				unborn, err := c.checkBeforeLine(ctx, op)
				if err != nil {
					return err
				}
				markSelf = !unborn
			}
			if err := c.markWorkTrees(ctx, op, gitDir, markSelf); err != nil {
				return err
			}
			recorded = markSelf
			return nil
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
	if err := c.renormalise(ctx, op, filepath.Join(gitDir, renormaliseMarker), recorded); err != nil {
		return fmt.Errorf("%w: %w", ErrRenormalisePending, err)
	}
	return nil
}

// errNeedTreeHold is ensureNeutral's answer, through beforeAdd, when the
// line is to be added and the caller does not hold the tree yet.
var errNeedTreeHold = errors.New("git: the tree's hold is needed before the neutral line is added")

// checkBeforeLine runs, under the tree's hold and before the line is
// written, every check the renormalisation could fail (integration A
// phase 4): unborn reports a HEAD that is positively unborn (nothing
// checked out, nothing to renormalise); a torn or unreadable HEAD, a
// cancelled request and a HEAD tree over the client's budget are errors.
// The measure reads the stored objects (`ls-tree -l` sizes), which do not
// depend on attributes, so it needs no line.
func (c *CLIClient) checkBeforeLine(ctx context.Context, op string) (unborn bool, err error) {
	if unborn, err := c.probeHead(ctx, op); err != nil || unborn {
		return unborn, err
	}
	return false, c.checkStoredTrees(ctx, op, "HEAD")
}

// probeHead is headUnborn behind the test hook.
func (c *CLIClient) probeHead(ctx context.Context, op string) (bool, error) {
	if renormaliseHeadProbeHook != nil {
		renormaliseHeadProbeHook()
	}
	return c.headUnborn(ctx, op)
}

// markWorkTrees records the real changes of each work tree the line is
// about to change, by content under the attributes still in force, and
// writes each record as that tree's marker: this one first when markSelf
// (a failure refuses the line), then every other worktree `git worktree
// list` names (a bare or prunable entry has no tree; a failure there is
// logged and that tree left unmarked).
func (c *CLIClient) markWorkTrees(ctx context.Context, op, selfGitDir string, markSelf bool) error {
	if markSelf {
		paths, err := c.realChanges(ctx, op, "")
		if err != nil {
			return err
		}
		if err := writeRenormaliseRecord(filepath.Join(selfGitDir, renormaliseMarker), renormaliseRecord{changes: paths}); err != nil {
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
	return writeRenormaliseRecord(filepath.Join(gd, renormaliseMarker), renormaliseRecord{changes: paths})
}

// realChanges lists the tracked paths of the work tree at dir ("" for
// this one) that differ from HEAD or from the index under the attributes
// in force (contentStatus). Called before the line is written, these are
// the tree's real uncommitted changes — a file checked out with the old
// conversions compares equal under them.
func (c *CLIClient) realChanges(ctx context.Context, op, dir string) ([]string, error) {
	changes, err := c.contentStatus(ctx, op, dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(changes))
	for _, ch := range changes {
		paths = append(paths, ch.path)
	}
	return paths, nil
}

// contentStatus is `git status` of the work tree at dir ("" for this one)
// compared by content: forceContentCompare on that tree's index, and
// core.fsmonitor off, so neither stat data nor a stale monitor can hide an
// edit (review round 5, P3: the record trusted both, so an edit they hid
// looked conversion-only after the line and was rewritten). Another work
// tree is not held: it is read with --no-optional-locks (its index is never
// written) and its index's time is put back. Renames are not paired (each
// side is its own entry) and submodules are not compared.
//
// The output is streamed through the record reader the listings share
// (listing.go; INIT-024 integration A, phase 5) and held to the same
// bounds as a tree listing (listingBounds): the paths it keeps to the
// listing limit (the budget's path bytes, one NUL per entry and a margin)
// and its records to the record ceiling. Every tracked path of a tree the
// budget admits therefore fits; a status past either fails closed.
func (c *CLIClient) contentStatus(ctx context.Context, op, dir string) ([]trackedChange, error) {
	var pre []string
	if dir != "" {
		// Another tree, not held: read only, never rewrite its index.
		pre = []string{"--no-optional-locks", "-C", dir}
	}
	index, _, err := c.runWith(ctx, op, runOpts{}, append(pre, "rev-parse", "--git-path", "index")...)
	if err != nil {
		return nil, err
	}
	index = strings.TrimSuffix(index, "\n")
	if !filepath.IsAbs(index) {
		base := c.repoPath
		if dir != "" {
			base = dir
		}
		index = filepath.Join(base, index)
	}
	restore, err := forceContentCompare(index)
	if err != nil {
		return nil, &GitError{Kind: ErrKindPermanent, Op: op, Message: "cannot set the index up for a content comparison", Err: err}
	}
	args := make([]string, 0, len(pre)+9)
	args = append(args, pre...)
	args = append(args, "-c", "core.fsmonitor=false",
		"status", "--porcelain=v1", "-z", "--untracked-files=no", "--no-renames", "--ignore-submodules=all")
	// The status may refresh the index (the held tree): it runs under the
	// client's mutate deadline, as runWith's commands do.
	runCtx, cancel := withMutateDeadline(ctx, c.CheckoutLimits().MutateTimeout)
	defer cancel()
	out := newStatusCollector(op, c.listingBounds())
	err = c.runListing(runCtx, op, out, func() *GitError {
		return &GitError{Kind: ErrKindPermanent, Op: op, Message: "unterminated `git status -z` output"}
	}, args...)
	// A status that refreshed the index (the held tree, when it could take
	// index.lock) wrote it with a fresh time and restore leaves it; one
	// that did not (another tree, or a lock someone else held) gets its
	// original time back.
	if rerr := restore(); rerr != nil && err == nil {
		err = &GitError{Kind: ErrKindPermanent, Op: op, Message: "cannot put the index's time back after a content comparison", Err: rerr}
	}
	if err != nil {
		return nil, err
	}
	return out.changes, nil
}

// statusCollector parses `git status --porcelain=v1 -z --no-renames` as a
// stream (contentStatus): records of "XY <path>", each its own NUL-ended
// record. It keeps every record, holding the paths' bytes (plus a NUL
// each) to the listing limit and the records to the record ceiling; a
// record that is not "XY <path>" is malformed and fails the call.
type statusCollector struct {
	nulRecords
	op      string
	bounds  listingBounds
	kept    int64
	changes []trackedChange
	err     *GitError
}

func newStatusCollector(op string, bounds listingBounds) *statusCollector {
	s := &statusCollector{op: op, bounds: bounds}
	s.nulRecords = nulRecords{pending: s.pendingRecord, record: s.record}
	return s
}

// pendingRecord bounds a record git has not finished writing to the room
// the status has left (listingBounds.carryBound), plus its "XY " prefix.
func (s *statusCollector) pendingRecord(n int64) error {
	if n > s.bounds.carryBound(s.kept)+3 {
		s.err = listingTooLarge(s.op, s.bounds.bytes, false)
		return errStopListing
	}
	return nil
}

func (s *statusCollector) record(rec []byte) error {
	if len(rec) < 4 || rec[2] != ' ' {
		s.err = &GitError{Kind: ErrKindPermanent, Op: s.op, Message: "unparseable `git status -z` output"}
		return errStopListing
	}
	if int64(len(s.changes)) >= s.bounds.records {
		s.err = listingTooLarge(s.op, s.bounds.records, true)
		return errStopListing
	}
	path := rec[3:]
	s.kept += int64(len(path)) + 1
	if s.kept > s.bounds.bytes {
		s.err = listingTooLarge(s.op, s.bounds.bytes, false)
		return errStopListing
	}
	s.changes = append(s.changes, trackedChange{x: rec[0], y: rec[1], path: string(path)})
	return nil
}

func (s *statusCollector) refusal() *GitError { return s.err }

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

// renormaliseHeadProbeHook, when set (tests only), runs right before the
// renormalisation looks at HEAD.
var renormaliseHeadProbeHook func()

// renormaliseAfterLineHook, when set (tests only), runs once the line is
// in place, before the marked tree is compared; an error it returns stops
// the renormalisation there, as an I/O error or a crash would.
var renormaliseAfterLineHook func() error

// renormalise is EnsureNeutralWorkTree's last step: the marked tree's
// conversion-only paths brought to their stored bytes under the tree's
// hold, the mark cleared. recorded says this call ran the checks, wrote
// the record and has held the tree since: then every file that differs
// under the line and is not recorded can only be a conversion. Otherwise
// (a retry, a linked worktree's mark, an old or damaged marker) HEAD is
// checked again and the tracked state is quarantined before anything is
// rewritten: an edit made since the record cannot be told from a
// conversion.
func (c *CLIClient) renormalise(ctx context.Context, op, marker string, recorded bool) error {
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
	if renormaliseAfterLineHook != nil {
		if err := renormaliseAfterLineHook(); err != nil {
			return err
		}
	}
	if !recorded {
		if unborn, err := c.checkBeforeLine(ctx, op); err != nil {
			return err
		} else if unborn {
			return done() // nothing checked out
		}
	}
	rec, haveRecord, err := readRenormaliseRecord(marker)
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(rec.changes))
	for _, p := range rec.changes {
		keep[p] = true
	}
	changes, err := c.contentStatus(ctx, op, "")
	if err != nil {
		return err
	}
	var rewrite []string
	for _, ch := range changes {
		// The index entry matches HEAD and the file's content differs.
		if ch.x == ' ' && ch.y == 'M' && !keep[ch.path] {
			rewrite = append(rewrite, ch.path)
		}
	}
	log := observe.Logger(ctx)
	if len(rewrite) == 0 {
		if len(rec.changes) > 0 {
			log.Warn("neutral checkout attributes added: tracked paths with uncommitted changes left as they were (they keep the bytes they were checked out with)",
				"repo_path", c.repoPath, "left_as_they_were", len(rec.changes))
		}
		return done()
	}
	quarantine, sha := !recorded, ""
	if quarantine {
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
	switch {
	case quarantine:
		log.Warn("work tree checked out with conversions brought to stored bytes after the neutral checkout attributes were added; it was not recorded and held by this call (a retry, a linked worktree or an unreadable record), so the tracked state was quarantined first",
			"repo_path", c.repoPath, "rewritten", len(rewrite), "left_as_they_were", len(rec.changes), "record", haveRecord, "quarantine_sha", sha)
	default:
		log.Warn("work tree checked out with conversions brought to stored bytes after the neutral checkout attributes were added",
			"repo_path", c.repoPath, "rewritten", len(rewrite), "left_as_they_were", len(rec.changes))
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
