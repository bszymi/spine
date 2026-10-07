package projection

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// INIT-023 TASK-011 review round 2: `git show <commit>:<path>` resolves a
// path to the object id its tree entry records and prints that object,
// whatever the entry's mode. A gitlink (mode 160000) is meant to record a
// commit in another repository, but nothing stops a push from recording
// the id of a blob that IS in this repository — any pushed branch can
// put one in the object store. The read then succeeds, and the
// projection used to accept the hidden body while every diff and
// repository viewer shows only "Subproject commit <id>". Workflow files
// and the branch-protection config are now typed from the commit's tree
// BEFORE every read, not only after a failed one.

// writeBlob stores body in the object database without adding it to any
// tree, as any pushed branch could, and returns its id.
func (r *scratchRepo) writeBlob(body string) string {
	r.t.Helper()
	f := filepath.Join(r.t.TempDir(), "blob")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return r.run("hash-object", "-w", "--", f)
}

// addGitlinkTo stages a gitlink at path recording id.
func (r *scratchRepo) addGitlinkTo(path, id string) {
	r.t.Helper()
	r.run("update-index", "--add", "--cacheinfo", "160000,"+id+","+path)
}

// assertHiddenBlobGitlink checks the attack's preconditions with the real
// client: the tree records a gitlink, the diff shows a type change or an
// addition, and yet `git show` prints the hidden body.
func assertHiddenBlobGitlink(t *testing.T, repo *scratchRepo, from, to, path, id, body string) {
	t.Helper()
	if ls := repo.run("ls-tree", to, "--", path); !strings.HasPrefix(ls, "160000 commit "+id+"\t") {
		t.Fatalf("precondition: %s must be a gitlink recording %s, ls-tree says %q", path, id, ls)
	}
	if typ := repo.run("cat-file", "-t", id); typ != "blob" {
		t.Fatalf("precondition: the recorded id must name a blob in this repository, got %q", typ)
	}
	got, err := repo.git.ReadFile(context.Background(), to, path)
	if err != nil || string(got) != body {
		t.Fatalf("precondition: git show %s:%s must print the hidden body, got %q, %v", to[:8], path, got, err)
	}
	if from != "" {
		if d := repo.run("diff", "--name-status", from, to, "--", path); d == "" {
			t.Fatalf("precondition: the diff must touch %s", path)
		}
	}
}

