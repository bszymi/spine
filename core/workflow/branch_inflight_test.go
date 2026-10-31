package workflow_test

// INIT-024 TASK-011: the two new EnterBranch refusals through
// workflow.Service — a branch another write scope holds stays git_error
// (git's own refusal before the task); a wait under the in-flight checkout
// ceiling that ends first is unavailable (retryable).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/testutil"
)

func TestCreate_CheckoutRefusalsKeepTheirClassification(t *testing.T) {
	svc, repo := newSvc(t)
	t.Setenv("TMPDIR", t.TempDir())
	ctx := context.Background()
	testutil.WriteFile(t, repo, "notes/seed.md", string(make([]byte, 1024)))
	mustRun(t, repo, "git", "add", "notes/seed.md")
	mustRun(t, repo, "git", "commit", "-q", "-m", "seed")
	mustRun(t, repo, "git", "branch", "spine/run/held", "main")
	mustRun(t, repo, "git", "branch", "spine/run/other", "main")
	if err := git.EnsureNeutralCheckoutAttributes(ctx, repo); err != nil {
		t.Fatal(err)
	}
	st, err := git.MeasureTree(ctx, git.NewCLIClient(repo), "main")
	if err != nil {
		t.Fatal(err)
	}
	w := git.InflightWeight(st)
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{InflightMaxBytes: w + w/2})
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })

	held, err := git.EnterBranch(ctx, repo, "spine/run/held", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Cleanup()

	// The 10 s context only keeps a refusal that waits from hanging the
	// test; the refusal must come at once.
	hctx, hcancel := context.WithTimeout(workflow.WithWriteContext(ctxWithActor(), workflow.WriteContext{Branch: "spine/run/held"}), 10*time.Second)
	defer hcancel()
	_, err = svc.Create(hctx,
		"task-default", fmtYAML("task-default", "1.0"))
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrGit || !strings.Contains(se.Message, git.ErrBranchInUse.Error()) {
		t.Errorf("held branch: err = %v, want git_error naming the held branch", err)
	}

	wctx, cancel := context.WithTimeout(workflow.WithWriteContext(ctxWithActor(), workflow.WriteContext{Branch: "spine/run/other"}), 200*time.Millisecond)
	defer cancel()
	_, err = svc.Create(wctx, "task-default", fmtYAML("task-default", "1.0"))
	if !errors.As(err, &se) || se.Code != domain.ErrUnavailable || !strings.Contains(se.Message, git.CheckoutsInFlightMessage) {
		t.Errorf("ceiling full until the request ended: err = %v, want unavailable", err)
	}
}
