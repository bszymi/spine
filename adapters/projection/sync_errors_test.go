package projection

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// INIT-023 TASK-011, audit P3-14: one Diff per sync, and every git or
// store failure holds the sync (status error, last_synced_commit
// unchanged) so the next tick retries — no governance change is skipped
// while the state advances.

const (
	shaC1 = "1111111111111111111111111111111111111111"
	shaC2 = "2222222222222222222222222222222222222222"
)

// scriptedGit serves a fixed head, a scripted Diff and canned file
// contents. Unimplemented GitClient methods panic.
type scriptedGit struct {
	git.GitClient

	mu        sync.Mutex
	head      string
	diffs     []git.FileDiff
	failDiffs int // fail this many Diff calls first
	diffCalls int
	files     map[string]string // path -> content at head
	readErr   map[string]error  // path -> ReadFile error
}

func (g *scriptedGit) Head(context.Context) (string, error) { return g.head, nil }

func (g *scriptedGit) Diff(context.Context, string, string) ([]git.FileDiff, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.diffCalls++
	if g.diffCalls <= g.failDiffs {
		return nil, errors.New("fatal: unable to read tree (simulated)")
	}
	return g.diffs, nil
}

func (g *scriptedGit) ListFiles(_ context.Context, _, pattern string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for p := range g.files {
		if pattern == "" || p == pattern {
			out = append(out, p)
		}
	}
	return out, nil
}

func (g *scriptedGit) ReadFile(_ context.Context, _, path string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.readErr[path]; err != nil {
		return nil, err
	}
	body, ok := g.files[path]
	if !ok {
		return nil, &git.GitError{Kind: git.ErrKindNotFound, Op: "read_file", Message: "does not exist"}
	}
	return []byte(body), nil
}

func (g *scriptedGit) calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.diffCalls
}

func governanceChangeGit() *scriptedGit {
	return &scriptedGit{
		head: shaC2,
		diffs: []git.FileDiff{
			{Path: BranchProtectionConfigPath, Status: "modified"},
			{Path: wfPath, Status: "modified"},
		},
		files: map[string]string{
			BranchProtectionConfigPath: "version: 1\nrules:\n  - branch: main\n    protections: [no-delete]\n",
			wfPath:                     reviewedWorkflow,
		},
	}
}

// AC 12 (inverted TestPOC_IncrementalSyncDropsSecondDiffError).
func TestIncrementalSync_DiffFailureIsErrorAndRetried(t *testing.T) {
	g := governanceChangeGit()
	g.failDiffs = 1
	st := allRowsStore{newMapStore()} // no rows: the sweep is a no-op
	st.setState(shaC1, "idle")
	svc := NewService(g, st, nil, 0)

	err := svc.IncrementalSync(context.Background())
	if err == nil {
		t.Fatal("a Diff failure must be returned")
	}
	got := st.syncState()
	if got.Status != "error" || got.LastSyncedCommit != shaC1 || !strings.Contains(got.ErrorDetail, "diff") {
		t.Fatalf("sync state after Diff failure: %+v", got)
	}
	if st.bpUpserts != 0 || st.upserts(wfPath) != 0 {
		t.Fatalf("no governance write may happen on a failed Diff (bp=%d wf=%d)", st.bpUpserts, st.upserts(wfPath))
	}

	// Diff healthy again: the same window is retried in full.
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if st.bpUpserts != 1 || st.bpCommit != shaC2 {
		t.Fatalf("branch protection must be re-projected on retry (upserts=%d commit=%s)", st.bpUpserts, st.bpCommit)
	}
	if r := st.row(wfPath); r == nil || r.SourceCommit != shaC2 {
		t.Fatalf("workflow must be projected on retry: %+v", r)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != shaC2 {
		t.Fatalf("sync state after retry: %+v", got)
	}
}

// AC 13: exactly one Diff per IncrementalSync that does work.
func TestIncrementalSync_OneDiffPerSync(t *testing.T) {
	g := governanceChangeGit()
	g.diffs = append(g.diffs, git.FileDiff{Path: taskPath, Status: "added"})
	g.files[taskPath] = taskDoc("Pending", "T")
	st := allRowsStore{newMapStore()}
	st.setState(shaC1, "idle")
	svc := NewService(g, st, nil, 0)

	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.calls() != 1 {
		t.Fatalf("Diff called %d times in one sync, want 1", g.calls())
	}
	if _, ok := st.artifacts[taskPath]; !ok {
		t.Fatal("the artifact from the same diff must be projected")
	}
	// Up to date: no further Diff.
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.calls() != 1 {
		t.Fatalf("an up-to-date sync must not Diff, got %d calls", g.calls())
	}
}

// AC 14: the old row's delete error on a workflow rename is counted.
func TestIncrementalSync_WorkflowRenameDeleteErrorIsCounted(t *testing.T) {
	const oldPath = "workflows/old-name.yaml"
	g := &scriptedGit{
		head:  shaC2,
		diffs: []git.FileDiff{{OldPath: oldPath, Path: wfPath, Status: "renamed"}},
		files: map[string]string{wfPath: reviewedWorkflow},
	}
	st := allRowsStore{newMapStore()}
	st.setState(shaC1, "idle")
	st.putRow(acceptedRow(t, oldPath, strings.Replace(reviewedWorkflow, "id: task-default", "id: old-name", 1), shaC1))
	st.deleteWorkflowErr[oldPath] = errSimulated
	svc := NewService(g, st, nil, 0)
	svc.workflowsVerified.Store(true) // isolate from the sweep

	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("counted errors are reported through sync_state, got %v", err)
	}
	if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != shaC1 {
		t.Fatalf("a failed delete of the old row must hold the sync: %+v", got)
	}
	if st.row(oldPath) == nil {
		t.Fatal("precondition: the old row is still there")
	}
}

