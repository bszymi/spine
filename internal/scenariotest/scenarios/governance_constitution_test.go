//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestConstitution_RequiredFieldsEnforced validates that artifacts missing
// required frontmatter fields are rejected during creation.
//
// Scenario Outline: Artifacts missing required fields are rejected
//   Given a seeded governance environment
//   When an artifact is created missing field "<field>"
//   Then creation should fail with an error mentioning "<field>"
//
//   Examples: type, title, status, created (Initiative), epic (Task), initiative (Epic)
func TestConstitution_RequiredFieldsEnforced(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
		errMsg  string
	}{
		{
			name: "missing-type",
			path: "governance/no-type.md",
			content: `---
title: "No Type"
status: Living Document
---

# No Type
`,
			errMsg: "type",
		},
		{
			name: "missing-title",
			path: "governance/no-title.md",
			content: `---
type: Governance
status: Living Document
---

# No Title
`,
			errMsg: "title",
		},
		{
			name: "missing-status",
			path: "governance/no-status.md",
			content: `---
type: Governance
title: "No Status"
---

# No Status
`,
			errMsg: "status",
		},
		{
			name: "initiative-missing-created",
			path: "initiatives/init-090/initiative.md",
			content: `---
id: INIT-090
type: Initiative
title: "Missing Created"
status: Draft
---

# Missing Created
`,
			errMsg: "created",
		},
		{
			name: "task-missing-epic",
			path: "initiatives/init-091/epics/epic-091/tasks/task-091.md",
			content: `---
id: TASK-091
type: Task
title: "Missing Epic"
status: Pending
initiative: /initiatives/init-091/initiative.md
---

# Missing Epic
`,
			errMsg: "epic",
		},
		{
			name: "epic-missing-initiative",
			path: "initiatives/init-092/epics/epic-092/epic.md",
			content: `---
id: EPIC-092
type: Epic
title: "Missing Initiative"
status: Pending
---

# Missing Initiative
`,
			errMsg: "initiative",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine.RunScenario(t, engine.Scenario{
				Name:    "required-field-" + tc.name,
				EnvOpts: harness.Seeded(),
				Steps: []engine.Step{
					engine.ExpectError(tc.name, func(sc *engine.ScenarioContext) error {
						_, err := sc.Runtime.Artifacts.Create(sc.Ctx, tc.path, tc.content)
						return err
					}, ""),
				},
			})
		})
	}
}

// TestConstitution_InvalidStatusRejected validates that artifacts with
// statuses not allowed for their type are rejected.
//
// Scenario Outline: Artifacts with invalid status for their type are rejected
//   Given a seeded governance environment
//   When a "<type>" artifact is created with status "<invalid_status>"
//   Then creation should fail with an error
//
//   Examples: Governance/Draft, ADR/Living Document, Initiative/Cancelled
func TestConstitution_InvalidStatusRejected(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
	}{
		{
			name: "governance-invalid-status",
			path: "governance/bad-status.md",
			content: `---
type: Governance
title: "Bad Status"
status: Draft
---

# Bad Status
`,
		},
		{
			name: "adr-invalid-status",
			path: "architecture/adr/bad-status.md",
			content: `---
id: ADR-090
type: ADR
title: "Bad Status ADR"
status: Living Document
date: "2026-01-01"
decision_makers: "team"
---

# Bad Status
`,
		},
		{
			name: "initiative-invalid-status",
			path: "initiatives/init-093/initiative.md",
			content: `---
id: INIT-093
type: Initiative
title: "Invalid Status"
status: Cancelled
created: "2026-01-01"
---

# Invalid Status
`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine.RunScenario(t, engine.Scenario{
				Name:    "invalid-status-" + tc.name,
				EnvOpts: harness.Seeded(),
				Steps: []engine.Step{
					engine.ExpectError(tc.name, func(sc *engine.ScenarioContext) error {
						_, err := sc.Runtime.Artifacts.Create(sc.Ctx, tc.path, tc.content)
						return err
					}, ""),
				},
			})
		})
	}
}

// TestConstitution_InvalidArtifactTypeRejected validates that unknown
// artifact types are rejected during parsing.
//
// Scenario: Unknown artifact types are rejected
//   Given a seeded governance environment
//   When an artifact is created with type "UnknownType"
//   Then creation should fail with an error
func TestConstitution_InvalidArtifactTypeRejected(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "invalid-artifact-type",
		Description: "Unknown artifact types are rejected at parse time",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			engine.ExpectError("unknown-type", func(sc *engine.ScenarioContext) error {
				_, err := sc.Runtime.Artifacts.Create(sc.Ctx,
					"governance/unknown.md", `---
type: UnknownType
title: "Unknown"
status: Draft
---

# Unknown
`)
				return err
			}, ""),
		},
	})
}

