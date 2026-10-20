package projection

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
)

// INIT-023 TASK-011 review round 1: a read failure is deterministic
// (skipped for an artifact, a not_a_file rejection for a workflow) only
// when the commit's tree PROVES there is no regular file at that path.
// git's stderr — which echoes the committed, attacker-chosen path — is
// never consulted, and a failure of git itself (a lock, a killed or
// silently failing process) holds the sync.

// addGitlink stages a gitlink (submodule entry) whose commit is not in
// the repository: `git show <c>:<path>` fails with
// "fatal: bad object <c>:<path>".
func (r *scratchRepo) addGitlink(path string) {
	r.t.Helper()
	r.run("update-index", "--add", "--cacheinfo", "160000,"+gitlinkTarget+","+path)
}

// commitIndex commits exactly what is staged (commit's `add -A` would
// drop a gitlink that has no working-tree directory) and returns HEAD.
func (r *scratchRepo) commitIndex(msg string, stage ...string) string {
	r.t.Helper()
	if len(stage) > 0 {
		r.run(append([]string{"add", "--"}, stage...)...)
	}
	r.run("commit", "-q", "-m", msg)
	return r.run("rev-parse", "HEAD")
}

func syncedPayload(t *testing.T, router *recordingRouter) map[string]any {
	t.Helper()
	synced := router.ofType(domain.EventProjectionSynced)
	if len(synced) == 0 {
		t.Fatal("no projection_synced event")
	}
	var p map[string]any
	if err := json.Unmarshal(synced[len(synced)-1].Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// P1: a gitlink whose path makes git's stderr look transient ("lock",
// "timeout", "Could not resolve") is still deterministic. Before the fix
// every tick returned "read changed artifact: … repository locked" and
// the sync stayed error@c1 — a wedge any member could plant.
//
// Since the INIT-023 wave 4 follow-up the git client never reads a
// gitlink (it types the entry and refuses it as git.ErrNotBlob), so
// discovery records each one as a not_a_file refusal — one Warn, one
// projection_file_skipped, counted in "refused" — instead of a read
// error that the classifier then proved deterministic (read_skipped).
func TestIncrementalSync_GitlinkWithTransientLookingNameIsSkipped(t *testing.T) {
	logs := captureLogs(t)
	repo := newScratchRepo(t)
	repo.write(taskPath, taskDoc("Pending", "T"))
	repo.commit("c1")
	st := allRowsStore{newMapStore()}
	router := &recordingRouter{}
	svc := NewService(repo.git, st, router, 0)
	if err := svc.FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}

	gitlinks := []string{"initiatives/blocked.md", "docs/timeout.md", "docs/Could not resolve.md", "docs/x.lock.md"}
	for _, p := range gitlinks {
		repo.addGitlink(p)
	}
	repo.write(taskPath, taskDoc("Completed", "T"))
	c2 := repo.commitIndex("c2: gitlinks plus a real change", taskPath)
	if n := strings.Count(repo.run("ls-tree", "-r", c2), "160000 commit"); n != len(gitlinks) {
		t.Fatalf("precondition: %d gitlinks committed, want %d", n, len(gitlinks))
	}

	for tick := 1; tick <= 3; tick++ {
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatalf("tick %d: a gitlink must not hold the sync: %v", tick, err)
		}
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state: %+v", got)
	}
	if a := st.artifacts[taskPath]; a == nil || a.Status != "Completed" {
		t.Fatalf("the real change in the same window must be projected: %+v", a)
	}
	for _, p := range gitlinks {
		if n := len(logs.lines("level=WARN", "projection skipped file", "reason=not_a_file", filepath.Base(p))); n != 1 {
			t.Errorf("expected one Warn for %s, got %d", p, n)
		}
	}
	if p := syncedPayload(t, router); p["refused"] != float64(len(gitlinks)) || p["read_skipped"] != float64(0) {
		t.Fatalf("projection_synced payload = %+v, want refused %d and read_skipped 0", p, len(gitlinks))
	}
}

