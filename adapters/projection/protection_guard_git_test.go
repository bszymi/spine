package projection

// INIT-023 TASK-006 projection protection-removal guard on a real
// repository (git.CLIClient) with a fake provenance reader: the audit's
// verify-r1-2 projection PoC inverted (AC 18), the accepted cases (AC 19)
// and the refused ones, laundering and replay included (AC 20).

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

const (
	pgProtected = "version: 1\nrules:\n  - branch: main\n    protections: [no-delete, no-direct-write]\n"
	pgRelaxed   = "version: 1\nrules: []\n"
)

// guardMapStore is mapStore with the installed rows readable.
type guardMapStore struct{ *mapStore }

func (s guardMapStore) ListBranchProtectionRules(context.Context) ([]store.BranchProtectionRuleProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.BranchProtectionRuleProjection, 0, len(s.bpRules))
	for _, r := range s.bpRules {
		r.SourceCommit = s.bpCommit
		out = append(out, r)
	}
	return out, nil
}

type pgFix struct {
	t      *testing.T
	r      *scratchRepo
	st     guardMapStore
	svc    *Service
	prov   *fakeProvenance
	router *recordingRouter
	c0     string // the commit the protected rules were installed from
}

// newPGFix: main at c0 carries the protected config, projected and
// synced at c0; the guard is on over prov.
func newPGFix(t *testing.T) *pgFix {
	t.Helper()
	f := &pgFix{t: t, r: newScratchRepo(t), prov: &fakeProvenance{}, router: &recordingRouter{}}
	f.r.write(BranchProtectionConfigPath, pgProtected)
	f.r.write("README.md", "r\n")
	f.c0 = f.r.commit("seed")
	f.st = guardMapStore{newMapStore()}
	f.st.bpRules = protectedMainRows(f.c0)
	f.st.bpCommit = f.c0
	f.st.setState(f.c0, "idle")
	f.svc = NewService(f.r.git, f.st, f.router, 0)
	if err := f.svc.WithAuthoritativeBranch("main"); err != nil {
		t.Fatal(err)
	}
	f.svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true, Provenance: f.prov, Events: f.router})
	return f
}

func (f *pgFix) sync() {
	f.t.Helper()
	if err := f.svc.IncrementalSync(context.Background()); err != nil {
		f.t.Fatalf("IncrementalSync: %v", err)
	}
}

func (f *pgFix) relax(msg string) string {
	f.t.Helper()
	f.r.write(BranchProtectionConfigPath, pgRelaxed)
	return f.r.commit(msg)
}

func (f *pgFix) requireProtected(why string) {
	f.t.Helper()
	rows, _ := f.st.ListBranchProtectionRules(context.Background())
	if len(rows) == 0 {
		f.t.Fatalf("%s: rules removed", why)
	}
	contributorDenied(f.t, rows)
}

func (f *pgFix) requireInstalledRelaxed(why string, source string) {
	f.t.Helper()
	if len(f.st.bpRules) != 0 || f.st.bpCommit != source {
		f.t.Fatalf("%s: rows %d from %q, want the empty ruleset from %s", why, len(f.st.bpRules), f.st.bpCommit, source)
	}
}

func (f *pgFix) refusals() int {
	return len(f.router.ofType(domain.EventBranchProtectionRemovalRefused))
}

func override(commit, base string) store.GovernanceAuthorization {
	return store.GovernanceAuthorization{Source: store.GovernanceSourceOverrideEvent, CommitSHA: commit, BaseSHA: base,
		ActorID: "op-1", EventID: "evt-o"}
}

func approved(merge, base string) store.GovernanceAuthorization {
	return store.GovernanceAuthorization{Source: store.GovernanceSourceApprovedMerge, CommitSHA: merge, BaseSHA: base,
		ActorID: "op-1", RunID: "run-x"}
}

