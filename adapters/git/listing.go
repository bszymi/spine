package git

// Streamed NUL-record listings (INIT-024 TASK-004, review finding N-6).
//
// ListFiles and Diff used to collect a command's whole stdout under the
// per-command limit (DefaultMaxOutputBytes, 16 MiB) and split it
// afterwards, so the size of the RAW listing was the limit. A tree whose
// `ls-tree -r` prints more than that — about 160,000 files, or a few
// thousand deep paths, which cost a few KB as a pack — made every listing
// of it fail with ErrOutputTooLarge: every FullRebuild and the sweep of
// every IncrementalSync, until the change was reverted. Discovery, the
// sweep and the branch-protection lookup keep a handful of those paths.
//
// The listings now parse git's NUL-delimited stdout as git writes it and
// bound what they KEEP, not what git prints:
//
//   - The bytes kept — each kept path plus its NUL — stay within the
//     client's limit: listingLimit for a tree listing, diffLimit for a
//     diff, which holds both trees' paths. Past it the listing fails with
//     the same permanent *GitError wrapping *OutputTooLargeError
//     (ErrOutputTooLarge) as before, so a listing that cannot be held
//     still fails closed. There is no per-path length limit: one long path
//     is bounded by the same total, and a cap on it would let a single
//     path at a governed location refuse every listing of the tree (review
//     round 1).
//   - A path the caller does not keep (ListFiles' pattern, ListFilesFunc's
//     predicate) is read and dropped: it costs the time to read it, not
//     memory. The raw output size is no longer a limit; the number of
//     records is (recordCeiling): a tree of a few small, shared subtrees
//     expands to millions of listed paths, so a listing stops, fails
//     closed, at a generous multiple of the checkout budget's entry count —
//     a tree no budgeted checkout would admit. A caller that wants only
//     one directory names it (ListFilesUnder), so git walks that subtree
//     alone.
//   - The one record git has not finished writing is held only up to the
//     room the listing has left (never less than listingCarryFloor), and
//     git is stopped at the first refusal (the command's context is
//     cancelled: SIGTERM, gitcmd.go).
//
// nulRecords, the record reader, is also the core of the checkout
// budget's tree measure (treeListingSink, treebudget.go): one reader, two
// owners whose per-record bounds differ.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// listingCarryFloor is the least a listing lets one unfinished record
// grow to, whatever room its limit has left: a record that does not fit
// the room and is longer than this is refused before it is complete (a
// kept record that long could not fit anyway; a dropped one costs the
// refusal of a listing whose limit is all but used up).
const listingCarryFloor = 4096

// listingBounds is what one streamed listing may keep and read.
type listingBounds struct {
	bytes   int64 // kept bytes: every kept path plus its NUL
	records int64 // records git may print, kept or not (recordCeiling)
}

// carryBound bounds the unfinished record of a listing that has kept
// kept bytes: the room left, never less than listingCarryFloor.
func (b listingBounds) carryBound(kept int64) int64 { return max(listingCarryFloor, b.bytes-kept) }

// errStopListing is what a record stream's Write returns once it has
// stopped: os/exec's copy goroutine then stops and closes the pipe, so git
// dies on EPIPE if the cancelled context has not stopped it already.
var errStopListing = errors.New("git: listing stopped")

// recordStream is the stdout of a streamed listing (runListing).
type recordStream interface {
	io.Writer
	// bindStop hands the stream the command's cancel, which it calls
	// when it stops.
	bindStop(stop context.CancelFunc)
	// refusal is the error the stream stopped git with (a size refusal
	// or a malformed record), nil when it did not stop.
	refusal() *GitError
	// unterminated reports output that ended inside a record: a
	// truncated listing.
	unterminated() bool
}

