package projection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/store"
)

// fakeGitReader wraps git.GitClient, returning canned responses for
// ReadFile + ListFiles. Tests opt into specific responses; everything
// else delegates to the embedded nil GitClient so unexpected calls
// trip a clear nil-method-value panic.
type fakeGitReader struct {
	git.GitClient
	content      []byte
	readErr      error
	fileExists   bool // simulates ListFiles returning the path
	listFilesErr error
}

func (f *fakeGitReader) ReadFile(_ context.Context, _, _ string) ([]byte, error) {
	return f.content, f.readErr
}

func (f *fakeGitReader) ListFiles(_ context.Context, _, pattern string) ([]string, error) {
	if f.listFilesErr != nil {
		return nil, f.listFilesErr
	}
	if !f.fileExists {
		return nil, nil
	}
	return []string{pattern}, nil
}

// fakeStore captures the rules the projection handler would write. All
// other store methods embed a nil Store — any call besides the branch-
// protection surface will panic, making accidental dependencies obvious.
type fakeStore struct {
	store.Store
	upserts     [][]store.BranchProtectionRuleProjection
	lastCommit  string
	upsertErr   error
	upsertCalls int
}

func (f *fakeStore) UpsertBranchProtectionRules(_ context.Context, rules []store.BranchProtectionRuleProjection, sourceCommit string) error {
	f.upsertCalls++
	f.lastCommit = sourceCommit
	// copy so test assertions see a stable snapshot
	snap := make([]store.BranchProtectionRuleProjection, len(rules))
	copy(snap, rules)
	f.upserts = append(f.upserts, snap)
	return f.upsertErr
}

func newFakeService(gc *fakeGitReader, st *fakeStore) *Service {
	return NewService(gc, st, nil, 0)
}

func TestProjectBranchProtection_ParsedFileReplacesRules(t *testing.T) {
	gc := &fakeGitReader{
		fileExists: true,
		content: []byte(`
version: 1
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
  - branch: "release/*"
    protections: [no-delete]
`),
	}
	st := &fakeStore{}
	s := newFakeService(gc, st)

	if err := s.projectBranchProtection(context.Background(), "sha-abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if st.upsertCalls != 1 {
		t.Fatalf("Upsert called %d times, want 1", st.upsertCalls)
	}
	if st.lastCommit != "sha-abc123" {
		t.Fatalf("sourceCommit = %q, want %q", st.lastCommit, "sha-abc123")
	}

	got := st.upserts[0]
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].BranchPattern != "main" || got[0].RuleOrder != 0 {
		t.Fatalf("row 0 = %+v, want main/0", got[0])
	}
	var prots []string
	if err := json.Unmarshal(got[0].Protections, &prots); err != nil {
		t.Fatalf("protections JSON: %v", err)
	}
	if !reflect.DeepEqual(prots, []string{"no-delete", "no-direct-write"}) {
		t.Fatalf("row 0 protections = %v", prots)
	}
	if got[1].BranchPattern != "release/*" || got[1].RuleOrder != 1 {
		t.Fatalf("row 1 = %+v, want release/* /1", got[1])
	}
}

