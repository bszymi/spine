//go:build !unix

package git_test

import "os/exec"

// exitSignal: no POSIX signals here (newGitCmd falls back to Kill).
func exitSignal(*exec.ExitError) (string, bool) { return "", false }
