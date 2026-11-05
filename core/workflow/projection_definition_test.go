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

// nonStringKeyInputs pass strict Parse, but a mapping in each has a key
// yaml.v3 does not read as a plain string, so decoding into `any` gives a
// map[interface{}]interface{} (INIT-024 TASK-014). Go 1.26's json.Marshal
// refused that type whatever its keys, and so the projection refused
// these files. Go 1.27 turns on the v2-backed encoding/json engine, which
// marshals integer and tagged-string keys as JSON strings (`{"1":"x"}`)
// and would accept most of them. The refusal is now explicit, so these
// files are refused under every Go version: SMP compiles this package
// with its own toolchain.
func nonStringKeyInputs() map[string]string {
	commit := func(m string) string { return rtBase + rtStepsWith("", ", commit: "+m) }
	return map[string]string{
		"int_key":             commit("{1: x}"),
		"negative_int_key":    commit("{-1: x}"),
		"bool_key":            commit("{true: x}"),
		"null_key":            commit("{~: x}"),
		"float_key":           commit("{1.5: x}"),
		"timestamp_key":       commit("{2026-10-06: x}"),
		"mixed_keys":          commit("{a: x, 1: y}"),
		"binary_tagged_key":   commit("{!!binary aGk=: x}"),
		"int_key_block_style": rtBase + "steps:\n  - id: a\n    name: A\n    type: manual\n    outcomes:\n      - id: ok\n        name: OK\n        next_step: end\n        commit:\n          1: x\n",
		"custom_tagged_key":   commit("{!k a: x}"),
		// The key reads as `description` once decoded, so strict Parse
		// takes it for the known field; a YAML viewer shows a base64 blob.
		"top_level_binary_tagged_key": strings.Replace(rtBase, "description: d", "!!binary ZGVzY3JpcHRpb24=: d", 1) + rtSteps,
		"validation_config":           rtBase + rtStepsWith("    validation:\n      - type: t\n        config: {1: x}\n", ""),
		"precondition_config":         rtBase + rtStepsWith("    preconditions:\n      - type: t\n        config: {2: y}\n", ""),
	}
}

func TestProjectionDefinition_RefusesNonStringMappingKeys(t *testing.T) {
	for name, body := range nonStringKeyInputs() {
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
			// The explicit check refuses it, not the JSON encoder: the
			// result must not depend on which encoding/json engine the
			// binary was built with.
			if !strings.Contains(err.Error(), "not a plain YAML string") {
				t.Fatalf("the refusal must come from the explicit mapping-key check, got %v", err)
			}
		})
	}
}

// INIT-024 TASK-014: `.nan` and `.inf` are strings to strict Parse (the
// fields hold strings) but floats to the `any` decode, and JSON cannot
// hold them. Both encoding/json engines refuse them; the explicit check
// keeps the refusal independent of the engine.
func TestProjectionDefinition_RefusesNonFiniteNumbers(t *testing.T) {
	for name, body := range map[string]string{
		"nan_name":          strings.Replace(rtBase, "name: n", "name: .nan", 1) + rtSteps,
		"inf_description":   strings.Replace(rtBase, "description: d", "description: .inf", 1) + rtSteps,
		"neg_inf_in_commit": rtBase + rtStepsWith("", ", commit: {k: -.inf}"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := workflow.Parse("workflows/task-default.yaml", []byte(body)); err != nil {
				t.Fatalf("precondition: Parse must accept this input, got %v", err)
			}
			_, def, err := workflow.ProjectionDefinition("workflows/task-default.yaml", []byte(body))
			if !errors.Is(err, workflow.ErrProjectionRoundTrip) {
				t.Fatalf("expected ErrProjectionRoundTrip, got err=%v def=%s", err, def)
			}
			if !strings.Contains(err.Error(), "non-finite number") {
				t.Fatalf("the refusal must come from the explicit check, got %v", err)
			}
		})
	}
}