// nulRecords splits a stream of NUL-terminated records as git writes it
// and hands each complete record to its owner. Like stdoutSink it
// implements only io.Writer. The owner's callbacks decide what a record
// may cost:
//
//   - pending(n) is asked before a record that does not end in the
//     current write is carried to n bytes: it bounds the one partial
//     record the reader holds;
//   - record(rec) gets each complete record without its NUL. rec is
//     valid only during the call (it may be the carry, reused for the
//     next record), so an owner that keeps it copies it.
//
// Either one returning an error stops the reader: Write returns
// errStopListing, stop is called, and every later Write refuses.
type nulRecords struct {
	pending   func(n int64) error
	record    func(rec []byte) error
	stop      context.CancelFunc
	carry     []byte // the record being read, when it spans writes
	delivered int64  // bytes git handed the reader
	stopped   bool
}

func (s *nulRecords) Write(p []byte) (int, error) {
	if s.stopped {
		return 0, errStopListing
	}
	n := len(p)
	s.delivered += int64(n)
	for len(p) > 0 {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			if err := s.pending(int64(len(s.carry) + len(p))); err != nil {
				return 0, s.halt()
			}
			s.carry = append(s.carry, p...)
			return n, nil
		}
		rec := p[:i]
		if len(s.carry) > 0 {
			s.carry = append(s.carry, rec...)
			rec = s.carry
		}
		if err := s.record(rec); err != nil {
			return 0, s.halt()
		}
		s.carry = s.carry[:0]
		p = p[i+1:]
	}
	return n, nil
}

func (s *nulRecords) halt() error {
	s.stopped = true
	if s.stop != nil {
		s.stop()
	}
	return errStopListing
}

func (s *nulRecords) bindStop(stop context.CancelFunc) { s.stop = stop }

func (s *nulRecords) unterminated() bool { return len(s.carry) > 0 }

// --- ListFiles / ListFilesFunc ---

// pathCollector keeps the paths of `git ls-tree -r --name-only -z` that
// keep accepts (nil keeps every path).
type pathCollector struct {
	nulRecords
	op      string
	keep    func(string) bool
	bounds  listingBounds
	kept    int64 // bytes kept: every kept path plus its NUL
	records int64 // records read, kept or not
	paths   []string
	err     *GitError
}

func newPathCollector(op string, bounds listingBounds, keep func(string) bool) *pathCollector {
	s := &pathCollector{op: op, keep: keep, bounds: bounds}
	s.nulRecords = nulRecords{pending: s.pendingPath, record: s.path}
	return s
}

// pendingPath bounds a path git has not finished writing to the room the
// listing has left (listingBounds.carryBound): a path the caller drops
// costs no more memory than one it keeps. path applies the same bound to
// a path that ends in the write that completes it, so the verdict on a
// long path does not depend on how the pipe chunked it.
func (s *pathCollector) pendingPath(n int64) error {
	if n > s.bounds.carryBound(s.kept) {
		s.err = listingTooLarge(s.op, s.bounds.bytes, false)
		return errStopListing
	}
	return nil
}

func (s *pathCollector) path(rec []byte) error {
	if len(rec) == 0 {
		s.err = &GitError{Kind: ErrKindPermanent, Op: s.op, Message: "empty path in `git ls-tree -z` output"}
		return errStopListing
	}
	if s.records++; s.records > s.bounds.records {
		s.err = listingTooLarge(s.op, s.bounds.records, true)
		return errStopListing
	}
	if int64(len(rec)) > s.bounds.carryBound(s.kept) {
		s.err = listingTooLarge(s.op, s.bounds.bytes, false)
		return errStopListing
	}
	p := string(rec)
	if s.keep != nil && !s.keep(p) {
		return nil
	}
	s.kept += int64(len(p)) + 1
	if s.kept > s.bounds.bytes {
		s.err = listingTooLarge(s.op, s.bounds.bytes, false)
		return errStopListing
	}
	s.paths = append(s.paths, p)
	return nil
}

func (s *pathCollector) refusal() *GitError { return s.err }

// listingTooLarge is the size refusal of a streamed listing: the
// envelope runSink has always returned for an overrun ("output exceeds
// read limit", matching ErrOutputTooLarge). records marks the record
// ceiling (Limit is then a record count).
func listingTooLarge(op string, limit int64, records bool) *GitError {
	return &GitError{Kind: ErrKindPermanent, Op: op, Message: "output exceeds read limit",
		Err: &OutputTooLargeError{Op: op, Limit: limit, Records: records}}
}