// killingGit puts a `git` wrapper first on PATH that, while the flag
// file exists, makes the blob read of the given path fail the way an OOM
// kill or a silent git failure does — no stderr — and otherwise runs the
// real git. The blob read is `git cat-file blob <oid>` (ReadFile reads by
// object id since the INIT-023 wave 4 follow-up; before it, a `show` or
// `cat-file` of `<ref>:<path>`): the wrapper fails it when <oid> is the
// blob at <path> in any commit of the repository, so the entry probes
// (`ls-tree … <path>`) and TreeEntryType still run, and still prove a
// blob is there. It returns the flag file path.
func killingGit(t *testing.T, path, how string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	flag := filepath.Join(dir, "fail")
	action := "kill -KILL $$"
	if how == "silent_exit" {
		action = "exit 1"
	}
	script := "#!/bin/sh\n" +
		"if [ -e '" + flag + "' ] && [ \"$1\" = cat-file ] && [ \"$2\" = blob ]; then\n" +
		"  if '" + realGit + "' rev-list --all --objects 2>/dev/null | grep -qxF \"$3 " + path + "\"; then " + action + "; fi\n" +
		"fi\n" +
		"exec '" + realGit + "' \"$@\"\n"
	//nolint:gosec // G306: the wrapper must be executable; it lives under t.TempDir().
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return flag
}

// P2: with the production git client, a git process killed by a signal
// or exiting without a message reaches the projection as a *GitError of
// Kind permanent with empty stderr. Before the fix that was skipped as
// deterministic: the sync advanced to idle@head and the change was never
// applied. The tree proves the entry is a blob, so it now holds the sync.
func TestIncrementalSync_KilledGitReadHoldsTheSync(t *testing.T) {
	for _, how := range []string{"signal", "silent_exit"} {
		for _, change := range []string{"modified", "deleted"} {
			t.Run(how+"/"+change, func(t *testing.T) {
				repo := newScratchRepo(t)
				repo.write(taskPath, taskDoc("Pending", "T"))
				c1 := repo.commit("c1")
				st := allRowsStore{newMapStore()}
				svc := NewService(repo.git, st, nil, 0)
				if err := svc.FullRebuild(context.Background()); err != nil {
					t.Fatal(err)
				}
				if change == "modified" {
					repo.write(taskPath, taskDoc("Completed", "T"))
				} else {
					repo.remove(taskPath)
				}
				c2 := repo.commit("c2")

				flag := killingGit(t, taskPath, how)
				if err := os.WriteFile(flag, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				// Precondition: the git client calls this failure permanent.
				_, rerr := repo.git.ReadFile(context.Background(), c2, taskPath)
				if change == "deleted" {
					_, rerr = repo.git.ReadFile(context.Background(), c1, taskPath)
				}
				var gerr *git.GitError
				if !errors.As(rerr, &gerr) || gerr.Kind != git.ErrKindPermanent {
					t.Fatalf("precondition: expected a permanent GitError, got %v", rerr)
				}

				err := svc.IncrementalSync(context.Background())
				if err == nil || !strings.Contains(err.Error(), "read changed artifact") {
					t.Fatalf("a failed git process must hold the sync, got %v", err)
				}
				if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != c1 {
					t.Fatalf("sync state: %+v", got)
				}
				if a := st.artifacts[taskPath]; a == nil || a.Status != "Pending" {
					t.Fatalf("nothing may be applied past the failed read: %+v", a)
				}

				if err := os.Remove(flag); err != nil {
					t.Fatal(err)
				}
				if err := svc.IncrementalSync(context.Background()); err != nil {
					t.Fatalf("retry: %v", err)
				}
				if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
					t.Fatalf("sync state after retry: %+v", got)
				}
				a := st.artifacts[taskPath]
				switch change {
				case "modified":
					if a == nil || a.Status != "Completed" {
						t.Fatalf("retry must project the modification: %+v", a)
					}
				case "deleted":
					if a != nil {
						t.Fatalf("retry must delete the row: %+v", a)
					}
				}
			})
		}
	}
}

