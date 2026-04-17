package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/bszymi/spine/internal/cli"
	"github.com/bszymi/spine/internal/observe"
	"github.com/spf13/cobra"
)

// outputFormat holds the global output format flag, bound by the root
// command's --output persistent flag.
var outputFormat string

// globalWorkspaceID is set by the root --workspace persistent flag and
// consumed by subcommands that call the API.
var globalWorkspaceID string

// newAPIClient constructs a CLI client from SPINE_SERVER_URL and
// SPINE_TOKEN, scoped to the workspace selected by --workspace.
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

// printResponse renders a JSON API response per the --output flag.
func printResponse(data []byte) error {
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		fmt.Println(string(data))
		return nil
	}
	return cli.PrintResult(cli.OutputFormat(outputFormat), parsed)
}

func main() {
	logLevel := os.Getenv("SPINE_LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	logFormat := os.Getenv("SPINE_LOG_FORMAT")
	if logFormat == "" {
		logFormat = "json"
	}
	observe.SetupLogger(logLevel, logFormat)

	root := &cobra.Command{
		Use:   "spine",
		Short: "Spine — Git-native Product-to-Execution System",
	}

	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "json", "Output format: json or table")

	globalWorkspaceID = os.Getenv("SPINE_WORKSPACE_ID")
	root.PersistentFlags().StringVar(&globalWorkspaceID, "workspace", globalWorkspaceID, "Workspace ID (overrides SPINE_WORKSPACE_ID)")

	root.AddCommand(serveCmd())
	root.AddCommand(healthCmd())
	root.AddCommand(migrateCmd())
	root.AddCommand(initRepoCmd())
	root.AddCommand(artifactCmd())
	root.AddCommand(runCmd())
	root.AddCommand(taskCmd())
	root.AddCommand(queryCmd())
	root.AddCommand(workflowCmd())
	root.AddCommand(validateCmd())
	root.AddCommand(discussionCmd())
	root.AddCommand(workspaceCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
