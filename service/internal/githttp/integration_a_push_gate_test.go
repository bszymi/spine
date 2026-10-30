package githttp

// INIT-024 integration A, phase 5: TASK-005's push gate (storage probe,
// git slots, command-read deadline, apply-time admission re-check) over
// TASK-003's phase-4 neutral-attributes step (EnsureNeutralWorkTree in
// installBudgetHook: every check that can fail runs before the line; past
// it only an I/O error or a crash leaves the renormalisation pending).
//
// The order on a push POST is: storage probe and receive-pack alignment;
// admission (command section under its deadline); a git slot; the budget
// hook's install with the neutral line; the re-check's channel; the CGI,
// whose one pre-receive hook process measures the pushed trees and then
// asks for the re-check.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// convertedServedTree is servedConvertedTree (a work tree checked out with
// conversions, no neutral line) with the run branch the push-gate fixture
// pushes.
func convertedServedTree(t *testing.T) string {
	t.Helper()
	repo := servedConvertedTree(t)
	nulGit(t, repo, nil, "branch", pgrRun)
	return repo
}

func iaNoLineNoMarkerNoHook(t *testing.T, repo string) {
	t.Helper()
	if has, err := git.HasNeutralCheckoutAttributes(context.Background(), repo); err != nil || has {
		t.Errorf("neutral line: %v (%v); want none", has, err)
	}
	for _, p := range []string{filepath.Join(repo, ".git", "spine-renormalise-pending"), filepath.Join(repo, ".git", "hooks", "pre-receive")} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: %v; want none", p, err)
		}
	}
}

// TestRecheck_NoLineRefusesThePushBeforeTheRecheck (coordination a): a
// served tree whose HEAD is over the budget before the line is added gets
// no line (TASK-003 phase 4), so the push is refused at the hook's install
// — "push gate unavailable", before the re-check is armed and before git
// sees the pack — whether or not its admission would have changed. The
// refusal does not depend on the re-check, and nothing is written.
func TestRecheck_NoLineRefusesThePushBeforeTheRecheck(t *testing.T) {
	for _, review := range []bool{false, true} {
		repo := convertedServedTree(t)
		f := pgrRunSetup(t, repo)
		git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{MaxEntries: 1}})
		resp := pgrDemux(pgrHeldPush(t, f.h, f.repo, pgrAlice, pgrCommand(f.base, f.evil, "refs/heads/"+pgrRun), f.pack,
			func() bool { return f.defs.lookups() >= 1 },
			func() {
				if review {
					f.defs.set(domain.StepTypeReview)
				}
			}))
		git.SetDefaultCheckoutLimits(git.CheckoutLimits{})
		if !strings.Contains(resp, "push gate unavailable") {
			t.Errorf("review=%v: want the push refused at the hook's install, got:\n%q", review, resp)
		}
		if tip := f.runTip(t); tip != f.base {
			t.Errorf("review=%v: the run branch moved to %s", review, tip)
		}
		iaNoLineNoMarkerNoHook(t, repo)
		if n := pgrSlotsHeld(f.h); n != 0 {
			t.Errorf("review=%v: %d slots still held", review, n)
		}
	}
}

// TestStorageProbe_RefusedRepositoryNeverGetsTheLine (coordination b): the
// case-sensitivity probe runs before the budget hook's install, so a
// repository whose storage folds ref names, or whose probe fails, is
// refused — the advertisement and the POST — without ever being given the
// neutral line, a renormalise marker or the hook.
func TestStorageProbe_RefusedRepositoryNeverGetsTheLine(t *testing.T) {
	for _, tc := range []struct {
		name  string
		probe func(context.Context, string) (bool, error)
		code  int
	}{
		{"storage folds", func(context.Context, string) (bool, error) { return true, nil }, http.StatusForbidden},
		{"probe fails", func(context.Context, string) (bool, error) { return false, errors.New("probe failed") }, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := convertedServedTree(t)
			f := pgrRunSetup(t, repo)
			pgrSetStorageProbe(f.h, tc.probe)
			adv := pgrServe(f.h, http.MethodGet, "/info/refs?service=git-receive-pack", "", nil, repo, "ws-1", pgrAlice, "")
			if adv.Code != tc.code {
				t.Errorf("push advertisement: %d %q; want %d", adv.Code, adv.Body.String(), tc.code)
			}
			body := strings.NewReader(string(pgrCommand(f.base, f.evil, "refs/heads/"+pgrRun)) + string(f.pack))
			post := pgrServe(f.h, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request", body, repo, "ws-1", pgrAlice, "")
			if post.Code != tc.code {
				t.Errorf("push: %d %q; want %d", post.Code, post.Body.String(), tc.code)
			}
			if tip := f.runTip(t); tip != f.base {
				t.Errorf("the run branch moved to %s", tip)
			}
			iaNoLineNoMarkerNoHook(t, repo)
		})
	}
}

