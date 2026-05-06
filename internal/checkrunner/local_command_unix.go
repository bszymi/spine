//go:build !windows

package checkrunner

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child shell into its own process
// group with itself as leader. cancelProcessGroup signals SIGKILL to
// the whole group, both at deadline (via cmd.Cancel) and as an
// end-of-Run sweep (so backgrounded descendants are reaped on the
// success path too). Without this, `sh -c long-cmd` only kills `sh`;
// descendants (a `make test` rebuild loop, a leaked `sleep 999`, a
// dev server) survive in the workspace and keep consuming resources
// after Run returns.
//
// Flagged by codex pass 1 as P1 (timeout) and pass 4 as P2 (success
// path leaks descendants holding pipe FDs).
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
	// Setpgid was set on Start, so the child's PID is also the PGID.
	// Signal the whole group via -pid rather than looking up the PGID
	// after the fact — Getpgid on a leader that has already exited
	// can fail even when descendants still hold the group alive
	// (codex pass 4 P2). As long as any group member is still alive,
	// kill(-pid, SIGKILL) reaches them.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// ESRCH means the group is already empty — no work to do.
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		// Fall back to single-PID kill in case the platform refused
		// the negative-PID form.
		return cmd.Process.Kill()
	}
	return nil
}
