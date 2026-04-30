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
	cmd.AddCommand(runMergeCmd())

	return cmd
}

func runMergeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "merge",
		Short: "Manage per-repository merge outcomes for a run",
	}
	cmd.AddCommand(runMergeResolveCmd())
	cmd.AddCommand(runMergeRetryCmd())
	return cmd
}

func runMergeResolveCmd() *cobra.Command {
	var reason string
	var commitSHA string
	cmd := &cobra.Command{
		Use:   "resolve <run_id> <repository_id>",
		Short: "Mark a failed per-repo merge as resolved externally",
		Long: "Mark a failed per-repo merge outcome as resolved-externally so the run\n" +
			"can resume. The reason and optional upstream commit SHA are recorded on\n" +
			"the outcome row, in a primary-repo ledger commit, and as an audit event.\n" +
			"Operator role required.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reason == "" {
				return fmt.Errorf("--reason is required")
			}
			body := map[string]string{"reason": reason}
			if commitSHA != "" {
				body["target_commit_sha"] = commitSHA
			}
			c := newAPIClient()
			path := "/api/v1/runs/" + args[0] + "/repositories/" + args[1] + "/resolve"
			data, err := c.Post(context.Background(), path, body)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Audit reason for the resolution (required)")
	cmd.Flags().StringVar(&commitSHA, "commit-sha", "", "Upstream commit SHA the operator merged the fix to (optional)")
	return cmd
}

func runMergeRetryCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "retry <run_id> <repository_id>",
		Short: "Retry a failed per-repo merge",
		Long: "Reset a failed per-repo merge outcome to pending so the next scheduler\n" +
			"tick re-attempts the merge. The reason is recorded as an audit event.\n" +
			"Operator role required.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reason == "" {
				return fmt.Errorf("--reason is required")
			}
			body := map[string]string{"reason": reason}
			c := newAPIClient()
			path := "/api/v1/runs/" + args[0] + "/repositories/" + args[1] + "/retry"
			data, err := c.Post(context.Background(), path, body)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Audit reason for the retry (required)")
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
