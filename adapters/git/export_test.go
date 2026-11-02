package git

import (
	"context"
	"math"
	"testing"
)

// ClassifyGitErrorForTest exposes classifyGitError for testing.
func ClassifyGitErrorForTest(stderr string) *GitError {
	return classifyGitError("test", stderr)
}

// ResetPushAuthForTest exposes the cache reset to external tests in
// this package so each test case can start from a clean slate.
func ResetPushAuthForTest() {
	resetPushAuthForTest()
}

// AskpassPathForTest exposes the GIT_ASKPASS helper path the client
// created at construction. Tests use it to verify that the
// per-binding-token-overrides-credential-helper sequence engaged
// correctly: a non-empty value means NewCLIClient created the askpass
// script, which only happens when pushToken is set AND credentialHelper
// is empty at construction time. Internal-only — never expose this in
// production code.
func (c *CLIClient) AskpassPathForTest() string {
	return c.askpassPath
}

// ReadBoundsForTest exposes the client's effective read bounds (INIT-023
// TASK-003): the per-blob limit, the per-command stdout limit and the
// GIT_ALLOC_LIMIT read commands run with.
func (c *CLIClient) ReadBoundsForTest() (blob, output, gitAlloc int64) {
	return c.blobLimit(), c.outputLimit(), c.gitAllocLimit()
}

// SetBlobSizeForTest replaces ReadFile's size probe, so a test can make
// it under-report and exercise the read's own cap.
func (c *CLIClient) SetBlobSizeForTest(probe func(ctx context.Context, spec string) (int64, error)) {
	c.blobSize = probe
}

// StderrKeptForTest feeds p to the stderr sink runBounded uses, in
// chunks of chunk bytes (io.Copy's 32 KiB when chunk <= 0), and returns
// the text it keeps for classification.
func StderrKeptForTest(p []byte, chunk int) string {
	if chunk <= 0 {
		chunk = 32 << 10
	}
	s := &stderrSink{}
	for len(p) > 0 {
		n := min(chunk, len(p))
		_, _ = s.Write(p[:n])
		p = p[n:]
	}
	return s.String()
}

// ParseNameStatusZForTest runs Diff's streamed -z parser
// (nameStatusCollector) over a whole output, written in one piece, with
// no bounds, and ends it as Diff does (refusal, endOfStream, finish). It
// returns the parse error itself (errMalformedDiff-wrapped), as the
// buffered parser it replaced did.
func ParseNameStatusZForTest(out string) ([]FileDiff, error) {
	return ParseNameStatusZChunkedForTest(out, len(out))
}

// unboundedForTest is no limit at all.
var unboundedForTest = listingBounds{bytes: math.MaxInt64, records: math.MaxInt64}

// ParseNameStatusZChunkedForTest is ParseNameStatusZForTest with the
// output written in chunk-sized writes (as os/exec's copy would hand it
// over), so a test can split records at every byte.
func ParseNameStatusZChunkedForTest(out string, chunk int) ([]FileDiff, error) {
	s := newNameStatusCollector("diff", unboundedForTest)
	feedForTest(&s.nulRecords, []byte(out), chunk)
	err := s.refusal()
	if err == nil {
		err = endOfStream(s, unterminatedDiff)
	}
	if err == nil {
		err = s.finish()
	}
	if err != nil {
		return nil, err.Err
	}
	return s.diffs, nil
}

// ListedForTest feeds a `ls-tree -r --name-only -z` output to ListFiles'
// streamed collector in chunk-sized writes, bounded to limit kept bytes
// and records records (<= 0: none), and ends it as runListing does,
// returning what it kept or the refusal ListFiles would return. carry
// reports the most bytes the collector ever carried for one unfinished
// record.
func ListedForTest(out []byte, chunk int, limit, records int64, keep func(string) bool) (paths []string, carry int, err error) {
	b := listingBounds{bytes: limit, records: records}
	if records <= 0 {
		b.records = math.MaxInt64
	}
	s := newPathCollector("list_files", b, keep)
	carry = feedForTest(&s.nulRecords, out, chunk)
	if gerr := s.refusal(); gerr != nil {
		return nil, carry, gerr
	}
	if gerr := endOfStream(s, unterminatedListing); gerr != nil {
		return nil, carry, gerr
	}
	return s.paths, carry, nil
}

