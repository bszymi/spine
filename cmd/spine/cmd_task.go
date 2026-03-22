package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func taskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Task governance operations",
	}

	for _, action := range []string{"accept", "reject", "cancel", "abandon", "supersede"} {
		cmd.AddCommand(taskActionCmd(action))
	}

	return cmd
}

func taskActionCmd(action string) *cobra.Command {
	return &cobra.Command{
		Use:   fmt.Sprintf("%s <path>", action),
		Short: fmt.Sprintf("%s a task", action),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/tasks/"+args[0]+"/"+action, nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
}
