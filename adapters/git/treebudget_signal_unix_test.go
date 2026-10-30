//go:build unix

package git_test

import (
	"os/exec"
	"syscall"
)

// exitSignal returns the name of the signal that terminated the process
// behind e ("terminated" for SIGTERM), and false when it exited instead.
func exitSignal(e *exec.ExitError) (string, bool) {
	ws, ok := e.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return "", false
	}
	return ws.Signal().String(), true
}
