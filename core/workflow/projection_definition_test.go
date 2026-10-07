package workflow_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/core/yamlsafe"
)

// INIT-023 TASK-011 (audit P3-13): the projection must store a workflow
// only when strict Parse accepts it AND the stored JSON reads back, in
// the loaders' case-folding decoder, to exactly what Parse returned.

// legacyProjectionEncoder is the projection encoder as it shipped at
// Spine 61994fe (adapters/projection/service.go projectWorkflow):
// yamlsafe.Decode, decode into `any`, json.Marshal. Re-implemented here
// independently of the production helper so the byte-identity assertions
// below compare against the historical shape, not against themselves.
func legacyProjectionEncoder(content []byte) ([]byte, error) {
	node, err := yamlsafe.Decode(content)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return nil, err
	}
	return json.Marshal(raw)
}

// pocFoldWorkflow and pocFoldOutcome are the two audit payloads, copied
// verbatim: a look-alike key (U+017F LATIN SMALL LETTER LONG S) that a
// strict reader refuses but a case-folding JSON reader maps onto
// `steps` / `next_step`.
const pocFoldWorkflow = `id: task-default
name: Default Task Workflow
version: "8.0"
status: Active
description: Human-reviewed task workflow
applies_to: [Task]
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    execution: {mode: hybrid, eligible_actor_types: [ai_agent], required_skills: [implementation]}
    outcomes:
      - {id: completed, name: Done, next_step: review}
  - id: review
    name: Human Code Review
    type: review
    execution: {mode: human_only, eligible_actor_types: [human], required_skills: [review]}
    outcomes:
      - {id: accepted, name: Accepted, next_step: publish}
  - id: publish
    name: Publish
    type: internal
    execution: {mode: spine_only, handler: merge}
    retry: {limit: 1}
    outcomes:
      - {id: merged, name: Merged, next_step: end}
ſteps:
  - id: execute
    name: Execute Task
    type: manual
    execution: {mode: hybrid, eligible_actor_types: [ai_agent], required_skills: [implementation]}
    outcomes:
      - {id: completed, name: Done, next_step: publish}
  - id: publish
    name: Publish
    type: internal
    execution: {mode: spine_only, handler: merge}
    retry: {limit: 1}
    outcomes:
      - {id: merged, name: Merged, next_step: end}
`

const pocFoldOutcome = `id: task-default
name: Default Task Workflow
version: "8.0"
status: Active
description: d
applies_to: [Task]
entry_step: validate
steps:
  - id: validate
    name: Validate Build and Tests
    type: automated
    execution: {mode: automated_only, eligible_actor_types: [automated_system]}
    retry: {limit: 2}
    outcomes:
      - id: passed
        name: Passed
        next_step: publish
      - id: failed
        name: Failed
        next_step: validate
        next_ſtep: publish
  - id: publish
    name: Publish
    type: internal
    execution: {mode: spine_only, handler: merge}
    retry: {limit: 1}
    outcomes:
      - {id: merged, name: Merged, next_step: end}
`

const rtBase = "id: task-default\nname: n\nversion: \"1\"\nstatus: Active\ndescription: d\napplies_to: [Task]\nentry_step: a\n"
const rtSteps = "steps:\n  - id: a\n    name: A\n    type: manual\n    outcomes:\n      - {id: ok, name: OK, next_step: end}\n"

func rtStepsWith(stepExtra, outcomeExtra string) string {
	return "steps:\n  - id: a\n    name: A\n    type: manual\n" + stepExtra +
		"    outcomes:\n      - {id: ok, name: OK, next_step: end" + outcomeExtra + "}\n"
}

// smugglingInputs are refused by strict Parse (AC 1).
func smugglingInputs() map[string]string {
	return map[string]string{
		"fold_steps_poc":   pocFoldWorkflow,
		"fold_outcome_poc": pocFoldOutcome,
		"upper_Steps":      rtBase + "Steps:\n  - id: b\n    name: B\n    type: manual\n    outcomes: []\n",
		"duplicate_id":     rtBase + "id: x\n" + rtSteps,
		"top_level_path":   rtBase + rtSteps + "path: workflows/other.yaml\n",
		"top_level_commit": rtBase + rtSteps + "commit_sha: abc123\n",
	}
}

