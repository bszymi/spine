package engine

// INIT-024 TASK-002 (review finding N-5): the governance guard covers
// every directory the projection could install workflow definitions from,
// not only the ones the embedder listed in GovernanceGuard.WorkflowDirs.
// The guarded set is built at decision time from the configured entries,
// the orchestrator's artifacts directory (<ArtifactsDir()>/workflows/,
// whatever order WithArtifactsDir and WithGovernanceGuard were called
// in) and a structural backstop: the parent directory of every changed
// *.yaml / *.yml file whose parent's last segment folds to "workflows".

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

// TestGovernanceGuard_ArtifactsDirWorkflowHeldWithoutWorkflowDirs is AC 1,
// the review PoC's artifacts_dir_wf case: a guard with no WorkflowDirs, an
// orchestrator never told the artifacts directory, and a run that adds
// spine/workflows/task-default.yaml — which a projection configured with
// artifacts_dir "spine" installs as live policy. The merge is held.
func TestGovernanceGuard_ArtifactsDirWorkflowHeldWithoutWorkflowDirs(t *testing.T) {
	g := newGovRepo(t, govRepoOpts{
		runFiles: map[string]string{"spine/workflows/task-default.yaml": govReviewLess},
		guard:    &GovernanceGuard{Enabled: true},
	})
	g.toMerge()
	g.requireHeld("spine/workflows/task-default.yaml without WorkflowDirs")
	if paths := g.heldPaths(); !hasPath(paths, "spine/workflows/task-default.yaml") {
		t.Fatalf("held paths %v do not name spine/workflows/task-default.yaml", paths)
	}
	if got := g.mainFile("spine/workflows/task-default.yaml"); got != "" {
		t.Fatalf("held merge reached main: %q", got)
	}
}

// TestGovernanceGuard_UsesOrchestratorArtifactsDir is AC 2: the
// orchestrator's artifacts directory is read when the merge is decided, so
// WithArtifactsDir called AFTER WithGovernanceGuard still guards
// <dir>/workflows/ — including a nested file the backstop does not catch
// (its parent is "steps", not "workflows"). Without an artifacts directory
// the same nested file merges: the backstop is not what holds it.
func TestGovernanceGuard_UsesOrchestratorArtifactsDir(t *testing.T) {
	const nested = "spine/workflows/steps/x.yaml"
	for _, c := range []struct {
		name  string
		dir   string
		after bool // WithArtifactsDir after WithGovernanceGuard
		held  bool
	}{
		{"artifacts dir set after the guard", "spine", true, true},
		{"artifacts dir set before the guard", "spine", false, true},
		{"artifacts dir spelled ./spine/", "./spine/", true, true},
		{"no artifacts dir", "", true, false},
		{"another artifacts dir", "other", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := govRepoOpts{runFiles: map[string]string{nested: "id: x\n"}}
			if c.after {
				o.guard = &GovernanceGuard{Enabled: true} // enabled by newGovRepo
			}
			g := newGovRepo(t, o)
			g.orch.WithArtifactsDir(c.dir)
			if !c.after {
				if err := g.orch.WithGovernanceGuard(GovernanceGuard{Enabled: true}); err != nil {
					t.Fatalf("WithGovernanceGuard: %v", err)
				}
			}
			g.toMerge()
			if c.held {
				g.requireHeld(c.name)
				if paths := g.heldPaths(); !hasPath(paths, nested) {
					t.Fatalf("held paths %v do not name %s", paths, nested)
				}
			} else {
				g.requireMerged(c.name)
			}
		})
	}
}

// TestGovernanceGuard_BackstopIgnoresNonWorkflowYAML is AC 3: the
// backstop holds a YAML file directly inside any directory named
// "workflows" (case-folded, at any depth), and nothing else.
func TestGovernanceGuard_BackstopIgnoresNonWorkflowYAML(t *testing.T) {
	for _, c := range []struct {
		path string
		held bool
	}{
		{"docs/notes.yaml", false},
		{"docs/workflows/README.md", false},
		{"docs/workflows.yaml", false},
		{"docs/workflows/sub/x.yaml", false},
		{"docs/workflowsx/x.yaml", false},
		{"Docs/WORKFLOWS/x.YML", true},
		{"docs/workflows/x.yaml", true},
		{"a/b/c/workflows/x.yml", true},
		{"docs/wor\u212aflows/x.yaml", true}, // KELVIN SIGN folds onto "k"
		{".github/workflows/ci.yml", true},   // the documented cost (Decision 1)
		{"app/[id]/workflows/x.yaml", true},  // glob characters are literal in a derived entry
	} {
		t.Run(strings.ReplaceAll(c.path, "/", "_"), func(t *testing.T) {
			g := newGovRepo(t, govRepoOpts{
				runFiles: map[string]string{c.path: "id: x\n"},
				guard:    &GovernanceGuard{Enabled: true},
			})
			g.toMerge()
			if c.held {
				g.requireHeld(c.path)
				if paths := g.heldPaths(); !hasPath(paths, c.path) {
					t.Fatalf("held paths %v do not name %s", paths, c.path)
				}
			} else {
				g.requireMerged(c.path)
			}
		})
	}
}

