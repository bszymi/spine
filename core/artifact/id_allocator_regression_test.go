package artifact_test

// INIT-023 TASK-003 AC 1: the design-phase PoC for audit P3-9, committed
// with its assertion unchanged. On Spine 61994fe ListFiles returned the
// C-quoted path of an epic whose slug holds a non-ASCII letter, which
// fails NextID's parent-directory prefix check, so NextID allocated
// EPIC-001 a second time. With raw NUL-delimited listings it sees the
// epic and allocates EPIC-002.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
)

func TestT003_NextIDSeesNonASCIISlug(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	p := filepath.Join(dir, "initiatives/INIT-001/epics/EPIC-001-r\xc3\xa9sum\xc3\xa9/epic.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "seed")
	id, err := artifact.NextID(context.Background(), git.NewCLIClient(dir), "initiatives/INIT-001/epics", domain.ArtifactTypeEpic, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if id != "EPIC-002" {
		t.Errorf("NextID = %s, want EPIC-002 (EPIC-001 already exists under a non-ASCII slug)", id)
	}
}
