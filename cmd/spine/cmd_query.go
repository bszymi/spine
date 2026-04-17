package main

import (
	"os"

	"github.com/bszymi/spine/internal/cli"
	"github.com/spf13/cobra"
)

func queryCmd() *cobra.Command {
	apiURL := os.Getenv("SPINE_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8080"
	}
	token := os.Getenv("SPINE_TOKEN")
	outputFmt := "table"

	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query artifacts, graph, history, and runs",
	}
	cmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "table", "Output format: table or json")

	artifactsCmd := &cobra.Command{
		Use:   "artifacts",
		Short: "List artifacts with optional filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			artType, _ := cmd.Flags().GetString("type")
			status, _ := cmd.Flags().GetString("status")
			parent, _ := cmd.Flags().GetString("parent")
			client := cli.NewClient(apiURL, token).WithWorkspace(globalWorkspaceID)
			return cli.QueryArtifacts(cmd.Context(), client, artType, status, parent, cli.OutputFormat(outputFmt))
		},
	}
	artifactsCmd.Flags().String("type", "", "Filter by artifact type")
	artifactsCmd.Flags().String("status", "", "Filter by status")
	artifactsCmd.Flags().String("parent", "", "Filter by parent path")

	graphCmd := &cobra.Command{
		Use:   "graph [artifact-path]",
		Short: "Display artifact relationship graph",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) > 0 {
				path = args[0]
			}
			depth, _ := cmd.Flags().GetInt("depth")
			client := cli.NewClient(apiURL, token).WithWorkspace(globalWorkspaceID)
			return cli.QueryGraph(cmd.Context(), client, path, depth, cli.OutputFormat(outputFmt))
		},
	}
	graphCmd.Flags().Int("depth", 0, "Maximum traversal depth")

	historyCmd := &cobra.Command{
		Use:   "history [artifact-path]",
		Short: "Show change history for an artifact",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) > 0 {
				path = args[0]
			}
			client := cli.NewClient(apiURL, token).WithWorkspace(globalWorkspaceID)
			return cli.QueryHistory(cmd.Context(), client, path, cli.OutputFormat(outputFmt))
		},
	}

	runsCmd := &cobra.Command{
		Use:   "runs",
		Short: "List workflow runs with optional filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			task, _ := cmd.Flags().GetString("task")
			status, _ := cmd.Flags().GetString("status")
			client := cli.NewClient(apiURL, token).WithWorkspace(globalWorkspaceID)
			return cli.QueryRuns(cmd.Context(), client, task, status, cli.OutputFormat(outputFmt))
		},
	}
	runsCmd.Flags().String("task", "", "Filter by task path")
	runsCmd.Flags().String("status", "", "Filter by run status")

	cmd.AddCommand(artifactsCmd, graphCmd, historyCmd, runsCmd)
	return cmd
}
