package artifact_test

// Regression tests for INIT-023 TASK-004 (security audit 2026-10-06,
// finding F5), ported from the audit PoC poc/verify-r1-8 (absolute and
// relative dangling-link escape through artifact.Create with a write
// context) plus the in-root ".git" variants. Every TestF5_* here fails on
// Spine 61994fe.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/worktreefs"
	"github.com/bszymi/spine/testutil"
)

type f5NopEvents struct{}

func (f5NopEvents) Emit(context.Context, domain.Event) error { return nil }
func (f5NopEvents) Subscribe(context.Context, domain.EventType, event.EventHandler) error {
	return nil
}

const f5Doc = `---
type: Governance
title: Attacker Controlled
status: Living Document
version: "0.1"
---

# Attacker Controlled

* merge=union
`

func f5Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=a@x", "-c", "user.name=a"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// f5Svc protects main the way the defaults do, so the write-context tests
// exercise the realistic "contributor writes on a run branch" path.
func f5Svc(repo string) *artifact.Service {
	svc := artifact.NewService(git.NewCLIClient(repo), f5NopEvents{}, repo)
	svc.WithPolicy(branchprotect.New(branchprotect.StaticRules([]config.Rule{{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDirectWrite, config.KindNoDelete},
	}})))
	return svc
}

// f5OpenSvc writes straight to the main working tree (no protection), the
// standalone shape in which .git/ is a real directory inside the root.
func f5OpenSvc(repo string) *artifact.Service {
	svc := artifact.NewService(git.NewCLIClient(repo), f5NopEvents{}, repo)
	svc.WithPolicy(branchprotect.New(branchprotect.StaticRules([]config.Rule{})))
	return svc
}

func f5Ctx(branch string) context.Context {
	ctx := observe.WithActorID(observe.WithTraceID(context.Background(), "f5"), "contrib-1")
	ctx = domain.WithActor(ctx, &domain.Actor{ActorID: "contrib-1", Type: domain.ActorTypeHuman,
		Role: domain.RoleContributor, Status: domain.ActorStatusActive})
	if branch != "" {
		ctx = artifact.WithWriteContext(ctx, artifact.WriteContext{Branch: branch})
	}
	return ctx
}

// f5RequireUnsafePath asserts the refusal contract: ErrInvalidParams (400)
// whose cause is a worktreefs path refusal.
func f5RequireUnsafePath(t *testing.T, err error) {
	t.Helper()
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrInvalidParams {
		t.Fatalf("want ErrInvalidParams (unsafe path) refused before any write, got %v", err)
	}
	if !errors.Is(err, worktreefs.ErrUnsafePath) {
		t.Fatalf("want errors.Is(err, worktreefs.ErrUnsafePath), got %v", err)
	}
}

func f5RequireCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// plantOnRunBranch commits a symlink at linkPath on branch run-evil, then
// returns to main so only the run branch carries it (audit attacker A1).
func plantOnRunBranch(t *testing.T, repo, linkPath, target string) {
	t.Helper()
	f5Git(t, repo, "checkout", "-q", "-b", "run-evil")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, linkPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(repo, linkPath)); err != nil {
		t.Fatal(err)
	}
	f5Git(t, repo, "add", "-A")
	f5Git(t, repo, "commit", "-q", "-m", "plant symlink")
	f5Git(t, repo, "checkout", "-q", "main")
}

func TestF5_Create_DanglingSymlinkAbsolute_NoEscape(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	wtTmp := t.TempDir()
	t.Setenv("TMPDIR", wtTmp) // worktree = $TMPDIR/spine-wt-parent-*/wt
	outside := t.TempDir()
	victim := filepath.Join(outside, "pwned.md")
	plantOnRunBranch(t, repo, "governance/evil.md", victim)

	_, err := f5Svc(repo).Create(f5Ctx("run-evil"), "governance/evil.md", f5Doc)
	if _, statErr := os.Lstat(victim); statErr == nil {
		t.Errorf("file written outside the worktree at %s", victim)
	}
	if left, _ := filepath.Glob(filepath.Join(wtTmp, "spine-wt-parent-*")); len(left) != 0 {
		t.Errorf("worktree parents leaked: %v", left)
	}
	f5RequireUnsafePath(t, err)
}

