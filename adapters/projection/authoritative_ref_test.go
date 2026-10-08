package projection

import (
	"context"
	"strings"
	"testing"
)

// INIT-023 TASK-011, audit P3-22: with an authoritative branch the
// projection reads refs/heads/<branch>, never the shared working tree's
// HEAD. These are the inverted TestPOC_ProjectionFollowsCheckedOutRunBranch
// (AC 15), its zero-value control (AC 16) and argument / unborn / shadow
// cases (AC 17).

const protectMain = "version: 1\nrules:\n  - branch: main\n    protections: [no-delete, no-direct-write]\n"

// runBranchFixture projects main (1 rule), then checks out a run branch
// whose commit relaxes protection to `rules: []`, leaving it checked out
// — the engine's applyCommitStatus window.
func runBranchFixture(t *testing.T, authoritative bool) (*scratchRepo, *mapStore, *Service, string) {
	t.Helper()
	repo := newScratchRepo(t)
	repo.write(BranchProtectionConfigPath, protectMain)
	mainSHA := repo.commit("main")

	st := newMapStore()
	svc := NewService(repo.git, st, nil, 0)
	if authoritative {
		if err := svc.WithAuthoritativeBranch("main"); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.bpRules) != 1 {
		t.Fatalf("main projected %d rules, want 1", len(st.bpRules))
	}

	repo.run("checkout", "-q", "-b", "spine/run/r1")
	repo.write(BranchProtectionConfigPath, "version: 1\nrules: []\n")
	repo.commit("relax")
	return repo, st, svc, mainSHA
}

func TestAuthoritativeBranch_RunBranchCheckoutIsIgnored(t *testing.T) {
	t.Run("IncrementalSync", func(t *testing.T) {
		repo, st, svc, mainSHA := runBranchFixture(t, true)
		if err := svc.IncrementalSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(st.bpRules) != 1 {
			t.Fatalf("sync during a run-branch checkout installed %d rules, want main's 1", len(st.bpRules))
		}
		if got := st.syncState().LastSyncedCommit; got != mainSHA || got != repo.run("rev-parse", "refs/heads/main") {
			t.Fatalf("last_synced_commit = %s, want refs/heads/main %s", got, mainSHA)
		}
	})
	t.Run("FullRebuild", func(t *testing.T) {
		_, st, svc, mainSHA := runBranchFixture(t, true)
		if err := svc.FullRebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(st.bpRules) != 1 || st.syncState().LastSyncedCommit != mainSHA {
			t.Fatalf("rebuild during a run-branch checkout: rules=%d last=%s", len(st.bpRules), st.syncState().LastSyncedCommit)
		}
	})
}

// AC 16: the zero value follows HEAD (SMP's bare-repository behaviour,
// where HEAD cannot be moved by a push). Proves the option — not the
// test setup — provides the protection above.
func TestAuthoritativeBranch_ZeroValueFollowsHead(t *testing.T) {
	repo, st, svc, _ := runBranchFixture(t, false)
	if svc.AuthoritativeBranch() != "" {
		t.Fatalf("zero value = %q", svc.AuthoritativeBranch())
	}
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.bpRules) != 0 {
		t.Fatalf("without the option the checked-out branch is projected; got %d rules", len(st.bpRules))
	}
	if st.syncState().LastSyncedCommit != repo.run("rev-parse", "HEAD") {
		t.Fatal("without the option last_synced_commit follows HEAD")
	}
}

func TestWithAuthoritativeBranch_ArgumentValidation(t *testing.T) {
	svc := NewService(nil, nil, nil, 0)
	for _, bad := range []string{" main", "main ", "-x", "refs/heads/main", "a..b", "ma\x00in", "ma\x01in", "ma\nin", "m:a"} {
		if err := svc.WithAuthoritativeBranch(bad); err == nil {
			t.Errorf("WithAuthoritativeBranch(%q) must fail", bad)
		}
	}
	if err := svc.WithAuthoritativeBranch("main"); err != nil || svc.AuthoritativeBranch() != "main" {
		t.Fatalf("main: %v / %q", err, svc.AuthoritativeBranch())
	}
	// "" restores the Head() default (and short-circuits before
	// refname.Validate, which refuses an empty name).
	if err := svc.WithAuthoritativeBranch(""); err != nil || svc.AuthoritativeBranch() != "" {
		t.Fatalf("empty: %v / %q", err, svc.AuthoritativeBranch())
	}
	if err := svc.ProbeAuthoritativeRef(context.Background()); err != nil {
		t.Fatalf("probe without a branch is a no-op, got %v", err)
	}
}

func TestAuthoritativeBranch_UnbornMainIsSyncError(t *testing.T) {
	repo := newScratchRepo(t) // init -b main, no commit on main
	repo.run("checkout", "-q", "-b", "spine/init")
	repo.write(BranchProtectionConfigPath, protectMain)
	initSHA := repo.commit("init-repo seed")

	st := newMapStore()
	st.setState(initSHA, "idle") // e.g. a store left by projecting spine/init
	svc := NewService(repo.git, st, nil, 0)
	if err := svc.WithAuthoritativeBranch("main"); err != nil {
		t.Fatal(err)
	}
	err := svc.IncrementalSync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refs/heads/main") {
		t.Fatalf("expected an error naming refs/heads/main, got %v", err)
	}
	if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != initSHA || !strings.Contains(got.ErrorDetail, "refs/heads/main") {
		t.Fatalf("sync state (error_detail must name the ref): %+v", got)
	}
	st.setState(initSHA, "idle")
	if err := svc.FullRebuild(context.Background()); err == nil || !strings.Contains(err.Error(), "refs/heads/main") {
		t.Fatalf("FullRebuild must refuse an unborn authoritative branch, got %v", err)
	}
	if got := st.syncState(); got.Status != "error" || got.LastSyncedCommit != initSHA || !strings.Contains(got.ErrorDetail, "refs/heads/main") {
		t.Fatalf("FullRebuild must record the failure, keeping the commit: %+v", got)
	}
}

