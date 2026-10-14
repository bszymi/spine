package git

// Read bounds for the git CLI client (INIT-023 TASK-003; audit F4).
//
// Before this file, CLIClient.run collected every command's stdout and
// stderr in uncapped bytes.Buffers, and ReadFile copied the result twice
// more: an 8 MiB blob cost 49 MiB of Go heap, and a compressible blob of
// a few hundred MiB (a pack of well under a MiB) took the whole
// customer-app process down — every workspace on the replica, on every
// projection sync that re-read it. Three bounds now apply:
//
//   - Spine's heap. ReadFile probes the blob's size (`git cat-file -s`)
//     before reading and refuses anything over the per-blob limit; the
//     read itself goes into one buffer pre-sized to the probed size and
//     capped at the limit. Every other command's stdout is capped at the
//     per-command limit. Stderr keeps its head and tail (64 KiB in all).
//   - git's own heap. The git child runs in the same cgroup. The size
//     probe sees only the target object, so a small blob stored as a
//     delta on a huge base, or a gitlink named *.md that `git show`
//     renders as a commit with its patch, passes the probe and then
//     makes git allocate the large object. Read commands therefore run
//     with GIT_ALLOC_LIMIT, which git checks on every heap allocation
//     (wrapper.c memory_limit_check): it refuses instead of allocating,
//     and the refusal is mapped to the same typed errors as a stdout
//     overflow, before git's own text ("packed object … is corrupt")
//     can be classified as corruption.
//   - Mutating commands (checkout, merge, commit, add, branch, config,
//     push) are never failed on output size: git performs the side
//     effect before it prints, so a failing ceiling would report a merge
//     that had already moved main as failed. Their stdout is drained —
//     the head is kept, the rest discarded — and they get no alloc limit
//     (checkout and merge legitimately materialise large files). `diff`
//     gets the failing ceiling but no alloc limit: rename detection loads
//     both blobs of a candidate pair, and a legitimate large rename on
//     main must not wedge projection sync (KNOWN-LIMITATIONS.md, "Git
//     read bounds").

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"

	"github.com/bszymi/spine/core/worktreefs"
)

const (
	// DefaultMaxBlobBytes caps one ReadFile (owner question 1, default
	// pending the owner: 4 MiB). It is defined as worktreefs'
	// DefaultMaxReadBytes so an oversized artifact is refused at the same
	// size whether it is read through `git show` or from a worktree
	// (INIT-023 TASK-004 I7). The largest tracked .md is about 158 KiB in
	// SMP and 34 KiB in Spine; Spine's API refuses writes over 512 KiB.
	DefaultMaxBlobBytes int64 = worktreefs.DefaultMaxReadBytes

	// DefaultMaxOutputBytes caps the stdout of every other command
	// (owner question 1: 16 MiB, about 70x SMP's own `ls-tree -r`).
	DefaultMaxOutputBytes int64 = 16 << 20

	// gitAllocFactor derives the default GIT_ALLOC_LIMIT from the
	// per-blob limit (owner question 4: 4x, 16 MiB with the defaults). At
	// 4x a legitimate artifact stored as a delta on a base up to 16 MiB
	// is still readable.
	gitAllocFactor = 4

	// minGitAllocBytes floors GIT_ALLOC_LIMIT whatever the options say.
	// git makes fixed internal allocations unrelated to repository
	// content — 524 256 bytes in ls-tree, log, merge-base and diff,
	// measured on git 2.39.5 and 2.54 even in an empty repository — so a
	// limit derived from a small per-blob limit (4 x 64 KiB) would make
	// every listing fail. 1 MiB is twice the largest fixed allocation
	// seen; it never applies with the default limits (16 MiB).
	minGitAllocBytes int64 = 1 << 20

	// maxStderrBytes bounds the stderr kept for classification: the first
	// stderrHeadBytes and the last bytes up to the total, with a marker
	// between them when anything was dropped. The deciding `fatal:` line
	// is usually the last one.
	maxStderrBytes  = 64 << 10
	stderrHeadBytes = 32 << 10
	stderrMarker    = "\n…[truncated]…\n"
	stderrTailBytes = maxStderrBytes - stderrHeadBytes - len(stderrMarker)

	// maxProbeBytes bounds `git cat-file -s` output: one decimal size.
	maxProbeBytes = 64

	// minOutputBytes floors the per-command stdout limit, whatever
	// WithMaxOutputBytes says (codex review round 3). Below it, ordinary
	// output stops fitting: Merge reads its result ("Updating <a>..<b>",
	// "Fast-forward") from the head of `git merge`'s drained output, and
	// the `rev-parse HEAD` after every commit and merge prints one object
	// id. 4 KiB holds both many times over and is still a bound.
	minOutputBytes int64 = 4 << 10
)

// WithMaxBlobBytes sets the per-blob ReadFile limit. n <= 0 keeps the
// default (DefaultMaxBlobBytes).
func WithMaxBlobBytes(n int64) CLIOption {
	return func(c *CLIClient) {
		if n > 0 {
			c.maxBlobBytes = n
		}
	}
}