func TestF5_Create_DanglingSymlinkRelative_NoEscape(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	wtTmp := t.TempDir()
	t.Setenv("TMPDIR", wtTmp) // worktree = $TMPDIR/spine-wt-parent-*/wt
	plantOnRunBranch(t, repo, "governance/evil.md", "../../../escaped.md")

	_, err := f5Svc(repo).Create(f5Ctx("run-evil"), "governance/evil.md", f5Doc)
	if _, statErr := os.Lstat(filepath.Join(wtTmp, "escaped.md")); statErr == nil {
		t.Errorf("file written outside the worktree at %s", filepath.Join(wtTmp, "escaped.md"))
	}
	if left, _ := filepath.Glob(filepath.Join(wtTmp, "spine-wt-parent-*")); len(left) != 0 {
		t.Errorf("worktree parents leaked: %v", left)
	}
	f5RequireUnsafePath(t, err)
}

// The symlinked-directory variant: a link one level up, on the run branch.
func TestF5_Create_SymlinkedParentDir_NoEscape(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	outside := t.TempDir()
	plantOnRunBranch(t, repo, "governance", outside)

	_, err := f5Svc(repo).Create(f5Ctx("run-evil"), "governance/new.md", f5Doc)
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("file written through the symlinked directory into %s", outside)
	}
	f5RequireUnsafePath(t, err)
}

// In-root link into .git/ in the standalone main working tree: os.Root
// alone would follow this (relative, stays inside the root), and on
// 61994fe it planted attacker lines in .git/info/attributes that git
// applies to every later merge.
func TestF5_Create_InRootSymlinkIntoDotGit_Refused(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "governance"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../.git/info/attributes", filepath.Join(repo, "governance", "attrs.md")); err != nil {
		t.Fatal(err)
	}
	f5Git(t, repo, "add", "-A")
	f5Git(t, repo, "commit", "-q", "-m", "plant")

	_, err := f5OpenSvc(repo).Create(f5Ctx(""), "governance/attrs.md", f5Doc)
	if _, statErr := os.Lstat(filepath.Join(repo, ".git", "info", "attributes")); statErr == nil {
		t.Errorf(".git/info/attributes was created through an in-root symlink")
	}
	f5RequireUnsafePath(t, err)
}

func TestF5_Create_DotGitPath_RefusedBeforeWrite(t *testing.T) {
	for _, p := range []string{".git/info/attributes", "governance/.git/x.md", ".GIT/info/attributes", ".git./x.md", "git~1/x.md"} {
		t.Run(p, func(t *testing.T) {
			repo := testutil.NewTempRepo(t)
			_, err := f5OpenSvc(repo).Create(f5Ctx(""), p, f5Doc)
			f5RequireUnsafePath(t, err)
			if _, statErr := os.Lstat(filepath.Join(repo, ".git", "info", "attributes")); statErr == nil {
				t.Errorf(".git/info/attributes was created")
			}
		})
	}
}