// --- Diff ---

// nameStatusCollector parses `git diff --name-status -z` as a stream:
// records of "<status>\0<path>\0", or "<status>\0<src>\0<dst>\0" for a
// status starting with R (rename) or C (copy); see errMalformedDiff for
// the mapping. Every record is kept: the kept bytes are held to the diff
// limit and the records to the diff's record ceiling.
//
// A malformed stream is an error and returns no records: one mis-split
// would shift every later record onto the wrong path. The checks are the
// ones the buffered parser made, in its order within a record: a record
// cut short is "missing a path" (finish), and an empty path is reported
// once the record is complete.
type nameStatusCollector struct {
	nulRecords
	op     string
	bounds listingBounds
	kept   int64
	diffs  []FileDiff
	fields []string // the fields of the record being read: status, then paths
	err    *GitError
	parse  error // the malformed-stream cause, wrapped in err
}

func newNameStatusCollector(op string, bounds listingBounds) *nameStatusCollector {
	s := &nameStatusCollector{op: op, bounds: bounds, fields: make([]string, 0, 3)}
	s.nulRecords = nulRecords{pending: s.pendingField, record: s.field}
	return s
}

// pendingField bounds a field git has not finished writing to the room
// the diff has left (listingBounds.carryBound).
func (s *nameStatusCollector) pendingField(n int64) error {
	if n > s.bounds.carryBound(s.kept) {
		s.err = listingTooLarge(s.op, s.bounds.bytes, false)
		return errStopListing
	}
	return nil
}

func (s *nameStatusCollector) malformed(format string, args ...any) error {
	s.parse = fmt.Errorf("%w: "+format, append([]any{errMalformedDiff}, args...)...)
	s.err = &GitError{Kind: ErrKindPermanent, Op: s.op, Message: "unparseable diff output", Err: s.parse}
	return errStopListing
}

func (s *nameStatusCollector) field(rec []byte) error {
	if int64(len(rec)) > s.bounds.carryBound(s.kept) { // as pendingPath's, whatever the chunking
		s.err = listingTooLarge(s.op, s.bounds.bytes, false)
		return errStopListing
	}
	if len(s.fields) == 0 {
		if int64(len(s.diffs)) >= s.bounds.records {
			s.err = listingTooLarge(s.op, s.bounds.records, true)
			return errStopListing
		}
		status := string(rec)
		if !validDiffStatus(status) {
			return s.malformed("unexpected status field at record %d", len(s.diffs)+1)
		}
		s.fields = append(s.fields, status)
		return nil
	}
	s.kept += int64(len(rec)) + 1
	if s.kept > s.bounds.bytes {
		s.err = listingTooLarge(s.op, s.bounds.bytes, false)
		return errStopListing
	}
	s.fields = append(s.fields, string(rec))
	status := s.fields[0]
	paths := 1
	if status[0] == 'R' || status[0] == 'C' {
		paths = 2
	}
	if len(s.fields) < 1+paths {
		return nil
	}
	p1, p2 := s.fields[1], ""
	if paths == 2 {
		p2 = s.fields[2]
	}
	if p1 == "" || (paths == 2 && p2 == "") {
		return s.malformed("record %d has an empty path", len(s.diffs)+1)
	}
	var fd FileDiff
	switch status[0] {
	case 'A':
		fd = FileDiff{Path: p1, Status: "added"}
	case 'D':
		fd = FileDiff{Path: p1, Status: "deleted"}
	case 'R':
		fd = FileDiff{OldPath: p1, Path: p2, Status: "renamed"}
	case 'C':
		fd = FileDiff{Path: p2, Status: "added"}
	default: // M, T and anything else git may print for one path
		fd = FileDiff{Path: p1, Status: "modified"}
	}
	s.diffs = append(s.diffs, fd)
	s.fields = s.fields[:0]
	return nil
}

