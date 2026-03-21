package git

// ClassifyGitErrorForTest exposes classifyGitError for testing.
func ClassifyGitErrorForTest(stderr string) *GitError {
	return classifyGitError("test", stderr)
}
