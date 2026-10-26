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
//   - Spine's heap. ReadFile probes the blob's size (`git ls-tree -l`;
//     `git cat-file -s` before the wave 4 follow-up) before reading and
//     refuses anything over the per-blob limit; the read itself goes
//     into one buffer pre-sized to the probed size and capped at the
//     limit. Every other command's stdout is capped at the per-command
//     limit. Stderr keeps its head and tail (64 KiB in all).
//   - git's own heap. The git child runs in the same cgroup. The size
//     probe sees only the target object, so a small blob stored as a
//     delta on a huge base passes the probe and then makes git allocate
//     the large object. (A gitlink named *.md that `git show` rendered
//     as a commit with its patch was the other bypass, and one that
//     GIT_ALLOC_LIMIT did not stop — a diff queue is millions of small
//     allocations. Since the wave 4 follow-up ReadFile reads blobs only,
//     with `git cat-file blob` after `git cat-file -t` has proved the
//     object is one, and refuses a gitlink unread.) Read
//     commands therefore run with GIT_ALLOC_LIMIT, which git checks on
//     every heap allocation
//     (wrapper.c memory_limit_check): it refuses instead of allocating,
//     and the refusal is mapped to the same typed errors as a stdout
//     overflow, before git's own text ("packed object … is corrupt")
//     can be classified as corruption. GIT_ALLOC_LIMIT bounds ONE
//     allocation, not git's total: an object at the end of a delta
//     chain whose bases are each under the limit is rebuilt base by
//     base, and git keeps the intermediate bases in its delta-base
//     cache (core.deltaBaseCacheLimit, 96 MiB by default) — a 3 MB tip
//     on a chain of eight 15 MiB bases took the git child to 127 MiB
//     with no allocation near the limit (review round 1). Read and diff
//     commands therefore also pin that cache to the per-blob limit
//     (deltaBaseCacheEnv). What remains is the chain step itself: the
//     base being patched, the result and the cache, about three times
//     the alloc limit per concurrent read (55 MB measured for that
//     chain at the default 16 MiB; KNOWN-LIMITATIONS.md, "Git read
//     bounds").
//   - Mutating commands (checkout, merge, commit, add, branch, config,
//     push) are never failed on output size: git performs the side
//     effect before it prints, so a failing ceiling would report a merge
//     that had already moved main as failed. Their stdout is drained —
//     the head is kept, the rest discarded — and they get no alloc limit
//     (checkout and merge legitimately materialise large files). What
//     they WRITE is bounded instead by the checkout budget and the
//     mutate deadline when the client carries them (INIT-024 TASK-003,
//     treebudget.go). `diff`
//     gets the failing ceiling but no alloc limit: rename detection loads
//     both blobs of a candidate pair, and a legitimate large rename on
//     main must not wedge projection sync (KNOWN-LIMITATIONS.md, "Git
//     read bounds").

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"regexp"
	"strconv"
	"strings"
	"time"

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

	// minSinkGrowth is the smallest buffer a growing stdout sink
	// allocates. Growth goes in powers of two from here (never past the
	// limit), so what a command's output costs is a function of its size
	// alone, not of how the pipe happened to chunk it.
	minSinkGrowth = 512
)