func TestProjection_GitlinkToExistingBlobAtWorkflowPathIsRejection(t *testing.T) {
	repo, st, router, svc, c1 := newIntegrityFixture(t, reviewedWorkflow)
	ctx := context.Background()

	// c2: the reviewed workflow file becomes a gitlink whose recorded id
	// is a review-less body's blob, and a second gitlink adds a new
	// workflow the same way.
	const newPath = "workflows/hidden.yaml"
	newBody := strings.Replace(reviewlessWorkflow, "id: task-default", "id: hidden-wf", 1)
	hidden, hiddenNew := repo.writeBlob(reviewlessWorkflow), repo.writeBlob(newBody)
	repo.remove(wfPath)
	repo.addGitlinkTo(wfPath, hidden)
	repo.addGitlinkTo(newPath, hiddenNew)
	c2 := repo.commitIndex("c2")
	if d := repo.run("diff", "--name-status", c1, c2, "--", wfPath); d != "T\t"+wfPath {
		t.Fatalf("precondition: the diff shows a type change, got %q", d)
	}
	assertHiddenBlobGitlink(t, repo, c1, c2, wfPath, hidden, reviewlessWorkflow)
	assertHiddenBlobGitlink(t, repo, c1, c2, newPath, hiddenNew, newBody)

	assertNoHiddenRow := func(t *testing.T, st allRowsStore, stage string) {
		t.Helper()
		if r := st.row(wfPath); r != nil && (r.SourceCommit == c2 || !hasStep(decodeDef(t, r.Definition), "review")) {
			t.Fatalf("%s: the hidden review-less body was projected: %+v", stage, r)
		}
		if r := st.row(newPath); r != nil {
			t.Fatalf("%s: the hidden new workflow was projected: %+v", stage, r)
		}
	}

	// Incremental sync: both are not_a_file rejections, the reviewed row
	// is kept quarantined, and the sync advances.
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state: %+v", got)
	}
	assertNoHiddenRow(t, st, "incremental")
	assertKeptQuarantined(t, st, c1)
	assertRejections(t, router, map[string][2]string{
		wfPath:  {"not_a_file", "kept"},
		newPath: {"not_a_file", "not_projected"},
	})

	// A new instance sweeps every stored row at head first.
	router.reset()
	if err := NewService(repo.git, st, router, 0).IncrementalSync(ctx); err != nil {
		t.Fatalf("fresh instance: %v", err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("fresh instance sync state: %+v", got)
	}
	assertNoHiddenRow(t, st, "sweep")
	assertKeptQuarantined(t, st, c1)

	// A FullRebuild over the same store keeps the reviewed row
	// quarantined.
	if err := NewService(repo.git, st, nil, 0).FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("rebuild sync state: %+v", got)
	}
	assertNoHiddenRow(t, st, "rebuild")
	assertKeptQuarantined(t, st, c1)

	// A FullRebuild into an empty store (a new deployment, SMP
	// provisioning) projects neither path.
	empty := allRowsStore{newMapStore()}
	emptyRouter := &recordingRouter{}
	if err := NewService(repo.git, empty, emptyRouter, 0).FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if got := empty.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("fresh rebuild sync state: %+v", got)
	}
	if r := empty.row(wfPath); r != nil {
		t.Fatalf("fresh rebuild projected %s: %+v", wfPath, r)
	}
	assertNoHiddenRow(t, empty, "fresh rebuild")
	assertRejections(t, emptyRouter, map[string][2]string{
		wfPath:  {"not_a_file", "not_projected"},
		newPath: {"not_a_file", "not_projected"},
	})

	// Replacing the gitlink with a regular file — the visible change a
	// reviewer approves — projects it.
	repo.run("rm", "--cached", "-q", "--", wfPath)
	repo.write(wfPath, reviewlessWorkflow)
	c3 := repo.commitIndex("c3", wfPath)
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if r := st.row(wfPath); r == nil || r.SourceCommit != c3 || r.Status != "Active" {
		t.Fatalf("a regular file at c3 must project: %+v", r)
	}
}

const (
	bpStrict = `version: 1
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
  - branch: "release/*"
    protections: [no-delete]
`
	bpRelaxed = "version: 1\nrules: []\n"
)

