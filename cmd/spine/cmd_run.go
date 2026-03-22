package main

import (
	"context"
	"fmt"

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

	return cmd
}

func runStartCmd() *cobra.Command {
	var taskPath string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a new workflow run",
		RunE: func(cmd *cobra.Command, args []string) error {
			if taskPath == "" {
				return fmt.Errorf("--task is required")
			}

			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/runs", map[string]string{
				"task_path": taskPath,
			})
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&taskPath, "task", "", "Task path to run workflow for")
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