// Checkout budget defaults (INIT-024 TASK-003, review finding N-1; owner
// question 1, defaults pending the owner). The read bounds above limit
// what a git command PRINTS; these limit what a checkout or merge WRITES
// (treebudget.go). They apply only to a client that carries a budget
// (WithTreeBudget, or SetDefaultCheckoutLimits, which `spine serve`
// calls): an unset field of an enabled TreeBudget means its default.
// Measured with the budget's own count (`ls-tree -r -t`, directories
// included): Spine's HEAD at this task is 1,961 entries / 21,420,845
// bytes, SMP 3addd44b 4,393 entries / 33 MB (`git ls-tree -r -l` alone,
// files only, gives Spine 1,624 at a560c210 and SMP 3,708), so the
// defaults are ~45x the entries and ~60x the bytes of the largest real
// tree.
const (
	// DefaultTreeMaxEntries caps the entries of a tree — files,
	// symbolic links, gitlinks and directories — each of which costs an
	// inode (or a directory) when written.
	DefaultTreeMaxEntries int64 = 200_000
	// DefaultTreeMaxTotalBytes caps the summed sizes of a tree's blobs,
	// after the checkout-time expansion its .gitattributes can apply
	// (treebudget_attrs.go).
	DefaultTreeMaxTotalBytes int64 = 2 << 30
	// DefaultTreeMaxPathBytesTotal caps the summed full-path bytes of a
	// tree's entries. It also bounds the depth of a tree (a path of
	// depth d costs O(d^2) summed). It does NOT keep an admitted tree's
	// listing within DefaultMaxOutputBytes (review round 1): a
	// `ls-tree -r --name-only -z` adds one NUL per entry to the path
	// bytes, so a tree near both defaults (16 MiB of paths, 200,000
	// entries) lists to ~16.2 MiB and ListFiles refuses it (N-6, INIT-024
	// TASK-004, whose listing cap must be at least this path budget plus
	// one byte per entry and a margin, or be derived from the budget).
	// The default stays 16 MiB pending owner question 1.
	DefaultTreeMaxPathBytesTotal int64 = 16 << 20
	// DefaultMutateTimeout is the wall-clock deadline `spine serve`
	// gives every local mutating git command (owner question 2: 2 min).
	// A client with no deadline (the library default) runs them under
	// the caller's context alone, as before.
	DefaultMutateTimeout = 2 * time.Minute
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
// errors.Is: the blob exceeds the per-blob read limit, or git refused an
// allocation over its alloc limit while producing it. A retry cannot
// shrink the object.
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
	// Size is the size the probe reported (`git ls-tree -l`, which reads
	// only the object header), or -1 when the read itself overran Limit
	// (the probe under-reported, or the entry probe's own output did) or
	// git refused an allocation over its alloc limit.
	Size  int64
	Limit int64
	// GitAllocLimit is non-zero when git itself refused an allocation
	// over this GIT_ALLOC_LIMIT while producing the content (Size is then
	// -1). The content may well be under Limit: what git could not
	// allocate is something larger it needed to build it — the base of a
	// small blob stored as a delta on a large one, or a tree object on
	// the path. (Before the wave 4 follow-up it could also be the patch
	// of a gitlink rendered as a commit; ReadFile now refuses a gitlink
	// as ErrNotBlob without reading it, but Error() keeps its wording.)
	// Error() says so, so a 3 MB file is never reported as over a 4 MiB
	// limit (review round 1).
	GitAllocLimit int64
}

func (e *BlobTooLargeError) Error() string {
	switch {
	case e.GitAllocLimit > 0:
		return fmt.Sprintf("%q at %q cannot be read within the read limits: git needed an allocation over "+
			"the %d-byte git allocation limit to produce it (a delta on a large base, or a gitlink rendered "+
			"with its patch)", e.Path, e.Ref, e.GitAllocLimit)
	case e.Size < 0:
		return fmt.Sprintf("%q at %q exceeds the %d-byte read limit", e.Path, e.Ref, e.Limit)
	}
	return fmt.Sprintf("%q at %q is %d bytes, over the %d-byte read limit", e.Path, e.Ref, e.Size, e.Limit)
}

// Is makes errors.Is(err, ErrBlobTooLarge) hold.
func (e *BlobTooLargeError) Is(target error) bool { return target == ErrBlobTooLarge }

// ErrNotBlob is what ReadFile's other content refusal matches with
// errors.Is (INIT-023 wave 4 follow-up): the commit's tree records a
// directory (a tree) or a gitlink (mode 160000, a commit) at the path, not
// a file — or a file entry whose recorded object is not a blob (a tag, a
// tree or a commit; NotBlobError.FileEntry). ReadFile reads blobs only:
// `git show <rev>:<path>` used to render a directory as a listing and a
// gitlink whose commit is in the repository as that commit with its whole
// patch — content no file holds, and a diff queue no GIT_ALLOC_LIMIT
// bounds (272 MiB in the git child, measured by SMP at ae513dca). Like a
// size refusal it is a property of the commit: no retry can make the path
// a file.
var ErrNotBlob = errors.New("git: the path names no file")

// ObjectTag is the type `git cat-file -t` prints for an annotated tag
// object. A tree entry never has it as its own type (ls-tree types an
// entry from its mode); it is the type a NotBlobError with FileEntry
// reports when a file entry records a tag's id.
const ObjectTag = "tag"

// NotBlobMessage is the GitError.Message of every ErrNotBlob refusal.
const NotBlobMessage = "object is not a file"