// AC 28: artifact read errors are classified, never silently dropped.
func TestIncrementalSync_ArtifactReadErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"non_git_error", errors.New("fork/exec git: resource temporarily unavailable")},
		{"transient_git_error", &git.GitError{Kind: git.ErrKindTransient, Op: "read_file", Message: "repository locked"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &scriptedGit{
				head:    shaC2,
				diffs:   []git.FileDiff{{Path: taskPath, Status: "modified"}},
				files:   map[string]string{taskPath: taskDoc("Completed", "Done")},
				readErr: map[string]error{taskPath: tc.err},
			}
			st := allRowsStore{newMapStore()}
			st.setState(shaC1, "idle")
			svc := NewService(g, st, nil, 0)

			err := svc.IncrementalSync(context.Background())
			if err == nil || !errors.Is(err, tc.err) {
				t.Fatalf("expected the read error to hold the sync, got %v", err)
			}
			if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != shaC1 {
				t.Fatalf("sync state: %+v", got)
			}

			g.mu.Lock()
			g.readErr = nil
			g.mu.Unlock()
			if err := svc.IncrementalSync(context.Background()); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if a, ok := st.artifacts[taskPath]; !ok || a.Status != "Completed" {
				t.Fatalf("the retry must project the change, got %+v", a)
			}
			if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != shaC2 {
				t.Fatalf("sync state after retry: %+v", got)
			}
		})
	}

	t.Run("gitlink_is_skipped_logged_and_counted", func(t *testing.T) {
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
		// A gitlink (submodule entry) named x.md whose commit is not in
		// this repository: `git show <c>:x.md` fails with "bad object",
		// which the git client classifies as a permanent GitError.
		repo.run("update-index", "--add", "--cacheinfo", "160000,"+gitlinkTarget+",x.md")
		repo.run("commit", "-q", "-m", "gitlink")
		head := repo.run("rev-parse", "HEAD")

		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatalf("a deterministic read failure must not wedge the sync: %v", err)
		}
		if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
			t.Fatalf("sync state: %+v", got)
		}
		if n := len(logs.lines("level=WARN", "skipped unreadable changed file", "x.md")); n != 1 {
			t.Fatalf("expected one Warn naming x.md, got %d:\n%s", n, logs.String())
		}
		synced := router.ofType(domain.EventProjectionSynced)
		if len(synced) != 1 {
			t.Fatalf("expected one projection_synced event, got %d", len(synced))
		}
		var p map[string]any
		if err := json.Unmarshal(synced[0].Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p["read_skipped"] != float64(1) || p["workflows_rejected"] != float64(0) {
			t.Fatalf("projection_synced payload = %+v", p)
		}
	})
}

// The head resolution failure is a sync error even when the store last
// reported idle.
func TestIncrementalSync_HeadFailureMarksError(t *testing.T) {
	st := allRowsStore{newMapStore()}
	st.setState(shaC1, "idle")
	svc := NewService(&headFailGit{}, st, nil, 0)
	if err := svc.IncrementalSync(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != shaC1 || !strings.Contains(got.ErrorDetail, "resolve sync target") {
		t.Fatalf("sync state: %+v", got)
	}
}

type headFailGit struct{ git.GitClient }

// gitlinkTarget is a commit id that does not exist in the scratch repo.
const gitlinkTarget = "0123456789abcdef0123456789abcdef01234567"

func (headFailGit) Head(context.Context) (string, error) { return "", errSimulated }
