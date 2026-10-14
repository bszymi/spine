package artifact_test

// INIT-023 TASK-003 (audit F4, P3-9): discovery skips, records and
// advances past oversized files and unsafe paths; raw NUL-delimited paths
// make non-ASCII artifacts visible; oversized reads map to 413.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/core/worktreefs"
)

const (
	refusedInitiative = "---\nid: INIT-001\ntype: Initiative\ntitle: Seed\nstatus: Pending\ncreated: 2026-10-06\n---\n\n# seed\n"
	refusedGovernance = "---\ntype: Governance\ntitle: G\nstatus: Living Document\n---\n\n# G\n"
)

func refusedEpic(id string) string {
	return "---\nid: " + id + "\ntype: Epic\ntitle: E\nstatus: Pending\ncreated: 2026-10-06\ninitiative: /initiatives/INIT-001/initiative.md\nlinks:\n  - type: parent\n    target: /initiatives/INIT-001/initiative.md\n---\n\n# " + id + "\n"
}

// rawTreeRepo commits files (any byte sequence as a path — invalid UTF-8
// and newlines included, which no filesystem test fixture can hold)
// straight into the object store and points main at the commit.
func rawTreeRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_INDEX_FILE="+filepath.Join(dir, ".git", "t003-index"))
	run := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, errb.String())
		}
		return strings.TrimSpace(out.String())
	}
	run(nil, "init", "-q", "-b", "main")
	var index bytes.Buffer
	for p, body := range files {
		blob := run([]byte(body), "hash-object", "-w", "--stdin")
		fmt.Fprintf(&index, "100644 %s 0\t%s\x00", blob, p)
	}
	run(index.Bytes(), "update-index", "-z", "--add", "--index-info")
	tree := run(nil, "write-tree")
	commit := run(nil, "commit-tree", tree, "-m", "raw")
	run(nil, "update-ref", "refs/heads/main", commit)
	return dir, commit
}

// countingGit counts ReadFile calls per path.
type countingGit struct {
	git.GitClient
	mu    sync.Mutex
	reads map[string]int
}

func (g *countingGit) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	g.mu.Lock()
	if g.reads == nil {
		g.reads = map[string]int{}
	}
	g.reads[path]++
	g.mu.Unlock()
	return g.GitClient.ReadFile(ctx, ref, path)
}

