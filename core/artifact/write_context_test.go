package artifact

import (
	"context"
	"testing"
)

func TestWriteContext_RoundTrip(t *testing.T) {
	ctx := context.Background()

	// No write context set.
	if wc := GetWriteContext(ctx); wc != nil {
		t.Error("expected nil WriteContext")
	}

	// Set and retrieve.
	ctx = WithWriteContext(ctx, WriteContext{Branch: "run-abc123"})
	wc := GetWriteContext(ctx)
	if wc == nil {
		t.Fatal("expected non-nil WriteContext")
	}
	if wc.Branch != "run-abc123" {
		t.Errorf("expected branch run-abc123, got %s", wc.Branch)
	}
}

func TestWriteContext_DefaultBehavior(t *testing.T) {
	ctx := context.Background()

	// Without WriteContext, GetWriteContext returns nil.
	wc := GetWriteContext(ctx)
	if wc != nil {
		t.Error("expected nil for default context")
	}
}

func TestWriteContext_EmptyBranch(t *testing.T) {
	ctx := WithWriteContext(context.Background(), WriteContext{Branch: ""})
	wc := GetWriteContext(ctx)
	if wc == nil {
		t.Fatal("expected non-nil WriteContext even with empty branch")
	}
	// Empty branch means "use current branch" (backward compatible).
	if wc.Branch != "" {
		t.Errorf("expected empty branch, got %s", wc.Branch)
	}
}
