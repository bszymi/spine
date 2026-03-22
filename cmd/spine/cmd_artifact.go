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
			data, err := c.Get(context.Background(), "/api/v1/artifacts/"+args[0], params)
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
			data, err := c.Put(context.Background(), "/api/v1/artifacts/"+path, map[string]string{
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
			data, err := c.Post(context.Background(), "/api/v1/artifacts/"+args[0]+"/validate", nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
}

func newAPIClient() *cli.Client {
	baseURL := os.Getenv("SPINE_SERVER_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	token := os.Getenv("SPINE_TOKEN")
	return cli.NewClient(baseURL, token)
}

func printResponse(data []byte) error {
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		fmt.Println(string(data))
		return nil
	}
	return cli.PrintJSON(parsed)
}