func TestProjection_GitlinkToExistingBlobAtBranchProtectionIsHeld(t *testing.T) {
	logs := captureLogs(t)
	ctx := context.Background()
	repo := newScratchRepo(t)
	repo.write(BranchProtectionConfigPath, bpStrict)
	repo.write(taskPath, taskDoc("Pending", "T"))
	c1 := repo.commit("c1")
	st := allRowsStore{newMapStore()}
	svc := NewService(repo.git, st, nil, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if len(st.bpRules) != 2 || st.bpCommit != c1 {
		t.Fatalf("after rebuild: %d rules at %s", len(st.bpRules), st.bpCommit)
	}

	// c2: the config becomes a gitlink whose recorded id is a `rules: []`
	// blob. The diff shows a submodule pointer, not the relaxation.
	relaxed := repo.writeBlob(bpRelaxed)
	repo.remove(BranchProtectionConfigPath)
	repo.addGitlinkTo(BranchProtectionConfigPath, relaxed)
	c2 := repo.commitIndex("c2")
	assertHiddenBlobGitlink(t, repo, c1, c2, BranchProtectionConfigPath, relaxed, bpRelaxed)

	assertRulesKept := func(t *testing.T, stage string) {
		t.Helper()
		if len(st.bpRules) != 2 || st.bpCommit != c1 {
			t.Fatalf("%s: the strict rules must stay: %d rules at %s", stage, len(st.bpRules), st.bpCommit)
		}
	}

	// Like a malformed config (ADR-009 §1): the sync holds in error and
	// the previous rules stay, on every tick.
	for tick := 1; tick <= 2; tick++ {
		if err := svc.IncrementalSync(ctx); err != nil {
			t.Fatalf("tick %d: counted errors are reported through sync_state, got %v", tick, err)
		}
		if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != c1 {
			t.Fatalf("tick %d: sync state %+v", tick, got)
		}
		assertRulesKept(t, "incremental")
	}
	if n := len(logs.lines("level=WARN", "failed to project branch-protection config", "tree entry: gitlink")); n == 0 {
		t.Fatalf("expected a Warn naming the gitlink entry:\n%s", logs.String())
	}

	// A FullRebuild keeps them too (the wipe never touches the rules).
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "error" {
		t.Fatalf("rebuild sync state: %+v", got)
	}
	assertRulesKept(t, "rebuild")

	// An empty store's rebuild writes no rules from the hidden blob.
	empty := allRowsStore{newMapStore()}
	if err := NewService(repo.git, empty, nil, 0).FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if empty.bpUpserts != 0 {
		t.Fatalf("fresh rebuild wrote %d rules from the hidden blob", len(empty.bpRules))
	}

	// The same relaxation committed as a regular file — the change a
	// reviewer sees — is projected.
	repo.run("rm", "--cached", "-q", "--", BranchProtectionConfigPath)
	repo.write(BranchProtectionConfigPath, bpRelaxed)
	c3 := repo.commitIndex("c3", BranchProtectionConfigPath)
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c3 {
		t.Fatalf("after the visible fix: %+v", got)
	}
	if len(st.bpRules) != 0 || st.bpCommit != c3 {
		t.Fatalf("the visible relaxation must project: %d rules at %s", len(st.bpRules), st.bpCommit)
	}
}

// countingTypingGit is scriptedGit plus a scripted TreeEntryTyper, and it
// counts reads per path.
type countingTypingGit struct {
	*scriptedGit
	typ    string
	typErr error

	mu    sync.Mutex
	reads map[string]int
	types map[string]int
}

func (g *countingTypingGit) TreeEntryType(_ context.Context, _, path string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.types == nil {
		g.types = map[string]int{}
	}
	g.types[path]++
	return g.typ, g.typErr
}

func (g *countingTypingGit) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	g.mu.Lock()
	if g.reads == nil {
		g.reads = map[string]int{}
	}
	g.reads[path]++
	g.mu.Unlock()
	return g.scriptedGit.ReadFile(ctx, ref, path)
}

func (g *countingTypingGit) readsOf(path string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reads[path]
}

