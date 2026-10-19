package projection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

// Adversarial review of the INIT-023 wave 4 follow-up (P1, P2). A file
// entry's MODE says nothing about the object it records: `update-index
// --cacheinfo 100644,<id>,<path>` records any id, and a push with the
// default receive config checks only that the object exists. The
// projection types the entry before reading a governance file
// (TreeEntryType), but that reads only the mode — "blob" — so the object
// was whatever the git client's read made of it:
//
//   - P1: an annotated tag whose target is a blob. `git cat-file blob <tag>`
//     peels it, so the read returned the target's bytes cleanly. git diff
//     shows the tag object's own text ("object <id>\ntype blob\ntag …"), so
//     a reviewed workflow became review-less and the branch-protection
//     rules were relaxed with nothing visible to review.
//   - P2: a tree or a commit. `cat-file blob` failed with a plain Permanent
//     "bad file", which the classifier — TreeEntryType says "blob" — took
//     for infrastructure and held the sync on, at every tick, until
//     someone rewrote the file: a wedge any push could plant.
//
// The git client now types the recorded object itself and refuses
// anything but a blob as git.ErrNotBlob (NotBlobError.FileEntry); the
// projection describes it as entryNonBlobObject.

// writeObject runs `git <args>` with stdin in the repository and returns
// its trimmed stdout (mktag reads the tag from stdin).
func (r *scratchRepo) writeObject(stdin string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// writeTag stores an annotated tag object pointing at target (of type
// typ) without any ref naming it, as any pushed branch's history could
// carry one, and returns its id.
func (r *scratchRepo) writeTag(target, typ string) string {
	r.t.Helper()
	return r.writeObject(fmt.Sprintf("object %s\ntype %s\ntag hidden\ntagger t <t@t> 0 +0000\n\nm\n", target, typ), "mktag")
}

// addFileEntryTo stages a regular-file entry (mode 100644) at path that
// records id, whatever object id names.
func (r *scratchRepo) addFileEntryTo(path, id string) {
	r.t.Helper()
	r.run("update-index", "--add", "--cacheinfo", "100644,"+id+","+path)
}

// assertFileEntryRecords checks a fixture's preconditions: the tree holds
// a regular-file entry at path recording id, an object of type objType;
// TreeEntryType therefore says "blob"; and the real client's ReadFile
// refuses it as not a file with FileEntry set.
func assertFileEntryRecords(t *testing.T, repo *scratchRepo, commit, path, id, objType string) {
	t.Helper()
	if ls := repo.run("ls-tree", commit, "--", path); !strings.HasPrefix(ls, "100644 blob "+id+"\t") {
		t.Fatalf("precondition: %s must be a file entry recording %s, ls-tree says %q", path, id, ls)
	}
	if typ := repo.run("cat-file", "-t", id); typ != objType {
		t.Fatalf("precondition: the recorded object must be a %s, got %q", objType, typ)
	}
	if typ, err := repo.git.TreeEntryType(context.Background(), commit, path); err != nil || typ != git.TreeEntryBlob {
		t.Fatalf("precondition: TreeEntryType(%s) = %q, %v; want blob (the mode)", path, typ, err)
	}
	got, err := repo.git.ReadFile(context.Background(), commit, path)
	var nb *git.NotBlobError
	if !errors.As(err, &nb) || !nb.FileEntry || nb.Type != objType || got != nil {
		t.Fatalf("ReadFile(%s:%s) = %q, %v; want the not-a-file refusal of a file entry recording a %s", commit[:8], path, got, err, objType)
	}
}

// assertTagHidesBody checks the P1 attack's preconditions on top: the
// follow-up's read (`git cat-file blob <tag>`) printed the hidden body,
// and the diff a reviewer sees shows only the tag object's text.
func assertTagHidesBody(t *testing.T, repo *scratchRepo, from, to, path, tag, body string) {
	t.Helper()
	assertFileEntryRecords(t, repo, to, path, tag, git.ObjectTag)
	if got := repo.run("cat-file", "blob", tag); got != strings.TrimSpace(body) {
		t.Fatalf("precondition: cat-file blob of the tag must print the hidden body, got %q", got)
	}
	// Every line the diff adds is a line of the tag object's own text.
	d := repo.run("diff", from, to, "--", path)
	tagText := "\n" + repo.run("cat-file", "tag", tag) + "\n"
	added := 0
	for _, line := range strings.Split(d, "\n") {
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}
		added++
		if !strings.Contains(tagText, "\n"+line[1:]+"\n") {
			t.Fatalf("precondition: the diff adds %q, which is not the tag object's text:\n%s", line, d)
		}
	}
	if added == 0 || !strings.Contains(d, "+object ") {
		t.Fatalf("precondition: the diff must show the tag object's text:\n%s", d)
	}
}

