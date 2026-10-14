package projection

// INIT-023 TASK-003 (audit F4, P3-9): the projection skips, records and
// advances past oversized and unsafe-named files. Tests that measure
// runtime.MemStats.TotalAlloc do not run in parallel.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

const refusedTestLimit = 1 << 20 // WithMaxBlobBytes(1 MiB); the blobs are 8 MiB

const refusedGovDoc = "---\ntype: Governance\ntitle: G\nstatus: Living Document\n---\n\n# G\n"

func allocDuring(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func boundedClient(dir string, opts ...git.CLIOption) *git.CLIClient {
	return git.NewCLIClient(dir, append([]git.CLIOption{git.WithMaxBlobBytes(refusedTestLimit)}, opts...)...)
}

func eightMiB(prefix string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for b.Len() < 8<<20 {
		b.WriteString("ordinary meeting notes\n")
	}
	return b.String()
}

// skipEvents decodes the projection_file_skipped events.
func skipEvents(t *testing.T, r *recordingRouter) []map[string]any {
	t.Helper()
	var out []map[string]any
	evs := r.ofType(domain.EventProjectionFileSkipped)
	for i := range evs {
		e := &evs[i]
		var p map[string]any
		dec := json.NewDecoder(bytes.NewReader(e.Payload))
		dec.UseNumber() // exact byte counts
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("skip payload: %v", err)
		}
		p["_event_id"] = e.EventID
		p["_artifact_path"] = e.ArtifactPath
		out = append(out, p)
	}
	return out
}

func wantSkipID(head, reason, path string) string {
	sum := sha256.Sum256([]byte(reason + "\x00" + path))
	return "projskip-" + head[:12] + "-" + hex.EncodeToString(sum[:])[:12]
}

// assertOneSkip checks the single projection_file_skipped event and its
// payload (§7: path, reason, size_bytes, limit_bytes, commit; plus
// git_alloc_limit_bytes, null unless git refused an allocation — review
// round 1).
func assertOneSkip(t *testing.T, r *recordingRouter, head, path, reason string, size, limit any) {
	t.Helper()
	assertOneSkipAlloc(t, r, head, path, reason, size, limit, nil)
}

func assertOneSkipAlloc(t *testing.T, r *recordingRouter, head, path, reason string, size, limit, gitAlloc any) {
	t.Helper()
	evs := skipEvents(t, r)
	if len(evs) != 1 {
		t.Fatalf("projection_file_skipped events = %+v, want exactly one", evs)
	}
	ev := evs[0]
	display := path
	if reason == "unsafe_path" {
		display = strconv.Quote(path)
	}
	want := map[string]any{
		"path": display, "reason": reason, "size_bytes": size, "limit_bytes": limit,
		"git_alloc_limit_bytes": gitAlloc, "commit": head,
		"_event_id": wantSkipID(head, reason, path), "_artifact_path": display,
	}
	for k, v := range want {
		if fmt.Sprint(ev[k]) != fmt.Sprint(v) {
			t.Errorf("event %s = %v, want %v (event %+v)", k, ev[k], v, ev)
		}
	}
	if len(ev) != len(want) {
		t.Errorf("event payload has extra fields: %+v", ev)
	}
}