// TestProtectionRemovalGuard_IncrementalSyncKeepsRulesOnSlippedPolicy is
// AC 18, the inverted verify-r1-2 projection PoC: a governed-merge-shaped
// merge carried `rules: []` into main with no operator record. The sync
// ends idle at the merge commit, the rules still protect main (a
// contributor may neither direct-write nor delete it), exactly one
// removal_refused event across three ticks, a later commit that does not
// touch the file does not re-query provenance, and an operator override
// push that EDITS the file installs it on the next tick. FullRebuild
// refuses the same way.
func TestProtectionRemovalGuard_IncrementalSyncKeepsRulesOnSlippedPolicy(t *testing.T) {
	f := newPGFix(t)
	f.r.run("checkout", "-q", "-b", "spine/run/poc-1")
	f.r.write("src/feature.txt", "legit deliverable\n")
	f.r.write(BranchProtectionConfigPath, pgRelaxed)
	f.r.commit("deliverable")
	f.r.run("checkout", "-q", "main")
	f.r.run("merge", "-q", "--no-ff", "-m", "Merge run run-poc: tasks/TASK-001.md", "spine/run/poc-1")
	merged := f.r.run("rev-parse", "HEAD")

	for i := 0; i < 3; i++ {
		f.sync()
	}
	if st := f.st.syncState(); st.Status != "idle" || st.LastSyncedCommit != merged {
		t.Fatalf("sync state = %+v, want idle at the merge commit %s", st, merged)
	}
	f.requireProtected("slipped policy")
	if n := f.refusals(); n != 1 {
		t.Fatalf("removal_refused events across three ticks = %d, want 1", n)
	}
	calls := f.prov.calls
	f.r.write("docs/note.md", "unrelated\n")
	f.r.commit("unrelated")
	f.sync()
	if f.prov.calls != calls {
		t.Fatal("a commit that does not touch the config re-queried provenance")
	}
	f.requireProtected("unrelated commit")

	// FullRebuild refuses the same way (rules kept, not counted as an
	// error — INIT-023 TASK-011 D2).
	if err := f.svc.FullRebuild(context.Background()); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}
	f.requireProtected("rebuild")
	if st := f.st.syncState(); st.Status != "idle" {
		t.Fatalf("rebuild state = %+v, want idle", st)
	}

	// The operator re-affirms with an override push that edits the file.
	before := f.r.run("rev-parse", "HEAD")
	f.r.write(BranchProtectionConfigPath, pgRelaxed+"# re-affirmed by op-1\n")
	o := f.r.commit("operator re-affirms the relaxation")
	f.prov.records = []store.GovernanceAuthorization{override(o, before)}
	f.sync()
	f.requireInstalledRelaxed("override re-affirm", o)
}

// TestProtectionRemovalGuard_AcceptedCases is AC 19.
func TestProtectionRemovalGuard_AcceptedCases(t *testing.T) {
	t.Run("override push of the relaxed config", func(t *testing.T) {
		f := newPGFix(t)
		o := f.relax("operator override push")
		f.prov.records = []store.GovernanceAuthorization{override(o, f.c0)}
		f.sync()
		f.requireInstalledRelaxed("override", o)
		calls := f.prov.calls
		f.r.write("docs/later.md", "x\n")
		f.r.commit("later")
		f.sync()
		f.requireInstalledRelaxed("after an unrelated commit", o)
		if f.prov.calls != calls {
			t.Fatal("provenance re-queried for an unrelated commit")
		}
	})
	t.Run("consumed approval of a governed merge", func(t *testing.T) {
		f := newPGFix(t)
		f.r.run("checkout", "-q", "-b", "spine/run/r1")
		f.r.write(BranchProtectionConfigPath, pgRelaxed)
		f.r.commit("run work")
		f.r.run("checkout", "-q", "main")
		pre := f.r.run("rev-parse", "HEAD")
		f.r.run("merge", "-q", "--no-ff", "-m", "Merge run r1", "spine/run/r1")
		m := f.r.run("rev-parse", "HEAD")
		f.prov.records = []store.GovernanceAuthorization{approved(m, pre)}
		f.sync()
		f.requireInstalledRelaxed("approval", m)
		calls := f.prov.calls
		f.r.write("docs/later.md", "x\n")
		f.r.commit("later")
		f.sync()
		f.requireInstalledRelaxed("after an unrelated commit", m)
		if f.prov.calls != calls {
			t.Fatal("provenance re-queried for an unrelated commit")
		}
	})
}

