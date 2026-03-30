package main

import (
	"context"
	"fmt"
	"os"

	"github.com/bszymi/spine/internal/cli"
	"github.com/spf13/cobra"
)

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Manage workflow runs",
	}

	cmd.AddCommand(runStartCmd())
	cmd.AddCommand(runStatusCmd())
	cmd.AddCommand(runCancelCmd())
	cmd.AddCommand(runInspectCmd())

	return cmd
}

func runStartCmd() *cobra.Command {
	var taskPath string
	var mode string
	var contentFile string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a new workflow run",
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]string{}

			if mode == "planning" {
				if contentFile == "" {
					return fmt.Errorf("--content is required when --mode is planning")
				}
				if taskPath == "" {
					return fmt.Errorf("--task is required (artifact path for the new artifact)")
				}
				content, err := os.ReadFile(contentFile)
				if err != nil {
					return fmt.Errorf("read content file: %w", err)
				}
				body["mode"] = "planning"
				body["artifact_content"] = string(content)
				body["task_path"] = taskPath
			} else {
				if taskPath == "" {
					return fmt.Errorf("--task is required")
				}
				body["task_path"] = taskPath
				if mode != "" {
					body["mode"] = mode
				}
			}

			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/runs", body)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&taskPath, "task", "", "Artifact path (required for both standard and planning modes)")
	cmd.Flags().StringVar(&mode, "mode", "", "Run mode: standard (default) or planning")
	cmd.Flags().StringVar(&contentFile, "content", "", "Path to file containing artifact content (required for planning mode)")
	return cmd
}

func runStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <run_id>",
		Short: "Get run status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newAPIClient()
			data, err := c.Get(context.Background(), "/api/v1/runs/"+args[0], nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
}

func runInspectCmd() *cobra.Command {
	outputFmt := "table"
	cmd := &cobra.Command{
		Use:   "inspect <run_id>",
		Short: "Detailed view of run state, step history, errors, and timeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newAPIClient()
			return cli.InspectRun(cmd.Context(), c, args[0], cli.OutputFormat(outputFmt))
		},
	}
	cmd.Flags().StringVarP(&outputFmt, "output", "o", "table", "Output format: table or json")
	return cmd
}

func runCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run_id>",
		Short: "Cancel a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/runs/"+args[0]+"/cancel", nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
}
