//go:build windows

package checkrunner

import "os/exec"

// configureProcessGroup is a no-op on Windows. POSIX process groups do
// not exist; cancelProcessGroup falls back to Process.Kill which kills
// only the spawned shell. LocalCommandRunner's shell defaults to
// {"sh", "-c"} which is unavailable on stock Windows; the test suite
// skips this runner on Windows for that reason.
func configureProcessGroup(cmd *exec.Cmd) {}

func cancelProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// leaderClearlyKilled — on Windows, ProcessState gives no clean
// signal that distinguishes "we Killed the process" from "process
// exited with code 1". Trust runnerKilledLeader as the hint and
// accept the resulting false positive for the rare case where the
// leader exited cleanly first and Cancel ran during drain — better
// than codex pass 15 P2's regression of timeouts being reported as
// Fail.
func leaderClearlyKilled(cmd *exec.Cmd, runnerKilledLeader bool) bool {
	return runnerKilledLeader
}