// P1 at a workflow path: the reviewed workflow's entry is replaced by one
// recording a tag whose target is a review-less body, and a new workflow
// is added the same way. Both are not_a_file rejections; the reviewed
// row is kept quarantined and nothing hidden is projected — incremental,
// sweep, rebuild and a rebuild into an empty store.
func TestProjection_FileEntryRecordingATagAtWorkflowPathIsRejection(t *testing.T) {
	repo, st, router, svc, c1 := newIntegrityFixture(t, reviewedWorkflow)
	ctx := context.Background()

	const newPath = "workflows/hidden.yaml"
	newBody := strings.Replace(reviewlessWorkflow, "id: task-default", "id: hidden-wf", 1)
	tag := repo.writeTag(repo.writeBlob(reviewlessWorkflow), "blob")
	tagNew := repo.writeTag(repo.writeBlob(newBody), "blob")
	repo.remove(wfPath)
	repo.addFileEntryTo(wfPath, tag)
	repo.addFileEntryTo(newPath, tagNew)
	c2 := repo.commitIndex("c2")
	if d := repo.run("diff", "--name-status", c1, c2, "--", wfPath); d != "M\t"+wfPath {
		t.Fatalf("precondition: the diff shows a modification, got %q", d)
	}
	assertTagHidesBody(t, repo, c1, c2, wfPath, tag, reviewlessWorkflow)
	assertTagHidesBody(t, repo, c1, c2, newPath, tagNew, newBody)

	assertNoHiddenRow := func(t *testing.T, st allRowsStore, stage string) {
		t.Helper()
		if r := st.row(wfPath); r != nil && (r.SourceCommit == c2 || !hasStep(decodeDef(t, r.Definition), "review")) {
			t.Fatalf("%s: the hidden review-less body was projected: %+v", stage, r)
		}
		if r := st.row(newPath); r != nil {
			t.Fatalf("%s: the hidden new workflow was projected: %+v", stage, r)
		}
	}

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

	router.reset()
	if err := NewService(repo.git, st, router, 0).IncrementalSync(ctx); err != nil {
		t.Fatalf("fresh instance: %v", err)
	}
	assertNoHiddenRow(t, st, "sweep")
	assertKeptQuarantined(t, st, c1)

	if err := NewService(repo.git, st, nil, 0).FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("rebuild sync state: %+v", got)
	}
	assertNoHiddenRow(t, st, "rebuild")
	assertKeptQuarantined(t, st, c1)

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
}

// P1 at the branch-protection config: a tag whose target is a `rules: []`
// blob. Like a gitlink there, the sync holds in error and the strict rules
// stay, on every tick and through a rebuild; an empty store's rebuild
// writes no rules from the hidden blob.
func TestProjection_FileEntryRecordingATagAtBranchProtectionIsHeld(t *testing.T) {
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

	tag := repo.writeTag(repo.writeBlob(bpRelaxed), "blob")
	repo.remove(BranchProtectionConfigPath)
	repo.addFileEntryTo(BranchProtectionConfigPath, tag)
	c2 := repo.commitIndex("c2")
	assertTagHidesBody(t, repo, c1, c2, BranchProtectionConfigPath, tag, bpRelaxed)

	for tick := 1; tick <= 2; tick++ {
		if err := svc.IncrementalSync(ctx); err != nil {
			t.Fatalf("tick %d: counted errors are reported through sync_state, got %v", tick, err)
		}
		if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != c1 {
			t.Fatalf("tick %d: sync state %+v", tick, got)
		}
		if len(st.bpRules) != 2 || st.bpCommit != c1 {
			t.Fatalf("tick %d: the strict rules must stay: %d rules at %s", tick, len(st.bpRules), st.bpCommit)
		}
	}
	if n := len(logs.lines("level=WARN", "failed to project branch-protection config", "tree entry: "+entryNonBlobObject)); n == 0 {
		t.Fatalf("expected a Warn naming the non-blob object entry:\n%s", logs.String())
	}
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if len(st.bpRules) != 2 || st.bpCommit != c1 {
		t.Fatalf("rebuild: the strict rules must stay: %d rules at %s", len(st.bpRules), st.bpCommit)
	}
	empty := allRowsStore{newMapStore()}
	if err := NewService(repo.git, empty, nil, 0).FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if empty.bpUpserts != 0 {
		t.Fatalf("fresh rebuild wrote %d rules from the hidden blob", len(empty.bpRules))
	}
}

