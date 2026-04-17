package main

import (
	"github.com/bszymi/spine/internal/cli"
	"github.com/spf13/cobra"
)

func initRepoCmd() *cobra.Command {
	var artifactsDir string
	var noBranch bool

	cmd := &cobra.Command{
		Use:   "init-repo [path]",
		Short: "Initialize a new Spine repository with directory structure and seed documents",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}
			return cli.InitRepo(path, cli.InitOpts{
				ArtifactsDir: artifactsDir,
				NoBranch:     noBranch,
			})
		},
	}
	cmd.Flags().StringVar(&artifactsDir, "artifacts-dir", "spine", "Directory for Spine artifacts (use / for repo root)")
	cmd.Flags().BoolVar(&noBranch, "no-branch", false, "Commit directly to current branch instead of spine/init")
	return cmd
}