// NotBlobError is the typed cause of ReadFile's not-a-file refusal. It is
// returned inside the envelope
// *GitError{Kind: ErrKindPermanent, Op: "read_file", Message: NotBlobMessage},
// the Kind a dangling gitlink always had, so IsRetryable is false and a
// caller that keys on ErrKindNotFound never takes it for "absent" (a
// directory or a gitlink at a governed path is not a missing file).
//
// Type is the type of the object the path names. For a directory or a
// gitlink it is the tree entry's type as `git ls-tree` prints it
// (TreeEntryTree or TreeEntryCommit, from the entry's mode — the object is
// never read). For a file entry (FileEntry) it is the recorded object's own
// type as `git cat-file -t` prints it: ObjectTag, TreeEntryTree or
// TreeEntryCommit. Ref and Path are caller- and branch-controlled text,
// kept for callers that sanitise them; Error never prints them.
type NotBlobError struct {
	Ref, Path, Type string
	// FileEntry is true when the tree entry at the path is a file — a blob
	// mode: 100644, 100755 or the symlink mode 120000, all of which ls-tree
	// prints as "blob" — that records the id of an object that is not a
	// blob (adversarial review of the wave 4 follow-up). Nothing makes the
	// recorded id match the mode: `update-index --cacheinfo`, mktree or any
	// tree writer records whatever id it is given, and a push with the
	// default receive config checks only that the object exists. git diff
	// shows such an entry as the object's own text; `git cat-file blob`
	// would peel a tag to its target blob — content no diff shows — so
	// ReadFile refuses it unread instead.
	FileEntry bool
}

func (e *NotBlobError) Error() string {
	if e.FileEntry {
		switch e.Type {
		case ObjectTag, TreeEntryTree, TreeEntryCommit:
			return "the path's file entry records a " + e.Type + " object, not file content"
		}
		return "the path's file entry records an object that is not file content"
	}
	switch e.Type {
	case TreeEntryTree:
		return "the path names a directory (tree), not a file"
	case TreeEntryCommit:
		return "the path names a gitlink (commit), not a file"
	}
	return "the path does not name a file"
}

// Is makes errors.Is(err, ErrNotBlob) hold.
func (e *NotBlobError) Is(target error) bool { return target == ErrNotBlob }

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

