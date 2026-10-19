package projection

// INIT-023 TASK-006 projection protection-removal guard, unit level: the
// audit PoC poc/store-projection/poc_bp_relax_test.go inverted (AC 17),
// and the cases that must never read provenance (AC 21).

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/branchprotect"
	bpproj "github.com/bszymi/spine/core/branchprotect/projection"
	"github.com/bszymi/spine/core/domain"
)

// guardFakeStore is fakeStore (branch_protection_test.go) with the
// installed rows readable, as the guard needs, and kept in step with the
// upserts.
type guardFakeStore struct {
	*fakeStore
	rows []store.BranchProtectionRuleProjection
}

func (s *guardFakeStore) ListBranchProtectionRules(context.Context) ([]store.BranchProtectionRuleProjection, error) {
	return s.rows, nil
}

func (s *guardFakeStore) UpsertBranchProtectionRules(ctx context.Context, rows []store.BranchProtectionRuleProjection, commit string) error {
	if err := s.fakeStore.UpsertBranchProtectionRules(ctx, rows, commit); err != nil {
		return err
	}
	s.rows = nil
	for _, r := range rows {
		r.SourceCommit = commit
		s.rows = append(s.rows, r)
	}
	return nil
}

func protectedMainRows(source string) []store.BranchProtectionRuleProjection {
	return []store.BranchProtectionRuleProjection{{BranchPattern: "main", RuleOrder: 0,
		Protections: []byte(`["no-delete","no-direct-write"]`), SourceCommit: source}}
}

// fakeProvenance is a GovernanceProvenanceReader.
type fakeProvenance struct {
	records []store.GovernanceAuthorization
	err     error
	calls   int
	t       *testing.T // when set, any call fails the test
}

func (p *fakeProvenance) ListGovernanceAuthorizations(context.Context, string, int) ([]store.GovernanceAuthorization, error) {
	p.calls++
	if p.t != nil {
		p.t.Helper()
		p.t.Errorf("provenance read where none is needed")
	}
	return p.records, p.err
}

func contributorDenied(t *testing.T, rows []store.BranchProtectionRuleProjection) {
	t.Helper()
	src, err := bpproj.New(staticRows(rows))
	if err != nil {
		t.Fatal(err)
	}
	pol := branchprotect.New(src)
	for _, kind := range []branchprotect.OperationKind{branchprotect.OpDirectWrite, branchprotect.OpDelete} {
		dec, _, err := pol.Evaluate(context.Background(), branchprotect.Request{Branch: "refs/heads/main", Kind: kind,
			Actor: domain.Actor{ActorID: "contrib-1", Role: domain.RoleContributor}})
		if err != nil || dec != branchprotect.DecisionDeny {
			t.Fatalf("%s main by a contributor = %s (%v), want deny", kind, dec, err)
		}
	}
}

// TestProtectionRemovalGuard_RefusesUnprovenancedEmptyRuleset is AC 17,
// the inverted poc_bp_relax: a projected `rules: []` with no operator
// record is refused — no upsert, the typed error, and the policy over the
// installed rows still denies. Mutation: guardProtectionRemoval returning
// nil installs the empty ruleset.
func TestProtectionRemovalGuard_RefusesUnprovenancedEmptyRuleset(t *testing.T) {
	gc := &fakeGitReader{fileExists: true, content: []byte("version: 1\nrules: []\n")}
	st := &guardFakeStore{fakeStore: &fakeStore{}, rows: protectedMainRows("c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0")}
	svc := NewService(gc, st, nil, 0)
	router := &recordingRouter{}
	svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true, Provenance: &fakeProvenance{}, Events: router})

	err := svc.projectBranchProtection(context.Background(), "abcdef1234567890abcdef1234567890abcdef12")
	if !errors.Is(err, ErrProtectionRemovalUnauthorized) {
		t.Fatalf("err = %v, want ErrProtectionRemovalUnauthorized", err)
	}
	if st.upsertCalls != 0 {
		t.Fatalf("upserts = %d, want 0 (the rules stay)", st.upsertCalls)
	}
	contributorDenied(t, st.rows)
	ev := router.ofType(domain.EventBranchProtectionRemovalRefused)
	if len(ev) != 1 || ev[0].EventID != "bp-removal-refused-abcdef123456" {
		t.Fatalf("removal_refused events = %+v", ev)
	}

	// Nil provenance while enabled refuses too.
	svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true})
	if err := svc.projectBranchProtection(context.Background(), "abcdef1234567890abcdef1234567890abcdef12"); !errors.Is(err, ErrProtectionRemovalUnauthorized) {
		t.Fatalf("nil reader: err = %v", err)
	}
	// Off: the v1 behaviour installs it.
	svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{})
	if err := svc.projectBranchProtection(context.Background(), "abcdef1234567890abcdef1234567890abcdef12"); err != nil || st.upsertCalls != 1 || len(st.rows) != 0 {
		t.Fatalf("guard off: err %v, upserts %d, rows %d", err, st.upsertCalls, len(st.rows))
	}
}

