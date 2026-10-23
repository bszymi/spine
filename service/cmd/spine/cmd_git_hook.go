package main

import (
	"context"

	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/spf13/cobra"
)

// gitHookCmd is the hidden `spine git-hook` command group: hooks the git
// HTTP endpoint installs into the repositories it serves, which git runs
// as this same binary (INIT-024 TASK-003). Not for operators to call.
func gitHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "git-hook",
		Short:  "Git hooks the Spine git HTTP endpoint installs (internal)",
		Hidden: true,
	}
	cmd.AddCommand(gitHookPreReceiveCmd())
	return cmd
}

// gitHookPreReceiveCmd is `spine git-hook pre-receive`: the
// checkout-budget pre-receive hook (githttp.PreReceiveHookMain). It reads
// git's "<old> <new> <ref>" lines on stdin, measures the tree of each new
// tip against the budget its flags name and exits non-zero — refusing the
// push — on any tip over the budget or not measurable. Its flags are
// parsed by PreReceiveHookMain (the root's persistent flags do not apply)
// and its only output is the refusal on stderr, which git relays to the
// pusher as "remote:" lines.
func gitHookPreReceiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "pre-receive",
		Short:              "Refuse a push whose tree exceeds the checkout budget (internal)",
		Hidden:             true,
		DisableFlagParsing: true,
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return githttp.PreReceiveHookMain(ctx, args, c.InOrStdin(), c.ErrOrStderr())
		},
	}
}
