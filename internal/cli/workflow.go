package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bszymi/spine/internal/yamlsafe"
)

// WorkflowSummary holds the key fields for display.
type WorkflowSummary struct {
	ID        string   `json:"id" yaml:"id"`
	Name      string   `json:"name" yaml:"name"`
	Version   string   `json:"version" yaml:"version"`
	Status    string   `json:"status" yaml:"status"`
	Mode      string   `json:"mode" yaml:"mode,omitempty"`
	AppliesTo []string `json:"applies_to" yaml:"applies_to"`
	Path      string   `json:"path" yaml:"-"`
}

// WorkflowDetail holds the full workflow for display.
type WorkflowDetail struct {
	WorkflowSummary `yaml:",inline"`
	EntryStep       string        `json:"entry_step" yaml:"entry_step"`
	Steps           []stepSummary `json:"steps" yaml:"steps"`
}

type stepSummary struct {
	ID       string   `json:"id" yaml:"id"`
	Name     string   `json:"name" yaml:"name"`
	Type     string   `json:"type" yaml:"type"`
	Timeout  string   `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Outcomes []string `json:"outcomes" yaml:"outcomes"`
}

// ListWorkflows reads all workflow YAML files from the workflows/ directory.
func ListWorkflows(repoPath string, format OutputFormat) error {
	wfDir := filepath.Join(repoPath, "workflows")
	entries, err := os.ReadDir(wfDir)
	if err != nil {
		return fmt.Errorf("read workflows directory: %w", err)
	}

	var workflows []WorkflowSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(wfDir, entry.Name())
		wf, err := parseWorkflowSummary(path)
		if err != nil {
			continue // skip unparseable files
		}
		wf.Path = "workflows/" + entry.Name()
		normalizeMode(wf)
		workflows = append(workflows, *wf)
	}

	if format == FormatJSON {
		return PrintJSON(workflows)
	}

	headers := []string{"ID", "NAME", "VERSION", "STATUS", "MODE", "APPLIES_TO", "PATH"}
	var rows [][]string
	for _, wf := range workflows {
		rows = append(rows, []string{
			wf.ID, wf.Name, wf.Version, wf.Status, wf.Mode,
			strings.Join(wf.AppliesTo, ", "), wf.Path,
		})
	}
	PrintTable(headers, rows)
	return nil
}

// ShowWorkflow displays the full workflow definition.
func ShowWorkflow(repoPath, workflowPath string, format OutputFormat) error {
	fullPath := filepath.Join(repoPath, workflowPath)
	detail, err := parseWorkflowDetail(fullPath)
	if err != nil {
		return err
	}
	detail.Path = workflowPath
	normalizeMode(&detail.WorkflowSummary)

	if format == FormatJSON {
		return PrintJSON(detail)
	}

	fmt.Printf("Workflow: %s (%s)\n", detail.Name, detail.ID)
	fmt.Printf("Version:  %s\n", detail.Version)
	fmt.Printf("Status:   %s\n", detail.Status)
	fmt.Printf("Mode:     %s\n", detail.Mode)
	fmt.Printf("Applies:  %s\n", strings.Join(detail.AppliesTo, ", "))
	fmt.Printf("Entry:    %s\n\n", detail.EntryStep)
	fmt.Println("Steps:")

	headers := []string{"ID", "NAME", "TYPE", "TIMEOUT", "OUTCOMES"}
	var rows [][]string
	for _, s := range detail.Steps {
		rows = append(rows, []string{
			s.ID, s.Name, s.Type, s.Timeout, strings.Join(s.Outcomes, ", "),
		})
	}
	PrintTable(headers, rows)
	return nil
}

// ResolveWorkflow finds which workflow would bind to a given artifact type.
func ResolveWorkflow(repoPath, artifactPath string, format OutputFormat) error {
	// Read the artifact to get its type.
	artFullPath := filepath.Join(repoPath, artifactPath)
	artData, err := os.ReadFile(artFullPath)
	if err != nil {
		return fmt.Errorf("read artifact: %w", err)
	}

	var artMeta struct {
		Type string `yaml:"type"`
	}
	if err := yamlsafe.DecodeInto(extractFrontMatter(artData), &artMeta); err != nil {
		return fmt.Errorf("parse artifact front matter: %w", err)
	}

	if artMeta.Type == "" {
		return fmt.Errorf("artifact at %s has no type field", artifactPath)
	}

	// Scan workflows for one that applies_to this type.
	wfDir := filepath.Join(repoPath, "workflows")
	entries, err := os.ReadDir(wfDir)
	if err != nil {
		return fmt.Errorf("read workflows directory: %w", err)
	}

	var matches []WorkflowSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(wfDir, entry.Name())
		wf, err := parseWorkflowSummary(path)
		if err != nil {
			continue
		}
		// Only consider Active workflows for binding resolution.
		if wf.Status != "Active" {
			continue
		}
		for _, at := range wf.AppliesTo {
			if strings.EqualFold(at, artMeta.Type) {
				wf.Path = "workflows/" + entry.Name()
				normalizeMode(wf)
				matches = append(matches, *wf)
			}
		}
	}

	if len(matches) == 0 {
		return fmt.Errorf("no workflow binds to artifact type %q", artMeta.Type)
	}

	result := map[string]any{
		"artifact_path": artifactPath,
		"artifact_type": artMeta.Type,
		"bindings":      matches,
	}

	if format == FormatJSON {
		return PrintJSON(result)
	}

	fmt.Printf("Artifact: %s (type: %s)\n\n", artifactPath, artMeta.Type)
	fmt.Println("Matching workflows:")
	for _, m := range matches {
		fmt.Printf("  %s (%s) [%s] — %s\n", m.ID, m.Version, m.Mode, m.Path)
	}
	return nil
}

// normalizeMode sets the default mode for workflows that omit it.
func normalizeMode(wf *WorkflowSummary) {
	if wf.Mode == "" {
		wf.Mode = "execution"
	}
}

func parseWorkflowSummary(path string) (*WorkflowSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wf WorkflowSummary
	if err := yamlsafe.DecodeInto(data, &wf); err != nil {
		return nil, err
	}
	return &wf, nil
}

func parseWorkflowDetail(path string) (*WorkflowDetail, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow: %w", err)
	}

	var raw struct {
		WorkflowSummary `yaml:",inline"`
		EntryStep       string `yaml:"entry_step"`
		Steps           []struct {
			ID       string `yaml:"id"`
			Name     string `yaml:"name"`
			Type     string `yaml:"type"`
			Timeout  string `yaml:"timeout"`
			Outcomes []struct {
				ID string `yaml:"id"`
			} `yaml:"outcomes"`
		} `yaml:"steps"`
	}
	if err := yamlsafe.DecodeInto(data, &raw); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}

	detail := &WorkflowDetail{
		WorkflowSummary: raw.WorkflowSummary,
		EntryStep:       raw.EntryStep,
	}
	for _, s := range raw.Steps {
		var outcomeIDs []string
		for _, o := range s.Outcomes {
			outcomeIDs = append(outcomeIDs, o.ID)
		}
		detail.Steps = append(detail.Steps, stepSummary{
			ID: s.ID, Name: s.Name, Type: s.Type,
			Timeout: s.Timeout, Outcomes: outcomeIDs,
		})
	}
	return detail, nil
}

// extractFrontMatter returns the YAML front matter from a Markdown file.
func extractFrontMatter(data []byte) []byte {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return nil
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return nil
	}
	return []byte(s[4 : 4+end])
}