func TestProjectBranchProtection_MissingFileWritesBootstrap(t *testing.T) {
	// Simulate "file not found" — the git client surfaces an error;
	// the projection handler absorbs it and writes bootstrap defaults
	// stamped with sentinel commit "bootstrap".
	gc := &fakeGitReader{fileExists: false}
	st := &fakeStore{}
	s := newFakeService(gc, st)

	if err := s.projectBranchProtection(context.Background(), "head-sha"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.lastCommit != "bootstrap" {
		t.Fatalf("sourceCommit = %q, want %q", st.lastCommit, "bootstrap")
	}
	if len(st.upserts) != 1 || len(st.upserts[0]) != 1 {
		t.Fatalf("upserts = %+v, want 1 row (bootstrap default)", st.upserts)
	}
	if st.upserts[0][0].BranchPattern != "main" {
		t.Fatalf("bootstrap row = %+v, want main", st.upserts[0][0])
	}
}

func TestProjectBranchProtection_EmptyCommittedFileIsParseError(t *testing.T) {
	// A committed blob that is empty is malformed, not "missing."
	// Falling back to bootstrap could silently drop previously
	// projected rules; instead the handler returns a parse error so
	// the caller retains the existing ruleset and holds the sync
	// state at the last-known-good commit.
	gc := &fakeGitReader{fileExists: true, content: []byte("\n\n\n")}
	st := &fakeStore{}
	s := newFakeService(gc, st)

	err := s.projectBranchProtection(context.Background(), "head-sha")
	if err == nil {
		t.Fatal("expected parse error for empty committed file, got nil")
	}
	if st.upsertCalls != 0 {
		t.Fatalf("empty file triggered %d upserts; want 0 (ruleset must be retained)", st.upsertCalls)
	}
}

func TestProjectBranchProtection_TransientGitErrorPropagates(t *testing.T) {
	// A git failure that isn't "file missing" must not silently
	// replace the ruleset with bootstrap defaults. Examples: repo
	// corruption, a transient `git show` failure, context cancel.
	// The handler must surface the error so the caller holds the
	// sync state at the last-known-good commit.
	gc := &fakeGitReader{listFilesErr: errors.New("fatal: unable to access object database")}
	st := &fakeStore{}
	s := newFakeService(gc, st)

	err := s.projectBranchProtection(context.Background(), "head-sha")
	if err == nil {
		t.Fatal("expected error from transient git failure, got nil")
	}
	if st.upsertCalls != 0 {
		t.Fatalf("transient git error triggered %d upserts; want 0 (ruleset must be retained)", st.upsertCalls)
	}
}

func TestProjectBranchProtection_ReadFileErrorOnExistingFilePropagates(t *testing.T) {
	// The file exists per ListFiles but ReadFile errors — also a
	// transient case that must not silently reset to bootstrap.
	gc := &fakeGitReader{fileExists: true, readErr: errors.New("short read")}
	st := &fakeStore{}
	s := newFakeService(gc, st)

	err := s.projectBranchProtection(context.Background(), "head-sha")
	if err == nil {
		t.Fatal("expected error when ReadFile fails, got nil")
	}
	if st.upsertCalls != 0 {
		t.Fatalf("read error triggered %d upserts; want 0", st.upsertCalls)
	}
}

func TestProjectBranchProtection_ExplicitEmptyRulesListIsPreserved(t *testing.T) {
	// `rules: []` is an intentional opt-out. The projection must NOT
	// silently replace it with bootstrap defaults — that would
	// reinstate protection the author explicitly removed.
	gc := &fakeGitReader{fileExists: true, content: []byte("version: 1\nrules: []\n")}
	st := &fakeStore{}
	s := newFakeService(gc, st)

	if err := s.projectBranchProtection(context.Background(), "head-sha"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.upserts) != 1 || len(st.upserts[0]) != 0 {
		t.Fatalf("upserts = %+v, want 1 empty slice", st.upserts)
	}
	if st.lastCommit != "head-sha" {
		t.Fatalf("sourceCommit = %q, want head-sha", st.lastCommit)
	}
}

func TestProjectBranchProtection_MalformedYAMLRetainsPreviousRules(t *testing.T) {
	// Parse errors never replace the ruleset. The caller counts the
	// returned error as a sync error, which holds the sync state at
	// the last-known-good commit.
	gc := &fakeGitReader{fileExists: true, content: []byte("version: 1\nrules: [bogus key: value")}
	st := &fakeStore{}
	s := newFakeService(gc, st)

	err := s.projectBranchProtection(context.Background(), "bad-sha")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if st.upsertCalls != 0 {
		t.Fatalf("malformed YAML triggered %d upserts; want 0 (rules must be retained)", st.upsertCalls)
	}
}

func TestProjectBranchProtection_StoreUpsertErrorPropagates(t *testing.T) {
	gc := &fakeGitReader{fileExists: true, content: []byte("version: 1\nrules: []\n")}
	st := &fakeStore{upsertErr: errors.New("db down")}
	s := newFakeService(gc, st)

	err := s.projectBranchProtection(context.Background(), "head-sha")
	if err == nil {
		t.Fatal("expected error from store, got nil")
	}
}

// Sanity: reading the BranchProtectionConfigPath constant so future
// refactors don't silently change the watched path.
func TestBranchProtectionConfigPath(t *testing.T) {
	if BranchProtectionConfigPath != ".spine/branch-protection.yaml" {
		t.Fatalf("BranchProtectionConfigPath = %q, want .spine/branch-protection.yaml", BranchProtectionConfigPath)
	}
	// Guard against a stray leading slash in future edits.
	if bytes.HasPrefix([]byte(BranchProtectionConfigPath), []byte("/")) {
		t.Fatal("BranchProtectionConfigPath must be repo-relative, not absolute")
	}
}
