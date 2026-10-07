package workflow_test

// Regression tests for INIT-023 TASK-004 (security audit 2026-10-06,
// finding F5, workflow sink). On Spine 61994fe workflow.Service joined
// scope.RepoDir + workflows/<id>.yaml and wrote with no containment check
// of any kind; every TestF5_* here fails there.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/worktreefs"
)

func wfGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=a@x", "-c", "user.name=a"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func wfRequireUnsafePath(t *testing.T, err error) {
	t.Helper()
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrInvalidParams {
		t.Fatalf("want ErrInvalidParams (unsafe path) refused before any write, got %v", err)
	}
	if !errors.Is(err, worktreefs.ErrUnsafePath) {
		t.Fatalf("want errors.Is(err, worktreefs.ErrUnsafePath), got %v", err)
	}
}

func wfPlantLink(t *testing.T, repo, rel, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(repo, rel)); err != nil {
		t.Fatal(err)
	}
	wfGit(t, repo, "add", "-A")
	wfGit(t, repo, "commit", "-q", "-m", "plant")
}

func TestF5_WorkflowCreate_DanglingLeaf_NoEscape(t *testing.T) {
	svc, repo := newSvc(t)
	outside := t.TempDir()
	wfPlantLink(t, repo, "workflows/evil.yaml", filepath.Join(outside, "x.yaml"))

	_, err := svc.Create(ctxWithActor(), "evil", fmtYAML("evil", "1.0"))
	if _, statErr := os.Lstat(filepath.Join(outside, "x.yaml")); statErr == nil {
		t.Errorf("workflow written outside the repo")
	}
	wfRequireUnsafePath(t, err)
}