// The backslash spellings git's NTFS rule treats as ".git" (core.protectNTFS
// is on by default on every platform, so `git add` refuses them on Linux and
// macOS too). Before review round 1 they passed Clean, the file was written,
// and the call ended in a 500 git_error ("invalid path") after a rollback;
// now they are refused 400 before anything touches the disk.
func TestF5_Create_BackslashDotGitPath_RefusedBeforeWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a backslash is a path separator on Windows")
	}
	for _, p := range []string{`.git\info\attributes`, `governance/.git\x.md`, `GIT~1\x.md`, `governance/a\.git`} {
		t.Run(p, func(t *testing.T) {
			repo := testutil.NewTempRepo(t)
			_, err := f5OpenSvc(repo).Create(f5Ctx(""), p, f5Doc)
			f5RequireUnsafePath(t, err)
			var pe *worktreefs.PathError
			if !errors.As(err, &pe) || pe.Reason != worktreefs.ReasonGitDir {
				t.Fatalf("want ReasonGitDir, got %v", err)
			}
			out, _ := exec.Command("git", "-C", repo, "status", "--porcelain", "--untracked-files=all").CombinedOutput()
			if strings.TrimSpace(string(out)) != "" {
				t.Errorf("the refused path touched the working tree:\n%s", out)
			}
		})
	}
}

// Update through a leaf symlink to an existing in-repo file must not write
// the target at all (not even write-then-restore): mtime must not move.
func TestF5_Update_SymlinkLeafToTrackedFile_TargetUntouched(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	realPath := filepath.Join(repo, "real.md")
	if err := os.WriteFile(realPath, []byte(f5Doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.md", filepath.Join(repo, "link.md")); err != nil {
		t.Fatal(err)
	}
	f5Git(t, repo, "add", "-A")
	f5Git(t, repo, "commit", "-q", "-m", "layout")
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(realPath, past, past); err != nil {
		t.Fatal(err)
	}

	_, err := f5OpenSvc(repo).Update(f5Ctx(""), "link.md", f5Doc+"\nchanged\n")
	fi, _ := os.Stat(realPath)
	if !fi.ModTime().Equal(past) {
		t.Errorf("symlink target was written (mtime moved to %v)", fi.ModTime())
	}
	f5RequireUnsafePath(t, err)
}

// AC10: a Create at an in-root symlink to an EXISTING file used to be 409
// already_exists (EvalSymlinks resolved in-root, os.Stat followed); it is
// now 400 invalid_params. Recorded for SMP TASK-008.
func TestF5_Create_InRootSymlinkToExistingFile_Is400Not409(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "real.md"), []byte(f5Doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.md", filepath.Join(repo, "link.md")); err != nil {
		t.Fatal(err)
	}
	f5Git(t, repo, "add", "-A")
	f5Git(t, repo, "commit", "-q", "-m", "layout")

	_, err := f5OpenSvc(repo).Create(f5Ctx(""), "link.md", f5Doc)
	f5RequireUnsafePath(t, err)
}

// AC10: the unchanged halves of the contract (a guard: passes before and
// after the fix).
func TestWorktreeContract_ExistingAndMissing(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	svc := f5OpenSvc(repo)
	ctx := f5Ctx("")
	if _, err := svc.Create(ctx, "governance/doc.md", f5Doc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := svc.Create(ctx, "governance/doc.md", f5Doc)
	f5RequireCode(t, err, domain.ErrAlreadyExists)
	if errors.Is(err, worktreefs.ErrUnsafePath) {
		t.Fatalf("an existing regular file is not an unsafe path: %v", err)
	}
	_, err = svc.Update(ctx, "governance/missing.md", f5Doc)
	f5RequireCode(t, err, domain.ErrNotFound)
}

// AC2 artifacts-dir row: with artifacts_dir set, "a/../../x.md" must not
// be folded by filepath.Join into a clean path that escapes the artifacts
// directory (here: the repository root).
//
// Mutation check: cleaning only after repoRelativePath accepts the path
// and creates x.md at the repository root.
func TestF5_Create_ArtifactsDirTraversal_Refused(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	svc := f5OpenSvc(repo)
	svc.WithArtifactsDir("spine")
	_, err := svc.Create(f5Ctx(""), "a/../../x.md", f5Doc)
	f5RequireUnsafePath(t, err)
	for _, p := range []string{"x.md", "spine/x.md"} {
		if _, statErr := os.Lstat(filepath.Join(repo, p)); statErr == nil {
			t.Errorf("%s was created", p)
		}
	}
}

// failCommits installs a pre-commit hook that rejects every commit, so
// StageAndCommit fails after the file write (AC11).
func failCommits(t *testing.T, repo string) {
	t.Helper()
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// AC11: a Create whose commit fails leaves neither the file nor the
// directories it created (on 61994fe the directories leaked).
func TestF5_Create_CommitFailure_RemovesFileAndCreatedDirs(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "governance"), 0o755); err != nil {
		t.Fatal(err)
	}
	failCommits(t, repo)
	_, err := f5OpenSvc(repo).Create(f5Ctx(""), "governance/a/b/doc.md", f5Doc)
	f5RequireCode(t, err, domain.ErrGit)
	for _, p := range []string{"governance/a/b/doc.md", "governance/a/b", "governance/a"} {
		if _, statErr := os.Lstat(filepath.Join(repo, p)); statErr == nil {
			t.Errorf("%s left behind after a failed create", p)
		}
	}
	if _, statErr := os.Stat(filepath.Join(repo, "governance")); statErr != nil {
		t.Errorf("pre-existing directory removed: %v", statErr)
	}
	out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("working tree not clean after rollback:\n%s", out)
	}
}

