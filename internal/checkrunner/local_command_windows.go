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