// feedForTest writes p to s in chunk-sized writes until s refuses one,
// and returns the most bytes s carried between writes.
func feedForTest(s *nulRecords, p []byte, chunk int) (maxCarry int) {
	if chunk <= 0 {
		chunk = len(p)
	}
	for len(p) > 0 {
		n := min(chunk, len(p))
		if _, err := s.Write(p[:n]); err != nil {
			return maxCarry
		}
		maxCarry = max(maxCarry, len(s.carry))
		p = p[n:]
	}
	return maxCarry
}

// CountedForTest feeds a `git diff --name-only -z` output to
// CountChangedPaths' counter in chunk-sized writes (records: its ceiling,
// <= 0 for none) and ends it as runListing does.
func CountedForTest(out []byte, chunk int, records int64) (int, error) {
	if records <= 0 {
		records = math.MaxInt64
	}
	s := &pathCounter{op: "count_changed_paths", maxRecords: records}
	for p := out; len(p) > 0; {
		n := min(max(chunk, 1), len(p))
		if _, err := s.Write(p[:n]); err != nil {
			break
		}
		p = p[n:]
	}
	if err := s.refusal(); err != nil {
		return 0, err
	}
	if err := endOfStream(s, unterminatedCount); err != nil {
		return 0, err
	}
	return s.n, nil
}

// DiffLimitForTest and RecordCeilingForTest are the client's diff limit
// and tree-listing record ceiling (diffLimit, recordCeiling).
func (c *CLIClient) DiffLimitForTest() int64     { return c.diffLimit() }
func (c *CLIClient) RecordCeilingForTest() int64 { return c.recordCeiling() }

// ListingLimitForTest is the bound the client's streamed listings hold
// the paths they keep to (listingLimit).
func (c *CLIClient) ListingLimitForTest() int64 { return c.listingLimit() }

// SetEnvHookForTest installs a hook called with the op and the
// environment of every streamed listing command (ListFiles, Diff,
// CountChangedPaths).
func (c *CLIClient) SetEnvHookForTest(hook func(op string, env []string)) { c.envHook = hook }

// IsGitAllocRefusalForTest exposes the alloc-limit stderr matcher.
func IsGitAllocRefusalForTest(stderr string, limit int64) bool {
	return isGitAllocRefusal(stderr, limit)
}

// MaxStderrBytesForTest is the bound on the stderr text kept.
const MaxStderrBytesForTest = maxStderrBytes

// OpClassDeclaredForTest reports whether op has an explicit output class.
func OpClassDeclaredForTest(op string) bool {
	_, ok := opClasses[op]
	return ok
}

// OpClassesForTest returns every declared op name with its output class
// ("read", "diff" or "mutate").
func OpClassesForTest() map[string]string {
	names := map[opClass]string{opRead: "read", opDiff: "diff", opMutate: "mutate"}
	out := make(map[string]string, len(opClasses))
	for op, c := range opClasses {
		out[op] = names[c]
	}
	return out
}

// DeltaBaseCacheEnvForTest exposes the GIT_CONFIG_PARAMETERS entry read
// and diff commands run with.
func DeltaBaseCacheEnvForTest(inherited string, limit int64) string {
	return deltaBaseCacheEnv(inherited, limit)
}

// SinkRun is what StdoutSinkForTest observed.
type SinkRun struct {
	Kept           string
	Caps           []int // the buffer's capacity after each Write
	Overflow       bool
	WriteErr       error
	HandOverAllocs float64 // allocations of String() on a text sink
}

var sinkText string