// AC11 with a failing pre-commit hook that rewrites the staged file before
// it exits non-zero (pre-commit-framework fixers do this). The file is
// still the one Create made, so the rollback removes it and the directories
// it created, and a retry fails the same way instead of with
// already_exists. Before review round 1 the rollback matched the written
// size and mtime as well as the inode and left the rewritten file (and its
// directories) behind; on 61994fe the directories leaked.
func TestF5_Create_CommitFailure_HookRewritesFile_StillRemoved(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "governance"), 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '\\nfixed by a hook\\n' >> governance/a/b/doc.md\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := f5OpenSvc(repo)
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := svc.Create(f5Ctx(""), "governance/a/b/doc.md", f5Doc)
		f5RequireCode(t, err, domain.ErrGit)
		for _, p := range []string{"governance/a/b/doc.md", "governance/a/b", "governance/a"} {
			if _, statErr := os.Lstat(filepath.Join(repo, p)); statErr == nil {
				t.Fatalf("attempt %d: %s left behind after a failed create", attempt, p)
			}
		}
	}
	out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("working tree not clean after rollback:\n%s", out)
	}
}

// installPreCommit installs body as the repository's pre-commit hook.
func installPreCommit(t *testing.T, repo, body string) {
	t.Helper()
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nset -e\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// hookReplaceByRename is the fixer shape of `sed -i`, `clang-format -i` and
// any tool that writes a temp file and renames it over the original: the
// staged file gets a new inode, so the Undo's identity match no longer
// names it.
const hookReplaceByRename = `f=governance/a/b/doc.md
cp "$f" "$f.tmp"
printf '\nfixed by a hook\n' >> "$f.tmp"
mv "$f.tmp" "$f"
`

// AC11 with a failing pre-commit hook that replaces the staged file by a
// rename before it exits non-zero (review round 2). The file at the path is
// no longer the inode Create made, so the identity match alone keeps it;
// the caller then vouches for it — the path is untracked in HEAD after the
// failed commit — and the rollback removes it and the directories Create
// made. A retry fails the same way instead of with already_exists. After
// review round 1 the rewritten file (and its directories) were left
// behind; on 61994fe the directories leaked.
func TestF5_Create_CommitFailure_HookReplacesFileByRename_Removed(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "governance"), 0o755); err != nil {
		t.Fatal(err)
	}
	installPreCommit(t, repo, hookReplaceByRename+"exit 1\n")
	svc := f5OpenSvc(repo)
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := svc.Create(f5Ctx(""), "governance/a/b/doc.md", f5Doc)
		f5RequireCode(t, err, domain.ErrGit)
		for _, p := range []string{"governance/a/b/doc.md", "governance/a/b", "governance/a"} {
			if _, statErr := os.Lstat(filepath.Join(repo, p)); statErr == nil {
				t.Fatalf("attempt %d: %s left behind after a failed create", attempt, p)
			}
		}
	}
	if _, statErr := os.Stat(filepath.Join(repo, "governance")); statErr != nil {
		t.Errorf("pre-existing directory removed: %v", statErr)
	}
	out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("working tree not clean after rollback:\n%s", out)
	}
}