// finish checks the end of a complete stream: a record whose paths never
// came is malformed.
func (s *nameStatusCollector) finish() *GitError {
	if s.err == nil && len(s.fields) > 0 {
		_ = s.malformed("record %d is missing a path", len(s.diffs)+1)
	}
	return s.err
}

func (s *nameStatusCollector) refusal() *GitError { return s.err }

// --- CountChangedPaths ---

// pathCounter counts the NUL-terminated records of `git diff --name-only
// -z` and keeps none of them: it holds only whether the record in
// progress has started, so its memory is constant whatever git prints.
// Past maxRecords (the diff's record ceiling) it stops and fails closed.
type pathCounter struct {
	op         string
	n          int
	maxRecords int64
	partial    bool // bytes of a record seen since the last NUL
	stop       context.CancelFunc
	err        *GitError
}

func (s *pathCounter) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, errStopListing
	}
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			s.partial = true
			return n, nil
		}
		if i == 0 && !s.partial {
			s.err = &GitError{Kind: ErrKindPermanent, Op: s.op, Message: "empty path in `git diff -z` output"}
			if s.stop != nil {
				s.stop()
			}
			return 0, errStopListing
		}
		if s.n++; int64(s.n) > s.maxRecords {
			s.err = listingTooLarge(s.op, s.maxRecords, true)
			if s.stop != nil {
				s.stop()
			}
			return 0, errStopListing
		}
		s.partial = false
		p = p[i+1:]
	}
	return n, nil
}

func (s *pathCounter) bindStop(stop context.CancelFunc) { s.stop = stop }
func (s *pathCounter) refusal() *GitError               { return s.err }
func (s *pathCounter) unterminated() bool               { return s.partial }

// --- the runner ---

// runListing runs a listing command with its stdout streamed into out
// and maps the outcome, as runSink does for a read or diff command:
//
//  1. git's GIT_ALLOC_LIMIT refusal (read commands), honoured even when
//     git exited 0, since its output may then be incomplete;
//  2. the stream's own refusal: git was stopped by us, so its exit
//     status and stderr say nothing;
//  3. a failed command, classified from git's stderr;
//  4. output that ended inside a record (endOfStream: the refusal
//     unterminated builds).
//
// The command runs in the environment runSink gives its class
// (commandEnv): the pinned delta-base cache and the C locale, and for a
// read command GIT_ALLOC_LIMIT.
func (c *CLIClient) runListing(ctx context.Context, op string, out recordStream, unterminated func() *GitError, args ...string) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	out.bindStop(cancel)
	class := classOf(op)
	cmd := newGitCmd(runCtx, args...)
	cmd.Dir = c.repoPath
	cmd.Env = c.commandEnv(class)
	if c.envHook != nil {
		c.envHook(op, cmd.Env)
	}
	stderr := &stderrSink{}
	cmd.Stdout = out
	cmd.Stderr = stderr
	runErr := cmd.Run()
	errText := stderr.String()
	if class == opRead && isGitAllocRefusal(errText, c.gitAllocLimit()) {
		return &GitError{Kind: ErrKindPermanent, Op: op, Message: "output exceeds read limit",
			Err: &OutputTooLargeError{Op: op, Limit: c.gitAllocLimit(), GitAlloc: true}}
	}
	if err := out.refusal(); err != nil {
		return err
	}
	if runErr != nil {
		gitErr := classifyGitError(op, strings.TrimSpace(errText))
		gitErr.Err = runErr
		return gitErr
	}
	if err := endOfStream(out, unterminated); err != nil {
		return err
	}
	return nil
}

// endOfStream is a listing's verdict on the end of output git finished
// cleanly: one that ended inside a record is truncated, the refusal
// unterminated builds. Every streamed listing, and the test shims that
// feed collectors directly, end through it.
func endOfStream(out recordStream, unterminated func() *GitError) *GitError {
	if out.unterminated() {
		return unterminated()
	}
	return nil
}
