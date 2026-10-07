package main

// INIT-023 TASK-013 review round 1: a failed admin bootstrap is retried
// in the background for as long as the workspace stays loaded. The
// pooled builder runs only when a ServiceSet is (re)built, so before
// this a single transient error during the rotation sweep left the
// rotated-away bearer live in a busy workspace until the next restart.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/workspace"
)

// flakyAuthStore fails ListTokensByActor — the rotation sweep's first
// call — while failures remain (a negative count fails forever), and
// counts the calls, and the bootstrap attempts (each starts with one
// GetActorByTokenHash).
type flakyAuthStore struct {
	*authCaptureStore
	mu          sync.Mutex
	failures    int
	listCalls   int
	lookupCalls int
}

func (f *flakyAuthStore) GetActorByTokenHash(ctx context.Context, hash string) (*domain.Actor, *domain.Token, error) {
	f.mu.Lock()
	f.lookupCalls++
	f.mu.Unlock()
	return f.authCaptureStore.GetActorByTokenHash(ctx, hash)
}

func (f *flakyAuthStore) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookupCalls
}

func (f *flakyAuthStore) ListTokensByActor(ctx context.Context, actorID string) ([]domain.Token, error) {
	f.mu.Lock()
	f.listCalls++
	fail := f.failures != 0
	if f.failures > 0 {
		f.failures--
	}
	f.mu.Unlock()
	if fail {
		return nil, errors.New("connection reset by peer")
	}
	return f.authCaptureStore.ListTokensByActor(ctx, actorID)
}

func (f *flakyAuthStore) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

// syncBuffer is a goroutine-safe log sink: the retry logs from its own
// goroutine while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func fastInternalAdminRetry(t *testing.T) {
	t.Helper()
	prevInit, prevMax := internalAdminRetryInitialDelay, internalAdminRetryMaxDelay
	internalAdminRetryInitialDelay, internalAdminRetryMaxDelay = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { internalAdminRetryInitialDelay, internalAdminRetryMaxDelay = prevInit, prevMax })
}

// newRotatedWorkspaceStore returns a store holding the live bootstrap
// row of a rotated-away bearer, created before cfg.ProcessStart.
func newRotatedWorkspaceStore(failures int) (*flakyAuthStore, workspaceDeliveryConfig) {
	cs := newAuthCaptureStore()
	cs.actors[auth.InternalAdminActorID] = &domain.Actor{ActorID: auth.InternalAdminActorID, Type: domain.ActorTypeAutomated,
		Name: "SMP Bootstrap Admin", Role: domain.RoleAdmin, Status: domain.ActorStatusActive}
	now := time.Now().UTC()
	cs.tokens["hash-rotated-away"] = &store.TokenRecord{TokenID: "bootstrap-aaaaaaaaaaaaaaaa", ActorID: auth.InternalAdminActorID,
		TokenHash: "hash-rotated-away", Name: "smp-admin-bootstrap", CreatedAt: now.Add(-time.Hour)}
	cfg := workspaceDeliveryConfig{
		SMPAdminToken: "fresh-bearer-0123456789abcdef0123456789",
		ProcessStart:  now,
	}
	return &flakyAuthStore{authCaptureStore: cs, failures: failures}, cfg
}

