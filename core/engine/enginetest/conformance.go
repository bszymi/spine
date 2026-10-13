// Package enginetest holds test suites that embedders run against their
// own implementations of engine ports.
//
// RunCommitBinderConformance pins the engine.CommitBinder contract that
// SHA-bound governed merges rely on (INIT-023 TASK-005, AC 14).
// *git.CLIClient runs it in adapters/git; an embedder's operator (SMP's
// RealGitOperator, SMP TASK-009) runs the same suite through its own
// Harness. The suite only shells out to the git CLI to build fixtures,
// so it needs nothing but git on PATH.
package enginetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/engine"
)

// Env is one fresh set of repositories for the suite.
type Env struct {
	// Binder is the implementation under test.
	Binder engine.CommitBinder

	// Source is the git repository (bare or not) whose refs/heads/*
	// ResolveBranchHead reads and whose objects reach the binder:
	// CLIClient's own repository, or a remote-backed operator's origin.
	// It must start with branch main at a commit. The suite writes it
	// with plumbing only (hash-object, update-index on a private index,
	// write-tree, commit-tree, update-ref) and never moves a branch that
	// is checked out there.
	Source string

	// Sync, when set, is called after the suite changed Source and before
	// it calls the binder, for an implementation that has to fetch. nil
	// when the binder's repository is Source.
	Sync func(t *testing.T)

	// Remote is the remote name PushCommit is called with, and RemoteDir
	// that remote's repository, which the suite reads and seeds (it may be
	// Source itself).
	Remote    string
	RemoteDir string

	// Local, when set, is the non-bare repository MergeBound and
	// CommitFilesOnto work in (CLIClient: Source). The suite then also
	// checks there that CommitFilesOnto moves no ref and leaves HEAD, the
	// index and the working tree alone, and plants local shadow refs.
	// Empty skips those checks.
	Local string
}

// Harness adapts one CommitBinder implementation to the suite.
type Harness struct {
	// New returns a fresh Env. It is called once per subtest.
	New func(t *testing.T) Env
}

// RunCommitBinderConformance runs every contract case as a subtest.
func RunCommitBinderConformance(t *testing.T, h Harness) {
	t.Helper()
	cases := []struct {
		name string
		run  func(t *testing.T, e *suiteEnv)
	}{
		{"ResolveBranchHead", testResolveBranchHead},
		{"VerifyCommit", testVerifyCommit},
		{"IsAncestor", testIsAncestor},
		{"CommitParents", testCommitParents},
		{"CommitFilesOnto", testCommitFilesOnto},
		{"CommitFilesOntoRefusals", testCommitFilesOntoRefusals},
		{"CommitFilesOntoDeterministic", testCommitFilesOntoDeterministic},
		{"MergeBound", testMergeBound},
		{"MergeBoundAlreadyUpToDateAfterRecomputedCascade", testMergeBoundRecomputedCascade},
		{"PushCommit", testPushCommit},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			env := h.New(t)
			c.run(t, &suiteEnv{Env: env, src: fixtureRepo{t: t, dir: env.Source}})
		})
	}
}

type suiteEnv struct {
	Env
	src fixtureRepo
}

func (e *suiteEnv) sync(t *testing.T) {
	t.Helper()
	if e.Sync != nil {
		e.Sync(t)
	}
}

// entry is a fixture tree entry.
type entry struct {
	mode    string // "100644" (default), "100755", "120000", "160000"
	content string // file content, symlink target, or gitlink commit id
}

// fixtureRepo builds commits with plumbing, so it works in bare and
// non-bare repositories alike and never touches a working tree.
type fixtureRepo struct {
	t   *testing.T
	dir string
}