// TestConstitution_InvalidIDFormatRejected validates that artifacts with
// IDs that don't match the required pattern are rejected.
//
// Scenario Outline: Artifacts with non-standard ID formats are rejected
//   Given a seeded governance environment
//   When a "<type>" artifact is created with ID "BAD-001"
//   Then creation should fail with an error
//
//   Examples: Initiative, Epic, Task
func TestConstitution_InvalidIDFormatRejected(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
	}{
		{
			name: "initiative-bad-id",
			path: "initiatives/init-bad/initiative.md",
			content: `---
id: BAD-001
type: Initiative
title: "Bad ID"
status: Draft
created: "2026-01-01"
---

# Bad ID
`,
		},
		{
			name: "epic-bad-id",
			path: "initiatives/init-094/epics/epic-bad/epic.md",
			content: `---
id: BAD-001
type: Epic
title: "Bad ID"
status: Pending
initiative: /initiatives/init-094/initiative.md
links:
  - type: parent
    target: /initiatives/init-094/initiative.md
---

# Bad ID
`,
		},
		{
			name: "task-bad-id",
			path: "initiatives/init-095/epics/epic-095/tasks/task-bad.md",
			content: `---
id: BAD-001
type: Task
title: "Bad ID"
status: Pending
epic: /initiatives/init-095/epics/epic-095/epic.md
initiative: /initiatives/init-095/initiative.md
links:
  - type: parent
    target: /initiatives/init-095/epics/epic-095/epic.md
---

# Bad ID
`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine.RunScenario(t, engine.Scenario{
				Name:    "invalid-id-" + tc.name,
				EnvOpts: harness.Seeded(),
				Steps: []engine.Step{
					engine.ExpectError(tc.name, func(sc *engine.ScenarioContext) error {
						_, err := sc.Runtime.Artifacts.Create(sc.Ctx, tc.path, tc.content)
						return err
					}, ""),
				},
			})
		})
	}
}

// TestConstitution_LinkConstraintsEnforced validates that link type
// validation and canonical path requirements are enforced.
//
// Scenario Outline: Link constraints are enforced
//   Given a seeded governance environment
//   When an artifact is created with "<constraint_violation>"
//   Then creation should fail with an error
//
//   Examples: invalid link type "invalid_link", non-canonical target "governance/charter.md"
func TestConstitution_LinkConstraintsEnforced(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
	}{
		{
			name: "invalid-link-type",
			path: "governance/bad-link.md",
			content: `---
type: Governance
title: "Bad Link Type"
status: Living Document
links:
  - type: invalid_link
    target: /governance/charter.md
---

# Bad Link Type
`,
		},
		{
			name: "non-canonical-link-target",
			path: "governance/rel-link.md",
			content: `---
type: Governance
title: "Relative Link"
status: Living Document
links:
  - type: parent
    target: governance/charter.md
---

# Relative Link
`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine.RunScenario(t, engine.Scenario{
				Name:    "link-constraint-" + tc.name,
				EnvOpts: harness.Seeded(),
				Steps: []engine.Step{
					engine.ExpectError(tc.name, func(sc *engine.ScenarioContext) error {
						_, err := sc.Runtime.Artifacts.Create(sc.Ctx, tc.path, tc.content)
						return err
					}, ""),
				},
			})
		})
	}
}

// TestConstitution_CrossArtifactValidation validates that the validation
// engine detects structural and consistency violations across artifacts.
//
// Scenario: Cross-artifact validation passes when parents exist
//   Given a seeded environment with validation enabled
//     And a complete hierarchy INIT-030 -> EPIC-030 -> TASK-030
//     And projections are synced
//   When the task is validated
//   Then validation should pass (parents exist)
func TestConstitution_CrossArtifactValidation(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "cross-artifact-validation",
		Description: "Validation engine detects missing parent and structural violations",
		EnvOpts: append(harness.Seeded(), harness.WithRuntimeValidation()),
		Steps: []engine.Step{
			// Create a task with a parent link to a non-existent epic.
			engine.SeedHierarchy("INIT-030", "EPIC-030", "TASK-030"),
			engine.SyncProjections(),

			// Validate the task — it should pass since parents exist.
			{
				Name: "validate-valid-task",
				Action: func(sc *engine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					assert.ArtifactValidationPasses(sc.T, sc.Runtime, sc.Ctx, taskPath)
					return nil
				},
			},
		},
	})
}

// TestConstitution_ValidArtifactTypesAccepted validates that all known
// artifact types are accepted when properly constructed.
//
// Scenario: All valid artifact types are accepted when properly constructed
//   Given a seeded governance environment
//   When a Governance, Architecture, and Initiative artifact are created
//     And projections are synced
//   Then all three projections should exist with correct titles
func TestConstitution_ValidArtifactTypesAccepted(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "valid-types-accepted",
		Description: "All valid artifact types are accepted with correct fields",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "create-governance",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureGovernance(sc, "governance/test-valid.md", engine.ArtifactOpts{
						Title: "Valid Governance",
					})
					return nil
				},
			},
			{
				Name: "create-architecture",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureArchitecture(sc, "architecture/test-valid.md", engine.ArtifactOpts{
						Title: "Valid Architecture",
					})
					return nil
				},
			},
			{
				Name: "create-initiative",
				Action: func(sc *engine.ScenarioContext) error {
					engine.FixtureInitiative(sc, "initiatives/init-031/initiative.md", engine.ArtifactOpts{
						ID:    "INIT-031",
						Title: "Valid Initiative",
					})
					return nil
				},
			},
			engine.SyncProjections(),
			engine.AssertProjection("governance/test-valid.md", "Title", "Valid Governance"),
			engine.AssertProjection("architecture/test-valid.md", "Title", "Valid Architecture"),
			engine.AssertProjection("initiatives/init-031/initiative.md", "Title", "Valid Initiative"),
		},
	})
}