// AC 9 (DiscoverAll): oversized → Refused{oversized}; unsafe paths →
// Refused{unsafe_path} with no read; non-ASCII Slugify output projected
// under its raw path.
func TestDiscoverAll_RefusesAndRecords(t *testing.T) {
	const limit = 64 << 10
	slug := artifact.Slugify("Wdrożenie")
	if slug != "wdrożenie" {
		t.Fatalf("Slugify(Wdrożenie) = %q; the fixture assumes it keeps the letter", slug)
	}
	resume := "initiatives/INIT-001/epics/EPIC-001-résumé/epic.md"
	wdr := "initiatives/INIT-001/epics/EPIC-002-" + slug + "/epic.md"
	plainWdr := slug + "/x.md"
	big := "docs/big.md"
	unsafe := []string{
		"a\tb.md", "x\ny.md", "\xff.md", // design
		"a\u0085b.md", "a\u2028b.md", "a\u202eb.md", // widened default (C1, LS, RLO)
		"a\u2029b.md", "a\u2066b.md", "a\x7fb.md", "dir\x01/x.md",
	}
	unsafeWorkflow := "workflows/a\tb.yaml"
	files := map[string]string{
		"initiatives/INIT-001/initiative.md": refusedInitiative,
		resume:                               refusedEpic("EPIC-001"),
		wdr:                                  refusedEpic("EPIC-002"),
		plainWdr:                             refusedGovernance,
		big:                                  refusedGovernance + strings.Repeat("x", 100<<10),
		"workflows/ok.yaml":                  "id: ok\n",
		unsafeWorkflow:                       "id: bad\n",
	}
	for _, p := range unsafe {
		files[p] = refusedGovernance
	}
	dir, commit := rawTreeRepo(t, files)
	g := &countingGit{GitClient: git.NewCLIClient(dir, git.WithMaxBlobBytes(limit))}

	res, err := artifact.DiscoverAll(context.Background(), g, commit)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("Errors = %+v, want none (refusals are not errors)", res.Errors)
	}

	want := map[string]artifact.RefusedFile{
		big:            {Path: big, Reason: artifact.RefusedOversized, Size: int64(len(files[big])), Limit: limit},
		unsafeWorkflow: {Path: unsafeWorkflow, Reason: artifact.RefusedUnsafePath},
	}
	for _, p := range unsafe {
		want[p] = artifact.RefusedFile{Path: p, Reason: artifact.RefusedUnsafePath}
	}
	got := map[string]artifact.RefusedFile{}
	for _, r := range res.Refused {
		if _, dup := got[r.Path]; dup {
			t.Errorf("%q refused twice", r.Path)
		}
		got[r.Path] = r
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Refused =\n %+v\nwant\n %+v", got, want)
	}
	for _, p := range append(unsafe, unsafeWorkflow) {
		if n := g.reads[p]; n != 0 {
			t.Errorf("unsafe path %q was read %d times; want none", p, n)
		}
	}

	var arts []string
	for _, a := range res.Artifacts {
		arts = append(arts, a.Path)
		if a.Path == big || artifact.UnsafePath(a.Path) {
			t.Errorf("refused file %q was also discovered as an artifact", a.Path)
		}
	}
	sort.Strings(arts)
	wantArts := []string{"initiatives/INIT-001/epics/EPIC-001-résumé/epic.md", wdr, "initiatives/INIT-001/initiative.md", plainWdr}
	sort.Strings(wantArts)
	if !reflect.DeepEqual(arts, wantArts) {
		t.Errorf("Artifacts = %q, want %q", arts, wantArts)
	}
	if !reflect.DeepEqual(res.Workflows, []string{"workflows/ok.yaml"}) {
		t.Errorf("Workflows = %q, want only workflows/ok.yaml", res.Workflows)
	}
	for _, s := range res.Skipped {
		if artifact.UnsafePath(s) || s == big {
			t.Errorf("refused file %q also listed as Skipped", s)
		}
	}
}

func TestUnsafePath(t *testing.T) {
	for _, tc := range []struct {
		p    string
		want bool
	}{
		{"plain.md", false},
		{" lead.md", false},
		{`"quoted.md`, false},
		{`back\slash.md`, false},
		{"résumé/x.md", false},
		{"wdrożenie/x.md", false},
		{"док/x.md", false},      // Cyrillic
		{"文書/x.md", false},       // CJK
		{"é/x.md", false},       // NFD combining mark: normalisation is not refused
		{"\U0001F600.md", false}, // emoji
		{"a\u200db.md", false},   // ZWJ (format char, not in the refused set)
		{"a\tb.md", true}, {"a\nb.md", true}, {"a\rb.md", true}, {"a\x00b.md", true},
		{"a\x1fb.md", true}, {"a\x7fb.md", true},
		{"a\u0080b.md", true}, {"a\u0085b.md", true}, {"a\u009fb.md", true},
		{"a\u2028b.md", true}, {"a\u2029b.md", true},
		{"a\u202ab.md", true}, {"a\u202eb.md", true}, {"a\u2066b.md", true}, {"a\u2069b.md", true},
		{"\xff.md", true}, {"a\xc3.md", true}, {"\xed\xa0\x80.md", true}, // invalid UTF-8, surrogate
	} {
		if got := artifact.UnsafePath(tc.p); got != tc.want {
			t.Errorf("UnsafePath(%q) = %v, want %v", tc.p, got, tc.want)
		}
	}
	// Slugify output is never refused.
	for _, title := range []string{"Wdrożenie", "Résumé", "Документ", "文書", "a\tb\u202ec\u2028d\u0085e"} {
		if s := artifact.Slugify(title); artifact.UnsafePath(s) {
			t.Errorf("Slugify(%q) = %q is refused", title, s)
		}
	}
}