func TestProjectionDefinition_RefusesSmugglingClassAsParseError(t *testing.T) {
	for name, body := range smugglingInputs() {
		t.Run(name, func(t *testing.T) {
			wf, def, err := workflow.ProjectionDefinition("workflows/task-default.yaml", []byte(body))
			if err == nil {
				t.Fatalf("expected a parse error, got wf=%+v def=%s", wf, def)
			}
			if errors.Is(err, workflow.ErrProjectionRoundTrip) {
				t.Fatalf("smuggling input must fail strict Parse, not the round trip: %v", err)
			}
			if _, perr := workflow.Parse("workflows/task-default.yaml", []byte(body)); perr == nil {
				t.Fatal("precondition: Parse must reject this input")
			}
		})
	}
}

// roundTripInputs pass strict Parse but must not be projected (AC 2).
func roundTripInputs() map[string]string {
	return map[string]string{
		"version_float": strings.Replace(rtBase, `version: "1"`, "version: 1.0", 1) + rtSteps,
		"retry_float":   rtBase + rtStepsWith("    retry: {limit: 1.5}\n", ""),
		"int_step_name": rtBase + "steps:\n  - id: a\n    name: 123\n    type: manual\n    outcomes:\n      - {id: ok, name: OK, next_step: end}\n",
		// Decodes without error, but as "2026-10-06T00:00:00Z": only the
		// DeepEqual catches it.
		"version_date":       strings.Replace(rtBase, `version: "1"`, "version: 2026-10-06", 1) + rtSteps,
		"nul_in_name":        strings.Replace(rtBase, "name: n", `name: "a\0b"`, 1) + rtSteps,
		"nul_in_commit_map":  rtBase + rtStepsWith("", `, commit: {status: "x\0"}`),
		"int_key_commit_map": rtBase + rtStepsWith("", ", commit: {1: x}"),
	}
}

func TestProjectionDefinition_RefusesRoundTripFailures(t *testing.T) {
	for name, body := range roundTripInputs() {
		t.Run(name, func(t *testing.T) {
			if _, err := workflow.Parse("workflows/task-default.yaml", []byte(body)); err != nil {
				t.Fatalf("precondition: Parse must accept this input, got %v", err)
			}
			_, def, err := workflow.ProjectionDefinition("workflows/task-default.yaml", []byte(body))
			if err == nil {
				t.Fatalf("expected ErrProjectionRoundTrip, got def=%s", def)
			}
			if !errors.Is(err, workflow.ErrProjectionRoundTrip) {
				t.Fatalf("expected ErrProjectionRoundTrip, got %v", err)
			}
		})
	}
}

// The six-character text `\u0000` inside a description is ordinary
// content; a byte search over the JSON would wrongly refuse it.
func TestProjectionDefinition_AcceptsLiteralBackslashU0000Text(t *testing.T) {
	body := strings.Replace(rtBase, "description: d", `description: 'escape \u0000 as text'`, 1) + rtSteps
	wf, def, err := workflow.ProjectionDefinition("workflows/task-default.yaml", []byte(body))
	if err != nil {
		t.Fatalf("expected acceptance, got %v", err)
	}
	if !strings.Contains(wf.Description, `\u0000`) {
		t.Fatalf("description = %q", wf.Description)
	}
	if err := workflow.VerifyProjectionDefinition(def); err != nil {
		t.Fatalf("VerifyProjectionDefinition: %v", err)
	}
}

func shippedWorkflowFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "workflows", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 8 {
		t.Fatalf("expected Spine's 8 shipped workflow files, found %d: %v", len(files), files)
	}
	return files
}

// AC 3: every shipped workflow is accepted, stores byte-identical JSON
// and verifies.
func TestProjectionDefinition_ShippedCorpusUnchanged(t *testing.T) {
	for _, f := range shippedWorkflowFiles(t) {
		t.Run(filepath.Base(f), func(t *testing.T) {
			content, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			path := "workflows/" + filepath.Base(f)
			wf, def, err := workflow.ProjectionDefinition(path, content)
			if err != nil {
				t.Fatalf("ProjectionDefinition: %v", err)
			}
			legacy, err := legacyProjectionEncoder(content)
			if err != nil {
				t.Fatalf("legacy encoder: %v", err)
			}
			if !bytes.Equal(def, legacy) {
				t.Fatalf("stored JSON changed:\n got %s\nwant %s", def, legacy)
			}
			if err := workflow.VerifyProjectionDefinition(def); err != nil {
				t.Fatalf("VerifyProjectionDefinition rejects a shipped file: %v", err)
			}
			parsed, err := workflow.Parse(path, content)
			if err != nil {
				t.Fatal(err)
			}
			if wf.ID != parsed.ID || wf.Path != path {
				t.Fatalf("returned definition differs from Parse: %+v", wf)
			}
		})
	}
}

