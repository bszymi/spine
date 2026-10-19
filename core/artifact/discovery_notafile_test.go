package artifact_test

// INIT-023 wave 4 follow-up (TASK-003 residual): the git client reads
// blobs only and refuses a gitlink or a directory as not a file
// (git.ErrNotBlob). Discovery records the refusal like TASK-003's size
// refusal (RefusedNotAFile: skipped, recorded, a governed row it replaces
// deleted), the artifact and workflow reads answer 404, and an occupied
// path is still an ID collision.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

// hiddenTask is the body a gitlink records the id of: an artifact no diff
// of the commit shows (a diff shows "Subproject commit <id>").
const hiddenTask = "---\nid: TASK-9\ntype: Task\ntitle: Hidden\nstatus: Completed\nepic: /initiatives/INIT-001/epics/EPIC-001/epic.md\n---\n\n# TASK-9\n"

type notAFileRepo struct {
	t   *testing.T
	dir string
}

func (r notAFileRepo) git(stdin []byte, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func (r notAFileRepo) write(rel, body string) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// newNotAFileRepo commits c1 (initiatives/INIT-001/initiative.md and
// governance/g.md) and c2, where the initiative is replaced by a gitlink
// to c1 itself (a commit in the repository: `git show` rendered it with
// its patch), docs/hidden.md is added as a gitlink recording the id of
// the hiddenTask blob (show printed the blob), and workflows/wl.yaml is a
// gitlink too.
func newNotAFileRepo(t *testing.T) (r notAFileRepo, c1, c2 string) {
	t.Helper()
	r = notAFileRepo{t: t, dir: t.TempDir()}
	r.git(nil, "init", "-q", "-b", "main")
	r.write("initiatives/INIT-001/initiative.md", refusedInitiative)
	r.write("governance/g.md", refusedGovernance)
	r.git(nil, "add", "-A")
	r.git(nil, "commit", "-q", "-m", "c1")
	c1 = r.git(nil, "rev-parse", "HEAD")
	hidden := r.git([]byte(hiddenTask), "hash-object", "-w", "--stdin")
	r.git(nil, "rm", "-q", "--cached", "initiatives/INIT-001/initiative.md")
	r.git(nil, "update-index", "--add", "--cacheinfo", "160000,"+c1+",initiatives/INIT-001/initiative.md")
	r.git(nil, "update-index", "--add", "--cacheinfo", "160000,"+hidden+",docs/hidden.md")
	r.git(nil, "update-index", "--add", "--cacheinfo", "160000,"+c1+",workflows/wl.yaml")
	r.git(nil, "commit", "-q", "-m", "c2: gitlinks")
	c2 = r.git(nil, "rev-parse", "HEAD")
	if got := r.git(nil, "show", "--end-of-options", c2+":docs/hidden.md", "--"); got != strings.TrimSpace(hiddenTask) {
		t.Fatalf("fixture: git show must print the hidden blob, got %q", got)
	}
	return r, c1, c2
}

func refusalsByPath(refused []artifact.RefusedFile) map[string]artifact.RefusedFile {
	m := make(map[string]artifact.RefusedFile, len(refused))
	for _, rf := range refused {
		m[rf.Path] = rf
	}
	return m
}

func TestDiscovery_GitlinksAreNotAFileRefusals(t *testing.T) {
	ctx := context.Background()
	r, c1, c2 := newNotAFileRepo(t)
	client := git.NewCLIClient(r.dir)
	notAFile := artifact.RefusedFile{Reason: artifact.RefusedNotAFile}

	t.Run("DiscoverChanges", func(t *testing.T) {
		cs, err := artifact.DiscoverChanges(ctx, client, c1, c2)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Created) != 0 || len(cs.Modified) != 0 || len(cs.ReadErrors) != 0 {
			t.Fatalf("created %v, modified %v, read errors %v; want none (the hidden blob must never be projected)",
				cs.Created, cs.Modified, cs.ReadErrors)
		}
		got := refusalsByPath(cs.Refused)
		for _, p := range []string{"initiatives/INIT-001/initiative.md", "docs/hidden.md"} {
			want := notAFile
			want.Path = p
			if got[p] != want {
				t.Errorf("refusal for %s = %+v, want %+v", p, got[p], want)
			}
		}
		if len(got) != 2 {
			t.Errorf("refused = %+v, want the two .md gitlinks", cs.Refused)
		}
		// The projected initiative became a gitlink: its row is removed,
		// as for a file modified past the read limit.
		if strings.Join(cs.Deleted, ",") != "initiatives/INIT-001/initiative.md" {
			t.Errorf("deleted = %v, want the initiative", cs.Deleted)
		}
	})

	t.Run("DiscoverAll", func(t *testing.T) {
		res, err := artifact.DiscoverAll(ctx, client, c2)
		if err != nil {
			t.Fatal(err)
		}
		got := refusalsByPath(res.Refused)
		for _, p := range []string{"initiatives/INIT-001/initiative.md", "docs/hidden.md"} {
			if got[p].Reason != artifact.RefusedNotAFile {
				t.Errorf("refusal for %s = %+v, want not_a_file", p, got[p])
			}
		}
		for _, a := range res.Artifacts {
			if a.Path != "governance/g.md" {
				t.Errorf("projected %s from a gitlink", a.Path)
			}
		}
		if len(res.Errors) != 0 {
			t.Errorf("errors = %+v, want none", res.Errors)
		}
	})

	t.Run("back to a file", func(t *testing.T) {
		r.git(nil, "rm", "-q", "--cached", "initiatives/INIT-001/initiative.md")
		r.write("initiatives/INIT-001/initiative.md", refusedInitiative)
		r.git(nil, "add", "initiatives/INIT-001/initiative.md")
		r.git(nil, "commit", "-q", "-m", "c3")
		c3 := r.git(nil, "rev-parse", "HEAD")
		cs, err := artifact.DiscoverChanges(ctx, client, c2, c3)
		if err != nil {
			t.Fatal(err)
		}
		// The old side was refused: treated as "was an artifact", so the
		// file is projected again (an upsert).
		if len(cs.Modified) != 1 || cs.Modified[0].Path != "initiatives/INIT-001/initiative.md" || len(cs.Refused) != 0 {
			t.Fatalf("changeset = %+v, want the initiative modified", cs)
		}
	})
}