// typingGit is scriptedGit plus the TreeEntryTyper capability with a
// scripted answer.
type typingGit struct {
	*scriptedGit
	typ    string
	typErr error
	calls  int
}

func (g *typingGit) TreeEntryType(context.Context, string, string) (string, error) {
	g.calls++
	return g.typ, g.typErr
}

// The classification table, with fakes: only a proved non-blob entry is
// skipped; every other answer — and every client without the capability —
// holds the sync.
func TestClassifyReadErrors_OnlyAProvedNonBlobIsSkipped(t *testing.T) {
	readErr := &git.GitError{Kind: git.ErrKindPermanent, Op: "read_file", Message: ""}
	for _, tc := range []struct {
		name     string
		typ      string
		typErr   error
		noTyper  bool
		wantHold bool
	}{
		{name: "gitlink", typ: git.TreeEntryCommit},
		{name: "tree", typ: git.TreeEntryTree},
		{name: "no entry at that path", typ: ""},
		{name: "blob", typ: git.TreeEntryBlob, wantHold: true},
		{name: "typing failed", typErr: errSimulated, wantHold: true},
		{name: "client without the capability", noTyper: true, wantHold: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sg := &scriptedGit{
				head:    shaC2,
				diffs:   []git.FileDiff{{Path: taskPath, Status: "modified"}},
				files:   map[string]string{taskPath: taskDoc("Completed", "Done")},
				readErr: map[string]error{taskPath: readErr},
			}
			var g git.GitClient = sg
			if !tc.noTyper {
				g = &typingGit{scriptedGit: sg, typ: tc.typ, typErr: tc.typErr}
			}
			st := allRowsStore{newMapStore()}
			st.setState(shaC1, "idle")
			err := NewService(g, st, nil, 0).IncrementalSync(context.Background())
			got := st.syncState()
			if tc.wantHold {
				if err == nil || got.Status != "error" || got.LastSyncedCommit != shaC1 {
					t.Fatalf("expected the sync held: err=%v state=%+v", err, got)
				}
				if tc.typErr != nil && !errors.Is(err, tc.typErr) {
					t.Fatalf("the typing failure must be in the error chain: %v", err)
				}
				return
			}
			if err != nil || got.Status != "idle" || got.LastSyncedCommit != shaC2 {
				t.Fatalf("expected a skip that advances: err=%v state=%+v", err, got)
			}
		})
	}

	t.Run("cancelled context holds without typing", func(t *testing.T) {
		g := &typingGit{scriptedGit: &scriptedGit{}, typ: git.TreeEntryCommit}
		svc := NewService(g, newMapStore(), nil, 0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := svc.classifyReadErrors(ctx, []artifact.DiscoveryError{{Path: taskPath, Ref: shaC2, Err: readErr}})
		if err == nil || g.calls != 0 {
			t.Fatalf("a cancelled context must hold without asking the tree: err=%v calls=%d", err, g.calls)
		}
	})
}

