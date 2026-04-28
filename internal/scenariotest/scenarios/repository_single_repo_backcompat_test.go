//go:build scenario

package scenarios_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// repositoriesYAMLPath is the governed catalog file that arrives with
// EPIC-001. A pre-INIT-014 workspace must not have it on disk, and
// running a task must not create it. The scenario asserts both halves.
const repositoriesYAMLPath = ".spine/repositories.yaml"

// assertNoCatalogFile is a scenario step that fails if
// /.spine/repositories.yaml exists in the test repo. Used as both the
// pre- and post-condition of the backward-compatibility scenario.
func assertNoCatalogFile(label string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: label,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			path := filepath.Join(sc.Repo.Dir, repositoriesYAMLPath)
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("expected %s to be absent (single-repo workspace), but it exists", repositoriesYAMLPath)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", path, err)
			}
			return nil
		},
	}
}

// assertNoRepositoryBindings is a scenario step that asserts the
// runtime.repositories table is empty. Used to confirm that running a
// task on a pre-INIT-014 workspace does not silently materialise a
// binding row (for the primary or otherwise — the primary is virtual
// per ADR-013 and the table CHECK forbids the reserved id).
func assertNoRepositoryBindings(label string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: label,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			n, err := sc.Runtime.Store.CountRepositoryBindings(sc.Ctx)
			if err != nil {
				return fmt.Errorf("count repository bindings: %w", err)
			}
			if n != 0 {
				return fmt.Errorf("expected 0 repository binding rows, got %d", n)
			}
			return nil
		},
	}
}

// gitHTTPInfoRefsNoRepoID is a scenario step that hits the smart Git
// HTTP endpoint at /git/info/refs (no repo_id segment) and asserts the
// upload-pack advertisement comes back. This is the URL shape every
// pre-INIT-014 client uses; if a future PR adds a mandatory repo_id
// path segment, this assertion fails and surfaces the regression.
func gitHTTPInfoRefsNoRepoID() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "git-http-info-refs-without-repo-id",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			base := sc.MustGet("gw_url").(string)
			resp, err := http.Get(base + "/git/info/refs?service=git-upload-pack") //nolint:gosec // G107: scenario test, URL is a httptest.Server we just started
			if err != nil {
				return fmt.Errorf("GET /git/info/refs: %w", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("expected 200, got %d (body: %s)", resp.StatusCode, body)
			}
			ct := resp.Header.Get("Content-Type")
			if ct != "application/x-git-upload-pack-advertisement" {
				return fmt.Errorf("expected git upload-pack content-type, got %q", ct)
			}
			if !bytes.Contains(body, []byte("git-upload-pack")) {
				return fmt.Errorf("response does not contain git-upload-pack announcement")
			}
			return nil
		},
	}
}

// TestSingleRepoBackcompat_TaskLifecycleNoCatalog locks in initiative
// success criterion #6 from INIT-014: a workspace that pre-dates
// /.spine/repositories.yaml must keep working with no migration.
//
// The scenario boots a workspace seeded with governance + workflow
// fixtures (the same shape an INIT-008 deployment shipped with) and
// asserts:
//
//   - Pre-condition: no /.spine/repositories.yaml on disk and no rows
//     in runtime.repositories.
//   - A standard task run starts, creates a branch, executes, reviews,
//     merges back to main, and cleans the branch up — the whole
//     lifecycle that single-repo workspaces use today.
//   - Post-condition: still no catalog file and still zero binding
//     rows (the run path must not implicitly materialise either).
//   - Git smart HTTP at /git/info/refs (no repo_id segment) still
//     responds with the upload-pack advertisement so existing clones
//     keep working.
//
// This scenario is the regression gate for EPIC-001 through EPIC-007:
// any future change that starts requiring a catalog file or binding
// row to run a task fails this test loudly.
func TestSingleRepoBackcompat_TaskLifecycleNoCatalog(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "single-repo-backcompat-task-lifecycle",
		Description: "Pre-INIT-014 workspace runs a full task lifecycle without growing a catalog file or binding rows",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			assertNoCatalogFile("precondition-no-catalog-file"),
			assertNoRepositoryBindings("precondition-no-binding-rows"),

			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				standardRunMergeWorkflowYAML,
				"seed task-default workflow",
			),
			scenarioEngine.SeedHierarchy("INIT-901", "EPIC-901", "TASK-901"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-901/epics/epic-901/tasks/task-901.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertBranchExists(),

			scenarioEngine.WriteOnBranch(
				"initiatives/init-901/epics/epic-901/tasks/task-901-deliverable.md",
				"# Deliverable\nSingle-repo backward-compat run output.\n",
				"Add deliverable",
			),

			scenarioEngine.SubmitStepResult("completed", "deliverable"),
			scenarioEngine.AssertCurrentStep("review"),
			scenarioEngine.SubmitStepResult("accepted"),
			scenarioEngine.AssertRunCompleted(),

			scenarioEngine.AssertFileExists("initiatives/init-901/epics/epic-901/tasks/task-901-deliverable.md"),
			scenarioEngine.AssertBranchNotExists(),

			assertNoCatalogFile("postcondition-no-catalog-file"),
			assertNoRepositoryBindings("postcondition-no-binding-rows"),

			setupGitHTTPServer(),
			enableGitHTTPExport(),
			gitHTTPInfoRefsNoRepoID(),
		},
	})
}
