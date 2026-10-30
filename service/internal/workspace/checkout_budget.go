package workspace

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/service/internal/githttp"
)

// checkoutBudgetWarnTimeout bounds the start-of-life measure.
const checkoutBudgetWarnTimeout = 60 * time.Second

// WarnIfOverCheckoutBudget measures the workspace tree's HEAD — the home
// branch, on a primary client — once, and logs a Warn naming the limit,
// its variable and the remedy when it is already over the client's
// checkout budget (INIT-024 TASK-003, review round 1; security P3-3). That
// happens on an upgrade to a repository already over the defaults, or when
// an operator lowers a SPINE_GIT_TREE_* limit below an existing tree: from
// then on every restore, checkout, worktree and merge of the tree that
// writes it is refused (fail closed) — only a merge whose result fits is
// admitted — and, when the neutral checkout attributes are not in place
// yet, they are not added while HEAD is over (so pushes are refused too).
// Without this line the first sign is a failed run.
//
// It measures what HEAD stores (git.MeasureStoredTree), which needs no
// neutral line (INIT-024 integration A: before, it measured through the
// line, so on the upgrade — the case it is for — it could only log at
// Debug that it had not measured).
//
// It is meant to run once per tree at `spine serve` start and when a
// shared-mode workspace's services are built, never per request, and in
// its own goroutine: it reads, never writes, and a failure to measure is
// logged at Debug (an empty repository has no HEAD tree). A client with
// no budget measures nothing.
// ctx should not be the request's own (pass context.WithoutCancel): the
// measure is bounded by checkoutBudgetWarnTimeout.
func WarnIfOverCheckoutBudget(ctx context.Context, c *git.CLIClient, workspaceID string, log *slog.Logger) {
	if c == nil || c.CheckoutLimits().Budget == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, checkoutBudgetWarnTimeout)
	defer cancel()
	_, err := git.MeasureStoredTree(ctx, c, "HEAD")
	var tl *git.TreeTooLargeError
	switch {
	case errors.As(err, &tl):
		log.Warn("workspace tree is over the git checkout budget: every checkout, merge, worktree and restore that writes it will be refused "+
			"until the tree is reduced (a merge whose result fits the budget is admitted) or the limit is raised; "+
			"neutral checkout attributes not yet in place are not added while it is over, so its pushes are refused too",
			append([]any{"workspace_id", workspaceID, "detail", tl.Error()}, githttp.TreeLimitLogAttrs(err)...)...)
	case err != nil:
		log.Debug("git checkout budget: workspace tree not measured at start", "workspace_id", workspaceID, "error", err)
	}
}
