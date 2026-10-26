package git

import (
	"context"
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

// ParseNameStatusZForTest exposes Diff's -z parser.
func ParseNameStatusZForTest(out string) ([]FileDiff, error) {
	return parseNameStatusZ(out)
}

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

// AttributeExpansionForTest is the worst-case checkout expansion factor
// of a .gitattributes text, for object ids of hexLen hex digits.
func AttributeExpansionForTest(text string, hexLen int) float64 {
	set := newAttrSet()
	set.add([]byte(text))
	classes, _ := set.evaluate()
	return classes.factor(hexLen)
}

// AttributeFiltersForTest returns the filter names a .gitattributes text
// sets.
func AttributeFiltersForTest(text string) []string {
	set := newAttrSet()
	set.add([]byte(text))
	_, filters := set.evaluate()
	return filters
}

// WorstAttributeExpansionForTest is the worst-case factor.
func WorstAttributeExpansionForTest(hexLen int) float64 { return attrAll.factor(hexLen) }

// ReadTreeForTest runs ResetBranch's two-tree switch (`read-tree -m -u
// from to`) through runWith, under the client's mutate deadline.
func (c *CLIClient) ReadTreeForTest(ctx context.Context, from, to string) error {
	_, _, err := c.runWith(ctx, "reset_branch", runOpts{}, "read-tree", "-m", "-u", "--end-of-options", from, to)
	return err
}

// CheckBudgetGitVersionOutputForTest exposes the budget's git floor parser.
func CheckBudgetGitVersionOutputForTest(line string) error { return checkBudgetGitVersionOutput(line) }
