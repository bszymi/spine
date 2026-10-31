package main

import (
	"bytes"
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
	vars := []string{"SPINE_GIT_TREE_MAX_ENTRIES", "SPINE_GIT_TREE_MAX_BYTES", "SPINE_GIT_TREE_MAX_PATH_BYTES", EnvGitMutateTimeout, EnvGitCheckoutInflightMaxBytes}
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

// TestLoadCheckoutLimits_InflightCeiling (INIT-024 TASK-011): the
// in-flight checkout ceiling is on by default at the effective per-tree
// byte limit (so it follows SPINE_GIT_TREE_MAX_BYTES), a positive integer
// of bytes replaces it, and anything else refuses to start naming the
// variable. Installed, it is the process default EnterBranch applies; a
// ceiling below the per-tree limit is accepted with a startup warning.
func TestLoadCheckoutLimits_InflightCeiling(t *testing.T) {
	vars := []string{"SPINE_GIT_TREE_MAX_ENTRIES", "SPINE_GIT_TREE_MAX_BYTES", "SPINE_GIT_TREE_MAX_PATH_BYTES", EnvGitMutateTimeout, EnvGitCheckoutInflightMaxBytes}
	unsetAll := func() {
		for _, v := range vars {
			t.Setenv(v, "")
		}
	}
	load := func() git.CheckoutLimits {
		t.Helper()
		l, err := loadCheckoutLimits()
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	unsetAll()
	if l := load(); l.InflightMaxBytes != git.DefaultTreeMaxTotalBytes {
		t.Errorf("default ceiling = %d, want the per-tree byte limit %d", l.InflightMaxBytes, int64(git.DefaultTreeMaxTotalBytes))
	}
	t.Setenv("SPINE_GIT_TREE_MAX_BYTES", "8589934592")
	if l := load(); l.InflightMaxBytes != 8<<30 {
		t.Errorf("ceiling with a raised tree limit = %d, want it to follow (%d)", l.InflightMaxBytes, int64(8<<30))
	}
	t.Setenv(EnvGitCheckoutInflightMaxBytes, " 1073741824 ")
	if l := load(); l.InflightMaxBytes != 1<<30 || l.Budget.MaxTotalBytes != 8<<30 {
		t.Errorf("explicit ceiling = %d (tree %d), want %d", l.InflightMaxBytes, l.Budget.MaxTotalBytes, int64(1<<30))
	}
	for _, bad := range []string{"0", "-1", "2GiB", "off", "1e9"} {
		unsetAll()
		t.Setenv(EnvGitCheckoutInflightMaxBytes, bad)
		if _, err := loadCheckoutLimits(); err == nil || !strings.Contains(err.Error(), EnvGitCheckoutInflightMaxBytes) {
			t.Errorf("%s=%q: err = %v, want a refusal naming the variable", EnvGitCheckoutInflightMaxBytes, bad, err)
		}
	}

	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	for _, tc := range []struct {
		ceiling  string
		want     int64
		warnings int
	}{
		{"", git.DefaultTreeMaxTotalBytes, 0},
		{"4294967296", 4 << 30, 0},
		{"1048576", 1 << 20, 1},
	} {
		unsetAll()
		t.Setenv(EnvGitCheckoutInflightMaxBytes, tc.ceiling)
		var logs bytes.Buffer
		if err := installCheckoutLimits(slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
			t.Fatal(err)
		}
		if got := git.DefaultCheckoutLimits().InflightMaxBytes; got != tc.want {
			t.Errorf("ceiling %q: process default %d, want %d", tc.ceiling, got, tc.want)
		}
		if !strings.Contains(logs.String(), "inflight_max_bytes=") {
			t.Errorf("ceiling %q: the startup line does not name the ceiling:\n%s", tc.ceiling, logs.String())
		}
		if n := strings.Count(logs.String(), "level=WARN"); n != tc.warnings {
			t.Errorf("ceiling %q: %d warnings, want %d:\n%s", tc.ceiling, n, tc.warnings, logs.String())
		}
	}
}
