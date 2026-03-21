package artifact

import (
	"context"
	"os/exec"
)

// execCmd wraps exec.Cmd for testability.
type execCmd struct {
	*exec.Cmd
}

func newExecCmd(ctx context.Context, name string, args ...string) *execCmd {
	return &execCmd{exec.CommandContext(ctx, name, args...)}
}
