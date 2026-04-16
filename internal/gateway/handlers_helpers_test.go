package gateway

import (
	"errors"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func TestValidateArtifactContent_UnderCap(t *testing.T) {
	if err := validateArtifactContent(strings.Repeat("a", 1024)); err != nil {
		t.Fatalf("unexpected error for 1KB content: %v", err)
	}
}

func TestValidateArtifactContent_AtCap(t *testing.T) {
	if err := validateArtifactContent(strings.Repeat("a", maxArtifactContentSize)); err != nil {
		t.Fatalf("unexpected error for content at exactly the cap: %v", err)
	}
}

func TestValidateArtifactContent_OverCap(t *testing.T) {
	err := validateArtifactContent(strings.Repeat("a", maxArtifactContentSize+1))
	if err == nil {
		t.Fatal("expected error for content over the cap")
	}
	var derr *domain.SpineError
	if !errors.As(err, &derr) {
		t.Fatalf("expected domain.SpineError, got %T", err)
	}
	if derr.Code != domain.ErrPayloadTooLarge {
		t.Fatalf("expected ErrPayloadTooLarge (413), got %s", derr.Code)
	}
}
