package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// QueryArtifacts queries artifacts with optional filters.
func QueryArtifacts(ctx context.Context, client *Client, artType, status, parent string, format OutputFormat) error {
	params := url.Values{}
	if artType != "" {
		params.Set("type", artType)
	}
	if status != "" {
		params.Set("status", status)
	}
	if parent != "" {
		params.Set("parent_path", parent)
	}

	data, err := client.Get(ctx, "/api/v1/query/artifacts", params)
	if err != nil {
		return fmt.Errorf("query artifacts: %w", err)
	}

	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	return PrintResult(format, result)
}

// QueryGraph displays the artifact relationship graph.
func QueryGraph(ctx context.Context, client *Client, artifactPath string, depth int, format OutputFormat) error {
	params := url.Values{}
	if artifactPath != "" {
		params.Set("root", artifactPath)
	}
	if depth > 0 {
		params.Set("depth", fmt.Sprintf("%d", depth))
	}

	data, err := client.Get(ctx, "/api/v1/query/graph", params)
	if err != nil {
		return fmt.Errorf("query graph: %w", err)
	}

	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	return PrintResult(format, result)
}

// QueryHistory shows change history for an artifact.
func QueryHistory(ctx context.Context, client *Client, artifactPath string, format OutputFormat) error {
	params := url.Values{}
	if artifactPath != "" {
		params.Set("path", artifactPath)
	}

	data, err := client.Get(ctx, "/api/v1/query/history", params)
	if err != nil {
		return fmt.Errorf("query history: %w", err)
	}

	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	return PrintResult(format, result)
}

// QueryRuns lists workflow runs with optional filters.
func QueryRuns(ctx context.Context, client *Client, taskPath, status string, format OutputFormat) error {
	params := url.Values{}
	if taskPath != "" {
		params.Set("task_path", taskPath)
	}
	if status != "" {
		params.Set("status", status)
	}

	data, err := client.Get(ctx, "/api/v1/query/runs", params)
	if err != nil {
		return fmt.Errorf("query runs: %w", err)
	}

	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	return PrintResult(format, result)
}
