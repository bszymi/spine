package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/bszymi/spine/internal/cli"
	"github.com/spf13/cobra"
)

func artifactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Manage governed artifacts",
	}

	cmd.AddCommand(artifactCreateCmd())
	cmd.AddCommand(artifactEntryCmd())
	cmd.AddCommand(artifactReadCmd())
	cmd.AddCommand(artifactUpdateCmd())
	cmd.AddCommand(artifactListCmd())
	cmd.AddCommand(artifactValidateCmd())

	return cmd
}

func artifactCreateCmd() *cobra.Command {
	var path, file string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new artifact",
		RunE: func(cmd *cobra.Command, args []string) error {
			if path == "" || file == "" {
				return fmt.Errorf("--path and --file are required")
			}

			content, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}

			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/artifacts", map[string]string{
				"path":    path,
				"content": string(content),
			})
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "Artifact path")
	cmd.Flags().StringVar(&file, "file", "", "Path to content file")
	return cmd
}

func artifactReadCmd() *cobra.Command {
	var ref string

	cmd := &cobra.Command{
		Use:   "read <path>",
		Short: "Read an artifact",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params := url.Values{}
			if ref != "" {
				params.Set("ref", ref)
			}

			c := newAPIClient()
			data, err := c.Get(context.Background(), "/api/v1/artifacts/"+normalizePath(args[0]), params)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&ref, "ref", "", "Git ref to read from")
	return cmd
}

func artifactUpdateCmd() *cobra.Command {
	var path, file string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update an artifact",
		RunE: func(cmd *cobra.Command, args []string) error {
			if path == "" || file == "" {
				return fmt.Errorf("--path and --file are required")
			}

			content, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}

			c := newAPIClient()
			data, err := c.Put(context.Background(), "/api/v1/artifacts/"+normalizePath(path), map[string]string{
				"content": string(content),
			})
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "Artifact path")
	cmd.Flags().StringVar(&file, "file", "", "Path to content file")
	return cmd
}

func artifactListCmd() *cobra.Command {
	var artType, status string
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List artifacts",
		RunE: func(cmd *cobra.Command, args []string) error {
			params := url.Values{}
			if artType != "" {
				params.Set("type", artType)
			}
			if status != "" {
				params.Set("status", status)
			}
			if limit > 0 {
				params.Set("limit", fmt.Sprintf("%d", limit))
			}

			c := newAPIClient()
			data, err := c.Get(context.Background(), "/api/v1/artifacts", params)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&artType, "type", "", "Filter by artifact type")
	cmd.Flags().StringVar(&status, "status", "", "Filter by status")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of results")
	return cmd
}

func artifactValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate an artifact",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/artifacts/"+normalizePath(args[0])+"/validate", nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
}

func artifactEntryCmd() *cobra.Command {
	var artType, epic, initiative, title string

	cmd := &cobra.Command{
		Use:   "entry",
		Short: "Create a governed artifact through a planning run",
		Long: `Start a governed artifact creation workflow.

Allocates the next sequential ID, creates a branch, and starts a
planning run with the creation workflow.

Examples:
  spine artifact entry --type Task --epic EPIC-003 --title "Implement validation"
  spine artifact entry --type Epic --initiative INIT-003 --title "New feature"
  spine artifact entry --type Initiative --title "New initiative"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
			}

			// Determine parent based on type and flags.
			var parent string
			switch artType {
			case "Task":
				if epic == "" {
					return fmt.Errorf("--epic is required for Task")
				}
				parent = epic
			case "Epic":
				if initiative == "" {
					return fmt.Errorf("--initiative is required for Epic")
				}
				parent = initiative
			case "Initiative":
				// No parent needed.
			default:
				return fmt.Errorf("--type must be Task, Epic, or Initiative")
			}

			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/artifacts/entry", map[string]string{
				"artifact_type": artType,
				"parent":        parent,
				"title":         title,
			})
			if err != nil {
				return err
			}

			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&artType, "type", "", "Artifact type (Task, Epic, Initiative)")
	cmd.Flags().StringVar(&epic, "epic", "", "Parent epic ID (required for Task)")
	cmd.Flags().StringVar(&initiative, "initiative", "", "Parent initiative ID (required for Epic)")
	cmd.Flags().StringVar(&title, "title", "", "Artifact title")
	return cmd
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// outputFormat holds the global output format flag.
var outputFormat string

// globalWorkspaceID is set by the root --workspace persistent flag.
var globalWorkspaceID string

func newAPIClient() *cli.Client {
	baseURL := os.Getenv("SPINE_SERVER_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	token := os.Getenv("SPINE_TOKEN")
	return cli.NewClient(baseURL, token).WithWorkspace(globalWorkspaceID)
}

// normalizePath strips a leading slash from canonical artifact paths.
func normalizePath(path string) string {
	if path != "" && path[0] == '/' {
		return path[1:]
	}
	return path
}

func printResponse(data []byte) error {
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		fmt.Println(string(data))
		return nil
	}
	return cli.PrintResult(cli.OutputFormat(outputFormat), parsed)
}