func TestF5_WorkflowCreate_SymlinkedWorkflowsDir_TypedRefusal(t *testing.T) {
	svc, repo := newSvc(t)
	if err := os.MkdirAll(filepath.Join(repo, "ci", "wf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ci", "wf", ".keep"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	wfPlantLink(t, repo, "workflows", "ci/wf")

	_, err := svc.Create(ctxWithActor(), "probe", fmtYAML("probe", "1.0"))
	if _, statErr := os.Lstat(filepath.Join(repo, "ci", "wf", "probe.yaml")); statErr == nil {
		t.Errorf("workflow written through the symlinked workflows/ directory")
	}
	// Owner question 1 default: refuse (on 61994fe: 500 "beyond a symbolic link").
	wfRequireUnsafePath(t, err)
}

func TestF5_WorkflowUpdate_SymlinkToOutsideWorkflow_NotTouched(t *testing.T) {
	svc, repo := newSvc(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.yaml")
	if err := os.WriteFile(victim, []byte(fmtYAML("evil", "1.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	wfPlantLink(t, repo, "workflows/evil.yaml", victim)
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(victim, past, past); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Update(ctxWithActor(), "evil", fmtYAML("evil", "2.0"))
	fi, _ := os.Stat(victim)
	if !fi.ModTime().Equal(past) {
		t.Errorf("outside workflow file was written (mtime moved to %v)", fi.ModTime())
	}
	wfRequireUnsafePath(t, err)
}

// TestF5_WorkflowUpdate_LeafLinkToDevZero_RefusedWithoutRead is AC15(a):
// on 61994fe the os.Stat existence check passed for a link to /dev/zero (a
// character device exists) and readFromDisk's os.ReadFile never reached
// EOF — the F4 cross-tenant OOM through a filesystem path. The leaf-link
// refusal must answer within 2 s having allocated almost nothing.
//
// A regression would read /dev/zero until the machine runs out of memory,
// so a watchdog ends the process instead of letting the read continue
// after the test has failed.
func TestF5_WorkflowUpdate_LeafLinkToDevZero_RefusedWithoutRead(t *testing.T) {
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skip("/dev/zero not available")
	}
	svc, repo := newSvc(t)
	wfPlantLink(t, repo, "workflows/zero.yaml", "/dev/zero")

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	done := make(chan error, 1)
	go func() {
		_, err := svc.Update(ctxWithActor(), "zero", fmtYAML("zero", "2.0"))
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case err := <-done:
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			if grew := after.TotalAlloc - before.TotalAlloc; grew >= 16<<20 {
				t.Errorf("Update allocated %d MiB; /dev/zero must not be read", grew>>20)
			}
			wfRequireUnsafePath(t, err)
			return
		case <-time.After(20 * time.Millisecond):
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		if time.Now().After(deadline) || ms.HeapAlloc > 256<<20 {
			fmt.Fprintf(os.Stderr, "FAIL: %s: Update is still reading /dev/zero (HeapAlloc=%d MiB); watchdog ends the process\n",
				t.Name(), ms.HeapAlloc>>20)
			os.Exit(1)
		}
	}
}

// AC15: a workflow file over the worktree read cap is refused 413 before
// it is read.
func TestF5_WorkflowUpdate_OversizedFile_PayloadTooLarge(t *testing.T) {
	svc, repo := newSvc(t)
	p := filepath.Join(repo, "workflows", "big.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	big := fmtYAML("big", "1.0") + "\n#" + strings.Repeat("x", int(worktreefs.DefaultMaxReadBytes)) + "\n"
	if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	wfGit(t, repo, "add", "-A")
	wfGit(t, repo, "commit", "-q", "-m", "big")

	_, err := svc.Update(ctxWithActor(), "big", fmtYAML("big", "2.0"))
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrPayloadTooLarge || !errors.Is(err, worktreefs.ErrTooLarge) {
		t.Fatalf("want ErrPayloadTooLarge, got %v", err)
	}
}

// A Create whose commit fails leaves neither the file nor the workflows/
// directory it created (on 61994fe the directory leaked).
func TestF5_WorkflowCreate_CommitFailure_RemovesFileAndDir(t *testing.T) {
	svc, repo := newSvc(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Create(ctxWithActor(), "wf-a", fmtYAML("wf-a", "1.0"))
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrGit {
		t.Fatalf("want ErrGit, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, "workflows")); statErr == nil {
		t.Fatal("workflows/ left behind after a failed create")
	}
}

// The same with a failing hook that replaces the staged file by a rename
// (`sed -i` shape, review round 2): the file is no longer the inode Create
// made, but it is untracked in HEAD, so the rollback removes it and the
// workflows/ directory, and a retry is not already_exists. After review
// round 1 both were left behind; on 61994fe the directory leaked.
func TestF5_WorkflowCreate_CommitFailure_HookReplacesFileByRename_Removed(t *testing.T) {
	svc, repo := newSvc(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nset -e\nf=workflows/wf-a.yaml\ncp \"$f\" \"$f.tmp\"\n" +
		"printf '# fixed by a hook\\n' >> \"$f.tmp\"\nmv \"$f.tmp\" \"$f\"\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := svc.Create(ctxWithActor(), "wf-a", fmtYAML("wf-a", "1.0"))
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrGit {
			t.Fatalf("attempt %d: want ErrGit, got %v", attempt, err)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "workflows")); statErr == nil {
			t.Fatalf("attempt %d: workflows/ left behind after a failed create", attempt)
		}
	}
}

// The other side of the vouching rule: a path tracked in HEAD by the time
// the rollback runs is never removed. The hook stands in for a concurrent
// writer in the shared standalone tree: it replaces the staged file by a
// rename and commits that version onto HEAD with plumbing (private index)
// before failing the outer commit. On 61994fe the rollback removed the
// path by name and left the committed workflow deleted in the working tree.
func TestF5_WorkflowCreate_CommitFailure_HookCommitsReplacedFile_Kept(t *testing.T) {
	svc, repo := newSvc(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -e
f=workflows/wf-a.yaml
cp "$f" "$f.tmp"
printf '# fixed by a hook\n' >> "$f.tmp"
mv "$f.tmp" "$f"
idx="$(git rev-parse --git-dir)/spine-test-index"
GIT_INDEX_FILE="$idx" git read-tree HEAD
blob=$(git hash-object -w "$f")
GIT_INDEX_FILE="$idx" git update-index --add --cacheinfo "100644,$blob,$f"
tree=$(GIT_INDEX_FILE="$idx" git write-tree)
c=$(git commit-tree "$tree" -p HEAD -m concurrent)
git update-ref HEAD "$c"
rm -f "$idx"
exit 1
`
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Create(ctxWithActor(), "wf-a", fmtYAML("wf-a", "1.0"))
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrGit {
		t.Fatalf("want ErrGit, got %v", err)
	}
	b, readErr := os.ReadFile(filepath.Join(repo, "workflows", "wf-a.yaml"))
	if readErr != nil || !strings.HasSuffix(string(b), "# fixed by a hook\n") {
		t.Fatalf("the workflow committed meanwhile was removed or changed by the rollback: %v", readErr)
	}
	out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("working tree does not match HEAD after rollback:\n%s", out)
	}
}

// An Update whose commit fails restores the original bytes (a guard: passes
// before and after the fix).
func TestWorkflowUpdate_CommitFailure_RestoresBytes(t *testing.T) {
	svc, repo := newSvc(t)
	if _, err := svc.Create(ctxWithActor(), "wf-b", fmtYAML("wf-b", "1.0")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctxWithActor(), "wf-b", fmtYAML("wf-b", "2.0")); err == nil {
		t.Fatal("expected the commit to fail")
	}
	b, err := os.ReadFile(filepath.Join(repo, "workflows", "wf-b.yaml"))
	if err != nil || string(b) != fmtYAML("wf-b", "1.0") {
		t.Fatalf("original not restored: %v", err)
	}
}