func TestRead_NotAFileIsNotFound(t *testing.T) {
	ctx := context.Background()
	r, _, c2 := newNotAFileRepo(t)
	client := git.NewCLIClient(r.dir)
	artSvc := artifact.NewService(client, nil, r.dir)

	assert404 := func(label string, err error, want string) {
		t.Helper()
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrNotFound {
			t.Fatalf("%s: err = %v, want ErrNotFound", label, err)
		}
		if !errors.Is(err, git.ErrNotBlob) {
			t.Errorf("%s: the not-a-file refusal is not the cause: %v", label, err)
		}
		if !strings.Contains(se.Message, want) {
			t.Errorf("%s: message %q does not say %q", label, se.Message, want)
		}
	}
	for _, tc := range []struct{ label, path, want string }{
		{"gitlink recording a blob", "docs/hidden.md", "gitlink"},
		{"gitlink to a commit", "initiatives/INIT-001/initiative.md", "gitlink"},
		{"directory", "governance", "directory"},
	} {
		a, err := artSvc.Read(ctx, tc.path, c2)
		if a != nil {
			t.Fatalf("%s: read %+v", tc.label, a)
		}
		assert404("artifact.Service.Read "+tc.label, err, tc.want)
	}

	// A "./"-relative path reads the file, as `git show` resolved it
	// (codex review, wave 4 follow-up).
	if a, err := artSvc.Read(ctx, "./governance/g.md", c2); err != nil || a == nil {
		t.Fatalf("Read(./governance/g.md) = %v, %v; want the artifact", a, err)
	}

	wfSvc := workflow.NewService(client, r.dir)
	_, err := wfSvc.Read(ctx, "wl", c2)
	assert404("workflow.Service.Read gitlink", err, "gitlink")

	// The path is occupied: still an ID collision.
	for _, p := range []string{"docs/hidden.md", "governance"} {
		collision, err := artifact.IsIDCollision(ctx, client, c2, c2, p)
		if err != nil || !collision {
			t.Fatalf("IsIDCollision(%s) = %v, %v; want true", p, collision, err)
		}
	}
}