// TestProtectionRemovalGuard_RefusedCases is AC 20 with the design
// check's laundering and bootstrap-replay cases. Each keeps the rules;
// each fails if its own check is removed (newness, reachability, content,
// changed config, the LastSyncedCommit anchor).
func TestProtectionRemovalGuard_RefusedCases(t *testing.T) {
	t.Run("replay of a record older than the installed rules", func(t *testing.T) {
		f := newPGFix(t)
		// History: relaxed at a (authorised then), protected again at p
		// (installed), relaxed again at head with the same bytes as a.
		a := f.relax("relaxed earlier")
		f.r.write(BranchProtectionConfigPath, pgProtected)
		p := f.r.commit("protected again")
		f.st.bpCommit = p
		f.st.setState(p, "idle")
		f.relax("relaxed again, no operator")
		f.prov.records = []store.GovernanceAuthorization{override(a, f.c0)}
		f.sync()
		f.requireProtected("replay")
	})
	t.Run("record not reachable from head", func(t *testing.T) {
		f := newPGFix(t)
		f.r.run("checkout", "-q", "-b", "side")
		side := f.relax("side relax")
		f.r.run("checkout", "-q", "main")
		f.relax("main relax, no operator")
		f.prov.records = []store.GovernanceAuthorization{override(side, f.c0)}
		f.sync()
		f.requireProtected("unreachable")
	})
	t.Run("record config bytes differ", func(t *testing.T) {
		f := newPGFix(t)
		f.r.write(BranchProtectionConfigPath, pgRelaxed+"# operator's version\n")
		o := f.r.commit("operator relax")
		f.r.write(BranchProtectionConfigPath, pgRelaxed)
		f.r.commit("someone else's relax")
		f.prov.records = []store.GovernanceAuthorization{override(o, f.c0)}
		f.sync()
		f.requireProtected("content differs")
	})
	t.Run("empty record list", func(t *testing.T) {
		f := newPGFix(t)
		f.relax("no operator")
		f.sync()
		f.requireProtected("no records")
		if f.refusals() != 1 {
			t.Fatalf("refusals = %d", f.refusals())
		}
	})
	t.Run("nil reader", func(t *testing.T) {
		f := newPGFix(t)
		f.svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true, Events: f.router})
		f.relax("no operator")
		f.sync()
		f.requireProtected("nil reader")
	})
	t.Run("laundering: an override that did not change the config", func(t *testing.T) {
		f := newPGFix(t)
		r := f.relax("relaxation slips in")
		f.sync() // refused
		f.r.write("hotfix.txt", "fix\n")
		h := f.r.commit("operator hotfix override push")
		f.prov.records = []store.GovernanceAuthorization{override(h, r)}
		if err := f.svc.FullRebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.requireProtected("laundered by a hotfix override")
	})
	t.Run("laundering: an approved merge that did not change the config", func(t *testing.T) {
		f := newPGFix(t)
		r := f.relax("relaxation slips in")
		f.sync() // refused
		f.r.run("checkout", "-q", "-b", "spine/run/wf")
		f.r.write("workflows/x.yaml", "id: x\n")
		f.r.commit("workflow edit")
		f.r.run("checkout", "-q", "main")
		f.r.run("merge", "-q", "--no-ff", "-m", "Merge run wf", "spine/run/wf")
		m := f.r.run("rev-parse", "HEAD")
		f.prov.records = []store.GovernanceAuthorization{approved(m, r)}
		if err := f.svc.FullRebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.requireProtected("laundered by an unrelated approved merge")
	})
	t.Run("bootstrap replay: a record older than the sync's start", func(t *testing.T) {
		f := newPGFix(t)
		a := f.relax("authorised relaxation (long ago)")
		f.r.remove(BranchProtectionConfigPath)
		s := f.r.commit("config deleted")
		// Installed: the bootstrap rows; synced at s.
		f.st.bpRules = bootstrapRows()
		f.st.bpCommit = "bootstrap"
		f.st.setState(s, "idle")
		f.relax("the old blob, re-added without an operator")
		f.prov.records = []store.GovernanceAuthorization{override(a, f.c0)}
		f.sync()
		f.requireProtected("bootstrap replay")
		if f.st.bpCommit != "bootstrap" {
			t.Fatalf("installed source = %q, want the bootstrap rows kept", f.st.bpCommit)
		}
	})
	t.Run("reader error holds the sync and is retried", func(t *testing.T) {
		f := newPGFix(t)
		o := f.relax("operator override")
		f.prov.err = errors.New("database down")
		err := f.svc.IncrementalSync(context.Background())
		_ = err
		f.requireProtected("reader error")
		if st := f.st.syncState(); st.Status != "error" || st.LastSyncedCommit != f.c0 {
			t.Fatalf("state = %+v, want error held at c0 (an infrastructure failure is retried)", st)
		}
		if f.refusals() != 0 {
			t.Fatal("a provenance outage was reported as a refusal")
		}
		f.prov.err = nil
		f.prov.records = []store.GovernanceAuthorization{override(o, f.c0)}
		f.sync()
		f.requireInstalledRelaxed("after the outage", o)
	})
}

// TestProtectionRemovalGuard_DeletionOnAnotherAuthoritativeBranch: with an
// authoritative branch other than main, deleting the file (bootstrap
// rules protect main only) is a removal and needs a record that deleted
// it.
func TestProtectionRemovalGuard_DeletionOnAnotherAuthoritativeBranch(t *testing.T) {
	f := newPGFix(t)
	f.st.bpRules = []store.BranchProtectionRuleProjection{{BranchPattern: "*", RuleOrder: 0,
		Protections: []byte(`["no-delete","no-direct-write"]`)}}
	f.svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true, AuthoritativeBranch: "trunk",
		Provenance: f.prov, Events: f.router})
	f.r.remove(BranchProtectionConfigPath)
	d := f.r.commit("delete the config")
	f.sync()
	if f.st.bpCommit == "bootstrap" || f.refusals() != 1 {
		t.Fatalf("deletion installed bootstrap rules for trunk without a record (source %q)", f.st.bpCommit)
	}
	f.r.write("x.txt", "x\n")
	f.r.commit("touch")
	f.prov.records = []store.GovernanceAuthorization{override(d, f.c0)}
	if err := f.svc.FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.st.bpCommit != "bootstrap" {
		t.Fatalf("an operator deletion was not installed: source %q", f.st.bpCommit)
	}
}
