package main

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/repository"
	"github.com/spf13/cobra"
)

func repositoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "repository",
		Aliases: []string{"repo"},
		Short:   "Manage workspace code repositories (INIT-014)",
	}

	cmd.AddCommand(repositoryRegisterCmd())
	cmd.AddCommand(repositoryListCmd())
	cmd.AddCommand(repositoryInspectCmd())
	cmd.AddCommand(repositoryDeactivateCmd())

	return cmd
}

func repositoryRegisterCmd() *cobra.Command {
	var (
		name, defaultBranch, role, description string
		cloneURL, credentialsRef, localPath    string
	)

	cmd := &cobra.Command{
		Use:   "register <repository_id>",
		Short: "Register a code repository in the workspace catalog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			// Pre-validate locally so a typoed ID or clone URL doesn't
			// spend a network round trip getting rejected server-side.
			if err := repository.ValidateCloneURL(cloneURL); err != nil {
				return err
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if defaultBranch == "" {
				return fmt.Errorf("--default-branch is required")
			}
			if localPath == "" {
				return fmt.Errorf("--local-path is required")
			}

			body := map[string]string{
				"id":             id,
				"name":           name,
				"default_branch": defaultBranch,
				"clone_url":      cloneURL,
				"local_path":     localPath,
			}
			if role != "" {
				body["role"] = role
			}
			if description != "" {
				body["description"] = description
			}
			if credentialsRef != "" {
				body["credentials_ref"] = credentialsRef
			}

			c := newAPIClient()
			data, err := c.Post(cmd.Context(), "/api/v1/repositories", body)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Display name (required)")
	cmd.Flags().StringVar(&defaultBranch, "default-branch", "", "Authoritative branch (required)")
	cmd.Flags().StringVar(&role, "role", "", "Free-form role label (e.g. service, library)")
	cmd.Flags().StringVar(&description, "description", "", "Optional one-line description")
	cmd.Flags().StringVar(&cloneURL, "clone-url", "", "Clone URL (required, https/ssh/git/file/SCP-like)")
	cmd.Flags().StringVar(&credentialsRef, "credentials-ref", "", "Optional secret reference for clone credentials")
	cmd.Flags().StringVar(&localPath, "local-path", "", "On-disk path for the workspace clone (required)")

	return cmd
}

func repositoryListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List repositories registered in the current workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newAPIClient()
			data, err := c.Get(cmd.Context(), "/api/v1/repositories", nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
}

func repositoryInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <repository_id>",
		Short: "Show full details for one repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newAPIClient()
			data, err := c.Get(cmd.Context(), "/api/v1/repositories/"+args[0], nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
}

func repositoryDeactivateCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "deactivate <repository_id>",
		Short: "Mark a repository binding inactive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !yes {
				fmt.Printf("Deactivate repository %q? It will stop resolving for execution. [y/N] ", id)
				var confirm string
				if _, err := fmt.Scanln(&confirm); err != nil || (confirm != "y" && confirm != "Y") {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/repositories/"+id+"/deactivate", nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	return cmd
}
