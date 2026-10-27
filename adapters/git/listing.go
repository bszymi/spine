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
//     client's listing limit (listingLimit), and no kept path is longer
//     than MaxListingPathBytes. Exceeding either is the same permanent
//     *GitError wrapping *OutputTooLargeError (ErrOutputTooLarge) as
//     before, so a listing that cannot be held still fails closed.
//   - A path the caller does not keep (ListFiles' pattern, ListFilesFunc's
//     predicate) is read and dropped: it costs the time to read it, not
//     memory. The raw output size is no longer a limit.
//   - The one record git has not finished writing is held only up to the
//     room the listing has left (never less than one maximal path), and
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

// MaxListingPathBytes is the longest path a streamed listing keeps
// (Linux's PATH_MAX; a longer path cannot be checked out there, and
// macOS's limit is lower). A kept path over it refuses the listing with
// ErrOutputTooLarge (OutputTooLargeError.PerPath); a path the caller
// drops is never held to it.
const MaxListingPathBytes = 4096

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
	op    string
	keep  func(string) bool
	limit int64 // kept-bytes cap
	kept  int64 // bytes kept: every kept path plus its NUL
	paths []string
	err   *GitError
}

func newPathCollector(op string, limit int64, keep func(string) bool) *pathCollector {
	s := &pathCollector{op: op, keep: keep, limit: limit}
	s.nulRecords = nulRecords{pending: s.pendingPath, record: s.path}
	return s
}

// pendingPath bounds a path git has not finished writing. Every path is
// kept when there is no predicate, so one longer than
// MaxListingPathBytes is already a refusal. With a predicate the path may
// be dropped once complete, so it is held up to the room the listing has
// left (never less than one maximal path): a path the caller drops costs
// no more memory than one it keeps.
func (s *pathCollector) pendingPath(n int64) error {
	bound := int64(MaxListingPathBytes)
	if s.keep != nil {
		bound = max(bound, s.limit-s.kept)
	}
	if n > bound {
		s.err = listingTooLarge(s.op, s.limit, s.keep == nil)
		return errStopListing
	}
	return nil
}

func (s *pathCollector) path(rec []byte) error {
	if len(rec) == 0 {
		s.err = &GitError{Kind: ErrKindPermanent, Op: s.op, Message: "empty path in `git ls-tree -z` output"}
		return errStopListing
	}
	p := string(rec)
	if s.keep != nil && !s.keep(p) {
		return nil
	}
	if len(p) > MaxListingPathBytes {
		s.err = listingTooLarge(s.op, MaxListingPathBytes, true)
		return errStopListing
	}
	s.kept += int64(len(p)) + 1
	if s.kept > s.limit {
		s.err = listingTooLarge(s.op, s.limit, false)
		return errStopListing
	}
	s.paths = append(s.paths, p)
	return nil
}

func (s *pathCollector) refusal() *GitError { return s.err }

// listingTooLarge is the size refusal of a streamed listing: the
// envelope runSink has always returned for an overrun ("output exceeds
// read limit", matching ErrOutputTooLarge). perPath marks a single kept
// path over MaxListingPathBytes (Limit is then that bound).
func listingTooLarge(op string, limit int64, perPath bool) *GitError {
	if perPath {
		limit = MaxListingPathBytes
	}
	return &GitError{Kind: ErrKindPermanent, Op: op, Message: "output exceeds read limit",
		Err: &OutputTooLargeError{Op: op, Limit: limit, PerPath: perPath}}
}

// --- Diff ---

// nameStatusCollector parses `git diff --name-status -z` as a stream:
// records of "<status>\0<path>\0", or "<status>\0<src>\0<dst>\0" for a
// status starting with R (rename) or C (copy); see parseNameStatusZ for
// the mapping. Every record is kept, so every path is held to
// MaxListingPathBytes and the kept bytes to the listing limit.
//
// A malformed stream is an error and returns no records: one mis-split
// would shift every later record onto the wrong path. The checks are the
// ones the buffered parser made, in its order within a record: a record
// cut short is "missing a path" (finish), and an empty path is reported
// once the record is complete.
type nameStatusCollector struct {
	nulRecords
	op     string
	limit  int64
	kept   int64
	diffs  []FileDiff
	fields []string // the fields of the record being read: status, then paths
	err    *GitError
	parse  error // the malformed-stream cause, wrapped in err
}

func newNameStatusCollector(op string, limit int64) *nameStatusCollector {
	s := &nameStatusCollector{op: op, limit: limit, fields: make([]string, 0, 3)}
	s.nulRecords = nulRecords{pending: s.pendingField, record: s.field}
	return s
}

func (s *nameStatusCollector) pendingField(n int64) error {
	if n > MaxListingPathBytes {
		s.err = listingTooLarge(s.op, MaxListingPathBytes, true)
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
	if len(s.fields) == 0 {
		status := string(rec)
		if !validDiffStatus(status) {
			return s.malformed("unexpected status field at record %d", len(s.diffs)+1)
		}
		s.fields = append(s.fields, status)
		return nil
	}
	if len(rec) > MaxListingPathBytes {
		s.err = listingTooLarge(s.op, MaxListingPathBytes, true)
		return errStopListing
	}
	s.kept += int64(len(rec)) + 1
	if s.kept > s.limit {
		s.err = listingTooLarge(s.op, s.limit, false)
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
type pathCounter struct {
	op      string
	n       int
	partial bool // bytes of a record seen since the last NUL
	stop    context.CancelFunc
	err     *GitError
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
		s.n++
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
//  4. output that ended inside a record (the refusal unterminated
//     builds).
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
	if out.unterminated() {
		return unterminated()
	}
	return nil
}
