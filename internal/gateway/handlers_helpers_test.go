package gateway

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func TestParsePagination_LimitFloor(t *testing.T) {
	// limit=1 would otherwise let a client amplify DB queries across
	// a large result set; clamp to the floor so every page is at
	// least 10 rows.
	r := httptest.NewRequest("GET", "/?limit=1", nil)
	limit, _ := parsePagination(r)
	if limit != 10 {
		t.Fatalf("expected limit floor of 10, got %d", limit)
	}
}

func TestParsePagination_LimitCeiling(t *testing.T) {
	r := httptest.NewRequest("GET", "/?limit=10000", nil)
	limit, _ := parsePagination(r)
	if limit != 200 {
		t.Fatalf("expected limit ceiling of 200, got %d", limit)
	}
}

func TestParsePagination_LimitDefault(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	limit, _ := parsePagination(r)
	if limit != 50 {
		t.Fatalf("expected default 50, got %d", limit)
	}
}

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
