package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/bszymi/spine/internal/cli"
	"github.com/spf13/cobra"
)

func workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage workspaces (shared mode)",
	}

	cmd.AddCommand(workspaceCreateCmd())
	cmd.AddCommand(workspaceListCmd())
	cmd.AddCommand(workspaceGetCmd())
	cmd.AddCommand(workspaceDeactivateCmd())

	return cmd
}

func workspaceCreateCmd() *cobra.Command {
	var displayName string
	var gitURL string

	cmd := &cobra.Command{
		Use:   "create <workspace_id>",
		Short: "Create and provision a new workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceID := args[0]

			body := map[string]string{
				"workspace_id": workspaceID,
			}
			if displayName != "" {
				body["display_name"] = displayName
			}
			if gitURL != "" {
				body["git_url"] = gitURL
			}

			c := newOperatorClient()
			data, err := c.Post(cmd.Context(), "/api/v1/workspaces", body)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&displayName, "name", "", "Display name for the workspace")
	cmd.Flags().StringVar(&gitURL, "git-url", "", "Remote Git URL to clone (optional)")

	return cmd
}

func workspaceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newOperatorClient()
			data, err := c.Get(cmd.Context(), "/api/v1/workspaces", nil)
			if err != nil {
				return err
			}

			if outputFormat == "json" {
				return printResponse(data)
			}

			// Table format.
			var resp struct {
				Workspaces []struct {
					WorkspaceID string `json:"workspace_id"`
					DisplayName string `json:"display_name"`
					Status      string `json:"status"`
				} `json:"workspaces"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				return printResponse(data)
			}

			fmt.Printf("%-20s %-30s %s\n", "WORKSPACE_ID", "DISPLAY_NAME", "STATUS")
			for _, ws := range resp.Workspaces {
				fmt.Printf("%-20s %-30s %s\n", ws.WorkspaceID, ws.DisplayName, ws.Status)
			}
			return nil
		},
	}
}

func workspaceGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <workspace_id>",
		Short: "Get workspace details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newOperatorClient()
			data, err := c.Get(cmd.Context(), "/api/v1/workspaces/"+args[0], nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
}

func workspaceDeactivateCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "deactivate <workspace_id>",
		Short: "Deactivate a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceID := args[0]

			if !yes {
				fmt.Printf("Deactivate workspace %q? This will stop serving requests for it. [y/N] ", workspaceID)
				var confirm string
				if _, err := fmt.Scanln(&confirm); err != nil || (confirm != "y" && confirm != "Y") {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			c := newOperatorClient()
			data, err := c.Post(cmd.Context(), "/api/v1/workspaces/"+workspaceID+"/deactivate", nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")

	return cmd
}

// newOperatorClient creates a CLI client that uses the operator token
// instead of a per-workspace actor token.
func newOperatorClient() *cli.Client {
	baseURL := os.Getenv("SPINE_SERVER_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	token := os.Getenv("SPINE_OPERATOR_TOKEN")
	return cli.NewClient(baseURL, token)
}