// Review round 1 (finding H): a value the projection row's btree-indexed
// columns cannot hold — PostgreSQL refuses an index entry over about
// 2.7 KB — is a round-trip rejection, not a store error that wedges the
// sync with the previous row left bindable. The path, id and status are
// capped at 1024 bytes; unindexed name and version are not.
func TestProjectionDefinition_RefusesOverLongIndexedFields(t *testing.T) {
	long := strings.Repeat("x", 6000)
	at := strings.Repeat("y", 1024)
	over := strings.Repeat("y", 1025)
	for _, tc := range []struct {
		name, path, body string
		refused          bool
	}{
		{"id 6000", "workflows/task-default.yaml", strings.Replace(rtBase, "id: task-default", "id: "+long, 1) + rtSteps, true},
		{"id 1025", "workflows/task-default.yaml", strings.Replace(rtBase, "id: task-default", "id: "+over, 1) + rtSteps, true},
		{"id 1024", "workflows/task-default.yaml", strings.Replace(rtBase, "id: task-default", "id: "+at, 1) + rtSteps, false},
		{"status 6000", "workflows/task-default.yaml", strings.Replace(rtBase, "status: Active", "status: "+long, 1) + rtSteps, true},
		{"path 6000", "workflows/" + long + ".yaml", rtBase + rtSteps, true},
		{"name 6000 unindexed", "workflows/task-default.yaml", strings.Replace(rtBase, "name: n", "name: "+long, 1) + rtSteps, false},
		{"version 6000 unindexed", "workflows/task-default.yaml", strings.Replace(rtBase, `version: "1"`, `version: "`+long+`"`, 1) + rtSteps, false},
		// The alias form a reviewer suggested hiding it with.
		{"id aliased to a long description", "workflows/task-default.yaml",
			strings.Replace(strings.Replace(rtBase, "description: d", "description: &desc "+long, 1), "id: task-default\n", "", 1) + "id: *desc\n" + rtSteps, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := workflow.Parse(tc.path, []byte(tc.body)); err != nil {
				t.Fatalf("precondition: Parse must accept this input, got %v", err)
			}
			_, _, err := workflow.ProjectionDefinition(tc.path, []byte(tc.body))
			if !tc.refused {
				if err != nil {
					t.Fatalf("expected acceptance, got %v", err)
				}
				return
			}
			if !errors.Is(err, workflow.ErrProjectionRoundTrip) {
				t.Fatalf("expected ErrProjectionRoundTrip, got %v", err)
			}
			if strings.Contains(err.Error(), long) || len(err.Error()) > 600 {
				t.Fatalf("the error must not echo the over-long value (%d bytes)", len(err.Error()))
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
		// The folding reader decodes one value and ignores what follows;
		// the strict reading refuses it, as json.Unmarshal did.
		"trailing_value":   `{"id":"x"} {"id":"y"}`,
		"trailing_garbage": `{"id":"x"} x`,
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

// INIT-023 codex 6.1 catch-up: every definition ProjectionDefinition
// stores must pass VerifyProjectionDefinition, or a later rejected edit of
// the file deletes the row (it looks unverifiable) instead of keeping it
// quarantined for the runs pinned to it. An integer the stored JSON holds
// exactly but float64 cannot (above 2^53) used to fail the verification:
// its strict reading decoded the JSON into `any`, which rounds through
// float64.
//
// Mutation check: decoding into `any` without UseNumber fails every
// "above_2^53" case.
func TestVerifyProjectionDefinition_AcceptsEveryStoredInteger(t *testing.T) {
	for _, n := range []string{
		"3",
		"9007199254740992",    // 2^53: still exact in float64
		"9007199254740993",    // 2^53+1: above_2^53
		"4611686018427387905", // 2^62+1: above_2^53
		"9223372036854775807", // max int64: above_2^53
		"-9007199254740993",   // above_2^53, negative
	} {
		for field, body := range map[string]string{
			"retry.limit": rtBase + rtStepsWith("    retry: {limit: "+n+", backoff: fixed}\n", ""),
			"divergence_points.min_branches+max_branches": rtBase + rtSteps +
				"divergence_points:\n  - {id: d, mode: exploratory, min_branches: " + n + ", max_branches: " + n + "}\n",
			"convergence_points.min_branches": rtBase + rtSteps +
				"convergence_points:\n  - {id: c, strategy: select_one, entry_policy: all_branches_terminal, evaluation_step: a, min_branches: " + n + "}\n",
		} {
			t.Run(field+"="+n, func(t *testing.T) {
				_, def, err := workflow.ProjectionDefinition("workflows/task-default.yaml", []byte(body))
				if err != nil {
					t.Fatalf("precondition: ProjectionDefinition accepts the body: %v", err)
				}
				if !bytes.Contains(def, []byte(n)) {
					t.Fatalf("precondition: the stored JSON holds %s exactly: %s", n, def)
				}
				if err := workflow.VerifyProjectionDefinition(def); err != nil {
					t.Fatalf("a stored definition ProjectionDefinition accepted fails verification: %v", err)
				}
			})
		}
	}
}

// The exact reading must not let a stored integer that no int field can
// hold through: it is refused by the folding reader (step 1) as before.
func TestVerifyProjectionDefinition_RefusesAnIntegerNoFieldHolds(t *testing.T) {
	for _, def := range []string{
		`{"id":"x","steps":[{"id":"a","retry":{"limit":9223372036854775808}}]}`,
		`{"id":"x","steps":[{"id":"a","retry":{"limit":1.5}}]}`,
		`{"id":"x","steps":[{"id":"a","retry":{"limit":1e3}}]}`,
	} {
		err := workflow.VerifyProjectionDefinition([]byte(def))
		if !errors.Is(err, workflow.ErrProjectionRoundTrip) {
			t.Fatalf("VerifyProjectionDefinition(%s) = %v, want ErrProjectionRoundTrip", def, err)
		}
	}
}