// The decision table for a workflow file, with fakes: the entry is typed
// before the read; only a blob is read; a type that cannot be established
// holds the sync; a client without the capability keeps the historical
// behaviour (a successful read is trusted).
func TestBuildWorkflowRow_TypesTheEntryBeforeEveryRead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		typ       string
		typErr    error
		noTyper   bool
		want      string // "row", "not_a_file" or "held"
		wantReads int
	}{
		{name: "gitlink", typ: git.TreeEntryCommit, want: "not_a_file"},
		{name: "tree", typ: git.TreeEntryTree, want: "not_a_file"},
		{name: "no entry at that path", typ: "", want: "not_a_file"},
		{name: "blob", typ: git.TreeEntryBlob, want: "row", wantReads: 1},
		{name: "typing failed", typErr: errSimulated, want: "held"},
		{name: "unknown type", typ: "symlink?", want: "held"},
		{name: "client without the capability", noTyper: true, want: "row", wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sg := &scriptedGit{
				head:  shaC2,
				diffs: []git.FileDiff{{Path: wfPath, Status: "modified"}},
				files: map[string]string{wfPath: reviewlessWorkflow},
			}
			var g git.GitClient = sg
			cg := &countingTypingGit{scriptedGit: sg, typ: tc.typ, typErr: tc.typErr}
			if !tc.noTyper {
				g = cg
			}
			st := allRowsStore{newMapStore()}
			st.setState(shaC1, "idle")
			router := &recordingRouter{}
			if err := NewService(g, st, router, 0).IncrementalSync(context.Background()); err != nil {
				t.Fatalf("IncrementalSync: %v", err)
			}
			got := st.syncState()
			switch tc.want {
			case "row":
				if r := st.row(wfPath); r == nil || r.Status != "Active" || got.Status != "idle" {
					t.Fatalf("expected a projected row: %+v, state %+v", r, got)
				}
			case "not_a_file":
				if r := st.row(wfPath); r != nil || got.Status != "idle" || got.LastSyncedCommit != shaC2 {
					t.Fatalf("expected a rejection that advances: row %+v, state %+v", r, got)
				}
				assertRejections(t, router, map[string][2]string{wfPath: {"not_a_file", "not_projected"}})
			case "held":
				if r := st.row(wfPath); r != nil || got.Status != "error" || got.LastSyncedCommit != shaC1 {
					t.Fatalf("expected the sync held: row %+v, state %+v", r, got)
				}
				if n := len(router.ofType(domain.EventProjectionWorkflowRejected)); n != 0 {
					t.Fatalf("an unproved entry type is not a rejection, got %d", n)
				}
			}
			if !tc.noTyper {
				if n := cg.readsOf(wfPath); n != tc.wantReads {
					t.Fatalf("ReadFile(%s) called %d times, want %d", wfPath, n, tc.wantReads)
				}
			}
		})
	}
}

// typingReader is fakeGitReader plus a scripted TreeEntryTyper.
type typingReader struct {
	*fakeGitReader
	typ    string
	typErr error
	reads  int
}

func (g *typingReader) TreeEntryType(context.Context, string, string) (string, error) {
	return g.typ, g.typErr
}

func (g *typingReader) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	g.reads++
	return g.fakeGitReader.ReadFile(ctx, ref, path)
}

// The same table for the branch-protection file: anything but a blob is a
// sync error with the rules left as they were, like a malformed config.
func TestProjectBranchProtection_TypesTheEntryBeforeReading(t *testing.T) {
	for _, tc := range []struct {
		name    string
		typ     string
		typErr  error
		wantErr string
	}{
		{name: "gitlink", typ: git.TreeEntryCommit, wantErr: "tree entry: gitlink"},
		{name: "tree", typ: git.TreeEntryTree, wantErr: "tree entry: tree"},
		{name: "listed but no entry", typ: "", wantErr: "tree entry: none"},
		{name: "typing failed", typErr: errSimulated, wantErr: "simulated failure"},
		{name: "unknown type", typ: "symlink?", wantErr: "unexpected tree entry type"},
		{name: "blob", typ: git.TreeEntryBlob},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &typingReader{
				fakeGitReader: &fakeGitReader{fileExists: true, content: []byte(bpRelaxed)},
				typ:           tc.typ, typErr: tc.typErr,
			}
			st := &fakeStore{}
			err := NewService(g, st, nil, 0).projectBranchProtection(context.Background(), shaC2)
			if tc.wantErr == "" {
				if err != nil || st.upsertCalls != 1 || g.reads != 1 {
					t.Fatalf("a blob must be read and projected: err=%v upserts=%d reads=%d", err, st.upsertCalls, g.reads)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if tc.typErr != nil && !errors.Is(err, tc.typErr) {
				t.Fatalf("the typing failure must be in the error chain: %v", err)
			}
			if st.upsertCalls != 0 || g.reads != 0 {
				t.Fatalf("nothing may be read or written: upserts=%d reads=%d", st.upsertCalls, g.reads)
			}
		})
	}
}