// A transient failure of the rotation sweep on the first load is
// retried until the superseded bearer is revoked, without waiting for
// the ServiceSet to be rebuilt.
func TestBootstrapInternalAdmin_RetriesUntilTheRotationSweepSucceeds(t *testing.T) {
	fastInternalAdminRetry(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))

	fs, cfg := newRotatedWorkspaceStore(2)
	ss := &workspace.ServiceSet{Config: workspace.Config{ID: "ws-busy"}, Store: fs}
	if err := bootstrapInternalAdmin(ctx, ss, cfg, log); err != nil {
		t.Fatalf("a transient sweep failure must not fail workspace load: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for fs.tokenByHash("hash-rotated-away").RevokedAt == nil {
		if time.Now().After(deadline) {
			t.Fatalf("superseded bearer still live after %d sweep attempts; log:\n%s", fs.calls(), logs.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := fs.calls(); got != 3 {
		t.Errorf("%d sweep attempts, want 3 (two failures, then success)", got)
	}
	if _, err := auth.NewService(fs).ValidateToken(ctx, cfg.SMPAdminToken); err != nil {
		t.Errorf("current bearer refused after the retry: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "attempt=2") {
		t.Errorf("failed retry not logged at Error with its attempt; log:\n%s", out)
	}
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "succeeded on retry") {
		if time.Now().After(deadline) {
			t.Fatalf("successful retry not logged; log:\n%s", logs.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
	if strings.Contains(logs.String(), cfg.SMPAdminToken) {
		t.Error("log leaks the bearer")
	}
}

// The retry belongs to the ServiceSet: it stops when the set closes
// (done), or its context is cancelled, however long the failure lasts.
func TestRetryInternalAdminBootstrap_StopsWhenTheServiceSetCloses(t *testing.T) {
	for _, stop := range []string{"done closed", "context cancelled"} {
		t.Run(stop, func(t *testing.T) {
			fs, cfg := newRotatedWorkspaceStore(-1) // fails forever
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				retryInternalAdminBootstrap(ctx, fs, done, "ws-closing", cfg, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)),
					time.Millisecond, 2*time.Millisecond)
			}()

			deadline := time.Now().Add(5 * time.Second)
			for fs.calls() < 2 {
				if time.Now().After(deadline) {
					t.Fatal("retry never attempted")
				}
				time.Sleep(time.Millisecond)
			}
			if stop == "done closed" {
				close(done)
			} else {
				cancel()
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatalf("retry still running after %s", stop)
			}
			if fs.tokenByHash("hash-rotated-away").RevokedAt != nil {
				t.Fatal("fixture: the sweep was meant to fail throughout")
			}
		})
	}
}

// A hash collision is deterministic until an operator cleans up the
// colliding row: it is neither retried after the first load nor by a
// retry that runs into one.
func TestBootstrapInternalAdmin_CollisionIsNotRetried(t *testing.T) {
	fastInternalAdminRetry(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	newCollidingStore := func() (*flakyAuthStore, workspaceDeliveryConfig) {
		fs, cfg := newRotatedWorkspaceStore(0)
		fs.actors["service-x"] = &domain.Actor{ActorID: "service-x", Type: domain.ActorTypeAutomated,
			Role: domain.RoleReader, Status: domain.ActorStatusActive}
		h := auth.HashToken(cfg.SMPAdminToken)
		fs.tokens[h] = &store.TokenRecord{TokenID: "service-x-token", ActorID: "service-x", TokenHash: h}
		return fs, cfg
	}

	fs, cfg := newCollidingStore()
	ss := &workspace.ServiceSet{Config: workspace.Config{ID: "ws-collide"}, Store: fs}
	if err := bootstrapInternalAdmin(ctx, ss, cfg, slog.New(slog.NewTextHandler(&syncBuffer{}, nil))); err != nil {
		t.Fatalf("non-production collision must not bubble up: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // ten initial delays
	if got := fs.attempts(); got != 1 {
		t.Fatalf("collision was retried (%d bootstrap attempts, want 1)", got)
	}

	fs, cfg = newCollidingStore()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		retryInternalAdminBootstrap(ctx, fs, nil, "ws-collide", cfg, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)),
			time.Millisecond, time.Millisecond)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("retry kept going after a collision")
	}
	if got := fs.attempts(); got != 1 {
		t.Fatalf("%d attempts after a collision, want 1", got)
	}
}

// The builder runs on the pool's context, which names no workspace, so
// core/auth's bootstrap log and audit lines — the ones the runbook
// tells operators to act on per workspace — carry the workspace id that
// bootstrapInternalAdmin adds.
func TestBootstrapInternalAdmin_CoreLogLinesNameTheWorkspace(t *testing.T) {
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	fs, cfg := newRotatedWorkspaceStore(0)
	ss := &workspace.ServiceSet{Config: workspace.Config{ID: "ws-named"}, Store: fs}
	if err := bootstrapInternalAdmin(context.Background(), ss, cfg, slog.Default()); err != nil {
		t.Fatal(err)
	}
	var revoked, audit string
	for _, line := range strings.Split(logs.String(), "\n") {
		switch {
		case strings.Contains(line, "superseded bootstrap admin token revoked"):
			revoked = line
		case strings.Contains(line, "audit_operation=bootstrap_admin_token_revoked"):
			audit = line
		}
	}
	for name, line := range map[string]string{"revocation": revoked, "audit": audit} {
		if !strings.Contains(line, "workspace_id=ws-named") {
			t.Errorf("%s line does not name the workspace: %q\nlog:\n%s", name, line, logs.String())
		}
	}
}