// Adversarial review of the wave 4 follow-up (P1, P2): a regular-file
// entry (mode 100644) can record the id of an object that is not a blob.
// The git client's `cat-file blob <oid>` peeled a tag to its target — so
// a tag pointing at the hiddenTask blob projected TASK-9 while the diff
// showed only the tag object's text — and failed a tree or a commit with
// a plain "bad file", a ReadError that ValidateBranch ignores. ReadFile
// now types the recorded object and refuses anything but a blob as not a
// file, so discovery records each one as RefusedNotAFile, the reads
// answer 404 naming the object's type, and the path is still taken.
func TestDiscovery_FileEntriesRecordingNonBlobObjectsAreNotAFileRefusals(t *testing.T) {
	ctx := context.Background()
	r := notAFileRepo{t: t, dir: t.TempDir()}
	r.git(nil, "init", "-q", "-b", "main")
	r.write("initiatives/INIT-001/initiative.md", refusedInitiative)
	r.write("governance/g.md", refusedGovernance)
	r.git(nil, "add", "-A")
	r.git(nil, "commit", "-q", "-m", "c1")
	c1 := r.git(nil, "rev-parse", "HEAD")
	hidden := r.git([]byte(hiddenTask), "hash-object", "-w", "--stdin")
	tag := r.git([]byte("object "+hidden+"\ntype blob\ntag v1\ntagger t <t@t> 0 +0000\n\nm\n"), "mktag")
	tree := r.git(nil, "rev-parse", c1+"^{tree}")
	wfTag := r.git([]byte("object "+r.git([]byte("id: wt\n"), "hash-object", "-w", "--stdin")+
		"\ntype blob\ntag v2\ntagger t <t@t> 0 +0000\n\nm\n"), "mktag")
	r.git(nil, "rm", "-q", "--cached", "initiatives/INIT-001/initiative.md")
	entries := map[string]string{ // path -> recorded object id
		"initiatives/INIT-001/initiative.md": tag,
		"docs/tree.md":                       tree,
		"docs/commit.md":                     c1,
		"workflows/wt.yaml":                  wfTag,
	}
	for p, id := range entries {
		r.git(nil, "update-index", "--add", "--cacheinfo", "100644,"+id+","+p)
	}
	r.git(nil, "commit", "-q", "-m", "c2: file entries recording non-blob objects")
	c2 := r.git(nil, "rev-parse", "HEAD")
	// Precondition: the follow-up's read peeled the tag to the hidden task.
	if got := r.git(nil, "cat-file", "blob", tag); got != strings.TrimSpace(hiddenTask) {
		t.Fatalf("fixture: cat-file blob of the tag must print the hidden task, got %q", got)
	}
	client := git.NewCLIClient(r.dir)
	mdPaths := []string{"initiatives/INIT-001/initiative.md", "docs/tree.md", "docs/commit.md"}

	cs, err := artifact.DiscoverChanges(ctx, client, c1, c2)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Created) != 0 || len(cs.Modified) != 0 || len(cs.ReadErrors) != 0 {
		t.Fatalf("created %v, modified %v, read errors %v; want none (the hidden task must never be projected)",
			cs.Created, cs.Modified, cs.ReadErrors)
	}
	got := refusalsByPath(cs.Refused)
	for _, p := range mdPaths {
		if want := (artifact.RefusedFile{Path: p, Reason: artifact.RefusedNotAFile}); got[p] != want {
			t.Errorf("refusal for %s = %+v, want %+v", p, got[p], want)
		}
	}
	if len(got) != len(mdPaths) {
		t.Errorf("refused = %+v, want the three .md entries", cs.Refused)
	}
	if strings.Join(cs.Deleted, ",") != "initiatives/INIT-001/initiative.md" {
		t.Errorf("deleted = %v, want the initiative whose entry now records a tag", cs.Deleted)
	}

	res, err := artifact.DiscoverAll(ctx, client, c2)
	if err != nil {
		t.Fatal(err)
	}
	got = refusalsByPath(res.Refused)
	for _, p := range mdPaths {
		if got[p].Reason != artifact.RefusedNotAFile {
			t.Errorf("DiscoverAll refusal for %s = %+v, want not_a_file", p, got[p])
		}
	}
	for _, a := range res.Artifacts {
		if a.Path != "governance/g.md" {
			t.Errorf("DiscoverAll projected %s (%s) from a non-blob object", a.Path, a.ID)
		}
	}
	if len(res.Errors) != 0 {
		t.Errorf("DiscoverAll errors = %+v, want none", res.Errors)
	}

	artSvc := artifact.NewService(client, nil, r.dir)
	for p, want := range map[string]string{
		"initiatives/INIT-001/initiative.md": "tag", "docs/tree.md": "tree", "docs/commit.md": "commit",
	} {
		a, err := artSvc.Read(ctx, p, c2)
		var se *domain.SpineError
		if a != nil || !errors.As(err, &se) || se.Code != domain.ErrNotFound || !errors.Is(err, git.ErrNotBlob) ||
			!strings.Contains(se.Message, "records a "+want+" object") {
			t.Errorf("Read(%s) = %+v, %v; want a 404 naming the recorded %s", p, a, err, want)
		}
		if collision, err := artifact.IsIDCollision(ctx, client, c2, c2, p); err != nil || !collision {
			t.Errorf("IsIDCollision(%s) = %v, %v; want true", p, collision, err)
		}
	}
	wfSvc := workflow.NewService(client, r.dir)
	if _, err := wfSvc.Read(ctx, "wt", c2); err == nil {
		t.Fatal("workflow Read of a file entry recording a tag succeeded")
	} else if var404 := new(domain.SpineError); !errors.As(err, &var404) || var404.Code != domain.ErrNotFound || !errors.Is(err, git.ErrNotBlob) {
		t.Fatalf("workflow Read = %v, want a 404 caused by the not-a-file refusal", err)
	}
}