// StdoutSinkForTest feeds p to a stdout sink in chunk-sized writes (as
// os/exec's copy goroutine would) and reports what it kept and how its
// buffer grew.
func StdoutSinkForTest(p []byte, chunk int, limit int64, failing, text bool) SinkRun {
	s := newStdoutSink(limit, 0, failing, text)
	var r SinkRun
	for len(p) > 0 {
		n := min(chunk, len(p))
		if _, err := s.Write(p[:n]); err != nil {
			r.WriteErr = err
			break
		}
		r.Caps = append(r.Caps, s.capacity())
		p = p[n:]
	}
	r.Overflow = s.overflow
	if text {
		r.HandOverAllocs = testing.AllocsPerRun(10, func() { sinkText = s.String() })
		r.Kept = s.String()
	} else {
		r.Kept = string(s.Bytes())
	}
	return r
}

// SetMeasureHookForTest installs the checkout-budget measure hook
// (INIT-024 TASK-003): it is called once per measure with the listing
// bytes git handed the parser and the totals when it stopped.
func (c *CLIClient) SetMeasureHookForTest(hook func(MeasureTrace)) {
	c.measureHook = hook
}

// ResetDefaultCheckoutLimitsForTest clears the process default
// (SetDefaultCheckoutLimits).
func ResetDefaultCheckoutLimitsForTest() {
	processCheckoutLimits.Store(nil)
}

// ReadTreeForTest runs ResetBranch's two-tree switch (`read-tree -m -u
// from to`) through runWith, under the client's mutate deadline.
func (c *CLIClient) ReadTreeForTest(ctx context.Context, from, to string) error {
	_, _, err := c.runWith(ctx, "reset_branch", runOpts{}, "read-tree", "-m", "-u", "--end-of-options", from, to)
	return err
}

// CheckBudgetGitVersionOutputForTest exposes the budget's git floor parser.
func CheckBudgetGitVersionOutputForTest(line string) error { return checkBudgetGitVersionOutput(line) }

// SetMergeResultHookForTest installs a hook called with every merge
// result tree a budgeted merge measures.
func (c *CLIClient) SetMergeResultHookForTest(hook func(tree string)) {
	c.mergeResultHook = hook
}

// InflightHeldForTest returns the summed weight the process's in-flight
// checkout gate has admitted (INIT-024 TASK-011).
func InflightHeldForTest() int64 {
	checkoutsInFlight.mu.Lock()
	defer checkoutsInFlight.mu.Unlock()
	return checkoutsInFlight.held
}

// InflightWaitersForTest returns how many admissions wait in the gate,
// over every repository.
func InflightWaitersForTest() int {
	checkoutsInFlight.mu.Lock()
	defer checkoutsInFlight.mu.Unlock()
	n := 0
	for _, s := range checkoutsInFlight.repos {
		n += s.waiters.Len()
	}
	return n
}

// InflightReposForTest returns how many repositories the gate tracks (it
// forgets one that holds and waits for nothing).
func InflightReposForTest() int {
	checkoutsInFlight.mu.Lock()
	defer checkoutsInFlight.mu.Unlock()
	return len(checkoutsInFlight.repos)
}

// AdmitCheckoutForTest weighs weight for the repository key repo in the
// process gate under ceiling, as EnterBranch does for a measured tree
// (op "worktree").
func AdmitCheckoutForTest(ctx context.Context, repo string, weight, ceiling int64) (func(), error) {
	return admitCheckout(ctx, "worktree", repo, weight, ceiling)
}

// RepoClaimKeyForTest exposes the key EnterBranch claims a branch under
// and weighs it for: the identity of the repository's git common
// directory.
func RepoClaimKeyForTest(repoDir string) string { return repoClaimKey(repoDir) }

// PathIdentityForTest is the key repoClaimKey gives the directory at path
// itself: its (device, inode) where the platform reports them, else its
// symlink-free path.
func PathIdentityForTest(path string) string {
	if id, ok := fileIdentity(path); ok {
		return id
	}
	return canonicalTreeKey(path)
}

// SetScopeReleaseHookForTest makes f run each time a branch scope is
// about to return its in-flight weight and its claim (the repository
// directory, the scope's private parent and worktree; "" when not created
// yet). The returned func removes it.
func SetScopeReleaseHookForTest(f func(repoDir, parent, worktreeDir string)) func() {
	scopeReleaseHook.Store(&f)
	return func() { scopeReleaseHook.Store(nil) }
}