var fixtureClock = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func (r fixtureRepo) run(env []string, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Spine Conformance", "GIT_AUTHOR_EMAIL=conformance@spine.local",
		"GIT_COMMITTER_NAME=Spine Conformance", "GIT_COMMITTER_EMAIL=conformance@spine.local")
	cmd.Env = append(cmd.Env, env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func (r fixtureRepo) git(args ...string) string {
	r.t.Helper()
	out, err := r.run(nil, "", args...)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// ref returns the object a ref points at, or "" when it does not exist.
func (r fixtureRepo) ref(name string) string {
	out, err := r.run(nil, "", "rev-parse", "--verify", "--quiet", name)
	if err != nil {
		return ""
	}
	return out
}

func (r fixtureRepo) setRef(name, sha string) {
	r.t.Helper()
	r.git("update-ref", name, sha)
}

func (r fixtureRepo) blob(content string) string {
	r.t.Helper()
	out, err := r.run(nil, content, "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// commit creates a commit with the given parents whose tree is the first
// parent's tree (empty without parents) with entries applied ("" content
// with mode "-" deletes).
func (r fixtureRepo) commit(msg string, entries map[string]entry, parents ...string) string {
	r.t.Helper()
	idx := filepath.Join(r.t.TempDir(), "index")
	env := []string{"GIT_INDEX_FILE=" + idx}
	if len(parents) > 0 {
		if _, err := r.run(env, "", "read-tree", parents[0]); err != nil {
			r.t.Fatal(err)
		}
	}
	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var info strings.Builder
	for _, p := range paths {
		en := entries[p]
		mode := en.mode
		if mode == "" {
			mode = "100644"
		}
		obj := en.content
		if mode != "160000" {
			obj = r.blob(en.content)
		}
		fmt.Fprintf(&info, "%s %s\t%s\x00", mode, obj, p)
	}
	if info.Len() > 0 {
		if _, err := r.run(env, info.String(), "update-index", "-z", "--index-info"); err != nil {
			r.t.Fatal(err)
		}
	}
	tree, err := r.run(env, "", "write-tree")
	if err != nil {
		r.t.Fatal(err)
	}
	fixtureClock = fixtureClock.Add(time.Minute)
	date := fmt.Sprintf("%d +0000", fixtureClock.Unix())
	args := []string{"commit-tree", "--no-gpg-sign"}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-m", msg, tree)
	out, err := r.run([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "", args...)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// treeOf lists a commit's tree as path → "mode object".
func (r fixtureRepo) treeOf(commit string) map[string]string {
	r.t.Helper()
	out := r.git("ls-tree", "-r", "-z", "--full-tree", commit)
	m := make(map[string]string)
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			r.t.Fatalf("ls-tree record %q", rec)
		}
		m[p] = f[0] + " " + f[2]
	}
	return m
}

// pushTo copies an object (and its history) from r into dst under ref,
// with the git CLI directly (fixture setup, not the binder).
func (r fixtureRepo) pushTo(dst, sha, ref string) {
	r.t.Helper()
	if _, err := r.run(nil, "", "push", "--quiet", "--force", dst, sha+":"+ref); err != nil {
		r.t.Fatal(err)
	}
}

func (r fixtureRepo) objectFormatLen() int {
	r.t.Helper()
	if r.git("rev-parse", "--show-object-format") == "sha256" {
		return 64
	}
	return 40
}

func mainTip(t *testing.T, e *suiteEnv) string {
	t.Helper()
	tip := e.src.ref("refs/heads/main")
	if tip == "" {
		t.Fatal("harness: Source has no refs/heads/main")
	}
	return tip
}

func ctx() context.Context { return context.Background() }

func requireNotFound(t *testing.T, what string, err error) {
	t.Helper()
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound {
		t.Fatalf("%s: want *git.GitError with ErrKindNotFound, got %T %v", what, err, err)
	}
}

func testResolveBranchHead(t *testing.T, e *suiteEnv) {
	base := mainTip(t, e)
	c1 := e.src.commit("feature 1", map[string]entry{"f.txt": {content: "one"}}, base)
	c2 := e.src.commit("tag target", map[string]entry{"f.txt": {content: "two"}}, base)
	e.src.setRef("refs/heads/feature", c1)
	// A pushed tag (and a doubly qualified ref) named like the branch must
	// never answer for it.
	e.src.setRef("refs/tags/feature", c2)
	e.src.setRef("refs/tags/refs/heads/feature", c2)
	// A branch that exists only as a tag shadow.
	e.src.setRef("refs/tags/ghost", c2)
	e.src.setRef("refs/tags/refs/heads/ghost", c2)
	e.sync(t)

	got, err := e.Binder.ResolveBranchHead(ctx(), "feature")
	if err != nil || got != c1 {
		t.Fatalf("ResolveBranchHead(feature) = %q, %v; want the branch %s, not the tag %s", got, err, c1, c2)
	}
	if got, err := e.Binder.ResolveBranchHead(ctx(), "refs/heads/feature"); err != nil || got != c1 {
		t.Fatalf("ResolveBranchHead(refs/heads/feature) = %q, %v; want %s", got, err, c1)
	}
	_, err = e.Binder.ResolveBranchHead(ctx(), "ghost")
	requireNotFound(t, "ResolveBranchHead(branch missing, tag present)", err)
	_, err = e.Binder.ResolveBranchHead(ctx(), "no-such-branch")
	requireNotFound(t, "ResolveBranchHead(unknown)", err)
	for _, bad := range []string{"", "-x", "--upload-pack=x", "refs/tags/feature", "HEAD", "a..b", "a b", "x\x00y", c1} {
		if got, err := e.Binder.ResolveBranchHead(ctx(), bad); err == nil {
			t.Errorf("ResolveBranchHead(%q) = %q, want a refusal", bad, got)
		}
	}
}

func testVerifyCommit(t *testing.T, e *suiteEnv) {
	base := mainTip(t, e)
	c := e.src.commit("commit", map[string]entry{"v.txt": {content: "v"}}, base)
	e.src.setRef("refs/heads/verify", c)
	tree := e.src.git("rev-parse", c+"^{tree}")
	blob := e.src.git("rev-parse", c+":v.txt")
	e.src.git("tag", "-a", "-m", "annotated", "annotated-tag", c)
	tagObj := e.src.git("rev-parse", "refs/tags/annotated-tag")
	// DC-4: in a SHA-1 repository a 64-digit hex string is resolved as a
	// ref NAME. Plant one in every namespace git consults.
	other := strings.Repeat("ab", 32)
	if e.src.objectFormatLen() == 64 {
		other = strings.Repeat("ab", 20)
	}
	e.src.setRef("refs/tags/"+other, c)
	e.src.setRef("refs/heads/"+other, c)
	if e.Local != "" && e.Local != e.Source {
		local := fixtureRepo{t: t, dir: e.Local}
		e.sync(t)
		local.setRef("refs/tags/"+other, local.ref("refs/heads/main"))
	}
	e.sync(t)

	if err := e.Binder.VerifyCommit(ctx(), c); err != nil {
		t.Fatalf("VerifyCommit(commit) = %v", err)
	}
	refused := map[string]string{
		"7-char prefix":        c[:7],
		"39 hex":               c[:len(c)-1],
		"uppercase":            strings.ToUpper(c),
		"non-hex":              strings.Repeat("g", len(c)),
		"tree id":              tree,
		"blob id":              blob,
		"annotated tag object": tagObj,
		"missing object":       strings.Repeat("1", len(c)),
		"leading dash":         "-" + c[1:],
		"option":               "--upload-pack=x",
		"other-length hex ref": other,
		"empty":                "",
	}
	for name, v := range refused {
		if err := e.Binder.VerifyCommit(ctx(), v); err == nil {
			t.Errorf("VerifyCommit(%s %q) = nil, want a refusal", name, v)
		}
	}
}

func testIsAncestor(t *testing.T, e *suiteEnv) {
	base := mainTip(t, e)
	a := e.src.commit("a", map[string]entry{"a": {content: "a"}}, base)
	b := e.src.commit("b", map[string]entry{"b": {content: "b"}}, a)
	sib := e.src.commit("sibling", map[string]entry{"s": {content: "s"}}, base)
	e.src.setRef("refs/heads/anc-b", b)
	e.src.setRef("refs/heads/anc-s", sib)
	e.sync(t)

	for _, tc := range []struct {
		anc, desc string
		want      bool
	}{
		{a, b, true}, {base, b, true}, {b, b, true}, {b, a, false}, {sib, b, false}, {b, sib, false},
	} {
		got, err := e.Binder.IsAncestor(ctx(), tc.anc, tc.desc)
		if err != nil || got != tc.want {
			t.Errorf("IsAncestor(%s, %s) = %v, %v; want %v", tc.anc[:8], tc.desc[:8], got, err, tc.want)
		}
	}
	missing := strings.Repeat("2", len(a))
	if _, err := e.Binder.IsAncestor(ctx(), missing, b); err == nil {
		t.Error("IsAncestor(missing, b): want an error")
	}
	if _, err := e.Binder.IsAncestor(ctx(), a, missing); err == nil {
		t.Error("IsAncestor(a, missing): want an error")
	}
	if _, err := e.Binder.IsAncestor(ctx(), "-x", b); err == nil {
		t.Error("IsAncestor(-x, b): want an error")
	}
}

func testCommitParents(t *testing.T, e *suiteEnv) {
	base := mainTip(t, e)
	p1 := e.src.commit("p1", map[string]entry{"p1": {content: "1"}}, base)
	p2 := e.src.commit("p2", map[string]entry{"p2": {content: "2"}}, base)
	merge := e.src.commit("merge", map[string]entry{"p2": {content: "2"}}, p1, p2)
	root := e.src.commit("root", map[string]entry{"r": {content: "r"}})
	e.src.setRef("refs/heads/parents-merge", merge)
	e.src.setRef("refs/heads/parents-root", root)
	e.sync(t)

	for _, tc := range []struct {
		sha  string
		want []string
	}{
		{merge, []string{p1, p2}}, {p1, []string{base}}, {root, []string{}},
	} {
		got, err := e.Binder.CommitParents(ctx(), tc.sha)
		if err != nil || got == nil || strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("CommitParents(%s) = %v (nil=%v), %v; want %v", tc.sha[:8], got, got == nil, err, tc.want)
		}
	}
	for _, bad := range []string{"-x", "main", merge[:7], strings.ToUpper(merge)} {
		if _, err := e.Binder.CommitParents(ctx(), bad); err == nil {
			t.Errorf("CommitParents(%q): want a refusal", bad)
		}
	}
}

// localState captures what CommitFilesOnto must not change in Local.
func localState(t *testing.T, dir string) string {
	t.Helper()
	r := fixtureRepo{t: t, dir: dir}
	return strings.Join([]string{
		r.git("for-each-ref", "--format=%(refname) %(objectname)"),
		r.git("symbolic-ref", "-q", "HEAD"),
		r.git("rev-parse", "HEAD"),
		r.git("ls-files", "-s"),
		r.git("status", "--porcelain", "--untracked-files=all"),
	}, "\n--\n")
}

// ontoFixture is a base with a regular file, an executable, a nested file
// and a symlink, on a branch so remote-backed binders can fetch it.
func ontoFixture(t *testing.T, e *suiteEnv, branch string) string {
	t.Helper()
	base := e.src.commit("onto base", map[string]entry{
		"a.md":       {content: "---\nstatus: Draft\n---\n"},
		"bin/run.sh": {mode: "100755", content: "#!/bin/sh\necho one\n"},
		"docs/x.md":  {content: "x\n"},
		"keep.txt":   {content: "keep\n"},
		"link":       {mode: "120000", content: "keep.txt"},
	}, mainTip(t, e))
	e.src.setRef("refs/heads/"+branch, base)
	e.sync(t)
	return base
}

// inspect publishes a commit to the remote under a scratch branch with
// the binder itself, so the suite can read its tree wherever the binder
// keeps its objects.
func inspect(t *testing.T, e *suiteEnv, sha, scratch string) map[string]string {
	t.Helper()
	if err := e.Binder.PushCommit(ctx(), e.Remote, scratch, sha); err != nil {
		t.Fatalf("PushCommit(%s) to inspect: %v", scratch, err)
	}
	return fixtureRepo{t: t, dir: e.RemoteDir}.treeOf(sha)
}

func testCommitFilesOnto(t *testing.T, e *suiteEnv) {
	base := ontoFixture(t, e, "onto-base")
	var before string
	if e.Local != "" {
		before = localState(t, e.Local)
	}
	files := map[string]string{
		"a.md":       "---\nstatus: Completed\n---\n",
		"bin/run.sh": "#!/bin/sh\necho two\n",
		"new/n.md":   "new\n",
	}
	res, err := e.Binder.CommitFilesOnto(ctx(), base, files, git.CommitOpts{
		Message:  "Update artifact status to Completed",
		Trailers: map[string]string{"Run-ID": "run-conformance", "Trace-ID": "trace-conformance"},
	})
	if err != nil {
		t.Fatalf("CommitFilesOnto: %v", err)
	}
	if e.Local != "" {
		if after := localState(t, e.Local); after != before {
			t.Fatalf("CommitFilesOnto changed refs, HEAD, the index or the working tree:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	}
	parents, err := e.Binder.CommitParents(ctx(), res.SHA)
	if err != nil || len(parents) != 1 || parents[0] != base {
		t.Fatalf("CommitFilesOnto parents = %v, %v; want [%s]", parents, err, base)
	}
	got := inspect(t, e, res.SHA, "onto-check")
	want := fixtureRepo{t: t, dir: e.RemoteDir}.treeOf(base)
	remote := fixtureRepo{t: t, dir: e.RemoteDir}
	for p, content := range files {
		mode := "100644"
		if p == "bin/run.sh" {
			mode = "100755" // the executable bit is preserved
		}
		want[p] = mode + " " + remote.blob(content)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("CommitFilesOnto tree:\n got  %v\n want %v", got, want)
	}
	if ref := e.src.ref("refs/heads/onto-base"); ref != base {
		t.Fatalf("CommitFilesOnto moved the base branch to %s", ref)
	}
}

func testCommitFilesOntoRefusals(t *testing.T, e *suiteEnv) {
	base := ontoFixture(t, e, "onto-refusals")
	for _, p := range []string{"../x", "/abs", ".git/config", "a/.GIT/b", "a/./b", "a//b", "a/", "link", "link/x",
		"bin", "keep.txt/x", "a\x00b", "a\nb", ""} {
		if res, err := e.Binder.CommitFilesOnto(ctx(), base, map[string]string{p: "x"}, git.CommitOpts{Message: "m"}); err == nil {
			t.Errorf("CommitFilesOnto(%q) = %s, want a refusal", p, res.SHA)
		}
	}
	for _, b := range []string{"", base[:7], strings.ToUpper(base), "-x", strings.Repeat("3", len(base))} {
		if _, err := e.Binder.CommitFilesOnto(ctx(), b, map[string]string{"a.md": "x"}, git.CommitOpts{Message: "m"}); err == nil {
			t.Errorf("CommitFilesOnto(base %q): want a refusal", b)
		}
	}
	if _, err := e.Binder.CommitFilesOnto(ctx(), base, nil, git.CommitOpts{Message: "m"}); err == nil {
		t.Error("CommitFilesOnto(no files, AllowEmpty false): want a refusal")
	}
	res, err := e.Binder.CommitFilesOnto(ctx(), base, nil, git.CommitOpts{Message: "ledger", AllowEmpty: true})
	if err != nil {
		t.Fatalf("CommitFilesOnto(no files, AllowEmpty): %v", err)
	}
	if parents, err := e.Binder.CommitParents(ctx(), res.SHA); err != nil || len(parents) != 1 || parents[0] != base {
		t.Fatalf("empty CommitFilesOnto parents = %v, %v", parents, err)
	}
}

func testCommitFilesOntoDeterministic(t *testing.T, e *suiteEnv) {
	base := ontoFixture(t, e, "onto-determinism")
	files := map[string]string{"a.md": "---\nstatus: Completed\n---\n", "docs/x.md": "y\n"}
	opts := git.CommitOpts{Message: "Cascade", Trailers: map[string]string{
		"Run-ID": "run-d", "Trace-ID": "trace-d", "Zeta": "z", "Alpha": "a", "Mid": "m",
	}}
	first, err := e.Binder.CommitFilesOnto(ctx(), base, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // commit-tree stamps seconds
	second, err := e.Binder.CommitFilesOnto(ctx(), base, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA != second.SHA {
		t.Fatalf("CommitFilesOnto is not deterministic: %s then %s (DC-2)", first.SHA, second.SHA)
	}
}

func testMergeBound(t *testing.T, e *suiteEnv) {
	base := mainTip(t, e)
	b := e.src.commit("target base", map[string]entry{"t.txt": {content: "t"}}, base)
	s1 := e.src.commit("reviewed", map[string]entry{"s1.txt": {content: "1"}}, base)
	s2 := e.src.commit("pushed later", map[string]entry{"s2.txt": {content: "2"}}, s1)
	e.src.setRef("refs/heads/mb-target", b)
	e.src.setRef("refs/heads/mb-source", s2)
	e.sync(t)

	if _, err := e.Binder.MergeBound(ctx(), git.MergeOpts{Source: "mb-source", Target: "mb-target", Strategy: "merge-commit", Message: "m"}); err == nil {
		t.Fatal("MergeBound without SourceSHA: want an error")
	}
	if _, err := e.Binder.MergeBound(ctx(), git.MergeOpts{SourceSHA: s1, Strategy: "merge-commit", Message: "m"}); err == nil {
		t.Fatal("MergeBound without Target: want an error")
	}
	if _, err := e.Binder.MergeBound(ctx(), git.MergeOpts{SourceSHA: s1[:7], Target: "mb-target", Strategy: "merge-commit", Message: "m"}); err == nil {
		t.Fatal("MergeBound with an abbreviated SourceSHA: want an error")
	}
	res, err := e.Binder.MergeBound(ctx(), git.MergeOpts{
		Source: "mb-source", SourceSHA: s1, Target: "mb-target", Strategy: "merge-commit", Message: "Merge run conformance",
	})
	if err != nil {
		t.Fatalf("MergeBound: %v", err)
	}
	if res.BaseSHA != b {
		t.Fatalf("MergeBound BaseSHA = %s, want the pre-merge target tip %s", res.BaseSHA, b)
	}
	parents, err := e.Binder.CommitParents(ctx(), res.SHA)
	if err != nil || len(parents) != 2 || parents[0] != b || parents[1] != s1 {
		t.Fatalf("MergeBound result parents = %v, %v; want [%s %s]", parents, err, b, s1)
	}
	if in, err := e.Binder.IsAncestor(ctx(), s2, res.SHA); err != nil || in {
		t.Fatalf("MergeBound merged Source's tip %s (in=%v, err=%v); it must merge SourceSHA only", s2, in, err)
	}
	again, err := e.Binder.MergeBound(ctx(), git.MergeOpts{
		Source: "mb-source", SourceSHA: s1, Target: "mb-target", Strategy: "merge-commit", Message: "Merge run conformance",
	})
	if err != nil {
		t.Fatalf("MergeBound replay: %v", err)
	}
	if again.SHA != res.SHA || again.BaseSHA != res.SHA {
		t.Fatalf("MergeBound replay = {SHA %s, BaseSHA %s}; want already up to date at %s", again.SHA, again.BaseSHA, res.SHA)
	}
}

func testMergeBoundRecomputedCascade(t *testing.T, e *suiteEnv) {
	base := ontoFixture(t, e, "cascade-source")
	e.src.setRef("refs/heads/cascade-target", mainTip(t, e))
	e.sync(t)
	files := map[string]string{"a.md": "---\nstatus: Completed\n---\n"}
	opts := git.CommitOpts{Message: "Update artifact status to Completed", Trailers: map[string]string{"Run-ID": "run-c", "Trace-ID": "trace-c"}}
	c, err := e.Binder.CommitFilesOnto(ctx(), base, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.Binder.MergeBound(ctx(), git.MergeOpts{SourceSHA: c.SHA, Target: "cascade-target", Strategy: "merge-commit", Message: "Merge run c"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	c2, err := e.Binder.CommitFilesOnto(ctx(), base, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	if c2.SHA != c.SHA {
		t.Fatalf("recomputed cascade %s != %s", c2.SHA, c.SHA)
	}
	replay, err := e.Binder.MergeBound(ctx(), git.MergeOpts{SourceSHA: c2.SHA, Target: "cascade-target", Strategy: "merge-commit", Message: "Merge run c"})
	if err != nil {
		t.Fatal(err)
	}
	if replay.SHA != first.SHA || replay.BaseSHA != first.SHA {
		t.Fatalf("replay with a recomputed cascade = {SHA %s, BaseSHA %s}; want already up to date at %s", replay.SHA, replay.BaseSHA, first.SHA)
	}
}

func testPushCommit(t *testing.T, e *suiteEnv) {
	base := mainTip(t, e)
	remote := fixtureRepo{t: t, dir: e.RemoteDir}
	c1 := e.src.commit("push 1", map[string]entry{"p": {content: "1"}}, base)
	c2 := e.src.commit("push 2", map[string]entry{"p": {content: "2"}}, c1)
	other := e.src.commit("diverged", map[string]entry{"p": {content: "x"}}, base)
	e.src.setRef("refs/heads/push-src", c2)
	e.src.setRef("refs/heads/push-other", other)
	e.sync(t)
	e.src.pushTo(e.RemoteDir, base, "refs/heads/push-a")
	e.src.pushTo(e.RemoteDir, base, "refs/heads/push-b")
	e.src.pushTo(e.RemoteDir, other, "refs/heads/push-c")
	if e.Local != "" {
		// The local branch of the same name points elsewhere: PushCommit
		// publishes the object, not the local ref.
		fixtureRepo{t: t, dir: e.Local}.setRef("refs/heads/push-a", other)
	}

	if err := e.Binder.PushCommit(ctx(), e.Remote, "push-a", c1); err != nil {
		t.Fatalf("PushCommit fast-forward: %v", err)
	}
	if got := remote.ref("refs/heads/push-a"); got != c1 {
		t.Fatalf("remote push-a = %s, want exactly %s", got, c1)
	}
	if err := e.Binder.PushCommit(ctx(), e.Remote, "push-a", c1); err != nil {
		t.Fatalf("PushCommit when the remote already has the commit: %v", err)
	}
	// The remote lags behind by more than one commit (an earlier push
	// failed after the local merge): still a fast-forward.
	if err := e.Binder.PushCommit(ctx(), e.Remote, "push-b", c2); err != nil {
		t.Fatalf("PushCommit catching up: %v", err)
	}
	if got := remote.ref("refs/heads/push-b"); got != c2 {
		t.Fatalf("remote push-b = %s, want %s", got, c2)
	}
	err := e.Binder.PushCommit(ctx(), e.Remote, "push-c", c2)
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.IsRetryable() {
		t.Fatalf("PushCommit non-fast-forward = %v; want a non-retryable *git.GitError", err)
	}
	if got := remote.ref("refs/heads/push-c"); got != other {
		t.Fatalf("remote push-c = %s after a refused push; want it unchanged at %s (no forced update)", got, other)
	}
	for _, tc := range []struct{ remote, branch, sha string }{
		{e.Remote, "push-a", "abc"},
		{e.Remote, "push-a", strings.ToUpper(c2)},
		{e.Remote, "push-a", "-" + c2[1:]},
		{"--upload-pack=x", "push-a", c2},
		{"https://example.invalid/x.git", "push-a", c2},
		{e.Remote, "refs/tags/push-a", c2},
		{e.Remote, "-push-a", c2},
		{e.Remote, "", c2},
	} {
		if err := e.Binder.PushCommit(ctx(), tc.remote, tc.branch, tc.sha); err == nil {
			t.Errorf("PushCommit(%q, %q, %q): want a refusal", tc.remote, tc.branch, tc.sha)
		}
	}
	if got := remote.ref("refs/heads/push-a"); got != c1 {
		t.Fatalf("remote push-a = %s after refused pushes; want %s", got, c1)
	}
}
