//go:build !windows

package checkrunner

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child shell into its own process
// group with itself as leader. cancelProcessGroup signals SIGKILL to
// the whole group when the deadline fires. Without this, `sh -c
// long-cmd` only kills `sh`; descendants (a `make test` rebuild loop,
// a leaked `sleep 999`, a dev server) survive in the workspace and
// keep consuming resources after Run reports OutcomeTimeout.
//
// Flagged by codex pass 1 as P1 — the timeout was returning to the
// caller without containing the work the timeout was supposed to bound.
//
// Linux/macOS implementation. Windows lives in
// local_command_windows.go and falls back to plain Process.Kill
// because POSIX process groups have no direct Windows equivalent (and
// the runner already documents that LocalCommandRunner is shell-based:
// the test suite skips on Windows via requireSh).
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func cancelProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Fall back to single-process kill — the child has already
		// exited or the OS lost track of the group. Either way SIGKILL
		// the leader is the best we can do.
		return cmd.Process.Kill()
	}
	// Negative pid signals the whole process group; the leader and
	// every descendant that did not detach via setsid receive SIGKILL.
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