// TestProtectionRemovalGuard_NoLookupCases is AC 21: changes that remove
// nothing from the authoritative branch never read provenance, and a
// workspace whose installed rules already lack no-direct-write on main is
// not guarded.
func TestProtectionRemovalGuard_NoLookupCases(t *testing.T) {
	const head = "abcdef1234567890abcdef1234567890abcdef12"
	for _, c := range []struct {
		name      string
		installed []store.BranchProtectionRuleProjection
		exists    bool
		content   string
		wantRows  int
	}{
		{"tightening adds release/*", protectedMainRows("c0"), true,
			"version: 1\nrules:\n  - branch: main\n    protections: [no-delete, no-direct-write]\n  - branch: \"release/*\"\n    protections: [no-delete]\n", 2},
		{"reordering", []store.BranchProtectionRuleProjection{
			{BranchPattern: "main", RuleOrder: 0, Protections: []byte(`["no-delete","no-direct-write"]`)},
			{BranchPattern: "staging", RuleOrder: 1, Protections: []byte(`["no-delete"]`)}}, true,
			"version: 1\nrules:\n  - branch: staging\n    protections: [no-delete]\n  - branch: main\n    protections: [no-direct-write, no-delete]\n", 2},
		{"deleting the file installs the bootstrap rules", protectedMainRows("c0"), false, "", 1},
		{"main without no-direct-write may lose no-delete", []store.BranchProtectionRuleProjection{
			{BranchPattern: "main", RuleOrder: 0, Protections: []byte(`["no-delete"]`)}}, true,
			"version: 1\nrules: []\n", 0},
		{"empty table is already unprotected", []store.BranchProtectionRuleProjection{}, true, "version: 1\nrules: []\n", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			gc := &fakeGitReader{fileExists: c.exists, content: []byte(c.content)}
			st := &guardFakeStore{fakeStore: &fakeStore{}, rows: c.installed}
			svc := NewService(gc, st, nil, 0)
			svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true, Provenance: &fakeProvenance{t: t}})
			if err := svc.projectBranchProtection(context.Background(), head); err != nil {
				t.Fatalf("projectBranchProtection: %v", err)
			}
			if st.upsertCalls != 1 || len(st.rows) != c.wantRows {
				t.Fatalf("upserts %d rows %d, want 1 and %d", st.upsertCalls, len(st.rows), c.wantRows)
			}
		})
	}
}

// TestProtectionRemovalGuard_StoreReadFailureIsAnError: the installed
// rules cannot be read — an infrastructure error, not a refusal.
func TestProtectionRemovalGuard_StoreReadFailureIsAnError(t *testing.T) {
	gc := &fakeGitReader{fileExists: true, content: []byte("version: 1\nrules: []\n")}
	st := &failingRulesStore{fakeStore: &fakeStore{}}
	svc := NewService(gc, st, nil, 0)
	svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true, Provenance: &fakeProvenance{}})
	err := svc.projectBranchProtection(context.Background(), "abcdef1234567890abcdef1234567890abcdef12")
	if err == nil || errors.Is(err, ErrProtectionRemovalUnauthorized) || st.upsertCalls != 0 {
		t.Fatalf("err = %v (upserts %d), want an infrastructure error and no upsert", err, st.upsertCalls)
	}
}

type failingRulesStore struct{ *fakeStore }

func (failingRulesStore) ListBranchProtectionRules(context.Context) ([]store.BranchProtectionRuleProjection, error) {
	return nil, errors.New("database down")
}

// TestService_WorkflowDirsFollowsArtifactsDir pins the set the standalone
// wiring hands the engine's governance-path guard (AC 30): exactly the
// directories this service installs workflows from.
func TestService_WorkflowDirsFollowsArtifactsDir(t *testing.T) {
	svc := NewService(&fakeGitReader{}, &fakeStore{}, nil, 0)
	if got := svc.WorkflowDirs(); len(got) != 1 || got[0] != "workflows/" {
		t.Fatalf("root layout WorkflowDirs = %v", got)
	}
	svc.WithArtifactsDir("spine")
	got := svc.WorkflowDirs()
	if len(got) != 2 || got[0] != "workflows/" || got[1] != "spine/workflows/" {
		t.Fatalf("artifacts_dir spine WorkflowDirs = %v, want [workflows/ spine/workflows/]", got)
	}
	if want := WorkflowDirs("spine"); len(want) != 2 || want[1] != got[1] {
		t.Fatalf("WorkflowDirs(\"spine\") = %v", want)
	}
}
