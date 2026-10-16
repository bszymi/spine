package projection

// INIT-023 TASK-006 projection protection-removal guard on a real
// repository (git.CLIClient) with a fake provenance reader: the audit's
// verify-r1-2 projection PoC inverted (AC 18), the accepted cases (AC 19)
// and the refused ones, laundering and replay included (AC 20).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

const (
	pgProtected = "version: 1\nrules:\n  - branch: main\n    protections: [no-delete, no-direct-write]\n"
	pgRelaxed   = "version: 1\nrules: []\n"
	// pgNoDirectWriteOnly drops no-delete from main and keeps
	// no-direct-write: the first of two relaxations (review round 2).
	pgNoDirectWriteOnly = "version: 1\nrules:\n  - branch: main\n    protections: [no-direct-write]\n"
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

func (f *pgFix) requireInstalledRelaxed(why, source string) {
	f.t.Helper()
	if len(f.st.bpRules) != 0 || f.st.bpCommit != source {
		f.t.Fatalf("%s: rows %d from %q, want the empty ruleset from %s", why, len(f.st.bpRules), f.st.bpCommit, source)
	}
}

func (f *pgFix) refusals() int {
	return len(f.router.ofType(domain.EventBranchProtectionRemovalRefused))
}

// mergeRun commits config on a fresh run branch cut from main and merges
// it into main with --no-ff, as a governed merge does. It returns main
// before the merge (the record's BaseSHA) and the merge commit.
func (f *pgFix) mergeRun(run, config string) (pre, merge string) {
	f.t.Helper()
	f.r.run("checkout", "-q", "-b", "spine/run/"+run)
	f.r.write(BranchProtectionConfigPath, config)
	f.r.commit("run " + run + " work")
	f.r.run("checkout", "-q", "main")
	pre = f.r.run("rev-parse", "HEAD")
	f.r.run("merge", "-q", "--no-ff", "-m", "Merge run "+run, "spine/run/"+run)
	return pre, f.r.run("rev-parse", "HEAD")
}

// commitConfig writes config on the current branch and commits it (an
// override push when a record names it, an unauthorised write otherwise).
func (f *pgFix) commitConfig(config, msg string) (pre, commit string) {
	f.t.Helper()
	pre = f.r.run("rev-parse", "HEAD")
	f.r.write(BranchProtectionConfigPath, config)
	return pre, f.r.commit(msg)
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

	// Review round 2: two authorised relaxations reach main before one
	// projection tick (two approvals inside one poll, a second merge
	// landing during the in-flight hold, a sync held for infrastructure).
	// The newest record removed no-direct-write; no-delete was removed by
	// the earlier authorised act, whose result is exactly the newest
	// record's base. Round 1 judged the newest base alone against
	// installed−candidate and refused both, for good: a FullRebuild could
	// not recover. Mutation: dropping the chain step (custody) refuses.
	t.Run("two approved relaxations in one sync window", func(t *testing.T) {
		f := newPGFix(t)
		pre1, m1 := f.mergeRun("r1", pgNoDirectWriteOnly)
		pre2, m2 := f.mergeRun("r2", pgRelaxed) // built on r1: cut from main after m1
		f.prov.records = []store.GovernanceAuthorization{approved(m2, pre2), approved(m1, pre1)}
		f.sync()
		f.requireInstalledRelaxed("two approvals in one window", m2)
		if f.refusals() != 0 {
			t.Fatalf("refusals = %d, want 0", f.refusals())
		}
	})
	t.Run("an override push then an approved merge in one sync window", func(t *testing.T) {
		f := newPGFix(t)
		preO, o := f.commitConfig(pgNoDirectWriteOnly, "operator override push drops no-delete")
		pre2, m2 := f.mergeRun("r2", pgRelaxed)
		f.prov.records = []store.GovernanceAuthorization{approved(m2, pre2), override(o, preO)}
		f.sync()
		f.requireInstalledRelaxed("override then approval in one window", m2)
	})
	t.Run("the earlier record arriving late is installed by a FullRebuild", func(t *testing.T) {
		// A refusal is re-evaluated on every FullRebuild: once the earlier
		// act's record is readable, the rebuild installs both acts' result.
		f := newPGFix(t)
		pre1, m1 := f.mergeRun("r1", pgNoDirectWriteOnly)
		pre2, m2 := f.mergeRun("r2", pgRelaxed)
		f.prov.records = []store.GovernanceAuthorization{approved(m2, pre2)}
		f.sync()
		f.requireProtected("the earlier act's record not readable yet")
		f.prov.records = []store.GovernanceAuthorization{approved(m2, pre2), approved(m1, pre1)}
		if err := f.svc.FullRebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.requireInstalledRelaxed("rebuild with both records", m2)
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
	t.Run("laundering: an approved merge whose own diff only adds a rule", func(t *testing.T) {
		// Review round 1: the relaxation is already on main (refused); a
		// run's diff against it adds a release/* rule, which reads as
		// tightening. Its approval changed the bytes, but the approved
		// diff removed nothing from main: it must not vouch for the
		// relaxation underneath. Mutation: dropping protectsAll installs
		// the release rule alone and leaves main unprotected.
		f := newPGFix(t)
		r := f.relax("relaxation slips in")
		f.sync() // refused
		f.r.run("checkout", "-q", "-b", "spine/run/tighten")
		f.r.write(BranchProtectionConfigPath, "version: 1\nrules:\n  - branch: release/*\n    protections: [no-delete]\n")
		f.r.commit("add a release rule")
		f.r.run("checkout", "-q", "main")
		f.r.run("merge", "-q", "--no-ff", "-m", "Merge run tighten", "spine/run/tighten")
		m := f.r.run("rev-parse", "HEAD")
		f.prov.records = []store.GovernanceAuthorization{approved(m, r)}
		f.sync()
		f.requireProtected("laundered by an approved merge that only adds a rule")
		if err := f.svc.FullRebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.requireProtected("rebuild after the laundering attempt")
	})
	t.Run("laundering: an unauthorised relaxation between authorised acts", func(t *testing.T) {
		// Review round 2. One window: r1 (approved) drops no-delete, r2
		// (approved) restores it, an unauthorised write drops it again
		// with other bytes, and r3 (approved) only adds a release rule on
		// top. no-delete was removed by an authorised act once in this
		// window, but the state r3 was approved over is not one any
		// operator authorised. A rule that accepted "some valid record
		// removed this kind" (the reviewer's per-kind union), or that
		// matched intermediate states by kinds instead of bytes, installs
		// it. Mutations: either relaxation is caught here.
		f := newPGFix(t)
		pre1, m1 := f.mergeRun("r1", pgNoDirectWriteOnly)
		pre2, m2 := f.mergeRun("r2", pgProtected)
		f.commitConfig(pgNoDirectWriteOnly+"# unauthorised\n", "relaxation slips in again")
		pre3, m3 := f.mergeRun("r3", pgNoDirectWriteOnly+"# unauthorised\n  - branch: release/*\n    protections: [no-delete]\n")
		f.prov.records = []store.GovernanceAuthorization{approved(m3, pre3), approved(m2, pre2), approved(m1, pre1)}
		f.sync()
		f.requireProtected("relaxation laundered through an earlier authorised act")
		if f.refusals() != 1 {
			t.Fatalf("refusals = %d, want 1", f.refusals())
		}
		if err := f.svc.FullRebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.requireProtected("rebuild after the laundering attempt")
	})
	t.Run("laundering: authorised bytes from a line the approved base does not descend from", func(t *testing.T) {
		// Review round 2. An operator authorised dropping no-delete on a
		// side line; the same bytes were written to main without one; an
		// approved run then dropped no-direct-write; the side line is
		// joined later with `merge -s ours` (tree unchanged), making the
		// side record reachable. The approved run's base was produced by
		// the unauthorised write, not by the side record, which is not an
		// ancestor of that base. Mutation: dropping the ancestry check
		// installs the empty ruleset.
		f := newPGFix(t)
		f.r.run("checkout", "-q", "-b", "side")
		preO, o := f.commitConfig(pgNoDirectWriteOnly, "operator override on a side line")
		f.r.run("checkout", "-q", "main")
		f.commitConfig(pgNoDirectWriteOnly, "the same bytes on main, no operator")
		pre2, m2 := f.mergeRun("r2", pgRelaxed)
		f.r.run("merge", "-q", "-s", "ours", "-m", "join the side line", "side")
		f.prov.records = []store.GovernanceAuthorization{approved(m2, pre2), override(o, preO)}
		f.sync()
		f.requireProtected("authorised bytes replayed from a parallel line")
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

// TestProtectionRemovalGuard_ApprovedMergeWithoutABase: an approved-merge
// record with no base (the store never returns one; this is the defensive
// branch) is judged against the bootstrap defaults as the state before
// it, which protect main only — so on another authoritative branch it
// accounts for nothing. Mutation: accepting a created base outright
// installs the empty ruleset for trunk.
func TestProtectionRemovalGuard_ApprovedMergeWithoutABase(t *testing.T) {
	f := newPGFix(t)
	f.st.bpRules = []store.BranchProtectionRuleProjection{{BranchPattern: "*", RuleOrder: 0,
		Protections: []byte(`["no-delete","no-direct-write"]`)}}
	f.svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true, AuthoritativeBranch: "trunk",
		Provenance: f.prov, Events: f.router})
	_, m := f.mergeRun("r1", pgRelaxed)
	f.prov.records = []store.GovernanceAuthorization{approved(m, "")}
	f.sync()
	if len(f.st.bpRules) == 0 || f.refusals() != 1 {
		t.Fatalf("an approved merge without a base installed %d rows (refusals %d), want the rules kept",
			len(f.st.bpRules), f.refusals())
	}
}

// flakyMergeBaseGit fails MergeBase for one commit with a retryable git
// error, n times.
type flakyMergeBaseGit struct {
	*git.CLIClient
	commit string
	n      int
}

func (f *flakyMergeBaseGit) MergeBase(ctx context.Context, a, b string) (string, error) {
	if (a == f.commit || b == f.commit) && f.n > 0 {
		f.n--
		return "", &git.GitError{Kind: git.ErrKindTransient, Op: "merge-base", Message: "repository locked"}
	}
	return f.CLIClient.MergeBase(ctx, a, b)
}

// TestProtectionRemovalGuard_RecordVerificationErrors (codex review round
// 1): a transient git failure while checking a record holds the sync
// (error, retried) instead of refusing — a refusal would advance past the
// authorised relaxation and never look at it again — while a record whose
// commit is not in this repository (an override recorded at pre-receive
// for a push that never landed) is a plain mismatch.
func TestProtectionRemovalGuard_RecordVerificationErrors(t *testing.T) {
	t.Run("transient: held and retried", func(t *testing.T) {
		f := newPGFix(t)
		o := f.relax("operator override")
		flaky := &flakyMergeBaseGit{CLIClient: f.r.git, commit: o, n: 1}
		f.svc = NewService(flaky, f.st, f.router, 0)
		if err := f.svc.WithAuthoritativeBranch("main"); err != nil {
			t.Fatal(err)
		}
		f.svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true, Provenance: f.prov, Events: f.router})
		f.prov.records = []store.GovernanceAuthorization{override(o, f.c0)}
		_ = f.svc.IncrementalSync(context.Background())
		f.requireProtected("transient verification failure")
		if st := f.st.syncState(); st.Status != "error" || st.LastSyncedCommit != f.c0 {
			t.Fatalf("state = %+v, want error held at c0", st)
		}
		if f.refusals() != 0 {
			t.Fatal("a transient verification failure was reported as a refusal")
		}
		f.sync()
		f.requireInstalledRelaxed("after the git failure cleared", o)
	})
	t.Run("transient on an earlier link of the chain: held and retried", func(t *testing.T) {
		// Review round 2: the newest record matches the candidate, but the
		// earlier authorised act it chains to cannot be checked this tick.
		// A refusal would advance past both acts for good.
		f := newPGFix(t)
		pre1, m1 := f.mergeRun("r1", pgNoDirectWriteOnly)
		pre2, m2 := f.mergeRun("r2", pgRelaxed)
		flaky := &flakyMergeBaseGit{CLIClient: f.r.git, commit: m1, n: 1}
		f.svc = NewService(flaky, f.st, f.router, 0)
		if err := f.svc.WithAuthoritativeBranch("main"); err != nil {
			t.Fatal(err)
		}
		f.svc.WithProtectionRemovalGuard(ProtectionRemovalGuard{Enabled: true, Provenance: f.prov, Events: f.router})
		f.prov.records = []store.GovernanceAuthorization{approved(m2, pre2), approved(m1, pre1)}
		_ = f.svc.IncrementalSync(context.Background())
		f.requireProtected("transient failure on the earlier link")
		if st := f.st.syncState(); st.Status != "error" || st.LastSyncedCommit != f.c0 {
			t.Fatalf("state = %+v, want error held at c0", st)
		}
		if f.refusals() != 0 {
			t.Fatal("a transient failure on a chain link was reported as a refusal")
		}
		f.sync()
		f.requireInstalledRelaxed("after the git failure cleared", m2)
	})
	t.Run("record commit not in the repository: mismatch", func(t *testing.T) {
		f := newPGFix(t)
		f.relax("no landed operator push")
		f.prov.records = []store.GovernanceAuthorization{override("0123456789abcdef0123456789abcdef01234567", f.c0)}
		f.sync()
		f.requireProtected("missing record commit")
		if st := f.st.syncState(); st.Status != "idle" || f.refusals() != 1 {
			t.Fatalf("state %+v refusals %d, want idle and one refusal", st, f.refusals())
		}
	})
}

// TestProtectionRemovalGuard_RebuildFailureKeepsTheNewnessAnchor (codex
// review round 3): with the bootstrap rows installed, the newness anchor is
// the commit the sync started from. A FullRebuild whose branch-protection
// step failed on infrastructure (here the provenance read) used to record
// the head as last_synced_commit, so the retry anchored at the head itself
// and refused the operator's authorisation as a replay. The rebuild now
// keeps the previous commit when that step fails, and the retry installs
// it.
func TestProtectionRemovalGuard_RebuildFailureKeepsTheNewnessAnchor(t *testing.T) {
	f := newPGFix(t)
	f.r.remove(BranchProtectionConfigPath)
	s := f.r.commit("config deleted")
	f.st.bpRules = bootstrapRows()
	f.st.bpCommit = "bootstrap"
	f.st.setState(s, "idle")
	o := f.relax("operator override re-adds a relaxed config")
	f.prov.records = []store.GovernanceAuthorization{override(o, s)}
	f.prov.err = errors.New("database down")
	if err := f.svc.FullRebuild(context.Background()); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}
	if st := f.st.syncState(); st.Status != "error" || st.LastSyncedCommit != s {
		t.Fatalf("after a failed rebuild state = %+v, want error held at the previous commit %s", st, s)
	}
	f.requireProtected("rebuild with a provenance outage")
	f.prov.err = nil
	f.sync()
	f.requireInstalledRelaxed("retry after the outage", o)
}

func pendingApproval(approved, mergeSHA string) store.GovernanceAuthorization {
	return store.GovernanceAuthorization{Source: store.GovernanceSourcePendingApproval, CommitSHA: approved,
		MergeSHA: mergeSHA, ActorID: "op-1", RunID: "run-x"}
}

// TestProtectionRemovalGuard_ApprovedMergeInFlightHoldsTheSync (review
// round 1, finding F4 and codex): the engine moves the local main to an
// approved merge before it records the consumption marker. A sync inside
// that window used to refuse the approved relaxation and advance past it;
// with bootstrap rows installed even a full rebuild then took the late
// record for a replay. The sync now holds (error, the rules untouched, no
// refusal event) while the run is committing with its approved commit on
// the head and no reachable marker, and installs once the marker exists.
// A pending approval never vouches for anything itself. Mutation:
// dropping the in-flight check refuses and advances.
func TestProtectionRemovalGuard_ApprovedMergeInFlightHoldsTheSync(t *testing.T) {
	approvedMerge := func(f *pgFix) (bound, pre, m string) {
		f.r.run("checkout", "-q", "-b", "spine/run/approved")
		f.r.write(BranchProtectionConfigPath, pgRelaxed)
		bound = f.r.commit("approved run work")
		f.r.run("checkout", "-q", "main")
		pre = f.r.run("rev-parse", "HEAD")
		f.r.run("merge", "-q", "--no-ff", "-m", "Merge run approved", "spine/run/approved")
		return bound, pre, f.r.run("rev-parse", "HEAD")
	}
	for _, bootstrap := range []bool{false, true} {
		name := "protected rows installed"
		if bootstrap {
			name = "bootstrap rows installed"
		}
		t.Run(name, func(t *testing.T) {
			f := newPGFix(t)
			anchor := f.c0
			if bootstrap {
				f.r.remove(BranchProtectionConfigPath)
				anchor = f.r.commit("config deleted")
				f.st.bpRules = bootstrapRows()
				f.st.bpCommit = "bootstrap"
				f.st.setState(anchor, "idle")
			}
			bound, pre, m := approvedMerge(f)

			// Inside the window: approval recorded, marker not yet.
			f.prov.records = []store.GovernanceAuthorization{pendingApproval(bound, "")}
			_ = f.svc.IncrementalSync(context.Background())
			if st := f.st.syncState(); st.Status != "error" || st.LastSyncedCommit != anchor {
				t.Fatalf("in the window: state %+v, want the sync held at %s", st, anchor)
			}
			f.requireProtected("approved merge in flight")
			if f.refusals() != 0 {
				t.Fatal("an approved merge in flight was refused")
			}
			// A stale marker from an earlier attempt does not end the window.
			f.prov.records = []store.GovernanceAuthorization{pendingApproval(bound, strings.Repeat("e", 40))}
			_ = f.svc.IncrementalSync(context.Background())
			if st := f.st.syncState(); st.Status != "error" || st.LastSyncedCommit != anchor {
				t.Fatalf("stale marker: state %+v, want the sync still held", st)
			}

			// The marker lands: the next sync installs the approved change.
			f.prov.records = []store.GovernanceAuthorization{approved(m, pre), pendingApproval(bound, m)}
			f.sync()
			f.requireInstalledRelaxed("after the marker", m)
		})
	}
	t.Run("a pending approval vouches for nothing", func(t *testing.T) {
		f := newPGFix(t)
		bound, _, _ := approvedMerge(f)
		// The marker is in place (reachable) but no approved-merge record
		// matches: the pending entry must not authorise the removal.
		f.prov.records = []store.GovernanceAuthorization{pendingApproval(bound, f.r.run("rev-parse", "HEAD"))}
		f.sync()
		f.requireProtected("pending approval alone")
		if f.refusals() != 1 {
			t.Fatalf("refusals = %d, want 1", f.refusals())
		}
	})
	t.Run("an approval whose commit is not on the head does not hold", func(t *testing.T) {
		f := newPGFix(t)
		f.r.run("checkout", "-q", "-b", "spine/run/other")
		other := f.r.commit("another run's approved commit")
		f.r.run("checkout", "-q", "main")
		f.relax("unprovenanced relaxation")
		f.prov.records = []store.GovernanceAuthorization{pendingApproval(other, "")}
		f.sync()
		f.requireProtected("unrelated pending approval")
		if f.refusals() != 1 {
			t.Fatalf("refusals = %d, want the unprovenanced relaxation refused", f.refusals())
		}
	})
}
