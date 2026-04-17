package main

import (
	"fmt"
	"net/url"
	"os"

	"github.com/bszymi/spine/internal/cli"
	"github.com/spf13/cobra"
)

func workflowCmd() *cobra.Command {
	repoPath := "."
	outputFmt := "table"

	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage workflow definitions",
	}
	cmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "table", "Output format: table or json")
	cmd.PersistentFlags().StringVar(&repoPath, "repo", ".", "Repository path")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all available workflow definitions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.ListWorkflows(repoPath, cli.OutputFormat(outputFmt))
		},
	}

	showCmd := &cobra.Command{
		Use:   "show [workflow-path]",
		Short: "Display workflow definition details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.ShowWorkflow(repoPath, args[0], cli.OutputFormat(outputFmt))
		},
	}

	resolveCmd := &cobra.Command{
		Use:   "resolve [artifact-path]",
		Short: "Show which workflow would bind to the given artifact",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.ResolveWorkflow(repoPath, args[0], cli.OutputFormat(outputFmt))
		},
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new workflow definition via the API (ADR-007)",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("id")
			file, _ := cmd.Flags().GetString("file")
			if id == "" || file == "" {
				return fmt.Errorf("--id and --file are required")
			}
			body, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			c := newAPIClient()
			data, err := c.Post(cmd.Context(), "/api/v1/workflows", map[string]string{
				"id":   id,
				"body": string(body),
			})
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	createCmd.Flags().String("id", "", "Workflow identifier (e.g. task-default)")
	createCmd.Flags().String("file", "", "Path to YAML file containing the workflow body")

	updateCmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update an existing workflow definition via the API",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			body, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			c := newAPIClient()
			data, err := c.Put(cmd.Context(), "/api/v1/workflows/"+args[0], map[string]string{
				"body": string(body),
			})
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	updateCmd.Flags().String("file", "", "Path to updated YAML file")

	validateCmd := &cobra.Command{
		Use:   "validate <id>",
		Short: "Validate a candidate workflow body via the API (no persist)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			body, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			c := newAPIClient()
			data, err := c.Post(cmd.Context(), "/api/v1/workflows/"+args[0]+"/validate", map[string]string{
				"body": string(body),
			})
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	validateCmd.Flags().String("file", "", "Path to candidate YAML file")

	apiListCmd := &cobra.Command{
		Use:   "api-list",
		Short: "List workflows via the API (instead of reading the working tree)",
		RunE: func(cmd *cobra.Command, args []string) error {
			params := url.Values{}
			if v, _ := cmd.Flags().GetString("applies_to"); v != "" {
				params.Set("applies_to", v)
			}
			if v, _ := cmd.Flags().GetString("status"); v != "" {
				params.Set("status", v)
			}
			if v, _ := cmd.Flags().GetString("mode"); v != "" {
				params.Set("mode", v)
			}
			c := newAPIClient()
			data, err := c.Get(cmd.Context(), "/api/v1/workflows", params)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	apiListCmd.Flags().String("applies_to", "", "Filter by artifact type")
	apiListCmd.Flags().String("status", "", "Filter by status (Active|Deprecated|Superseded)")
	apiListCmd.Flags().String("mode", "", "Filter by mode (execution|creation)")

	apiReadCmd := &cobra.Command{
		Use:   "api-read <id>",
		Short: "Read a workflow via the API (returns executable body)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params := url.Values{}
			if v, _ := cmd.Flags().GetString("ref"); v != "" {
				params.Set("ref", v)
			}
			c := newAPIClient()
			data, err := c.Get(cmd.Context(), "/api/v1/workflows/"+args[0], params)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	apiReadCmd.Flags().String("ref", "", "Git ref to read from")

	cmd.AddCommand(listCmd, showCmd, resolveCmd, createCmd, updateCmd, validateCmd, apiListCmd, apiReadCmd)
	return cmd
}
