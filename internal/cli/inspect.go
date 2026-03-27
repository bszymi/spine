package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// InspectRun fetches detailed run state and displays it in a readable format.
func InspectRun(ctx context.Context, client *Client, runID string, format OutputFormat) error {
	data, err := client.Get(ctx, "/api/v1/runs/"+runID, nil)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if format == FormatJSON {
		return PrintJSON(result)
	}

	// Rich table display — flat response format.
	fmt.Printf("Run: %s\n", colorize(str(result["run_id"]), colorBold))
	fmt.Printf("Task: %s\n", str(result["task_path"]))
	fmt.Printf("Workflow: %s (v%s)\n", str(result["workflow_id"]), str(result["workflow_version"]))
	fmt.Printf("Status: %s\n", ColorStatus(str(result["status"])))
	fmt.Printf("Step: %s\n", str(result["current_step_id"]))
	fmt.Printf("Trace: %s\n", str(result["trace_id"]))
	if result["started_at"] != nil {
		fmt.Printf("Started: %s\n", str(result["started_at"]))
	}
	if result["completed_at"] != nil {
		fmt.Printf("Completed: %s\n", str(result["completed_at"]))
	}
	fmt.Println()

	steps, _ := result["step_executions"].([]any)
	if len(steps) > 0 {
		fmt.Println("Step Executions:")
		headers := []string{"EXECUTION", "STEP", "STATUS", "ATTEMPT", "OUTCOME", "ERROR"}
		var rows [][]string
		for _, s := range steps {
			step, ok := s.(map[string]any)
			if !ok {
				continue
			}
			errMsg := ""
			if detail, ok := step["error_detail"].(map[string]any); ok && detail != nil {
				errMsg = fmt.Sprintf("[%s] %s", str(detail["classification"]), str(detail["message"]))
			}
			rows = append(rows, []string{
				str(step["execution_id"]),
				str(step["step_id"]),
				ColorStatus(str(step["status"])),
				str(step["attempt"]),
				str(step["outcome_id"]),
				truncate(errMsg, 50),
			})
		}
		PrintTable(headers, rows)
	}

	return nil
}

// ValidateArtifact runs cross-artifact validation from the CLI.
func ValidateArtifact(ctx context.Context, client *Client, artifactPath string, format OutputFormat) error {
	// Strip leading slash for canonical paths.
	if artifactPath != "" && artifactPath[0] == '/' {
		artifactPath = artifactPath[1:]
	}
	data, err := client.Post(ctx, "/api/v1/artifacts/"+artifactPath+"/validate", nil)
	if err != nil {
		return fmt.Errorf("validate artifact: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if format == FormatJSON {
		return PrintJSON(result)
	}

	status := str(result["status"])
	fmt.Printf("Validation: %s\n\n", ColorStatus(status))

	printValidationIssues("Errors", result["errors"])
	printValidationIssues("Warnings", result["warnings"])

	return nil
}

// ValidateAll runs system-wide validation from the CLI.
func ValidateAll(ctx context.Context, client *Client, format OutputFormat) error {
	data, err := client.Post(ctx, "/api/v1/system/validate", nil)
	if err != nil {
		return fmt.Errorf("validate all: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if format == FormatJSON {
		return PrintJSON(result)
	}

	total := str(result["total_artifacts"])
	status := str(result["status"])
	passed := str(result["passed"])
	failed := str(result["failed"])
	warnings := str(result["warnings"])

	if status == "passed" {
		fmt.Printf("Validated %s artifacts: %s\n", total, colorize("ALL PASSED", colorGreen))
		return nil
	}

	fmt.Printf("Validated %s artifacts: %s passed, %s failed, %s warnings\n\n",
		total, passed, colorize(failed, colorRed), colorize(warnings, colorYellow))

	results, _ := result["results"].([]any)
	for _, r := range results {
		res, ok := r.(map[string]any)
		if !ok {
			continue
		}
		resStatus := str(res["status"])
		if resStatus == "passed" {
			continue
		}
		printValidationIssues("  Errors", res["errors"])
		printValidationIssues("  Warnings", res["warnings"])
	}

	return nil
}

// QueryMetrics fetches the /system/metrics endpoint.
func QueryMetrics(ctx context.Context, client *Client) error {
	params := url.Values{}
	data, err := client.Get(ctx, "/api/v1/system/metrics", params)
	if err != nil {
		return fmt.Errorf("get metrics: %w", err)
	}
	fmt.Print(string(data))
	return nil
}

func printValidationIssues(label string, issues any) {
	items, ok := issues.([]any)
	if !ok || len(items) == 0 {
		return
	}
	fmt.Printf("%s:\n", label)
	for _, item := range items {
		iss, ok := item.(map[string]any)
		if !ok {
			continue
		}
		severity := str(iss["severity"])
		color := colorYellow
		if severity == "error" {
			color = colorRed
		}
		fmt.Printf("  %s [%s] %s (%s)\n",
			colorize(severity, color),
			str(iss["rule_id"]),
			str(iss["message"]),
			str(iss["classification"]),
		)
	}
}

// Color constants for terminal output.
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBold   = "\033[1m"
)

func colorize(s, color string) string {
	return color + s + colorReset
}

// ColorStatus returns a color-coded status string for terminal output.
func ColorStatus(status string) string {
	switch strings.ToLower(status) {
	case "completed", "passed", "active":
		return colorize(status, colorGreen)
	case "failed", "cancelled":
		return colorize(status, colorRed)
	case "waiting", "assigned", "warnings", "paused", "pending":
		return colorize(status, colorYellow)
	default:
		return status
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit-3] + "..."
}