// AC 4: rows stored by the 61994fe encoder from a smuggling or
// round-trip-failing body are refused on verification.
func TestVerifyProjectionDefinition_RejectsDisagreeingRows(t *testing.T) {
	inputs := smugglingInputs()
	inputs["version_float"] = roundTripInputs()["version_float"]
	encoded := 0
	for name, body := range inputs {
		legacy, err := legacyProjectionEncoder([]byte(body))
		if err != nil {
			// Duplicate keys fail the legacy encoder too, so no such row
			// can exist in a store.
			t.Logf("%s: not encodable by the 61994fe encoder (%v)", name, err)
			continue
		}
		encoded++
		t.Run(name, func(t *testing.T) {
			err := workflow.VerifyProjectionDefinition(legacy)
			if err == nil {
				t.Fatalf("expected the stored row to be refused: %s", legacy)
			}
			if !errors.Is(err, workflow.ErrProjectionRoundTrip) {
				t.Fatalf("expected ErrProjectionRoundTrip, got %v", err)
			}
		})
	}
	if encoded < 6 {
		t.Fatalf("expected at least 6 encodable inputs, got %d", encoded)
	}
}

func TestVerifyProjectionDefinition_AcceptsMinimalAndEmptyShapes(t *testing.T) {
	for name, def := range map[string]string{
		"minimal":       `{"id":"x","name":"n","version":"1","status":"Active","applies_to":["Task"]}`,
		"empty_steps":   `{"id":"x","steps":[]}`,
		"null_fields":   `{"id":"x","steps":null,"applies_to":null}`,
		"empty_commit":  `{"id":"x","steps":[{"id":"a","outcomes":[{"id":"o","commit":{}}]}]}`,
		"explicit_mode": `{"id":"x","mode":"creation"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := workflow.VerifyProjectionDefinition([]byte(def)); err != nil {
				t.Fatalf("VerifyProjectionDefinition(%s): %v", def, err)
			}
		})
	}
}

func TestVerifyProjectionDefinition_RejectsStoredOddities(t *testing.T) {
	for name, def := range map[string]string{
		"not_json":      `{`,
		"unknown_field": `{"id":"x","bogus":1}`,
		"number_string": `{"id":"x","version":1}`,
		"folded_key":    `{"id":"x","ſteps":[]}`,
		"upper_key":     `{"id":"x","Steps":[]}`,
		"path_key":      `{"id":"x","path":"workflows/y.yaml"}`,
		"commit_sha":    `{"id":"x","commit_sha":"abc"}`,
		"nul_value":     `{"id":"x","name":"a\u0000b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := workflow.VerifyProjectionDefinition([]byte(def))
			if err == nil {
				t.Fatalf("expected refusal of %s", def)
			}
			if !errors.Is(err, workflow.ErrProjectionRoundTrip) {
				t.Fatalf("expected ErrProjectionRoundTrip, got %v", err)
			}
		})
	}
}

// The returned typed definition is exactly Parse's, so the projection can
// fill its typed columns from it.
func TestProjectionDefinition_ReturnsParsedDefinition(t *testing.T) {
	body := rtBase + rtSteps
	wf, def, err := workflow.ProjectionDefinition("workflows/task-default.yaml", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if wf.Mode != "execution" || wf.Path != "workflows/task-default.yaml" || wf.CommitSHA != "" {
		t.Fatalf("unexpected normalised fields: mode=%q path=%q commit=%q", wf.Mode, wf.Path, wf.CommitSHA)
	}
	if bytes.Contains(def, []byte(`"mode"`)) || bytes.Contains(def, []byte(`"path"`)) {
		t.Fatalf("stored JSON must keep the authored shape (no injected mode/path): %s", def)
	}
}