// AC 11.1: a non-artifact 8 MiB docs/notes.md lands; the sync skips it,
// records it and advances without reading it.
func TestIncrementalSync_OversizedFileIsSkippedRecordedAndAdvances(t *testing.T) {
	ctx := context.Background()
	repo := newScratchRepo(t)
	repo.write("governance/g.md", refusedGovDoc)
	repo.commit("seed")
	st := newMapStore()
	ev := &recordingRouter{}
	svc := NewService(boundedClient(repo.dir), st, ev, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	notes := eightMiB("# Notes\n\n")
	repo.write("docs/notes.md", notes)
	head := repo.commit("notes")
	ev.reset()
	skippedBefore := observe.GlobalMetrics.ProjectionFilesSkipped.Value()

	var err error
	alloc := allocDuring(func() { err = svc.IncrementalSync(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
		t.Fatalf("sync state = %+v, want idle@%s", got, head)
	}
	if alloc >= 2<<20 {
		t.Errorf("IncrementalSync allocated %d KiB; want < 2 MiB", alloc>>10)
	}
	assertOneSkip(t, ev, head, "docs/notes.md", "oversized", len(notes), refusedTestLimit)
	if d := observe.GlobalMetrics.ProjectionFilesSkipped.Value() - skippedBefore; d != 1 {
		t.Errorf("ProjectionFilesSkipped rose by %d, want 1", d)
	}
	if p := syncedPayload(t, ev); fmt.Sprint(p["refused"]) != "1" || fmt.Sprint(p["errors"]) != "0" {
		t.Errorf("projection_synced = %+v, want refused 1, errors 0", p)
	}
}

// AC 11.2: FullRebuild with an oversized ARTIFACT at HEAD.
func TestFullRebuild_OversizedArtifactIsSkippedAndRecorded(t *testing.T) {
	ctx := context.Background()
	repo := newScratchRepo(t)
	repo.write("governance/g.md", refusedGovDoc)
	big := eightMiB(refusedGovDoc)
	repo.write("governance/big.md", big)
	head := repo.commit("seed")
	st := newMapStore()
	ev := &recordingRouter{}
	svc := NewService(boundedClient(repo.dir), st, ev, 0)

	var err error
	alloc := allocDuring(func() { err = svc.FullRebuild(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.artifacts["governance/big.md"]; ok {
		t.Fatal("the oversized artifact was projected")
	}
	if _, ok := st.artifacts["governance/g.md"]; !ok {
		t.Fatal("the small artifact was not projected")
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
		t.Fatalf("sync state = %+v, want idle@%s", got, head)
	}
	if alloc >= 2<<20 {
		t.Errorf("FullRebuild allocated %d KiB; want < 2 MiB", alloc>>10)
	}
	assertOneSkip(t, ev, head, "governance/big.md", "oversized", len(big), refusedTestLimit)

	// A second rebuild at the same commit re-emits the same event id,
	// which the delivery queue de-duplicates.
	first := ev.ofType(domain.EventProjectionFileSkipped)[0].EventID
	ev.reset()
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if again := ev.ofType(domain.EventProjectionFileSkipped); len(again) != 1 || again[0].EventID != first {
		t.Fatalf("second rebuild events = %+v, want the same id %s", again, first)
	}
}

// Review round 1 (P3): a file git refuses to produce under its
// allocation limit is reported with that limit. Here a gitlink named
// *.md records an in-repo commit that adds an 8 MiB blob: the size probe
// sees the small commit object, and `git show` would render the commit
// with its patch, so git refuses the allocation. The record must say
// which limit refused it (git_alloc_limit_bytes = the 4 MiB alloc limit
// that WithMaxBlobBytes(1 MiB) derives) instead of reading as a file
// "over" the 1 MiB read limit, and the rebuild still advances.
func TestFullRebuild_GitAllocRefusalNamesItsLimit(t *testing.T) {
	ctx := context.Background()
	repo := newScratchRepo(t)
	repo.write("governance/g.md", refusedGovDoc)
	repo.commit("seed")
	repo.run("checkout", "-q", "-b", "side")
	repo.write("big.bin", eightMiB(""))
	side := repo.commit("big")
	repo.run("checkout", "-q", "main")
	repo.addGitlinkTo("initiatives/g.md", side)
	head := repo.commitIndex("gitlink")
	st := newMapStore()
	ev := &recordingRouter{}
	const allocLimit = 4 * refusedTestLimit // the documented default: 4 x the per-blob limit
	svc := NewService(boundedClient(repo.dir), st, ev, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.artifacts["initiatives/g.md"]; ok {
		t.Fatal("the gitlink was projected")
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
		t.Fatalf("sync state = %+v, want idle@%s", got, head)
	}
	assertOneSkipAlloc(t, ev, head, "initiatives/g.md", "oversized", -1, refusedTestLimit, allocLimit)
}

// AC 11.3: a projected artifact modified past the limit loses its row
// (as for a parse failure) and the sync advances.
func TestIncrementalSync_ArtifactModifiedPastTheLimitLosesItsRow(t *testing.T) {
	ctx := context.Background()
	repo := newScratchRepo(t)
	repo.write("governance/g.md", refusedGovDoc)
	repo.commit("seed")
	st := newMapStore()
	ev := &recordingRouter{}
	svc := NewService(boundedClient(repo.dir), st, ev, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.artifacts["governance/g.md"]; !ok {
		t.Fatal("precondition: the artifact is projected")
	}
	repo.write("governance/g.md", eightMiB(refusedGovDoc))
	head := repo.commit("grow past the limit")
	ev.reset()
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.artifacts["governance/g.md"]; ok {
		t.Fatal("the row of an artifact modified past the read limit survived")
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
		t.Fatalf("sync state = %+v, want idle@%s", got, head)
	}
	if evs := skipEvents(t, ev); len(evs) != 1 || evs[0]["reason"] != "oversized" {
		t.Fatalf("skip events = %+v, want one oversized", evs)
	}
}

// AC 11.4, as amended by the orchestrator ruling (TASK-011's D2 and its
// isContentReadError hook): an oversized workflow is a read_bounds
// REJECTION that advances the sync — a verified previous row is kept
// quarantined, a new file is not projected — not a sync error that would
// wedge the projection on a file that never changes. The branch-protection
// config stays fail-closed: an oversized one is a held sync error with
// the rules unchanged (I4).
func TestProjection_OversizedGovernanceFiles(t *testing.T) {
	ctx := context.Background()
	pad := func(body string) string { return body + "# " + strings.Repeat("p", 8<<20) + "\n" }
	repo := newScratchRepo(t)
	repo.write("workflows/task-default.yaml", reviewedWorkflow)
	repo.write(".spine/branch-protection.yaml", protectMain)
	c1 := repo.commit("seed")
	st := newMapStore()
	ev := &recordingRouter{}
	svc := NewService(boundedClient(repo.dir), st, ev, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if r := st.row("workflows/task-default.yaml"); r == nil || r.Status != string(domain.WorkflowStatusActive) {
		t.Fatalf("precondition: the workflow is projected Active, got %+v", r)
	}
	rulesBefore := append([]store.BranchProtectionRuleProjection(nil), st.bpRules...)

	t.Run("workflow", func(t *testing.T) {
		repo.write("workflows/task-default.yaml", pad(reviewedWorkflow))
		repo.write("workflows/new.yaml", pad(strings.Replace(reviewlessWorkflow, "id: task-default", "id: new", 1)))
		c2 := repo.commit("oversized workflows")
		ev.reset()
		var err error
		alloc := allocDuring(func() { err = svc.IncrementalSync(ctx) })
		if err != nil {
			t.Fatal(err)
		}
		if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
			t.Fatalf("sync state = %+v, want idle@%s (a rejection advances)", got, c2)
		}
		if alloc >= 2<<20 {
			t.Errorf("IncrementalSync allocated %d KiB; want < 2 MiB", alloc>>10)
		}
		kept := st.row("workflows/task-default.yaml")
		if kept == nil || kept.Status != string(domain.WorkflowStatusQuarantined) || kept.SourceCommit != c1 {
			t.Fatalf("previous row = %+v, want it kept quarantined at %s", kept, c1)
		}
		if st.row("workflows/new.yaml") != nil {
			t.Fatal("an oversized new workflow was projected")
		}
		rej := ev.rejections(t)
		got := map[string]string{}
		for _, r := range rej {
			if r["check"] != "read_bounds" {
				t.Errorf("rejection %+v, want check read_bounds", r)
			}
			got[fmt.Sprint(r["workflow_path"])] = fmt.Sprint(r["action"])
		}
		if got["workflows/task-default.yaml"] != "kept" || got["workflows/new.yaml"] != "not_projected" || len(got) != 2 {
			t.Fatalf("rejections = %+v, want task-default kept and new not_projected", rej)
		}
		if evs := skipEvents(t, ev); len(evs) != 0 {
			t.Errorf("oversized workflows are reported as rejections, not skips: %+v", evs)
		}
	})

	t.Run("branch-protection config", func(t *testing.T) {
		before := st.syncState().LastSyncedCommit
		repo.write(".spine/branch-protection.yaml", pad("version: 1\nrules: []\n"))
		repo.commit("oversized config")
		for tick := 0; tick < 2; tick++ {
			_ = svc.IncrementalSync(ctx)
			if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != before {
				t.Fatalf("tick %d: sync state = %+v, want error@%s (held)", tick, got, before)
			}
		}
		if fmt.Sprint(st.bpRules) != fmt.Sprint(rulesBefore) {
			t.Fatalf("rules changed: %+v, want %+v", st.bpRules, rulesBefore)
		}
	})
}

// commitRaw commits files (any byte sequence as a path) on top of
// parent straight into the object store and moves main to it. The
// working tree is not updated (a filesystem cannot hold every such
// name); the projection reads commits only.
func commitRaw(t *testing.T, r *scratchRepo, parent string, files map[string]string) string {
	t.Helper()
	index := filepath.Join(r.dir, ".git", "t003-raw-index")
	_ = os.Remove(index)
	env := append(os.Environ(), "GIT_INDEX_FILE="+index, "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = r.dir
		cmd.Env = env
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	if parent != "" {
		run(nil, "read-tree", parent)
	}
	var in bytes.Buffer
	for p, body := range files {
		blob := run([]byte(body), "hash-object", "-w", "--stdin")
		fmt.Fprintf(&in, "100644 %s 0\t%s\x00", blob, p)
	}
	run(in.Bytes(), "update-index", "-z", "--add", "--index-info")
	cargs := []string{"commit-tree", run(nil, "write-tree")}
	if parent != "" {
		cargs = append(cargs, "-p", parent)
	}
	c := run(nil, append(cargs, "-m", "raw")...)
	run(nil, "update-ref", "refs/heads/main", c)
	return c
}

// utf8Store fails the test if any store call carries a path that is not
// valid UTF-8: PostgreSQL rejects such a text parameter (SQLSTATE
// 22021), which would wedge every sync.
type utf8Store struct {
	*mapStore
	t *testing.T
}

func (s *utf8Store) check(op, p string) {
	if !utf8.ValidString(p) {
		s.t.Errorf("%s called with invalid UTF-8 path %q", op, p)
	}
}

func (s *utf8Store) UpsertArtifactProjection(ctx context.Context, p *store.ArtifactProjection) error {
	s.check("UpsertArtifactProjection", p.ArtifactPath)
	return s.mapStore.UpsertArtifactProjection(ctx, p)
}

func (s *utf8Store) DeleteArtifactProjection(ctx context.Context, p string) error {
	s.check("DeleteArtifactProjection", p)
	return s.mapStore.DeleteArtifactProjection(ctx, p)
}

func (s *utf8Store) UpsertArtifactLinks(ctx context.Context, src string, l []store.ArtifactLink, c string) error {
	s.check("UpsertArtifactLinks", src)
	return s.mapStore.UpsertArtifactLinks(ctx, src, l, c)
}

func (s *utf8Store) DeleteArtifactLinks(ctx context.Context, src string) error {
	s.check("DeleteArtifactLinks", src)
	return s.mapStore.DeleteArtifactLinks(ctx, src)
}

func (s *utf8Store) UpsertWorkflowProjection(ctx context.Context, p *store.WorkflowProjection) error {
	s.check("UpsertWorkflowProjection", p.WorkflowPath)
	return s.mapStore.UpsertWorkflowProjection(ctx, p)
}

func (s *utf8Store) GetWorkflowProjection(ctx context.Context, p string) (*store.WorkflowProjection, error) {
	s.check("GetWorkflowProjection", p)
	return s.mapStore.GetWorkflowProjection(ctx, p)
}

func (s *utf8Store) DeleteWorkflowProjection(ctx context.Context, p string) error {
	s.check("DeleteWorkflowProjection", p)
	return s.mapStore.DeleteWorkflowProjection(ctx, p)
}

// AC 11.5 and 11.6: unsafe paths are refused and recorded on both entry
// points and never reach the store; a workflow at an unsafe path is not
// projected.
func TestProjection_UnsafePathsNeverReachTheStore(t *testing.T) {
	ctx := context.Background()
	unsafeArtifact := "initiatives/\xff/x.md"
	unsafeWorkflow := "workflows/a\tb.yaml"
	bidiArtifact := "governance/a\u202eb.md"

	t.Run("FullRebuild", func(t *testing.T) {
		repo := newScratchRepo(t)
		head := commitRaw(t, repo, "", map[string]string{
			"governance/g.md": refusedGovDoc,
			unsafeArtifact:    refusedGovDoc,
			bidiArtifact:      refusedGovDoc,
			unsafeWorkflow:    reviewedWorkflow,
		})
		st := &utf8Store{mapStore: newMapStore(), t: t}
		ev := &recordingRouter{}
		svc := NewService(git.NewCLIClient(repo.dir), st, ev, 0)
		if err := svc.FullRebuild(ctx); err != nil {
			t.Fatal(err)
		}
		if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
			t.Fatalf("sync state = %+v, want idle@%s", got, head)
		}
		if len(st.artifacts) != 1 || st.artifacts["governance/g.md"] == nil {
			t.Fatalf("artifacts = %v, want only governance/g.md", keysOf(st.artifacts))
		}
		if len(st.workflows) != 0 {
			t.Fatalf("an unsafe-named workflow was projected: %v", st.workflows)
		}
		assertSkips(t, ev, head, unsafeArtifact, bidiArtifact, unsafeWorkflow)
	})

	t.Run("IncrementalSync", func(t *testing.T) {
		repo := newScratchRepo(t)
		c1 := commitRaw(t, repo, "", map[string]string{"governance/g.md": refusedGovDoc})
		st := &utf8Store{mapStore: newMapStore(), t: t}
		ev := &recordingRouter{}
		svc := NewService(git.NewCLIClient(repo.dir), st, ev, 0)
		if err := svc.FullRebuild(ctx); err != nil {
			t.Fatal(err)
		}
		head := commitRaw(t, repo, c1, map[string]string{
			unsafeArtifact: refusedGovDoc,
			bidiArtifact:   refusedGovDoc,
			unsafeWorkflow: reviewedWorkflow,
		})
		ev.reset()
		if err := svc.IncrementalSync(ctx); err != nil {
			t.Fatal(err)
		}
		if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != head {
			t.Fatalf("sync state = %+v, want idle@%s", got, head)
		}
		if len(st.artifacts) != 1 || len(st.workflows) != 0 {
			t.Fatalf("artifacts %v, workflows %v; want only governance/g.md and no workflow", keysOf(st.artifacts), st.workflows)
		}
		assertSkips(t, ev, head, unsafeArtifact, bidiArtifact, unsafeWorkflow)
		if p := syncedPayload(t, ev); fmt.Sprint(p["refused"]) != "3" {
			t.Errorf("projection_synced refused = %v, want 3", p["refused"])
		}

		// Deleting and renaming them away touches nothing either.
		c3 := commitRawDelete(t, repo, head, unsafeArtifact, unsafeWorkflow)
		if err := svc.IncrementalSync(ctx); err != nil {
			t.Fatal(err)
		}
		if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c3 {
			t.Fatalf("after deletes: %+v, want idle@%s", got, c3)
		}
	})
}

// commitRawDelete removes paths from parent's tree.
func commitRawDelete(t *testing.T, r *scratchRepo, parent string, paths ...string) string {
	t.Helper()
	index := filepath.Join(r.dir, ".git", "t003-raw-index")
	_ = os.Remove(index)
	env := append(os.Environ(), "GIT_INDEX_FILE="+index, "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = r.dir
		cmd.Env = env
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	run(nil, "read-tree", parent)
	run([]byte(strings.Join(paths, "\x00")+"\x00"), "update-index", "-z", "--force-remove", "--stdin")
	tree := run(nil, "write-tree")
	c := run(nil, "commit-tree", tree, "-p", parent, "-m", "delete")
	run(nil, "update-ref", "refs/heads/main", c)
	return c
}

func assertSkips(t *testing.T, ev *recordingRouter, head string, paths ...string) {
	t.Helper()
	evs := skipEvents(t, ev)
	got := map[string]map[string]any{}
	for _, e := range evs {
		got[fmt.Sprint(e["path"])] = e
		if !utf8.ValidString(fmt.Sprint(e["_artifact_path"])) {
			t.Errorf("event artifact_path is not valid UTF-8: %q", e["_artifact_path"])
		}
	}
	if len(got) != len(paths) {
		t.Fatalf("skip events = %+v, want one per %q", evs, paths)
	}
	for _, p := range paths {
		e := got[strconv.Quote(p)]
		if e == nil || e["reason"] != "unsafe_path" || e["size_bytes"] != nil || e["limit_bytes"] != nil ||
			e["git_alloc_limit_bytes"] != nil || e["commit"] != head || e["_event_id"] != wantSkipID(head, "unsafe_path", p) {
			t.Errorf("skip event for %q = %+v", p, e)
		}
	}
}

func keysOf(m map[string]*store.ArtifactProjection) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// countingStore counts artifact writes.
type countingStore struct {
	*mapStore
	writes int
}

func (s *countingStore) UpsertArtifactProjection(ctx context.Context, p *store.ArtifactProjection) error {
	s.writes++
	return s.mapStore.UpsertArtifactProjection(ctx, p)
}

func (s *countingStore) DeleteArtifactProjection(ctx context.Context, p string) error {
	s.writes++
	return s.mapStore.DeleteArtifactProjection(ctx, p)
}

func (s *countingStore) UpsertWorkflowProjection(ctx context.Context, p *store.WorkflowProjection) error {
	s.writes++
	return s.mapStore.UpsertWorkflowProjection(ctx, p)
}

func (s *countingStore) DeleteWorkflowProjection(ctx context.Context, p string) error {
	s.writes++
	return s.mapStore.DeleteWorkflowProjection(ctx, p)
}

// AC 20: a read-command ceiling hit fails closed: the error is the typed
// one, nothing is written, and the state is error at the old commit.
func TestIncrementalSync_ReadCeilingHitFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := newScratchRepo(t)
	repo.write("governance/g.md", refusedGovDoc)
	c1 := repo.commit("seed")
	st := &countingStore{mapStore: newMapStore()}
	svc := NewService(git.NewCLIClient(repo.dir, git.WithMaxOutputBytes(4<<10)), st, nil, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		repo.write(fmt.Sprintf("initiatives/INIT-001/notes/note-%03d.md", i), refusedGovDoc)
	}
	repo.commit("300 files")
	st.writes = 0

	err := svc.IncrementalSync(ctx)
	if !errors.Is(err, git.ErrOutputTooLarge) {
		t.Fatalf("IncrementalSync = %v, want errors.Is(err, git.ErrOutputTooLarge)", err)
	}
	if got := st.syncState(); got.LastSyncedCommit != c1 || got.Status != "error" {
		t.Fatalf("sync state = %+v, want error@%s", got, c1)
	}
	if st.writes != 0 {
		t.Fatalf("the store recorded %d writes; want none", st.writes)
	}
}

// AC 22: the CHANGELOG upgrade note is pinned by behaviour. A store idle
// at a commit that already holds a non-ASCII artifact (as every store
// synced before TASK-003 is) does not pick it up incrementally while the
// file is unchanged; only a rebuild does.
func TestUpgrade_NonASCIIArtifactNeedsARebuild(t *testing.T) {
	ctx := context.Background()
	repo := newScratchRepo(t)
	epic := "initiatives/INIT-001/epics/EPIC-001-résumé/epic.md"
	repo.write("initiatives/INIT-001/initiative.md", "---\nid: INIT-001\ntype: Initiative\ntitle: Seed\nstatus: Pending\ncreated: 2026-10-06\n---\n\n# seed\n")
	repo.write(epic, "---\nid: EPIC-001\ntype: Epic\ntitle: Resume\nstatus: Pending\ncreated: 2026-10-06\ninitiative: /initiatives/INIT-001/initiative.md\nlinks:\n  - type: parent\n    target: /initiatives/INIT-001/initiative.md\n---\n\n# EPIC-001\n")
	c1 := repo.commit("seed")
	st := newMapStore()
	st.setState(c1, "idle") // synced before the fix: the epic was never projected
	svc := NewService(git.NewCLIClient(repo.dir), st, nil, 0)

	repo.write("docs/unrelated.md", "# notes\n")
	c2 := repo.commit("unrelated")
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state = %+v, want idle@%s", got, c2)
	}
	if _, ok := st.artifacts[epic]; ok {
		t.Fatal("IncrementalSync backfilled an unchanged non-ASCII artifact; revisit the CHANGELOG upgrade note and owner question 3")
	}
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.artifacts[epic]; !ok {
		t.Fatal("FullRebuild did not project the non-ASCII artifact")
	}
}