// tableGit serves canned ReadFile results keyed by "<ref>:<path>".
type tableGit struct {
	git.GitClient
	content map[string]string
	errs    map[string]error
	reads   map[string]int
}

func (g *tableGit) ReadFile(_ context.Context, ref, path string) ([]byte, error) {
	k := ref + ":" + path
	g.reads[path]++
	if err, ok := g.errs[k]; ok {
		return nil, err
	}
	if c, ok := g.content[k]; ok {
		return []byte(c), nil
	}
	return nil, &git.GitError{Kind: git.ErrKindNotFound, Op: "read_file", Message: "fatal: bad revision '" + k + "'"}
}

func tooLargeErr(ref, path string) error {
	return &git.GitError{Kind: git.ErrKindPermanent, Op: "read_file", Message: "blob exceeds read limit",
		Err: &git.BlobTooLargeError{Ref: ref, Path: path, Size: 5 << 20, Limit: 4 << 20}}
}

// Review round 1 (P3): when git refused an allocation over its alloc
// limit (a small file stored as a delta on a large base, a gitlink's
// patch), the refusal keeps that cause through discovery and
// Service.Read: the record carries the alloc limit, and the 413 message
// names it instead of calling a 3 MB file "over" a 4 MiB read limit.
func TestGitAllocRefusalKeepsItsCause(t *testing.T) {
	ctx := context.Background()
	const path = "governance/d.md"
	allocErr := &git.GitError{Kind: git.ErrKindPermanent, Op: "read_file", Message: "blob read exceeds git allocation limit",
		Err: &git.BlobTooLargeError{Ref: "main", Path: path, Size: -1, Limit: 4 << 20, GitAllocLimit: 16 << 20}}
	g := &tableGit{errs: map[string]error{"main:" + path: allocErr}, reads: map[string]int{}}

	cs, err := artifact.DiscoverChangesFromDiffs(ctx, g, "base", "main",
		[]git.FileDiff{{Path: path, Status: "added"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []artifact.RefusedFile{{Path: path, Reason: artifact.RefusedOversized, Size: -1, Limit: 4 << 20, GitAllocLimit: 16 << 20}}
	if !reflect.DeepEqual(cs.Refused, want) {
		t.Fatalf("Refused = %+v, want %+v", cs.Refused, want)
	}

	_, err = artifact.NewService(g, nil, t.TempDir()).Read(ctx, path, "main")
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrPayloadTooLarge {
		t.Fatalf("Read = %v, want ErrPayloadTooLarge", err)
	}
	if !strings.Contains(se.Message, "16777216-byte git allocation limit") || strings.Contains(se.Message, "over the 4194304") ||
		strings.Contains(se.Message, "exceeds the 4194304") {
		t.Errorf("message %q: want the git allocation limit named, and no claim that the file is over the read limit", se.Message)
	}
	for _, msg := range []string{allocErr.Error(), artifact.ReadLimitPhrase(allocErr)} {
		if !strings.Contains(msg, "read limit") || !strings.Contains(msg, "16777216-byte git allocation limit") {
			t.Errorf("refusal text %q: want it to name the read limits and the git allocation limit", msg)
		}
	}
}

// AC 9 (DiscoverChanges): every row of the design's §5 table.
func TestDiscoverChangesFromDiffs_RefusalTable(t *testing.T) {
	const from, to = "from", "to"
	big := func(p string) artifact.RefusedFile {
		return artifact.RefusedFile{Path: p, Reason: artifact.RefusedOversized, Size: 5 << 20, Limit: 4 << 20}
	}
	uns := func(p string) artifact.RefusedFile {
		return artifact.RefusedFile{Path: p, Reason: artifact.RefusedUnsafePath}
	}
	type want struct {
		created, modified, deleted []string
		refused                    []artifact.RefusedFile
		readErrors                 int
		noReads                    []string
	}
	for _, tc := range []struct {
		name    string
		diff    git.FileDiff
		content map[string]string
		errs    map[string]error
		want    want
	}{
		{"added unsafe", git.FileDiff{Path: "a\tb.md", Status: "added"}, nil, nil,
			want{refused: []artifact.RefusedFile{uns("a\tb.md")}, noReads: []string{"a\tb.md"}}},
		{"added oversized", git.FileDiff{Path: "big.md", Status: "added"}, nil,
			map[string]error{"to:big.md": tooLargeErr(to, "big.md")},
			want{refused: []artifact.RefusedFile{big("big.md")}}},
		{"added artifact (unchanged)", git.FileDiff{Path: "g.md", Status: "added"},
			map[string]string{"to:g.md": refusedGovernance}, nil, want{created: []string{"g.md"}}},
		{"deleted, old oversized", git.FileDiff{Path: "big.md", Status: "deleted"}, nil,
			map[string]error{"from:big.md": tooLargeErr(from, "big.md")}, want{deleted: []string{"big.md"}}},
		{"deleted unsafe", git.FileDiff{Path: "\xff.md", Status: "deleted"}, nil, nil,
			want{noReads: []string{"\xff.md"}}},
		// Wave 3 integration (codex review): a valid-UTF-8 unsafe path
		// may hold a row projected before TASK-003 under
		// core.quotePath=false; it is removed, still unread.
		{"deleted unsafe, valid UTF-8", git.FileDiff{Path: "a\u202eb.md", Status: "deleted"}, nil, nil,
			want{deleted: []string{"a\u202eb.md"}, noReads: []string{"a\u202eb.md"}}},
		{"deleted artifact (unchanged)", git.FileDiff{Path: "g.md", Status: "deleted"},
			map[string]string{"from:g.md": refusedGovernance}, nil, want{deleted: []string{"g.md"}}},
		{"renamed, old oversized", git.FileDiff{OldPath: "big.md", Path: "g.md", Status: "renamed"},
			map[string]string{"to:g.md": refusedGovernance}, map[string]error{"from:big.md": tooLargeErr(from, "big.md")},
			want{created: []string{"g.md"}, deleted: []string{"big.md"}}},
		{"renamed to an unsafe path", git.FileDiff{OldPath: "g.md", Path: "x\ny.md", Status: "renamed"},
			map[string]string{"from:g.md": refusedGovernance}, nil,
			want{deleted: []string{"g.md"}, refused: []artifact.RefusedFile{uns("x\ny.md")}, noReads: []string{"x\ny.md"}}},
		{"renamed to an oversized file", git.FileDiff{OldPath: "g.md", Path: "big.md", Status: "renamed"},
			map[string]string{"from:g.md": refusedGovernance}, map[string]error{"to:big.md": tooLargeErr(to, "big.md")},
			want{deleted: []string{"g.md"}, refused: []artifact.RefusedFile{big("big.md")}}},
		{"renamed from an unsafe path", git.FileDiff{OldPath: "a\u202eb.md", Path: "g.md", Status: "renamed"},
			map[string]string{"to:g.md": refusedGovernance}, nil,
			want{created: []string{"g.md"}, deleted: []string{"a\u202eb.md"}, noReads: []string{"a\u202eb.md"}}},
		{"renamed from an invalid-UTF-8 path", git.FileDiff{OldPath: "a\xffb.md", Path: "g.md", Status: "renamed"},
			map[string]string{"to:g.md": refusedGovernance}, nil,
			want{created: []string{"g.md"}, noReads: []string{"a\xffb.md"}}},
		{"modified unsafe", git.FileDiff{Path: "a\u0085b.md", Status: "modified"}, nil, nil,
			want{refused: []artifact.RefusedFile{uns("a\u0085b.md")}, deleted: []string{"a\u0085b.md"}, noReads: []string{"a\u0085b.md"}}},
		{"modified unsafe, invalid UTF-8", git.FileDiff{Path: "a\xffb.md", Status: "modified"}, nil, nil,
			want{refused: []artifact.RefusedFile{uns("a\xffb.md")}, noReads: []string{"a\xffb.md"}}},
		{"modified to oversized, was an artifact", git.FileDiff{Path: "g.md", Status: "modified"},
			map[string]string{"from:g.md": refusedGovernance}, map[string]error{"to:g.md": tooLargeErr(to, "g.md")},
			want{deleted: []string{"g.md"}, refused: []artifact.RefusedFile{big("g.md")}}},
		{"modified to oversized, was not an artifact", git.FileDiff{Path: "n.md", Status: "modified"},
			map[string]string{"from:n.md": "# notes\n"}, map[string]error{"to:n.md": tooLargeErr(to, "n.md")},
			want{refused: []artifact.RefusedFile{big("n.md")}}},
		{"modified, old oversized, now not an artifact", git.FileDiff{Path: "n.md", Status: "modified"},
			map[string]string{"to:n.md": "# notes\n"}, map[string]error{"from:n.md": tooLargeErr(from, "n.md")},
			want{deleted: []string{"n.md"}}},
		{"modified, old oversized, now an artifact", git.FileDiff{Path: "g.md", Status: "modified"},
			map[string]string{"to:g.md": refusedGovernance}, map[string]error{"from:g.md": tooLargeErr(from, "g.md")},
			want{modified: []string{"g.md"}}},
		{"modified, both oversized", git.FileDiff{Path: "g.md", Status: "modified"}, nil,
			map[string]error{"from:g.md": tooLargeErr(from, "g.md"), "to:g.md": tooLargeErr(to, "g.md")},
			want{deleted: []string{"g.md"}, refused: []artifact.RefusedFile{big("g.md")}}},
		{"other read error is a ReadError (unchanged)", git.FileDiff{Path: "g.md", Status: "added"}, nil,
			map[string]error{"to:g.md": &git.GitError{Kind: git.ErrKindTransient, Op: "read_file", Message: "repository locked"}},
			want{readErrors: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &tableGit{content: tc.content, errs: tc.errs, reads: map[string]int{}}
			cs, err := artifact.DiscoverChangesFromDiffs(context.Background(), g, from, to, []git.FileDiff{tc.diff})
			if err != nil {
				t.Fatal(err)
			}
			paths := func(as []*domain.Artifact) []string {
				var out []string
				for _, a := range as {
					out = append(out, a.Path)
				}
				return out
			}
			if got := paths(cs.Created); !reflect.DeepEqual(got, tc.want.created) {
				t.Errorf("Created = %q, want %q", got, tc.want.created)
			}
			if got := paths(cs.Modified); !reflect.DeepEqual(got, tc.want.modified) {
				t.Errorf("Modified = %q, want %q", got, tc.want.modified)
			}
			if !reflect.DeepEqual(cs.Deleted, tc.want.deleted) {
				t.Errorf("Deleted = %q, want %q", cs.Deleted, tc.want.deleted)
			}
			if !reflect.DeepEqual(cs.Refused, tc.want.refused) {
				t.Errorf("Refused = %+v, want %+v", cs.Refused, tc.want.refused)
			}
			if len(cs.ReadErrors) != tc.want.readErrors {
				t.Errorf("ReadErrors = %+v, want %d", cs.ReadErrors, tc.want.readErrors)
			}
			for _, p := range tc.want.noReads {
				if g.reads[p] != 0 {
					t.Errorf("unsafe path %q was read", p)
				}
			}
			for _, d := range cs.Deleted {
				if artifact.UnsafePath(d) && !artifact.StoredUnsafePath(d) {
					t.Errorf("invalid-UTF-8 path %q reached Deleted (the store would be called with it)", d)
				}
			}
		})
	}
}

// AC 10: an oversized artifact or workflow read is 413, with the size
// refusal as its cause; an oversized existing file is an ID collision.
func TestRead_OversizedIsPayloadTooLarge(t *testing.T) {
	ctx := context.Background()
	const limit = 64 << 10
	wf := "id: big\nname: Big\nversion: \"1\"\nstatus: Active\napplies_to: [Task]\nentry_step: s\nsteps:\n  - id: s\n    name: S\n    type: manual\n# " +
		strings.Repeat("x", 100<<10) + "\n"
	dir, _ := rawTreeRepo(t, map[string]string{
		"governance/big.md":  refusedGovernance + strings.Repeat("x", 100<<10),
		"workflows/big.yaml": wf,
		"governance/ok.md":   refusedGovernance,
	})
	client := git.NewCLIClient(dir, git.WithMaxBlobBytes(limit))
	assert413 := func(label string, err error) {
		t.Helper()
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrPayloadTooLarge {
			t.Fatalf("%s: err = %v, want ErrPayloadTooLarge", label, err)
		}
		if !errors.Is(err, git.ErrBlobTooLarge) {
			t.Errorf("%s: the size refusal is not the cause: %v", label, err)
		}
		if !strings.Contains(se.Message, "read limit") {
			t.Errorf("%s: message %q does not name the read limit", label, se.Message)
		}
	}

	artSvc := artifact.NewService(client, nil, dir)
	_, err := artSvc.Read(ctx, "governance/big.md", "main")
	assert413("artifact.Service.Read", err)
	if _, err := artSvc.Read(ctx, "governance/ok.md", "main"); err != nil {
		t.Fatalf("a small artifact must still read: %v", err)
	}
	var se *domain.SpineError
	if _, err := artSvc.Read(ctx, "governance/missing.md", "main"); !errors.As(err, &se) || se.Code != domain.ErrNotFound {
		t.Fatalf("a missing artifact must stay 404, got %v", err)
	}

	wfSvc := workflow.NewService(client, dir)
	_, err = wfSvc.Read(ctx, "big", "main")
	assert413("workflow.Service.Read", err)

	collision, err := artifact.IsIDCollision(ctx, client, "main", "main", "governance/big.md")
	if err != nil || !collision {
		t.Fatalf("IsIDCollision(oversized existing file) = %v, %v; want true", collision, err)
	}
	collision, err = artifact.IsIDCollision(ctx, client, "main", "main", "governance/missing.md")
	if err != nil || collision {
		t.Fatalf("IsIDCollision(missing file) = %v, %v; want false", collision, err)
	}
}

// Service.List and workflow.Service.List keep unsafe paths out, as they
// were while git C-quoted them, and now list non-ASCII ones.
func TestList_RawPathsWithoutUnsafeOnes(t *testing.T) {
	ctx := context.Background()
	wfBody := func(id string) string {
		return "id: " + id + "\nname: W\nversion: \"1\"\nstatus: Active\napplies_to: [Task]\nentry_step: s\nsteps:\n  - id: s\n    name: S\n    type: manual\n"
	}
	dir, _ := rawTreeRepo(t, map[string]string{
		"governance/résumé.md":    refusedGovernance,
		"governance/a\tb.md":      refusedGovernance,
		"governance/\xff.md":      refusedGovernance,
		"workflows/wé.yaml":       wfBody("we"),
		"workflows/a\u202eb.yaml": wfBody("ab"),
	})
	client := git.NewCLIClient(dir)
	t3gitHead(t, dir) // workflow.Service.List reads HEAD

	arts, err := artifact.NewService(client, nil, dir).List(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range arts {
		got = append(got, a.Path)
	}
	if !reflect.DeepEqual(got, []string{"governance/résumé.md"}) {
		t.Errorf("artifact List = %q, want only the non-ASCII artifact", got)
	}

	wfs, err := workflow.NewService(client, dir).List(ctx, workflow.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, w := range wfs {
		ids = append(ids, w.ID)
	}
	if !reflect.DeepEqual(ids, []string{"we"}) {
		t.Errorf("workflow List = %q, want only the non-ASCII workflow", ids)
	}
}

// t3gitHead checks out main so HEAD-relative reads see the commit.
func t3gitHead(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "symbolic-ref", "HEAD", "refs/heads/main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("symbolic-ref: %v\n%s", err, out)
	}
}

// AC 21: the git per-blob limit and the worktree read limit are one
// value (TASK-004 I7), so an oversized artifact is refused at the same
// size on every read path.
func TestReadLimitParityWithWorktreefs(t *testing.T) {
	if git.DefaultMaxBlobBytes != worktreefs.DefaultMaxReadBytes {
		t.Fatalf("git.DefaultMaxBlobBytes = %d, worktreefs.DefaultMaxReadBytes = %d; they must be equal",
			git.DefaultMaxBlobBytes, worktreefs.DefaultMaxReadBytes)
	}
}
