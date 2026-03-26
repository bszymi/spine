package engine

import (
	"fmt"
	"strings"
)

// ArtifactOpts provides overrides for fixture builders.
// Zero-value fields use defaults.
type ArtifactOpts struct {
	ID      string
	Title   string
	Status  string
	Created string // Initiative only
	Epic    string // Task only: canonical path to parent epic
	Init    string // Epic/Task: canonical path to parent initiative
	Links   []LinkOpt
	Body    string // markdown body after frontmatter
}

// LinkOpt defines a link for frontmatter generation.
type LinkOpt struct {
	Type   string
	Target string
}

// FixtureGovernance creates a valid Governance artifact and commits it.
// Returns the artifact path.
func FixtureGovernance(sc *ScenarioContext, path string, opts ArtifactOpts) string {
	sc.T.Helper()
	defaults(&opts, "Governance Doc", "Draft")
	content := buildFrontmatter("Governance", "", opts) + buildBody(opts, "Governance Document")
	return createFixture(sc, path, content)
}

// FixtureArchitecture creates a valid Architecture artifact and commits it.
func FixtureArchitecture(sc *ScenarioContext, path string, opts ArtifactOpts) string {
	sc.T.Helper()
	defaults(&opts, "Architecture Doc", "Living Document")
	content := buildFrontmatter("Architecture", "", opts) + buildBody(opts, "Architecture Document")
	return createFixture(sc, path, content)
}

// FixtureInitiative creates a valid Initiative artifact and commits it.
func FixtureInitiative(sc *ScenarioContext, path string, opts ArtifactOpts) string {
	sc.T.Helper()
	defaults(&opts, "Test Initiative", "Draft")
	if opts.ID == "" {
		opts.ID = "INIT-001"
	}
	if opts.Created == "" {
		opts.Created = "2026-01-01"
	}
	content := buildFrontmatter("Initiative", opts.ID, opts) + buildBody(opts, "Test Initiative")
	return createFixture(sc, path, content)
}

// FixtureEpic creates a valid Epic artifact linked to a parent Initiative.
func FixtureEpic(sc *ScenarioContext, path string, opts ArtifactOpts) string {
	sc.T.Helper()
	defaults(&opts, "Test Epic", "Pending")
	if opts.ID == "" {
		opts.ID = "EPIC-001"
	}
	if opts.Init == "" {
		opts.Init = "/initiatives/INIT-001/initiative.md"
	}
	// Ensure parent link exists.
	if !hasLinkType(opts.Links, "parent") {
		opts.Links = append(opts.Links, LinkOpt{Type: "parent", Target: opts.Init})
	}
	content := buildEpicFrontmatter(opts) + buildBody(opts, "Test Epic")
	return createFixture(sc, path, content)
}

// FixtureTask creates a valid Task artifact linked to a parent Epic and Initiative.
func FixtureTask(sc *ScenarioContext, path string, opts ArtifactOpts) string {
	sc.T.Helper()
	defaults(&opts, "Test Task", "Pending")
	if opts.ID == "" {
		opts.ID = "TASK-001"
	}
	if opts.Epic == "" {
		opts.Epic = "/initiatives/INIT-001/epics/EPIC-001/epic.md"
	}
	if opts.Init == "" {
		opts.Init = "/initiatives/INIT-001/initiative.md"
	}
	// Ensure parent link exists.
	if !hasLinkType(opts.Links, "parent") {
		opts.Links = append(opts.Links, LinkOpt{Type: "parent", Target: opts.Epic})
	}
	content := buildTaskFrontmatter(opts) + buildBody(opts, "Test Task")
	return createFixture(sc, path, content)
}

// FixtureHierarchy creates a complete Initiative -> Epic -> Task hierarchy
// and returns the paths. All artifacts are committed to the test repo.
func FixtureHierarchy(sc *ScenarioContext, initID, epicID, taskID string) (initPath, epicPath, taskPath string) {
	sc.T.Helper()

	slug := strings.ToLower(initID)
	epicSlug := strings.ToLower(epicID)

	initPath = fmt.Sprintf("initiatives/%s/initiative.md", slug)
	epicPath = fmt.Sprintf("initiatives/%s/epics/%s/epic.md", slug, epicSlug)
	taskPath = fmt.Sprintf("initiatives/%s/epics/%s/tasks/%s.md", slug, epicSlug, strings.ToLower(taskID))

	canonicalInit := "/" + initPath
	canonicalEpic := "/" + epicPath

	FixtureInitiative(sc, initPath, ArtifactOpts{ID: initID})
	FixtureEpic(sc, epicPath, ArtifactOpts{ID: epicID, Init: canonicalInit})
	FixtureTask(sc, taskPath, ArtifactOpts{ID: taskID, Epic: canonicalEpic, Init: canonicalInit})

	return initPath, epicPath, taskPath
}