// TestGovernanceGuard_BackstopWithCustomPaths: the backstop and the
// artifacts directory are added to a configured Paths set as well — an
// embedder that replaced the defaults without a workflows entry still
// holds a workflow definition — and Paths keeps guarding what it names.
func TestGovernanceGuard_BackstopWithCustomPaths(t *testing.T) {
	for _, c := range []struct {
		path string
		held bool
	}{
		{"workflows/task-default.yaml", true},
		{"policy/x.md", true},
		{".spine/branch-protection.yaml", false}, // not in the configured set
		{"docs/a.md", false},
	} {
		t.Run(strings.ReplaceAll(c.path, "/", "_"), func(t *testing.T) {
			g := newGovRepo(t, govRepoOpts{
				runFiles: map[string]string{c.path: "id: x\n"},
				guard:    &GovernanceGuard{Enabled: true, Paths: []string{"policy/"}},
			})
			g.toMerge()
			if c.held {
				g.requireHeld(c.path)
			} else {
				g.requireMerged(c.path)
			}
		})
	}
}

// TestGovernanceGuard_BackstopCoversPostMergeRenameFollowing: the
// post-merge assertion applies the backstop to the paths the merge
// actually changed. main renames drafts/wf.yaml into
// spine/workflows/task-x.yaml (a directory no configured entry names)
// after the run forked; the run edits only drafts/wf.yaml, so the
// pre-merge diff names no workflow file, and git's rename following
// carries the edit onto spine/workflows/task-x.yaml. The merge is refused
// and taken back, as for the configured workflows/ directory.
func TestGovernanceGuard_BackstopCoversPostMergeRenameFollowing(t *testing.T) {
	g := newGovRepo(t, govRepoOpts{
		mainFiles: map[string]string{"drafts/wf.yaml": govTaskDefault, "workflows/task-default.yaml": govTaskDefault, "README.md": "r\n"},
		runFiles:  map[string]string{"drafts/wf.yaml": govReviewLess},
		guard:     &GovernanceGuard{Enabled: true},
	})
	if err := os.MkdirAll(filepath.Join(g.dir, "spine", "workflows"), 0o750); err != nil {
		t.Fatal(err)
	}
	g.git("mv", "drafts/wf.yaml", "spine/workflows/task-x.yaml")
	g.git("commit", "-q", "-m", "main moves the draft into spine/workflows/")
	base := g.mainHead()

	g.toMerge()
	if got := g.mainHead(); got != base {
		t.Fatalf("local main = %s, want the base %s (taken back)", got, base)
	}
	if got := g.mainFile("spine/workflows/task-x.yaml"); got != govTaskDefault {
		t.Fatalf("main spine/workflows/task-x.yaml = %q, want it unchanged", got)
	}
	held := g.eventsOf(domain.EventGovernanceMergeHeld)
	if len(held) != 1 || !strings.Contains(string(held[0].Payload), `"post_merge":true`) ||
		!strings.Contains(string(held[0].Payload), "spine/workflows/task-x.yaml") {
		t.Fatalf("post-merge refusal events = %d %v", len(held), held)
	}
	if out := g.primaryOutcome(); out == nil || out.Status != domain.RepositoryMergeStatusFailed {
		t.Fatalf("primary outcome = %+v, want a refusal", out)
	}
}

// TestGovernanceGuard_BackstopHoldsOnlyTheDefinitionFiles: the backstop's
// entries are the matched files, not their directory. A merge that changes
// ci.yml together with a script, a README and a nested file under
// .github/workflows/ is held for ci.yml alone; the same siblings without a
// definition file merge.
func TestGovernanceGuard_BackstopHoldsOnlyTheDefinitionFiles(t *testing.T) {
	siblings := map[string]string{
		".github/workflows/scripts/build.sh":  "#!/bin/sh\n",
		".github/workflows/README.md":         "# CI\n",
		".github/workflows/matrix/linux.json": "{}\n",
		".github/workflows/nested/x.yaml":     "id: x\n",
	}
	t.Run("with a definition file", func(t *testing.T) {
		files := map[string]string{".github/workflows/ci.yml": "on: push\n"}
		for p, c := range siblings {
			files[p] = c
		}
		g := newGovRepo(t, govRepoOpts{runFiles: files, guard: &GovernanceGuard{Enabled: true}})
		g.toMerge()
		g.requireHeld("ci.yml with siblings")
		if paths := g.heldPaths(); len(paths) != 1 || paths[0] != ".github/workflows/ci.yml" {
			t.Fatalf("held paths = %v, want only .github/workflows/ci.yml", paths)
		}
	})
	t.Run("siblings alone", func(t *testing.T) {
		g := newGovRepo(t, govRepoOpts{runFiles: siblings, guard: &GovernanceGuard{Enabled: true}})
		g.toMerge()
		g.requireMerged("siblings without a definition file")
	})
}
