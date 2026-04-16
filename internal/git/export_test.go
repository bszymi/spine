package git

// ClassifyGitErrorForTest exposes classifyGitError for testing.
func ClassifyGitErrorForTest(stderr string) *GitError {
	return classifyGitError("test", stderr)
}

// ResetPushAuthForTest exposes the cache reset to external tests in
// this package so each test case can start from a clean slate.
func ResetPushAuthForTest() {
	resetPushAuthForTest()
}
