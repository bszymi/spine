package githttp

// The handler-specific seams of the INIT-024 TASK-005 tests
// (push_gate_races_test.go). The red runs at the base replace this file
// (go test -overlay) with one written against the base's handler, which
// has no storage probe, one semaphore and no command-read deadline.

import (
	"context"
	"testing"
	"time"
)

// pgrHandler builds a push-enabled handler with the given caps (0: the
// default) and command-read deadline (0: the default).
func pgrHandler(t *testing.T, cfg Config, perActor, perWorkspace, global int, commandRead time.Duration) *Handler {
	t.Helper()
	cfg.MaxConcurrentPerActor = perActor
	cfg.MaxConcurrentPerWorkspace = perWorkspace
	cfg.MaxConcurrent = global
	cfg.CommandReadTimeout = commandRead
	h, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// pgrSetStorageProbe replaces h's case-sensitivity probe.
func pgrSetStorageProbe(h *Handler, p func(ctx context.Context, repoPath string) (bool, error)) {
	h.storageProbe = p
}

// pgrSlotsHeld is the number of git slots h has handed out.
func pgrSlotsHeld(h *Handler) int {
	h.slots.mu.Lock()
	defer h.slots.mu.Unlock()
	return h.slots.total
}

// pgrDefaultCaps reports the handler's default caps (per actor, per
// workspace, process-wide).
func pgrDefaultCaps() (int, int, int) {
	return DefaultMaxConcurrentPerActor, DefaultMaxConcurrentPerWorkspace, DefaultMaxConcurrent
}
