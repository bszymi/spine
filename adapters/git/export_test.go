package git

import "context"

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
