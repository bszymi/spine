package engine_test

// INIT-023 wave 3 review (codex round 1, P2; TASK-003 with TASK-005): the
// status cascade took every .md file the run added from the diff. Since
// TASK-003's NUL-delimited Diff lists a path with a control character raw
// (git used to C-quote it, so its read failed and it was skipped), such a
// file was read and rewritten, and under merge binding CommitFilesOnto —
// which refuses a control byte in a path — failed the whole cascade
// commit: the run merged with its task's status never updated. A path
// discovery refuses is never a cascade path. Real git.CLIClient, binding
// on and off.

import (
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

func TestCascade_SkipsAnAddedFileAtAnUnsafePath(t *testing.T) {
	const unsafeChild = "docs/bad\tname.md"
	unsafeDoc := strings.Replace(childDraft, "TASK-002", "TASK-003", 1)
	for _, bindingOn := range []bool{true, false} {
		name := "binding off"
		if bindingOn {
			name = "binding on"
		}
		t.Run(name, func(t *testing.T) {
			e := newRegEnv(t, reviewWorkflow(), regOpts{})
			r := startSecondRun(t, e, reviewWorkflow(), bindingOn)
			first := e.branch
			e.branch = r.run.BranchName
			e.commitOnRun("deliverable", map[string]string{
				"tasks/child.md": childDraft,
				unsafeChild:      unsafeDoc,
			})
			e.branch = first
			r.submit(t, 0, "completed")
			r.submit(t, 1, "accepted")
			if got := r.runNow(t); got.Status != domain.RunStatusCompleted {
				t.Fatalf("run status = %s, want completed", got.Status)
			}
			for path, want := range map[string]string{
				"tasks/t.md":     "Completed",
				"tasks/child.md": "Completed",
				unsafeChild:      "Draft", // merged as reviewed, never rewritten
			} {
				if got := frontMatterStatus(e.mainFile("main", path)); got != want {
					t.Errorf("main:%q status = %q, want %q", path, got, want)
				}
			}
		})
	}
}