// deltaBaseCacheEnv is the GIT_CONFIG_PARAMETERS entry read and diff
// commands run with: whatever the parent process passes (inherited),
// then core.deltaBaseCacheLimit = the per-blob limit (review round 1,
// P2). GIT_ALLOC_LIMIT checks one allocation at a time, and rebuilding
// an object at the end of a delta chain keeps each intermediate base in
// git's delta-base cache — 96 MiB by default — so a chain of bases each
// under the alloc limit still grew the git child far past it (127 MiB
// for a 3 MB tip on eight 15 MiB bases; 55 MB with the cache pinned).
// The cache only saves re-inflating a shared base, so a small one costs
// nothing on these one-object reads and listings: trees are small, and
// the per-blob limit holds every tree of a large repository.
//
// It is the environment form of `git -c`, so the argv every caller and
// test pins is unchanged. The value uses the original 'key=value'
// quoting, which git has read since 1.7 and still reads alongside the
// 'key'='value' form 2.31 introduced; an inherited value of either form
// is kept, and ours comes last, so it wins over it and over every
// repository, global or system config file (command-line config has the
// highest precedence). Key and value are fixed text and digits: nothing
// here needs quoting.
func deltaBaseCacheEnv(inherited string, limit int64) string {
	pin := "'core.deltaBaseCacheLimit=" + strconv.FormatInt(limit, 10) + "'"
	if strings.TrimSpace(inherited) == "" {
		return "GIT_CONFIG_PARAMETERS=" + pin
	}
	return "GIT_CONFIG_PARAMETERS=" + inherited + " " + pin
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
//
// A sink keeps either bytes (ReadFile's content read, returned as
// []byte) or text (every other command, which run() returns as a
// string). Text goes into a strings.Builder, whose String() hands its
// buffer over without a copy, and the parsers' paths and messages are
// substrings of it: converting a []byte buffer instead held the output
// twice, so a 16 MiB listing peaked at 32 MiB (review round 1).
type stdoutSink struct {
	buf      []byte           // bytes mode
	text     *strings.Builder // text mode (nil in bytes mode)
	limit    int64
	failing  bool
	overflow bool
}

func newStdoutSink(limit, sizeHint int64, failing, text bool) *stdoutSink {
	s := &stdoutSink{limit: limit, failing: failing}
	if text {
		s.text = new(strings.Builder)
	}
	if sizeHint > 0 {
		n := int(min(sizeHint, limit, int64(math.MaxInt)))
		if s.text != nil {
			s.text.Grow(n) // exact on an empty builder
		} else {
			s.buf = make([]byte, 0, n)
		}
	}
	return s
}

func (s *stdoutSink) size() int {
	if s.text != nil {
		return s.text.Len()
	}
	return len(s.buf)
}

func (s *stdoutSink) capacity() int {
	if s.text != nil {
		return s.text.Cap()
	}
	return cap(s.buf)
}

// Bytes returns what a bytes-mode sink kept.
func (s *stdoutSink) Bytes() []byte { return s.buf }

// String returns what a text-mode sink kept, without copying it. The
// sink must not be written to afterwards.
func (s *stdoutSink) String() string {
	if s.text == nil {
		return string(s.buf)
	}
	return s.text.String()
}

func (s *stdoutSink) Write(p []byte) (int, error) {
	if s.overflow {
		if s.failing {
			return 0, errStdoutLimit
		}
		return len(p), nil
	}
	room := s.limit - int64(s.size())
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

// appendBounded appends p, growing the buffer to the next power of two
// at least twice its capacity (from minSinkGrowth, never past limit)
// rather than through append's ~1.25x growth for large slices, whose
// intermediate copies would add up to several times the final size.
// What growth allocates in all is under twice the final buffer.
func (s *stdoutSink) appendBounded(p []byte) {
	have := s.size()
	if need := have + len(p); need > s.capacity() {
		newCap := nextPowerOfTwo(max(2*s.capacity(), need, minSinkGrowth))
		if int64(newCap) > s.limit || newCap <= 0 {
			newCap = int(min(s.limit, int64(math.MaxInt)))
		}
		if s.text != nil {
			// Grow on a non-empty Builder allocates 2*cap+n, past the
			// limit; on an empty one it allocates exactly n. So the text
			// moves into a fresh builder of exactly newCap.
			grown := new(strings.Builder)
			grown.Grow(newCap)
			grown.WriteString(s.text.String())
			s.text = grown
		} else {
			grown := make([]byte, have, newCap)
			copy(grown, s.buf)
			s.buf = grown
		}
	}
	if s.text != nil {
		s.text.Write(p)
		return
	}
	s.buf = append(s.buf, p...)
}

// nextPowerOfTwo returns the smallest power of two >= n (n > 0), or 0
// when that does not fit in an int.
func nextPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	shift := bits.Len(uint(n - 1))
	if shift >= bits.UintSize-1 {
		return 0
	}
	return 1 << shift
}

// stderrSink keeps the head and the tail of a command's stderr, at most
// maxStderrBytes in all, and never fails a Write. classifyGitError
// searches the whole kept text, and the deciding line ("fatal: Unable to
// create '…/index.lock'") is usually the last one, so a head-only buffer
// could misclassify a transient failure as permanent. The tail is a
// fixed ring, allocated only once the head is full, so arbitrarily long
// stderr (a remote's sideband during a clone, say) costs O(1) memory.
type stderrSink struct {
	head  []byte
	ring  []byte // len stderrTailBytes once allocated
	pos   int    // next write offset in ring
	full  bool   // ring has wrapped at least once
	tailN int64  // bytes written after the head
}

func (s *stderrSink) Write(p []byte) (int, error) {
	n := len(p)
	if room := stderrHeadBytes - len(s.head); room > 0 {
		k := min(room, len(p))
		s.head = append(s.head, p[:k]...)
		p = p[k:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if s.ring == nil {
		s.ring = make([]byte, stderrTailBytes)
	}
	s.tailN += int64(len(p))
	if len(p) >= len(s.ring) {
		copy(s.ring, p[len(p)-len(s.ring):])
		s.pos, s.full = 0, true
		return n, nil
	}
	k := copy(s.ring[s.pos:], p)
	if k < len(p) {
		copy(s.ring, p[k:])
		s.pos, s.full = len(p)-k, true
		return n, nil
	}
	s.pos += k
	if s.pos == len(s.ring) {
		s.pos, s.full = 0, true
	}
	return n, nil
}

// String returns the kept text: the head, then — with a marker when
// bytes were dropped between them — the last stderrTailBytes.
func (s *stderrSink) String() string {
	var b bytes.Buffer
	b.Grow(len(s.head) + len(stderrMarker) + len(s.ring))
	b.Write(s.head)
	if s.tailN > int64(len(s.ring)) {
		b.WriteString(stderrMarker)
	}
	if s.full {
		b.Write(s.ring[s.pos:])
	}
	b.Write(s.ring[:s.pos])
	return b.String()
}