// G / K / C: a gitlink committed at a workflow path is a not_a_file
// rejection on every entry point. The verified previous row is kept
// quarantined, the sync advances, and neither the sweep of a new instance
// nor a FullRebuild freezes the projection on it.
func TestProjection_GitlinkAtWorkflowPathIsRejection(t *testing.T) {
	repo, st, router, svc, c1 := newIntegrityFixture(t, reviewedWorkflow)
	repo.write(taskPath, taskDoc("Pending", "T"))
	repo.commit("c1b: a task")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}

	// c2: the workflow file becomes a dangling gitlink, and a second
	// gitlink whose name looks transient lands at a new workflow path.
	repo.remove(wfPath)
	repo.addGitlink(wfPath)
	repo.addGitlink("workflows/blocked.yaml")
	repo.write(taskPath, taskDoc("Completed", "T"))
	c2 := repo.commitIndex("c2", taskPath)
	if n := strings.Count(repo.run("ls-tree", "-r", c2), "160000 commit"); n != 2 {
		t.Fatalf("precondition: %d gitlinks committed, want 2", n)
	}
	router.reset()

	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("a gitlink at a workflow path must not hold the sync: %+v", got)
	}
	assertKeptQuarantined(t, st, c1)
	if a := st.artifacts[taskPath]; a == nil || a.Status != "Completed" {
		t.Fatalf("the task change in the same window must be applied: %+v", a)
	}
	assertRejections(t, router, map[string][2]string{
		wfPath:                   {"not_a_file", "kept"},
		"workflows/blocked.yaml": {"not_a_file", "not_projected"},
	})

	// A new instance (restart, SMP pool rotation) sweeps first: the row
	// is still the kept, quarantined one, and later commits project.
	repo.write(taskPath, taskDoc("Cancelled", "T"))
	c3 := repo.commitIndex("c3", taskPath)
	fresh := NewService(repo.git, st, router, 0)
	if err := fresh.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("fresh instance: %v", err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c3 {
		t.Fatalf("fresh instance must not freeze on the gitlink: %+v", got)
	}
	assertKeptQuarantined(t, st, c1)
	if a := st.artifacts[taskPath]; a == nil || a.Status != "Cancelled" {
		t.Fatalf("fresh instance must project c3: %+v", a)
	}

	// A FullRebuild keeps the row quarantined, ends idle and marks the
	// sweep done; the next commit projects on that instance too.
	rebuilt := NewService(repo.git, st, router, 0)
	if err := rebuilt.FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c3 {
		t.Fatalf("rebuild: %+v", got)
	}
	assertKeptQuarantined(t, st, c1)
	if !rebuilt.workflowsVerified.Load() {
		t.Fatal("a rejection is not an infrastructure error: the rebuild must mark the sweep done")
	}
	repo.write(taskPath, taskDoc("Pending", "T2"))
	c4 := repo.commitIndex("c4", taskPath)
	if err := rebuilt.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c4 {
		t.Fatalf("after rebuild: %+v", got)
	}
}

// A workflow read that fails while the tree holds a blob is still an
// infrastructure error (the read of a real file failed).
func TestIncrementalSync_KilledWorkflowReadIsInfrastructure(t *testing.T) {
	repo, st, _, svc, c1 := newIntegrityFixture(t, reviewedWorkflow)
	repo.write(wfPath, strings.Replace(reviewedWorkflow, `version: "1.0"`, `version: "1.1"`, 1))
	c2 := repo.commit("c2")
	flag := killingGit(t, wfPath, "signal")
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("counted errors are reported through sync_state, got %v", err)
	}
	if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != c1 {
		t.Fatalf("a killed workflow read must hold the sync: %+v", got)
	}
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := st.row(wfPath); r == nil || r.Version != "1.1" || r.SourceCommit != c2 {
		t.Fatalf("retry must project c2: %+v", r)
	}
}

// assertRejections checks the reported rejections by path: check and
// action.
func assertRejections(t *testing.T, router *recordingRouter, want map[string][2]string) {
	t.Helper()
	got := map[string][2]string{}
	for _, p := range router.rejections(t) {
		path, _ := p["workflow_path"].(string)
		check, _ := p["check"].(string)
		action, _ := p["action"].(string)
		got[path] = [2]string{check, action}
	}
	if len(got) != len(want) {
		t.Fatalf("rejections = %+v, want %+v", got, want)
	}
	for path, w := range want {
		if got[path] != w {
			t.Fatalf("rejection for %s = %v, want %v (all: %+v)", path, got[path], w, got)
		}
	}
}
