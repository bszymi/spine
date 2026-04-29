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
