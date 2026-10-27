package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
)

// TestLoadCheckoutLimits: `spine serve` turns the checkout budget and the
// mutate deadline on by default (INIT-024 TASK-003, owner question 3);
// each variable can set another positive limit, and nothing — zero, a
// negative value, a unit, a typo — turns either off or silently falls
// back to the default.
func TestLoadCheckoutLimits(t *testing.T) {
	vars := []string{"SPINE_GIT_TREE_MAX_ENTRIES", "SPINE_GIT_TREE_MAX_BYTES", "SPINE_GIT_TREE_MAX_PATH_BYTES", EnvGitMutateTimeout}
	unsetAll := func() {
		for _, v := range vars {
			t.Setenv(v, "")
		}
	}

	unsetAll()
	l, err := loadCheckoutLimits()
	if err != nil {
		t.Fatal(err)
	}
	if l.Budget == nil || *l.Budget != git.DefaultTreeBudget() || l.MutateTimeout != git.DefaultMutateTimeout {
		t.Fatalf("defaults = %+v %+v", l, l.Budget)
	}

	t.Setenv("SPINE_GIT_TREE_MAX_ENTRIES", "500000")
	t.Setenv("SPINE_GIT_TREE_MAX_BYTES", " 8589934592 ")
	t.Setenv("SPINE_GIT_TREE_MAX_PATH_BYTES", "1024")
	t.Setenv(EnvGitMutateTimeout, "10m")
	l, err = loadCheckoutLimits()
	if err != nil {
		t.Fatal(err)
	}
	want := git.TreeBudget{MaxEntries: 500000, MaxTotalBytes: 8 << 30, MaxPathBytesTotal: 1024}
	if *l.Budget != want || l.MutateTimeout != 10*time.Minute {
		t.Fatalf("set = %+v %+v", l, l.Budget)
	}

	for _, bad := range []struct{ name, value string }{
		{"SPINE_GIT_TREE_MAX_ENTRIES", "0"},
		{"SPINE_GIT_TREE_MAX_ENTRIES", "-1"},
		{"SPINE_GIT_TREE_MAX_BYTES", "2GiB"},
		{"SPINE_GIT_TREE_MAX_PATH_BYTES", "off"},
		{EnvGitMutateTimeout, "0"},
		{EnvGitMutateTimeout, "-5s"},
		{EnvGitMutateTimeout, "120"},
	} {
		unsetAll()
		t.Setenv(bad.name, bad.value)
		if _, err := loadCheckoutLimits(); err == nil || !strings.Contains(err.Error(), bad.name) {
			t.Errorf("%s=%q: err = %v, want a refusal naming the variable", bad.name, bad.value, err)
		}
	}
}

// TestInstallCheckoutLimits_ReachesEveryClient: the limits `spine serve`
// loads become the process default — every git client, EnterBranch and
// the git HTTP endpoint's pre-receive hook budget (processTreeBudget).
func TestInstallCheckoutLimits_ReachesEveryClient(t *testing.T) {
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	t.Setenv("SPINE_GIT_TREE_MAX_ENTRIES", "4321")
	t.Setenv("SPINE_GIT_TREE_MAX_BYTES", "")
	t.Setenv("SPINE_GIT_TREE_MAX_PATH_BYTES", "")
	t.Setenv(EnvGitMutateTimeout, "")
	if err := installCheckoutLimits(slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	c := git.NewCLIClient(t.TempDir())
	if l := c.CheckoutLimits(); l.Budget == nil || l.Budget.MaxEntries != 4321 || l.MutateTimeout != git.DefaultMutateTimeout {
		t.Fatalf("client limits = %+v", l)
	}
	if b := processTreeBudget(); b.MaxEntries != 4321 || b.MaxTotalBytes != git.DefaultTreeMaxTotalBytes {
		t.Fatalf("hook budget = %+v", b)
	}
}
