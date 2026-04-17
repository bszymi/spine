package main

import (
	"github.com/bszymi/spine/internal/cli"
	"github.com/spf13/cobra"
)

func validateCmd() *cobra.Command {
	outputFmt := "table"

	cmd := &cobra.Command{
		Use:   "validate [artifact-path]",
		Short: "Run cross-artifact validation",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			all, _ := cmd.Flags().GetBool("all")
			c := newAPIClient()
			if all || len(args) == 0 {
				return cli.ValidateAll(cmd.Context(), c, cli.OutputFormat(outputFmt))
			}
			return cli.ValidateArtifact(cmd.Context(), c, args[0], cli.OutputFormat(outputFmt))
		},
	}
	cmd.Flags().Bool("all", false, "Validate entire repository")
	cmd.Flags().StringVarP(&outputFmt, "output", "o", "table", "Output format: table or json")
	return cmd
}
