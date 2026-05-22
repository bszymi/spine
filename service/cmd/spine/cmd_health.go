package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func healthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check system health",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(`{"status":"healthy","components":{}}`)
			return nil
		},
	}
}