// The other side of the vouching rule (review round 2): a file at the path
// that is tracked in HEAD by the time the rollback runs is never removed,
// even though Create made a file there. The hook stands in for a concurrent
// writer in the shared standalone tree — it replaces the staged file by a
// rename and commits that version onto HEAD with plumbing (a private index,
// so the outer commit's index is untouched) before failing the outer
// commit. On 61994fe the rollback removed the path by name and left the
// committed file deleted in the working tree.
func TestF5_Create_CommitFailure_HookCommitsReplacedFile_Kept(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "governance"), 0o755); err != nil {
		t.Fatal(err)
	}
	installPreCommit(t, repo, hookReplaceByRename+`idx="$(git rev-parse --git-dir)/spine-test-index"
GIT_INDEX_FILE="$idx" git read-tree HEAD
blob=$(git hash-object -w "$f")
GIT_INDEX_FILE="$idx" git update-index --add --cacheinfo "100644,$blob,$f"
tree=$(GIT_INDEX_FILE="$idx" git write-tree)
c=$(git commit-tree "$tree" -p HEAD -m concurrent)
git update-ref HEAD "$c"
rm -f "$idx"
exit 1
`)
	_, err := f5OpenSvc(repo).Create(f5Ctx(""), "governance/a/b/doc.md", f5Doc)
	f5RequireCode(t, err, domain.ErrGit)
	b, readErr := os.ReadFile(filepath.Join(repo, "governance", "a", "b", "doc.md"))
	if readErr != nil || !strings.HasSuffix(string(b), "fixed by a hook\n") {
		t.Fatalf("the file committed meanwhile was removed or changed by the rollback: %v", readErr)
	}
	out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("working tree does not match HEAD after rollback:\n%s", out)
	}
}

