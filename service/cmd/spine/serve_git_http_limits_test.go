package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
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

// TestGitHTTPLimits_EffectiveCapsByMode (review round 1, P3-2 ruling):
// shared mode defaults to 2 per actor, 4 per workspace, 8 in all (owner
// question 1); single mode defaults the per-workspace and per-actor caps
// to the process-wide one, so its capacity is 8 (never below the old 5)
// and follows SPINE_GIT_HTTP_MAX_CONCURRENT; explicit values win in both.
func TestGitHTTPLimits_EffectiveCapsByMode(t *testing.T) {
	ten := 10 * time.Second
	for _, tc := range []struct {
		name   string
		in     gitHTTPLimits
		shared bool
		want   gitHTTPLimits
	}{
		{"shared defaults", gitHTTPLimits{}, true, gitHTTPLimits{8, 4, 2, ten}},
		{"single defaults", gitHTTPLimits{}, false, gitHTTPLimits{8, 8, 8, ten}},
		{"single follows the ceiling", gitHTTPLimits{MaxConcurrent: 16}, false, gitHTTPLimits{16, 16, 16, ten}},
		{"single explicit per-actor", gitHTTPLimits{MaxConcurrentPerActor: 3}, false, gitHTTPLimits{8, 8, 3, ten}},
		{"single explicit per-workspace", gitHTTPLimits{MaxConcurrent: 16, MaxConcurrentPerWorkspace: 4}, false, gitHTTPLimits{16, 4, 16, ten}},
		{"shared ceiling only", gitHTTPLimits{MaxConcurrent: 16}, true, gitHTTPLimits{16, 4, 2, ten}},
		{"shared explicit", gitHTTPLimits{MaxConcurrent: 20, MaxConcurrentPerWorkspace: 6, MaxConcurrentPerActor: 3, CommandReadTimeout: time.Second}, true, gitHTTPLimits{20, 6, 3, time.Second}},
	} {
		if got := tc.in.effective(tc.shared); got != tc.want {
			t.Errorf("%s: effective = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestLogGitHTTPLimits: the effective caps are logged with the mode; a
// cap above the one that contains it warns that it has no effect, and in
// single mode a ceiling above an explicit per-workspace cap names the
// per-workspace cap as the binding one.
func TestLogGitHTTPLimits(t *testing.T) {
	capture := func(l gitHTTPLimits, shared bool) string {
		var buf bytes.Buffer
		logGitHTTPLimits(slog.New(slog.NewTextHandler(&buf, nil)), l, shared)
		return buf.String()
	}
	if out := capture(gitHTTPLimits{}, true); !strings.Contains(out, "mode=shared max_concurrent=8 max_concurrent_per_workspace=4 max_concurrent_per_actor=2 command_read_timeout=10s") || strings.Contains(out, "WARN") {
		t.Fatalf("shared defaults:\n%s", out)
	}
	if out := capture(gitHTTPLimits{}, false); !strings.Contains(out, "mode=single max_concurrent=8 max_concurrent_per_workspace=8 max_concurrent_per_actor=8") || strings.Contains(out, "WARN") {
		t.Fatalf("single defaults:\n%s", out)
	}
	if out := capture(gitHTTPLimits{MaxConcurrent: 2}, true); !strings.Contains(out, "level=WARN") || !strings.Contains(out, EnvGitHTTPMaxConcurrentPerWorkspace+" is above") {
		t.Errorf("per-workspace above the ceiling:\n%s", out)
	}
	if out := capture(gitHTTPLimits{MaxConcurrentPerActor: 6}, true); !strings.Contains(out, EnvGitHTTPMaxConcurrentPerActor+" is above") {
		t.Errorf("per-actor above per-workspace:\n%s", out)
	}
	out := capture(gitHTTPLimits{MaxConcurrent: 16, MaxConcurrentPerWorkspace: 4}, false)
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "single-workspace server") || !strings.Contains(out, EnvGitHTTPMaxConcurrentPerWorkspace+" is the binding cap") {
		t.Errorf("single mode, ceiling above an explicit per-workspace cap:\n%s", out)
	}
	if out := capture(gitHTTPLimits{MaxConcurrent: 16, MaxConcurrentPerWorkspace: 4}, true); strings.Contains(out, "single-workspace server") {
		t.Errorf("shared mode warned about single-mode capacity:\n%s", out)
	}
}

// TestBuildGitHTTPHandler_SlotDefaultsFollowTheMode: buildGitHTTPHandler
// hands githttp the defaults of the server's mode.
func TestBuildGitHTTPHandler_SlotDefaultsFollowTheMode(t *testing.T) {
	resolver := builderResolver{cfg: workspace.Config{ID: "ws", RepoPath: t.TempDir(), Status: workspace.StatusActive}}
	for _, tc := range []struct {
		shared bool
		want   string
	}{
		{false, "mode=single max_concurrent=8 max_concurrent_per_workspace=8 max_concurrent_per_actor=8"},
		{true, "mode=shared max_concurrent=8 max_concurrent_per_workspace=4 max_concurrent_per_actor=2"},
	} {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		if h := buildGitHTTPHandler(resolver, nil, true, branchprotect.NewPermissive(), nil, nil, tc.shared, gitHTTPLimits{}, log); h == nil {
			t.Fatal("git-http-backend unavailable")
		}
		if !strings.Contains(buf.String(), tc.want) {
			t.Errorf("shared=%v: want %q in\n%s", tc.shared, tc.want, buf.String())
		}
	}
}