// --- internal helpers ---

func defaults(opts *ArtifactOpts, defaultTitle, defaultStatus string) {
	if opts.Title == "" {
		opts.Title = defaultTitle
	}
	if opts.Status == "" {
		opts.Status = defaultStatus
	}
}

func createFixture(sc *ScenarioContext, path, content string) string {
	sc.T.Helper()
	a, err := sc.Runtime.Artifacts.Create(sc.Ctx, path, content)
	if err != nil {
		sc.T.Fatalf("create fixture %s: %v", path, err)
	}
	return a.Path
}

func buildFrontmatter(artifactType, id string, opts ArtifactOpts) string {
	var b strings.Builder
	b.WriteString("---\n")
	if id != "" {
		fmt.Fprintf(&b, "id: %s\n", id)
	}
	fmt.Fprintf(&b, "type: %s\n", artifactType)
	fmt.Fprintf(&b, "title: %q\n", opts.Title)
	fmt.Fprintf(&b, "status: %s\n", opts.Status)
	if opts.Created != "" {
		fmt.Fprintf(&b, "created: %q\n", opts.Created)
	}
	if len(opts.Links) > 0 {
		b.WriteString("links:\n")
		for _, l := range opts.Links {
			fmt.Fprintf(&b, "  - type: %s\n    target: %s\n", l.Type, l.Target)
		}
	}
	b.WriteString("---\n\n")
	return b.String()
}

func buildEpicFrontmatter(opts ArtifactOpts) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", opts.ID)
	b.WriteString("type: Epic\n")
	fmt.Fprintf(&b, "title: %q\n", opts.Title)
	fmt.Fprintf(&b, "status: %s\n", opts.Status)
	fmt.Fprintf(&b, "initiative: %s\n", opts.Init)
	if len(opts.Links) > 0 {
		b.WriteString("links:\n")
		for _, l := range opts.Links {
			fmt.Fprintf(&b, "  - type: %s\n    target: %s\n", l.Type, l.Target)
		}
	}
	b.WriteString("---\n\n")
	return b.String()
}

func buildTaskFrontmatter(opts ArtifactOpts) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", opts.ID)
	b.WriteString("type: Task\n")
	fmt.Fprintf(&b, "title: %q\n", opts.Title)
	fmt.Fprintf(&b, "status: %s\n", opts.Status)
	fmt.Fprintf(&b, "epic: %s\n", opts.Epic)
	fmt.Fprintf(&b, "initiative: %s\n", opts.Init)
	if len(opts.Links) > 0 {
		b.WriteString("links:\n")
		for _, l := range opts.Links {
			fmt.Fprintf(&b, "  - type: %s\n    target: %s\n", l.Type, l.Target)
		}
	}
	b.WriteString("---\n\n")
	return b.String()
}

func buildBody(opts ArtifactOpts, defaultHeading string) string {
	if opts.Body != "" {
		return opts.Body
	}
	return fmt.Sprintf("# %s\n", defaultHeading)
}

func hasLinkType(links []LinkOpt, linkType string) bool {
	for _, l := range links {
		if l.Type == linkType {
			return true
		}
	}
	return false
}

// SeedHierarchy returns a step that creates a full Initiative -> Epic -> Task
// hierarchy and stores the paths in scenario state.
func SeedHierarchy(initID, epicID, taskID string) Step {
	return Step{
		Name: fmt.Sprintf("seed-hierarchy-%s-%s-%s", initID, epicID, taskID),
		Action: func(sc *ScenarioContext) error {
			initPath, epicPath, taskPath := FixtureHierarchy(sc, initID, epicID, taskID)
			sc.Set("init_path", initPath)
			sc.Set("epic_path", epicPath)
			sc.Set("task_path", taskPath)
			return nil
		},
	}
}
