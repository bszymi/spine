package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// gitCancelWaitDelay bounds how long a git child may keep running, and
// keep its output pipes open, after its context ended and it was sent
// SIGTERM. git's own handlers remove its lock files and exit at once; the
// delay only matters when a helper git started (a smudge filter, a remote
// helper, ssh) still holds the pipes. After it os/exec kills the process
// and closes the pipes.
const gitCancelWaitDelay = 10 * time.Second

// newGitCmd builds every git child process this package starts (INIT-023
// TASK-012, design-check DC-1; audit F9 reached by A2 alone).
//
// exec.CommandContext's default cancellation is SIGKILL. git installs
// signal handlers (sigchain) that delete the lock files it holds —
// .git/index.lock, refs/heads/<n>.lock, packed-refs.lock — and those
// handlers run on SIGTERM, never on SIGKILL. A request whose context ended
// while git held the index (a client disconnecting during the submit that
// triggers a governed merge, or during an in-place artifact write) left a
// stale .git/index.lock behind: every later operation on the tree then
// failed "repository locked", which classifies as transient, so every
// later governed merge in the workspace kept failing until an operator
// removed the file. Measured for the design: 4 of 8 cancelled governed
// merges left the lock with SIGKILL, 0 of 8 with SIGTERM; a slow smudge
// filter makes the case deterministic.
//
// So the child is sent SIGTERM when ctx ends, and is killed only if it is
// still running gitCancelWaitDelay later. A platform where
// Process.Signal(SIGTERM) is unsupported (Windows) falls back to Kill,
// today's behaviour. This applies to every client, embedders' included: a
// cancelled git now exits through its own handler instead of being
// killed, and no successful result changes (the exit status of a
// cancelled command is still an *exec.ExitError, classified as before).
func newGitCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Cancel = func() error { return terminateGit(cmd.Process) }
	cmd.WaitDelay = gitCancelWaitDelay
	return cmd
}

// terminateGit is newGitCmd's Cancel: SIGTERM, or Kill where the signal
// cannot be delivered. os.ErrProcessDone is returned as is, which os/exec
// treats as "the process had already exited".
func terminateGit(p *os.Process) error {
	err := p.Signal(syscall.SIGTERM)
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return p.Kill()
}