// WithMaxOutputBytes sets the stdout limit of every command other than
// ReadFile's content read. n <= 0 keeps the default
// (DefaultMaxOutputBytes); a value below 4 KiB is raised to 4 KiB. Read
// commands fail when they exceed it; mutating commands keep the head and
// discard the rest.
func WithMaxOutputBytes(n int64) CLIOption {
	return func(c *CLIClient) {
		if n > 0 {
			c.maxOutputBytes = n
		}
	}
}

// WithMaxGitAllocBytes sets GIT_ALLOC_LIMIT for read commands. n <= 0
// keeps the derived default, 4 x the per-blob limit (computed after every
// option has applied). A value below the per-blob limit + 1 is raised to
// it (git's in-core read of an at-limit blob allocates size+1), and any
// value below 1 MiB is raised to 1 MiB (git's own fixed allocations).
func WithMaxGitAllocBytes(n int64) CLIOption {
	return func(c *CLIClient) {
		if n > 0 {
			c.maxGitAllocBytes = n
		}
	}
}

// ErrBlobTooLarge is what every ReadFile size refusal matches with
// errors.Is: the blob (or what `git show` renders for the path) exceeds
// the per-blob read limit, or git refused an allocation over its alloc
// limit while producing it. A retry cannot shrink the object.
var ErrBlobTooLarge = errors.New("git: blob exceeds the read limit")

// ErrOutputTooLarge is what every other command's size refusal matches
// with errors.Is.
var ErrOutputTooLarge = errors.New("git: command output exceeds the read limit")

// BlobTooLargeError is the typed cause of a ReadFile size refusal. It is
// returned inside the usual envelope,
// *GitError{Kind: ErrKindPermanent, Op: "read_file", Message: "blob exceeds read limit"},
// so callers that type-assert *GitError keep working and IsRetryable is
// false; errors.Is / errors.As reach it through GitError.Unwrap.
type BlobTooLargeError struct {
	Ref, Path string
	// Size is the size `git cat-file -s` reported, or -1 when the read
	// itself overran Limit (the object changed after the probe, `git
	// show` rendered more than the probed object — a tree or a gitlink —
	// or git refused an allocation over its alloc limit).
	Size  int64
	Limit int64
}

func (e *BlobTooLargeError) Error() string {
	if e.Size < 0 {
		return fmt.Sprintf("%q at %q exceeds the %d-byte read limit", e.Path, e.Ref, e.Limit)
	}
	return fmt.Sprintf("%q at %q is %d bytes, over the %d-byte read limit", e.Path, e.Ref, e.Size, e.Limit)
}

// Is makes errors.Is(err, ErrBlobTooLarge) hold.
func (e *BlobTooLargeError) Is(target error) bool { return target == ErrBlobTooLarge }

// OutputTooLargeError is the typed cause of a non-ReadFile size refusal.
type OutputTooLargeError struct {
	Op    string
	Limit int64
	// GitAlloc is true when git refused an allocation over its alloc
	// limit (Limit is then GIT_ALLOC_LIMIT), false when stdout overran
	// Limit.
	GitAlloc bool
}

func (e *OutputTooLargeError) Error() string {
	if e.GitAlloc {
		return fmt.Sprintf("git %s needed an allocation over the %d-byte git allocation limit", e.Op, e.Limit)
	}
	return fmt.Sprintf("git %s output exceeds the %d-byte read limit", e.Op, e.Limit)
}

// Is makes errors.Is(err, ErrOutputTooLarge) hold.
func (e *OutputTooLargeError) Is(target error) bool { return target == ErrOutputTooLarge }

// errStdoutLimit is returned by a failing stdoutSink's Write once the
// limit is crossed; os/exec's copy goroutine then stops and closes the
// pipe, so git dies on EPIPE instead of producing the rest.
var errStdoutLimit = errors.New("git: stdout limit reached")

// gitAllocRefusal matches the line git prints when GIT_ALLOC_LIMIT
// refuses an allocation: die() ("fatal: …") on the plain path, error()
// ("error: …") on the gentle one, after which git may also report the
// object as corrupt — or even crash (verified on git 2.54: a gitlink
// rendered as a commit with a patch exits 139). The match is anchored to
// a whole line and the caller also requires the limit to be the one this
// client set, so a committed path git echoes in an unrelated error (a
// path cannot hold a newline once discovery's UnsafePath check has run,
// but a caller-supplied one can) does not turn a miss into a size
// refusal by accident.
var gitAllocRefusal = regexp.MustCompile(`(?m)^(?:fatal|error): attempting to allocate [0-9]+ over limit ([0-9]+)\r?$`)

// isGitAllocRefusal reports whether stderr holds git's alloc-limit
// refusal for exactly limit.
func isGitAllocRefusal(stderr string, limit int64) bool {
	want := strconv.FormatInt(limit, 10)
	for _, m := range gitAllocRefusal.FindAllStringSubmatch(stderr, -1) {
		if m[1] == want {
			return true
		}
	}
	return false
}