// TestPushSlot_TreeWaitIsNotCutByTheCommandDeadline (coordination c): a
// push that adds the line waits for the work tree's hold while it holds
// its git slot (the hook's install runs after admission). The command-read
// deadline bounds only the command section, read before the slot, so it
// does not cut that wait; a read of another principal is served meanwhile
// (the slot, the tree hold and the alignment lock are never taken in the
// opposite order); and once the holder lets go the push lands and its slot
// is released.
func TestPushSlot_TreeWaitIsNotCutByTheCommandDeadline(t *testing.T) {
	repo := convertedServedTree(t)
	f := pgrRunSetup(t, repo)
	const commandRead = 200 * time.Millisecond
	f.h = pgrHandler(t, Config{
		ResolveRepoPath:    func(context.Context, string) (string, error) { return repo, nil },
		ReceivePackEnabled: true,
		Policy:             f.h.policy,
		RunRefs:            f.runs,
		StepDefs:           f.defs,
		OpTimeout:          30 * time.Second,
	}, 0, 0, 0, commandRead)

	held, release, holderDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		holderDone <- git.NewCLIClient(repo).WithWorkingTree(context.Background(), func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	released := false
	defer func() {
		if !released {
			close(release)
			<-holderDone
		}
	}()

	pushed := make(chan *httpResult, 1)
	go func() {
		body := strings.NewReader(string(pgrCommand(f.base, f.evil, "refs/heads/"+pgrRun)) + string(f.pack))
		w := pgrServe(f.h, http.MethodPost, "/git-receive-pack", "application/x-git-receive-pack-request", body, repo, "ws-1", pgrAlice, "")
		pushed <- &httpResult{code: w.Code, body: w.Body.String()}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for pgrSlotsHeld(f.h) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the push never took its slot (slots %d)", pgrSlotsHeld(f.h))
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(3 * commandRead)
	select {
	case r := <-pushed:
		t.Fatalf("the push ended while the tree was held: %d %q", r.code, r.body)
	default:
	}
	bob := &domain.Actor{ActorID: "bob", Role: domain.RoleContributor, Status: domain.ActorStatusActive}
	if w := pgrServe(f.h, http.MethodGet, "/info/refs?service=git-upload-pack", "", nil, repo, "ws-1", bob, ""); w.Code != http.StatusOK {
		t.Fatalf("a read by another principal while the push waits: %d %q", w.Code, w.Body.String())
	}

	close(release)
	released = true
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-pushed:
		if r.code != http.StatusOK || strings.Contains(pgrDemux(r.body), "push refused") {
			t.Fatalf("the push after the wait: %d %q", r.code, pgrDemux(r.body))
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the push did not answer after the tree was released")
	}
	if tip := f.runTip(t); tip != f.evil {
		t.Errorf("the run branch is at %s; want the pushed %s", tip, f.evil)
	}
	if n := pgrSlotsHeld(f.h); n != 0 {
		t.Errorf("%d slots still held", n)
	}
	if has, _ := git.HasNeutralCheckoutAttributes(context.Background(), repo); !has {
		t.Error("the push did not add the neutral line")
	}
}

type httpResult struct {
	code int
	body string
}
