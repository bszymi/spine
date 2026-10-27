package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/service/internal/githttp"
)

// TestLoadGitHTTPLimits (INIT-024 TASK-005): unset or blank means the
// default, a value in range replaces it, and anything else refuses to
// start, naming the variable.
func TestLoadGitHTTPLimits(t *testing.T) {
	vars := []string{EnvGitHTTPMaxConcurrent, EnvGitHTTPMaxConcurrentPerWorkspace, EnvGitHTTPMaxConcurrentPerActor, githttp.EnvCommandReadTimeout}
	unsetAll := func() {
		for _, v := range vars {
			t.Setenv(v, "")
		}
	}

	unsetAll()
	l, err := loadGitHTTPLimits()
	if err != nil {
		t.Fatal(err)
	}
	if l != (gitHTTPLimits{}) {
		t.Fatalf("unset = %+v, want the zero value (githttp's defaults)", l)
	}
	if e := l.effective(); e != (gitHTTPLimits{MaxConcurrent: 8, MaxConcurrentPerWorkspace: 4, MaxConcurrentPerActor: 2, CommandReadTimeout: 10 * time.Second}) {
		t.Fatalf("effective defaults = %+v, want 8/4/2/10s (owner question 1)", e)
	}

	t.Setenv(EnvGitHTTPMaxConcurrent, "16")
	t.Setenv(EnvGitHTTPMaxConcurrentPerWorkspace, " 8 ")
	t.Setenv(EnvGitHTTPMaxConcurrentPerActor, "3")
	t.Setenv(githttp.EnvCommandReadTimeout, "5s")
	if l, err = loadGitHTTPLimits(); err != nil {
		t.Fatal(err)
	}
	if l != (gitHTTPLimits{MaxConcurrent: 16, MaxConcurrentPerWorkspace: 8, MaxConcurrentPerActor: 3, CommandReadTimeout: 5 * time.Second}) {
		t.Fatalf("set = %+v", l)
	}

	for _, bad := range []struct{ name, value string }{
		{EnvGitHTTPMaxConcurrent, "0"},
		{EnvGitHTTPMaxConcurrent, "-1"},
		{EnvGitHTTPMaxConcurrent, "1025"},
		{EnvGitHTTPMaxConcurrentPerWorkspace, "four"},
		{EnvGitHTTPMaxConcurrentPerActor, "2.5"},
		{githttp.EnvCommandReadTimeout, "0"},
		{githttp.EnvCommandReadTimeout, "-1s"},
		{githttp.EnvCommandReadTimeout, "10"},
		{githttp.EnvCommandReadTimeout, "off"},
	} {
		unsetAll()
		t.Setenv(bad.name, bad.value)
		if _, err := loadGitHTTPLimits(); err == nil || !strings.Contains(err.Error(), bad.name) {
			t.Errorf("%s=%q: err = %v, want a refusal naming the variable", bad.name, bad.value, err)
		}
	}
}

// TestLogGitHTTPLimits: the effective caps are logged; a cap above the
// one that contains it warns that it has no effect.
func TestLogGitHTTPLimits(t *testing.T) {
	capture := func(l gitHTTPLimits) string {
		var buf bytes.Buffer
		logGitHTTPLimits(slog.New(slog.NewTextHandler(&buf, nil)), l)
		return buf.String()
	}
	out := capture(gitHTTPLimits{})
	if !strings.Contains(out, "max_concurrent=8 max_concurrent_per_workspace=4 max_concurrent_per_actor=2 command_read_timeout=10s") || strings.Contains(out, "WARN") {
		t.Fatalf("defaults:\n%s", out)
	}
	if out := capture(gitHTTPLimits{MaxConcurrent: 2}); !strings.Contains(out, "level=WARN") || !strings.Contains(out, EnvGitHTTPMaxConcurrentPerWorkspace+" is above") {
		t.Errorf("per-workspace above the ceiling:\n%s", out)
	}
	if out := capture(gitHTTPLimits{MaxConcurrentPerActor: 6}); !strings.Contains(out, EnvGitHTTPMaxConcurrentPerActor+" is above") {
		t.Errorf("per-actor above per-workspace:\n%s", out)
	}
}