// Review round 1 (finding D): the motivating case — `spine init-repo`
// then `spine serve` on an EMPTY projection store — reports
// sync_state = error naming the ref (before, the hand-off to FullRebuild
// returned without writing any state), and heals by itself once the
// spine/init branch is merged into main.
func TestAuthoritativeBranch_UnbornMainOnFreshStoreReportsError(t *testing.T) {
	repo := newScratchRepo(t) // init -b main, no commit on main
	repo.run("checkout", "-q", "-b", "spine/init")
	repo.write(BranchProtectionConfigPath, protectMain)
	initSHA := repo.commit("init-repo seed")

	st := newMapStore() // fresh: no sync_state row
	svc := NewService(repo.git, st, nil, 0)
	if err := svc.WithAuthoritativeBranch("main"); err != nil {
		t.Fatal(err)
	}
	for tick := 1; tick <= 2; tick++ {
		err := svc.IncrementalSync(context.Background())
		if err == nil || !strings.Contains(err.Error(), "refs/heads/main") {
			t.Fatalf("tick %d: expected an error naming refs/heads/main, got %v", tick, err)
		}
		got := st.syncState()
		if st.state == nil || got.Status != "error" || got.LastSyncedCommit != "" || !strings.Contains(got.ErrorDetail, "refs/heads/main") {
			t.Fatalf("tick %d: a fresh store must report error with the cause: %+v", tick, got)
		}
	}
	if len(st.bpRules) != 0 {
		t.Fatal("nothing may be projected from the unmerged spine/init branch")
	}

	// The spine/init PR merges: main exists, and the next tick rebuilds
	// from it (a store with no synced commit has no baseline to diff).
	repo.run("branch", "main", "spine/init")
	if err := svc.IncrementalSync(context.Background()); err != nil {
		t.Fatalf("after the merge: %v", err)
	}
	if got := st.syncState(); got.Status != "idle" || got.LastSyncedCommit != initSHA {
		t.Fatalf("after the merge: %+v", got)
	}
	if len(st.bpRules) != 1 {
		t.Fatalf("main's branch protection must be projected, got %d rules", len(st.bpRules))
	}
}

// AC 17 shadow-ref case: look-alike refs pointing at a relaxing commit
// cannot shadow refs/heads/main, and a tracked file named like the ref
// does not break resolution.
func TestAuthoritativeBranch_ShadowRefsDoNotWin(t *testing.T) {
	repo := newScratchRepo(t)
	repo.write(BranchProtectionConfigPath, protectMain)
	repo.write("refs/heads/main", "a tracked file, not a ref\n")
	repo.write("HEAD", "another tracked file\n")
	mainSHA := repo.commit("main")

	repo.run("checkout", "-q", "-b", "evil")
	repo.write(BranchProtectionConfigPath, "version: 1\nrules: []\n")
	evil := repo.commit("relax")
	repo.run("checkout", "-q", "main")
	for _, ref := range []string{"refs/tags/refs/heads/main", "refs/refs/heads/main", "refs/heads/refs/heads/main", "refs/remotes/refs/heads/main"} {
		repo.run("update-ref", ref, evil)
	}
	// Leave the relaxing branch checked out too.
	repo.run("checkout", "-q", "evil")

	st := newMapStore()
	svc := NewService(repo.git, st, nil, 0)
	if err := svc.WithAuthoritativeBranch("main"); err != nil {
		t.Fatal(err)
	}
	if err := svc.FullRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.bpRules) != 1 || st.syncState().LastSyncedCommit != mainSHA {
		t.Fatalf("shadow refs won: rules=%d last=%s main=%s", len(st.bpRules), st.syncState().LastSyncedCommit, mainSHA)
	}
}

func TestProbeAuthoritativeRef_WarnsOnceWhenMissing(t *testing.T) {
	logs := captureLogs(t)
	repo := newScratchRepo(t)
	repo.run("checkout", "-q", "-b", "master")
	repo.commit("c")
	svc := NewService(repo.git, newMapStore(), nil, 0)
	if err := svc.WithAuthoritativeBranch("main"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProbeAuthoritativeRef(context.Background()); err == nil {
		t.Fatal("expected the probe to report the missing branch")
	}
	if n := len(logs.lines("level=WARN", "authoritative branch does not resolve", "refs/heads/main")); n != 1 {
		t.Fatalf("expected exactly one Warn naming refs/heads/main, got %d:\n%s", n, logs.String())
	}
}

func TestIsHexSHA(t *testing.T) {
	for s, want := range map[string]bool{
		strings.Repeat("a", 40): true,
		strings.Repeat("0", 64): true,
		strings.Repeat("A", 40): false,
		strings.Repeat("a", 39): false,
		"refs/heads/main":       false,
		"":                      false,
	} {
		if got := isHexSHA(s); got != want {
			t.Errorf("isHexSHA(%q) = %v", s, got)
		}
	}
}
