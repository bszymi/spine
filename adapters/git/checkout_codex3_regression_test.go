package git_test

// INIT-024 TASK-011 codex review round 3: regressions for the in-flight
// gate, using only API that existed at 5cfe16dd, where both fail.
//   - P1: a branch that moved while its entry waited for room was checked
//     out at its new tip, while the gate held the old tree's weight.
//   - P2: the round-robin moved every repository it visited to the back,
//     restoring the original order after each pass, so one repository's
//     backlog could take each freed slot before another repository
//     waiting for the same room.

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// TestEnterBranch_BranchMovedWhileWaitingIsReweighed (P1): an entry waits
// for room; meanwhile its branch moves (a push) from a small tree to one
// as heavy as the holder's. Once admitted it holds the NEW tree's weight —
// the tree it checks out — not the small one it measured first.
func TestEnterBranch_BranchMovedWhileWaitingIsReweighed(t *testing.T) {
	ctx := context.Background()
	holder, run := claimRepo(t)

	repo := tbNeutralRepo(t)
	testutil.WriteFile(t, repo, "README.md", "hello\n")
	tbGit(t, repo, nil, "add", "README.md")
	tbGit(t, repo, nil, "commit", "-q", "-m", "readme")
	small := tbGit(t, repo, nil, "rev-parse", "HEAD")
	big := tbBomb(t, repo, small, 40, 256<<10)
	tbGit(t, repo, nil, "branch", "big-ref", big)
	tbGit(t, repo, nil, "branch", "grow", small)

	wHolder := treeWeight(t, holder, run)
	wSmall := treeWeight(t, repo, "grow")
	wBig := treeWeight(t, repo, "big-ref")
	// The small tree does not fit beside the holder's; the big one fits
	// alone once the holder is gone.
	ceiling := wHolder + wSmall - 1
	if wBig > ceiling {
		t.Fatalf("fixture: big tree %d over the ceiling %d", wBig, ceiling)
	}
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, InflightMaxBytes: ceiling}
	tmp := isolateTempDir(t)

	h, err := git.EnterBranchWithLimits(ctx, holder, run, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		s   *git.WriteScope
		err error
	}
	got := make(chan result, 1)
	go func() {
		s, err := git.EnterBranchWithLimits(t.Context(), repo, "grow", nil, limits)
		got <- result{s, err}
	}()
	waitUntil(t, "the entry to wait for room", func() bool { return git.InflightWaitersForTest() == 1 })
	tbGit(t, repo, nil, "update-ref", "refs/heads/grow", big) // a push moves the branch
	h.Cleanup()

	var r result
	select {
	case r = <-got:
	case <-time.After(20 * time.Second):
		t.Fatal("the waiting entry was neither admitted nor refused")
	}
	if r.err != nil {
		t.Fatalf("entry after its branch moved: %v", r.err)
	}
	held := git.InflightHeldForTest()
	onDisk, _ := duBytes(tmp)
	if tip := tbGit(t, r.s.RepoDir, nil, "rev-parse", "HEAD"); tip != big {
		t.Errorf("checked out %s, want the moved tip %s", tip, big)
	}
	r.s.Cleanup()
	if held != wBig {
		t.Errorf("the gate holds %d for a scope that checked out the moved tip weighing %d (the first measure weighed %d; %d bytes on disk)",
			held, wBig, wSmall, onDisk)
	}
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("gate holds %d after Cleanup, want 0", h)
	}
	assertEmptyDir(t, tmp)
}

// TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne (P2): X holds
// 70 of 100; repository A has a backlog of 30s and repository B one 30,
// queued after A's. Each time a slot frees, the repository that has
// waited longest since it was last served gets it: after A is served once,
// B — not A's next — takes the following slot.
func TestInflightGate_ServedRepositoryGoesBehindTheWaitingOne(t *testing.T) {
	ctx := t.Context()
	const ceiling = 100
	relX, err := git.AdmitCheckoutForTest(ctx, "x", 70, ceiling)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relX)
	relA1, err := git.AdmitCheckoutForTest(ctx, "a", 30, ceiling)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relA1)

	type admitted struct {
		release func()
		err     error
	}
	admit := func(repo string) chan admitted {
		ch := make(chan admitted, 1)
		go func() {
			r, err := git.AdmitCheckoutForTest(ctx, repo, 30, ceiling)
			if r != nil {
				t.Cleanup(r)
			}
			ch <- admitted{r, err}
		}()
		return ch
	}
	a2 := admit("a")
	waitUntil(t, "a2", func() bool { return git.InflightWaitersForTest() == 1 })
	a3 := admit("a")
	waitUntil(t, "a3", func() bool { return git.InflightWaitersForTest() == 2 })
	b1 := admit("b")
	waitUntil(t, "b1", func() bool { return git.InflightWaitersForTest() == 3 })

	relA1() // a2 is served (a's turn came first)
	var r2 admitted
	select {
	case r2 = <-a2:
	case <-time.After(10 * time.Second):
		t.Fatal("a2 not admitted after a1's release")
	}
	if r2.err != nil {
		t.Fatal(r2.err)
	}

	r2.release() // the next slot: b1 has waited since before a was served
	select {
	case r := <-b1:
		if r.err != nil {
			t.Fatal(r.err)
		}
		r.release()
	case r := <-a3:
		if r.release != nil {
			r.release()
		}
		t.Fatal("a3 took the freed slot before b1: a's backlog starves b")
	case <-time.After(10 * time.Second):
		t.Fatal("neither waiter was admitted after a2's release")
	}
	relX()
	select {
	case r := <-a3:
		if r.err != nil {
			t.Fatal(r.err)
		}
		r.release()
	case <-time.After(10 * time.Second):
		t.Fatal("a3 not admitted once the room was free")
	}
}