// AC11: an Update whose commit fails restores the original bytes and mode
// (a guard: os.WriteFile on an existing file kept the mode on 61994fe too;
// the fix must not lose it by replacing through a rename).
func TestUpdate_CommitFailure_RestoresBytesAndMode(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	p := filepath.Join(repo, "governance", "tool.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(f5Doc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	f5Git(t, repo, "add", "-A")
	f5Git(t, repo, "commit", "-q", "-m", "exec doc")
	failCommits(t, repo)

	_, err := f5OpenSvc(repo).Update(f5Ctx(""), "governance/tool.md", strings.Replace(f5Doc, "Attacker Controlled", "Changed", 2))
	f5RequireCode(t, err, domain.ErrGit)
	b, _ := os.ReadFile(p)
	fi, _ := os.Lstat(p)
	if string(b) != f5Doc || fi.Mode().Perm() != 0o755 || !fi.Mode().IsRegular() {
		t.Fatalf("rollback did not restore bytes/mode: mode=%v content-restored=%v", fi.Mode(), string(b) == f5Doc)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".spine-tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// An Update whose commit fails because a pre-commit hook deleted the file
// (or its directory) still restores the original (codex review, round 2).
// A guard: on 61994fe the rollback's os.WriteFile re-created the file (the
// 0644 rows pass there; it dropped the exec bit of the 0755 row and could
// not re-create a removed directory). The restore goes through the tree's
// confined create-or-replace, so the original mode comes back too.
func TestUpdate_CommitFailure_HookDeletesFile_Restored(t *testing.T) {
	for _, tc := range []struct {
		name, rel, hook string
		mode            os.FileMode
	}{
		{"file deleted", "governance/tool.md", "rm governance/tool.md\n", 0o644},
		{"exec file deleted keeps its mode", "governance/tool.md", "rm governance/tool.md\n", 0o755},
		{"directory removed", "governance/sub/tool.md", "rm -r governance/sub\n", 0o644},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testutil.NewTempRepo(t)
			p := filepath.Join(repo, filepath.FromSlash(tc.rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(f5Doc), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, tc.mode); err != nil {
				t.Fatal(err)
			}
			f5Git(t, repo, "add", "-A")
			f5Git(t, repo, "commit", "-q", "-m", "doc")
			installPreCommit(t, repo, tc.hook+"exit 1\n")

			_, err := f5OpenSvc(repo).Update(f5Ctx(""), tc.rel, strings.Replace(f5Doc, "Attacker Controlled", "Changed", 2))
			f5RequireCode(t, err, domain.ErrGit)
			b, readErr := os.ReadFile(p)
			if readErr != nil || string(b) != f5Doc {
				t.Fatalf("original not restored after the hook deleted it: %v", readErr)
			}
			if fi, _ := os.Lstat(p); !fi.Mode().IsRegular() || fi.Mode().Perm()&0o100 != tc.mode&0o100 {
				t.Errorf("restored mode = %v, want the exec bit of %v", fi.Mode(), tc.mode)
			}
			out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
			if strings.TrimSpace(string(out)) != "" {
				t.Errorf("working tree not clean after rollback:\n%s", out)
			}
		})
	}
}

// The update restore re-creates a deleted file but never writes through a
// link: a failing hook that swaps the updated file for a symlink to a file
// outside the tree leaves the target untouched, and the rollback reports
// the refusal instead (logged). On 61994fe the rollback's os.WriteFile
// followed the link and wrote the original bytes over the target.
func TestF5_Update_CommitFailure_HookPlantsLink_NotFollowed(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.md")
	if err := os.WriteFile(victim, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(repo, "governance", "tool.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(f5Doc), 0o644); err != nil {
		t.Fatal(err)
	}
	f5Git(t, repo, "add", "-A")
	f5Git(t, repo, "commit", "-q", "-m", "doc")
	installPreCommit(t, repo, "rm governance/tool.md\nln -s '"+victim+"' governance/tool.md\nexit 1\n")

	_, err := f5OpenSvc(repo).Update(f5Ctx(""), "governance/tool.md", strings.Replace(f5Doc, "Attacker Controlled", "Changed", 2))
	f5RequireCode(t, err, domain.ErrGit)
	if b, _ := os.ReadFile(victim); string(b) != "victim" {
		t.Fatal("the rollback wrote through a link a hook planted")
	}
	if fi, lerr := os.Lstat(p); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("want the planted link left in place, not replaced or followed: %v", lerr)
	}
}

// AC15(c): an Update whose worktree file is over the read cap is refused
// with ErrPayloadTooLarge (413), the status TASK-003 uses for an oversized
// git blob, and the file is left alone.
func TestF5_Update_OversizedWorktreeFile_PayloadTooLarge(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	p := filepath.Join(repo, "governance", "big.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	big := []byte(f5Doc + strings.Repeat("x", int(worktreefs.DefaultMaxReadBytes)))
	if err := os.WriteFile(p, big, 0o644); err != nil {
		t.Fatal(err)
	}
	f5Git(t, repo, "add", "-A")
	f5Git(t, repo, "commit", "-q", "-m", "big doc")

	_, err := f5OpenSvc(repo).Update(f5Ctx(""), "governance/big.md", f5Doc)
	f5RequireCode(t, err, domain.ErrPayloadTooLarge)
	if !errors.Is(err, worktreefs.ErrTooLarge) {
		t.Fatalf("want errors.Is(err, worktreefs.ErrTooLarge), got %v", err)
	}
	if fi, _ := os.Stat(p); fi.Size() != int64(len(big)) {
		t.Fatal("oversized file was modified")
	}
}