// P2: a file entry recording a tree at an artifact path, and one recording
// a commit (and a tag whose target is a commit) at workflow paths, are
// deterministic for the commit. The sync advances past them — a not_a_file
// skip for the artifact, not_a_file rejections for the workflows, the
// reviewed row kept quarantined — and keeps advancing on the next commit;
// before the fix it stayed error at c1 on every tick.
func TestIncrementalSync_FileEntryRecordingATreeOrCommitAdvances(t *testing.T) {
	ctx := context.Background()
	repo := newScratchRepo(t)
	repo.write(wfPath, reviewedWorkflow)
	repo.write(taskPath, taskDoc("Pending", "T"))
	c1 := repo.commit("c1")
	st := allRowsStore{newMapStore()}
	router := &recordingRouter{}
	svc := NewService(repo.git, st, router, 0)
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	router.reset()

	const (
		treeTask = "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-002.md"
		tagWf    = "workflows/tagged.yaml"
	)
	tree := repo.run("rev-parse", c1+"^{tree}")
	repo.addFileEntryTo(treeTask, tree)
	repo.remove(wfPath)
	repo.addFileEntryTo(wfPath, c1)
	tagCommit := repo.writeTag(c1, "commit")
	repo.addFileEntryTo(tagWf, tagCommit)
	c2 := repo.commitIndex("c2")
	assertFileEntryRecords(t, repo, c2, treeTask, tree, git.TreeEntryTree)
	assertFileEntryRecords(t, repo, c2, wfPath, c1, git.TreeEntryCommit)
	assertFileEntryRecords(t, repo, c2, tagWf, tagCommit, git.ObjectTag)
	for _, id := range []string{tree, c1, tagCommit} {
		if out, err := exec.Command("git", "-C", repo.dir, "cat-file", "blob", id).CombinedOutput(); err == nil {
			t.Fatalf("precondition: the follow-up's read `cat-file blob %s` must fail, printed %q", id, out)
		}
	}

	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c2 {
		t.Fatalf("sync state = %+v, want idle@%s (a file entry recording a tree or a commit is not infrastructure)", got, c2)
	}
	if _, ok := st.artifacts[treeTask]; ok {
		t.Fatalf("%s was projected", treeTask)
	}
	if _, ok := st.artifacts[taskPath]; !ok {
		t.Fatalf("%s must stay projected", taskPath)
	}
	assertOneSkip(t, router, c2, treeTask, "not_a_file", nil, nil)
	assertKeptQuarantined(t, st, c1)
	assertRejections(t, router, map[string][2]string{
		wfPath: {"not_a_file", "kept"},
		tagWf:  {"not_a_file", "not_projected"},
	})

	// An unrelated commit: the sync is not wedged.
	router.reset()
	repo.write("docs/unrelated.md", "# unrelated\n")
	c3 := repo.commitIndex("c3", "docs/unrelated.md")
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatalf("IncrementalSync c3: %v", err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != c3 {
		t.Fatalf("sync state after c3 = %+v, want idle@%s", got, c3)
	}

	// A rebuild into an empty store advances too, projecting neither.
	empty := allRowsStore{newMapStore()}
	if err := NewService(repo.git, empty, nil, 0).FullRebuild(ctx); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}
	if got := empty.syncState(); got.Status != "idle" || got.LastSyncedCommit != c3 {
		t.Fatalf("fresh rebuild sync state = %+v, want idle@%s", got, c3)
	}
	if _, ok := empty.artifacts[treeTask]; ok {
		t.Fatalf("fresh rebuild projected %s", treeTask)
	}
	for _, p := range []string{wfPath, tagWf} {
		if r := empty.row(p); r != nil {
			t.Fatalf("fresh rebuild projected %s: %+v", p, r)
		}
	}
}
