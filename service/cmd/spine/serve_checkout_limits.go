package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/service/internal/githttp"
)

// EnvGitMutateTimeout is the mutate deadline of `spine serve`'s git
// client (INIT-024 TASK-003).
const EnvGitMutateTimeout = "SPINE_GIT_MUTATE_TIMEOUT"

// EnvGitCheckoutInflightMaxBytes is the in-flight checkout ceiling of
// `spine serve` (INIT-024 TASK-011): the most measured bytes the trees of
// its concurrent branch-scoped writes may have on the temporary disk at
// once (git.CheckoutLimits.InflightMaxBytes).
const EnvGitCheckoutInflightMaxBytes = "SPINE_GIT_CHECKOUT_INFLIGHT_MAX_BYTES"

// loadCheckoutLimits reads the checkout budget, the mutate deadline and
// the in-flight checkout ceiling of `spine serve` (INIT-024 TASK-003,
// review finding N-1; TASK-011 for the ceiling). All are on by default in
// standalone (INIT-024 owner question 3): an unset or empty variable
// means its default (git.DefaultTreeMaxEntries,
// git.DefaultTreeMaxTotalBytes, git.DefaultTreeMaxPathBytesTotal,
// git.DefaultMutateTimeout; for the ceiling, the effective per-tree byte
// limit, so at most one maximal tree is in flight and smaller ones
// proceed together); a positive integer (bytes, for the byte limits) or a
// positive Go duration replaces it. Anything else — zero, a negative
// number, a unit suffix, a typo — refuses startup: there is no off
// switch, only other limits, and a typo must not silently mean the
// default.
func loadCheckoutLimits() (git.CheckoutLimits, error) {
	var b git.TreeBudget
	for _, v := range []struct {
		name string
		dst  *int64
	}{
		{githttp.EnvTreeMaxEntries, &b.MaxEntries},
		{githttp.EnvTreeMaxBytes, &b.MaxTotalBytes},
		{githttp.EnvTreeMaxPathBytes, &b.MaxPathBytesTotal},
	} {
		raw := strings.TrimSpace(os.Getenv(v.name))
		if raw == "" {
			continue
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return git.CheckoutLimits{}, fmt.Errorf("%s=%q: want a positive integer (the checkout budget has no off switch; set a larger limit instead)", v.name, raw)
		}
		*v.dst = n
	}
	b = b.Effective()
	d := git.DefaultMutateTimeout
	if raw := strings.TrimSpace(os.Getenv(EnvGitMutateTimeout)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return git.CheckoutLimits{}, fmt.Errorf("%s=%q: want a positive duration such as 2m (the deadline has no off switch; set a longer one instead)", EnvGitMutateTimeout, raw)
		}
		d = parsed
	}
	ceiling := b.MaxTotalBytes
	if raw := strings.TrimSpace(os.Getenv(EnvGitCheckoutInflightMaxBytes)); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return git.CheckoutLimits{}, fmt.Errorf("%s=%q: want a positive integer of bytes (the in-flight checkout ceiling has no off switch; set a larger one instead)", EnvGitCheckoutInflightMaxBytes, raw)
		}
		ceiling = n
	}
	return git.CheckoutLimits{Budget: &b, MutateTimeout: d, InflightMaxBytes: ceiling}, nil
}

// installCheckoutLimits loads the limits and makes them the process
// default of every git client `spine serve` builds — the primary
// workspace tree, shared-mode workspace trees, code-repository clones —
// and of EnterBranch (git.SetDefaultCheckoutLimits). The git HTTP
// endpoint's pre-receive hook uses the same budget
// (buildGitHTTPHandler). It must run before any git client is used.
func installCheckoutLimits(log *slog.Logger) error {
	l, err := loadCheckoutLimits()
	if err != nil {
		return err
	}
	// The budget measures a merge's result with `git merge-tree
	// --write-tree` (git >= 2.38); on an older git every merge would be
	// refused, so refuse to start instead.
	vctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := git.CheckBudgetGitVersion(vctx); err != nil {
		return fmt.Errorf("git checkout budget: %w", err)
	}
	git.SetDefaultCheckoutLimits(l)
	log.Info("git checkout budget on",
		"max_entries", l.Budget.MaxEntries,
		"max_total_bytes", l.Budget.MaxTotalBytes,
		"max_path_bytes", l.Budget.MaxPathBytesTotal,
		"mutate_timeout", l.MutateTimeout.String(),
		"inflight_max_bytes", l.InflightMaxBytes)
	if l.InflightMaxBytes < l.Budget.MaxTotalBytes {
		// Coherent but easy to miss: a branch-scoped write of a tree
		// between the two is refused, although the budget admits it.
		log.Warn("the in-flight checkout ceiling is below the per-tree byte limit: a branch-scoped write of a larger tree is refused",
			"inflight_max_bytes", l.InflightMaxBytes,
			"max_total_bytes", l.Budget.MaxTotalBytes,
			"ceiling_var", EnvGitCheckoutInflightMaxBytes,
			"tree_var", githttp.EnvTreeMaxBytes)
	}
	return nil
}