// --- limit accessors. The zero-value CLIClient (not built by
// NewCLIClient) still gets the default bounds: a missing option must
// never mean "unbounded". ---

func (c *CLIClient) blobLimit() int64 {
	if c.maxBlobBytes > 0 {
		return c.maxBlobBytes
	}
	return DefaultMaxBlobBytes
}

func (c *CLIClient) outputLimit() int64 {
	if c.maxOutputBytes > 0 {
		return max(c.maxOutputBytes, minOutputBytes)
	}
	return DefaultMaxOutputBytes
}

func (c *CLIClient) gitAllocLimit() int64 {
	return deriveGitAllocLimit(c.maxGitAllocBytes, c.blobLimit())
}

// deriveGitAllocLimit is the GIT_ALLOC_LIMIT rule: the explicit value,
// or gitAllocFactor x blob when unset; never below blob+1, never below
// minGitAllocBytes.
func deriveGitAllocLimit(explicit, blob int64) int64 {
	n := explicit
	if n <= 0 {
		if blob > math.MaxInt64/gitAllocFactor {
			n = math.MaxInt64
		} else {
			n = gitAllocFactor * blob
		}
	}
	if blob < math.MaxInt64 && n < blob+1 {
		n = blob + 1
	}
	return max(n, minGitAllocBytes)
}

// --- output sinks ---

// stdoutSink captures a command's stdout up to limit. It deliberately
// implements only io.Writer: os/exec copies with io.Copy, and an
// io.ReaderFrom (bytes.Buffer's) grows by MinRead before it sees EOF,
// which would double a buffer pre-sized to the exact blob.
//
// failing (read commands): the Write that would cross limit stores
// nothing more, records overflow and returns errStdoutLimit; os/exec's
// copy goroutine then closes the pipe and git dies on EPIPE.
//
// draining (mutating commands): Write keeps up to limit, drops the rest
// and always reports success, so git never sees EPIPE and never stops
// half way through printing after its side effect.
type stdoutSink struct {
	buf      []byte
	limit    int64
	failing  bool
	overflow bool
}

func newStdoutSink(limit, sizeHint int64, failing bool) *stdoutSink {
	s := &stdoutSink{limit: limit, failing: failing}
	if sizeHint > 0 {
		if sizeHint > limit {
			sizeHint = limit
		}
		s.buf = make([]byte, 0, sizeHint)
	}
	return s
}

func (s *stdoutSink) Write(p []byte) (int, error) {
	if s.overflow {
		if s.failing {
			return 0, errStdoutLimit
		}
		return len(p), nil
	}
	room := s.limit - int64(len(s.buf))
	if int64(len(p)) > room {
		s.overflow = true
		if s.failing {
			return 0, errStdoutLimit
		}
		s.appendBounded(p[:room])
		return len(p), nil
	}
	s.appendBounded(p)
	return len(p), nil
}

// appendBounded appends p, growing the buffer by doubling (never past
// limit) rather than through append's ~1.25x growth for large slices,
// whose intermediate copies would add up to several times the final
// size.
func (s *stdoutSink) appendBounded(p []byte) {
	need := len(s.buf) + len(p)
	if need > cap(s.buf) {
		newCap := 2 * cap(s.buf)
		if newCap < need {
			newCap = need
		}
		if int64(newCap) > s.limit {
			newCap = int(min(s.limit, int64(math.MaxInt)))
		}
		grown := make([]byte, len(s.buf), newCap)
		copy(grown, s.buf)
		s.buf = grown
	}
	s.buf = append(s.buf, p...)
}

// stderrSink keeps the head and the tail of a command's stderr, at most
// maxStderrBytes in all, and never fails a Write. classifyGitError
// searches the whole kept text, and the deciding line ("fatal: Unable to
// create '…/index.lock'") is usually the last one, so a head-only buffer
// could misclassify a transient failure as permanent.
type stderrSink struct {
	head    []byte
	tail    []byte
	dropped bool
}

func (s *stderrSink) Write(p []byte) (int, error) {
	n := len(p)
	if room := stderrHeadBytes - len(s.head); room > 0 {
		k := room
		if k > len(p) {
			k = len(p)
		}
		s.head = append(s.head, p[:k]...)
		p = p[k:]
	}
	if len(p) > 0 {
		s.tail = append(s.tail, p...)
		if len(s.tail) > 2*stderrTailBytes {
			s.dropped = true
			s.tail = append([]byte(nil), s.tail[len(s.tail)-stderrTailBytes:]...)
		}
	}
	return n, nil
}

// String returns the kept text: the head, then — with a marker when
// bytes were dropped between them — the last stderrTailBytes.
func (s *stderrSink) String() string {
	tail := s.tail
	dropped := s.dropped
	if len(tail) > stderrTailBytes {
		dropped = true
		tail = tail[len(tail)-stderrTailBytes:]
	}
	var b bytes.Buffer
	b.Grow(len(s.head) + len(stderrMarker) + len(tail))
	b.Write(s.head)
	if dropped {
		b.WriteString(stderrMarker)
	}
	b.Write(tail)
	return b.String()
}
