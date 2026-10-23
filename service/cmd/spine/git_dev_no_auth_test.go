package main

// INIT-023 TASK-015 (audit P3-30): SPINE_GIT_HTTP_DEV_NO_AUTH is the only
// switch that lets /git reads skip the bearer, and spine serve refuses it
// unless SPINE_ENV=development (owner question 1 default) — before any
// store, secret or listener is opened (AC 10).

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/testutil"
)

func TestP330_GuardGitHTTPDevNoAuth(t *testing.T) {
	for _, tc := range []struct {
		env     string
		on      bool
		wantErr bool
	}{
		{"development", true, false},
		{"", true, true},
		{"staging", true, true},
		{"production", true, true},
		{"production", false, false},
		{"staging", false, false},
		{"", false, false},
	} {
		err := guardGitHTTPDevNoAuth(tc.env, tc.on)
		if (err != nil) != tc.wantErr {
			t.Errorf("env=%q on=%v: err=%v, wantErr=%v", tc.env, tc.on, err, tc.wantErr)
		}
		if err != nil && (!strings.Contains(err.Error(), "SPINE_GIT_HTTP_DEV_NO_AUTH") || !strings.Contains(err.Error(), "SPINE_ENV=development")) {
			t.Errorf("env=%q: error must name the flag and the remedy: %v", tc.env, err)
		}
	}
}

func TestP330_GitHTTPDevNoAuthEnabled(t *testing.T) {
	for raw, want := range map[string]bool{
		"1": true, "true": true, "TRUE": true, "on": true, "yes": true, "Yes": true,
		"0": false, "": false, "false": false, "off": false, "maybe": false,
	} {
		t.Setenv("SPINE_GIT_HTTP_DEV_NO_AUTH", raw)
		if got := gitHTTPDevNoAuthEnabled(); got != want {
			t.Errorf("%q: got %v want %v", raw, got, want)
		}
	}
	// Surrounding whitespace is ignored.
	t.Setenv("SPINE_GIT_HTTP_DEV_NO_AUTH", "\ton \n")
	if !gitHTTPDevNoAuthEnabled() {
		t.Error("a padded affirmative value must be accepted")
	}
}

func TestP330_OverlappingCIDRs(t *testing.T) {
	proxies, err := gateway.ParseTrustedProxyCIDRs("172.28.0.1/32,10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	got := overlappingCIDRs([]string{"172.28.0.0/16", "192.168.5.0/24", "10.1.0.0/16", "172.28.1.0/24", "not-a-cidr"}, proxies)
	if len(got) != 2 || got[0] != "172.28.0.0/16" || got[1] != "10.1.0.0/16" {
		t.Fatalf("overlap = %v, want [172.28.0.0/16 10.1.0.0/16]", got)
	}
	if got := overlappingCIDRs([]string{"172.28.0.0/16"}, nil); len(got) != 0 {
		t.Fatalf("no proxies: overlap = %v", got)
	}
}

// The guard runs before any store, secret or listener is opened: with an
// unreachable database URL, a serve that skipped the guard would fail
// later, at the database, with an error that does not name the flag. An
// unset SPINE_ENV is refused one gate earlier since INIT-024 TASK-007
// (TestServe_RequiresKnownSpineEnv), so only the known non-development
// values reach this guard.
func TestP330_ServeRefusesGitDevNoAuthOutsideDevelopment(t *testing.T) {
	for _, env := range []string{"staging", "production"} {
		t.Run("env="+env, func(t *testing.T) {
			t.Setenv("SPINE_ENV", env)
			t.Setenv("SPINE_GIT_HTTP_DEV_NO_AUTH", "1")
			t.Setenv("SPINE_DEV_MODE", "")
			t.Setenv("SPINE_OPERATOR_TOKEN", "")
			t.Setenv("SMP_ADMIN_TOKEN", "")
			t.Setenv("SPINE_DATABASE_URL", "postgres://127.0.0.1:1/none?sslmode=disable")
			err := serveCmd().RunE(serveCmd(), nil)
			if err == nil || !strings.Contains(err.Error(), "SPINE_GIT_HTTP_DEV_NO_AUTH") {
				t.Fatalf("err = %v, want the SPINE_GIT_HTTP_DEV_NO_AUTH startup refusal", err)
			}
		})
	}
}

// buildServerConfig carries the flag into the gateway, and SPINE_DEV_MODE
// does not imply it.
func TestP330_BuildServerConfigWiresGitDevNoAuth(t *testing.T) {
	for _, on := range []bool{false, true} {
		repoPath := testutil.NewTempRepo(t)
		q := queue.NewMemoryQueue(16)
		ctx, cancel := context.WithCancel(context.Background())
		go q.Start(ctx)
		rt, err := buildServerConfig(ctx, serveDeps{
			Store:            stubStore{},
			RepoPath:         repoPath,
			SpineCfg:         &config.SpineConfig{ArtifactsDir: "/"},
			GitClient:        git.NewCLIClient(repoPath),
			Queue:            q,
			Events:           event.NewQueueRouter(q),
			DevMode:          true,
			GitHTTPDevNoAuth: on,
			RuntimeEnv:       "development",
		})
		if err != nil {
			cancel()
			t.Fatalf("buildServerConfig: %v", err)
		}
		if rt.Config.GitHTTPDevNoAuth != on {
			t.Errorf("GitHTTPDevNoAuth = %v, want %v", rt.Config.GitHTTPDevNoAuth, on)
		}
		cancel()
	}
}
