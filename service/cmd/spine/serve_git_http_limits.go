package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bszymi/spine/service/internal/githttp"
)

// The git HTTP endpoint's slot caps and command-read deadline (INIT-024
// TASK-005, review finding N-13).
const (
	EnvGitHTTPMaxConcurrent             = "SPINE_GIT_HTTP_MAX_CONCURRENT"
	EnvGitHTTPMaxConcurrentPerWorkspace = "SPINE_GIT_HTTP_MAX_CONCURRENT_PER_WORKSPACE"
	EnvGitHTTPMaxConcurrentPerActor     = "SPINE_GIT_HTTP_MAX_CONCURRENT_PER_ACTOR"

	// maxGitHTTPSlots is the largest accepted cap: each slot is a
	// git-http-backend process, so a typo such as 40000 is refused
	// rather than taken as "unbounded".
	maxGitHTTPSlots = 1024
)

// gitHTTPLimits are the git endpoint's caps and command-read deadline,
// as `spine serve` hands them to githttp.Config (0: the default).
type gitHTTPLimits struct {
	MaxConcurrent             int
	MaxConcurrentPerWorkspace int
	MaxConcurrentPerActor     int
	CommandReadTimeout        time.Duration
}

// loadGitHTTPLimits reads the caps and the deadline. An unset or blank
// variable means the default (githttp.DefaultMaxConcurrent 8,
// DefaultMaxConcurrentPerWorkspace 4, DefaultMaxConcurrentPerActor 2,
// DefaultCommandReadTimeout 10s; owner question 1). A whole number from 1
// to maxGitHTTPSlots, or a positive Go duration, replaces it. Anything
// else — zero, a negative number, a typo — refuses to start (INIT-024
// owner question 3: there is no off switch, and a typo must not silently
// mean the default).
func loadGitHTTPLimits() (gitHTTPLimits, error) {
	var l gitHTTPLimits
	for _, v := range []struct {
		name string
		dst  *int
	}{
		{EnvGitHTTPMaxConcurrent, &l.MaxConcurrent},
		{EnvGitHTTPMaxConcurrentPerWorkspace, &l.MaxConcurrentPerWorkspace},
		{EnvGitHTTPMaxConcurrentPerActor, &l.MaxConcurrentPerActor},
	} {
		raw := strings.TrimSpace(os.Getenv(v.name))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > maxGitHTTPSlots {
			return gitHTTPLimits{}, fmt.Errorf("%s=%q: want a whole number from 1 to %d", v.name, raw, maxGitHTTPSlots)
		}
		*v.dst = n
	}
	if raw := strings.TrimSpace(os.Getenv(githttp.EnvCommandReadTimeout)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return gitHTTPLimits{}, fmt.Errorf("%s=%q: want a positive duration such as 10s (the deadline has no off switch; set a longer one instead)",
				githttp.EnvCommandReadTimeout, raw)
		}
		l.CommandReadTimeout = d
	}
	return l, nil
}

// effective is l with every unset field filled in for the server's
// mode. Shared mode (a ServicePool of workspaces): 8 process-wide, 4 per
// workspace, 2 per actor (owner question 1). Single mode — one workspace,
// whose per-workspace cap is the server's whole capacity — defaults the
// per-workspace and per-actor caps to the process-wide one (8), so a
// single-workspace server never has fewer slots than the one cap of 5 it
// had, and raising SPINE_GIT_HTTP_MAX_CONCURRENT raises its capacity
// (review round 1, P3-2 ruling, refining owner question 1 for single
// mode).
func (l gitHTTPLimits) effective(shared bool) gitHTTPLimits {
	if l.MaxConcurrent == 0 {
		l.MaxConcurrent = githttp.DefaultMaxConcurrent
	}
	perWorkspace, perActor := githttp.DefaultMaxConcurrentPerWorkspace, githttp.DefaultMaxConcurrentPerActor
	if !shared {
		perWorkspace, perActor = l.MaxConcurrent, l.MaxConcurrent
	}
	if l.MaxConcurrentPerWorkspace == 0 {
		l.MaxConcurrentPerWorkspace = perWorkspace
	}
	if l.MaxConcurrentPerActor == 0 {
		l.MaxConcurrentPerActor = perActor
	}
	if l.CommandReadTimeout == 0 {
		l.CommandReadTimeout = githttp.DefaultCommandReadTimeout
	}
	return l
}

// logGitHTTPLimits reports the effective caps once at startup, and warns
// when a cap is set above the one that contains it, where it has no
// effect: a workspace never gets more than the process-wide ceiling, and
// an actor never more than its workspace's cap. In single mode the
// per-workspace cap is the server's capacity, so a process-wide cap set
// above an explicitly set per-workspace cap is named as having no effect
// (review round 1, P3-2).
func logGitHTTPLimits(log *slog.Logger, l gitHTTPLimits, shared bool) {
	e := l.effective(shared)
	mode := "single"
	if shared {
		mode = "shared"
	}
	log.Info("git HTTP slots",
		"mode", mode,
		"max_concurrent", e.MaxConcurrent,
		"max_concurrent_per_workspace", e.MaxConcurrentPerWorkspace,
		"max_concurrent_per_actor", e.MaxConcurrentPerActor,
		"command_read_timeout", e.CommandReadTimeout.String())
	if !shared && l.MaxConcurrent != 0 && l.MaxConcurrentPerWorkspace != 0 && l.MaxConcurrent > l.MaxConcurrentPerWorkspace {
		log.Warn(EnvGitHTTPMaxConcurrent+" is above "+EnvGitHTTPMaxConcurrentPerWorkspace+" on a single-workspace server; "+
			EnvGitHTTPMaxConcurrentPerWorkspace+" is the binding cap (the server's capacity), the rest has no effect",
			"max_concurrent", e.MaxConcurrent, "max_concurrent_per_workspace", e.MaxConcurrentPerWorkspace)
	}
	if e.MaxConcurrentPerWorkspace > e.MaxConcurrent {
		log.Warn(EnvGitHTTPMaxConcurrentPerWorkspace+" is above "+EnvGitHTTPMaxConcurrent+"; a workspace gets at most the process-wide ceiling",
			"max_concurrent_per_workspace", e.MaxConcurrentPerWorkspace, "max_concurrent", e.MaxConcurrent)
	}
	if e.MaxConcurrentPerActor > e.MaxConcurrentPerWorkspace {
		log.Warn(EnvGitHTTPMaxConcurrentPerActor+" is above "+EnvGitHTTPMaxConcurrentPerWorkspace+"; an actor gets at most its workspace's cap",
			"max_concurrent_per_actor", e.MaxConcurrentPerActor, "max_concurrent_per_workspace", e.MaxConcurrentPerWorkspace)
	}
}
