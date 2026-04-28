package gateway

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

type decodeTarget struct {
	Name string `json:"name"`
}

func TestDecodeJSON_Valid(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"}`))
	req.Header.Set("Content-Type", "application/json")
	var v decodeTarget
	if err := decodeJSON(req, &v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Name != "a" {
		t.Errorf("expected name=a, got %q", v.Name)
	}
}

// TestDecodeJSON_WhitespaceTail asserts that trailing whitespace after
// a single JSON value is accepted. Many clients (curl with a heredoc,
// pretty-printers) ship a trailing newline; rejecting that would be a
// false positive.
func TestDecodeJSON_WhitespaceTail(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"}   `+"\n\t"))
	req.Header.Set("Content-Type", "application/json")
	var v decodeTarget
	if err := decodeJSON(req, &v); err != nil {
		t.Fatalf("unexpected error for whitespace tail: %v", err)
	}
}

// TestDecodeJSON_TrailingObject is the core regression: a body
// containing two JSON values used to be silently parsed as the first
// only, hiding malformed clients. The decoder must now reject it as
// invalid_params so request semantics stay unambiguous.
func TestDecodeJSON_TrailingObject(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"}{"name":"b"}`))
	req.Header.Set("Content-Type", "application/json")
	var v decodeTarget
	err := decodeJSON(req, &v)
	if err == nil {
		t.Fatal("expected invalid_params for trailing JSON object, got nil")
	}
	var derr *domain.SpineError
	if !errors.As(err, &derr) {
		t.Fatalf("expected domain.SpineError, got %T", err)
	}
	if derr.Code != domain.ErrInvalidParams {
		t.Errorf("expected ErrInvalidParams, got %s", derr.Code)
	}
}

// TestDecodeJSON_TrailingStrayCloser guards the failure mode that
// `dec.More()` alone misses: `}` and `]` bytes after the first JSON
// value are valid array/object terminators inside an iteration
// context, so More() reports "no more" and lets them through. A
// second Decode expecting io.EOF catches them.
func TestDecodeJSON_TrailingStrayCloser(t *testing.T) {
	for _, tail := range []string{"]", "}"} {
		t.Run("tail="+tail, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"}`+tail))
			req.Header.Set("Content-Type", "application/json")
			var v decodeTarget
			err := decodeJSON(req, &v)
			if err == nil {
				t.Fatalf("expected invalid_params for trailing %q, got nil", tail)
			}
			var derr *domain.SpineError
			if !errors.As(err, &derr) || derr.Code != domain.ErrInvalidParams {
				t.Errorf("expected invalid_params, got %v", err)
			}
		})
	}
}

func TestDecodeJSON_TrailingScalar(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"} 42`))
	req.Header.Set("Content-Type", "application/json")
	var v decodeTarget
	err := decodeJSON(req, &v)
	if err == nil {
		t.Fatal("expected invalid_params for trailing scalar, got nil")
	}
	var derr *domain.SpineError
	if !errors.As(err, &derr) || derr.Code != domain.ErrInvalidParams {
		t.Errorf("expected invalid_params, got %v", err)
	}
}

func TestDecodeJSON_InvalidContentType(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"}`))
	req.Header.Set("Content-Type", "text/plain")
	var v decodeTarget
	err := decodeJSON(req, &v)
	if err == nil {
		t.Fatal("expected error for non-JSON content type")
	}
	var derr *domain.SpineError
	if !errors.As(err, &derr) || derr.Code != domain.ErrInvalidParams {
		t.Errorf("expected invalid_params, got %v", err)
	}
}

// TestDecodeJSON_OversizedTrailingTail asserts a body whose first
// JSON value fits inside maxBodySize but whose trailing content
// pushes the total over the cap still surfaces as 413, not the
// generic 400 we'd emit for malformed trailing JSON. The 413 signal
// is what tells clients to back off rather than retry — collapsing
// it to invalid_params would mislead them.
func TestDecodeJSON_OversizedTrailingTail(t *testing.T) {
	first := `{"name":"a"}`
	tail := strings.Repeat(" ", maxBodySize-len(first)+128) + `{"name":"b"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(first+tail))
	req.Header.Set("Content-Type", "application/json")
	var v decodeTarget
	err := decodeJSON(req, &v)
	if err == nil {
		t.Fatal("expected error for body+tail exceeding cap")
	}
	var derr *domain.SpineError
	if !errors.As(err, &derr) {
		t.Fatalf("expected domain.SpineError, got %T", err)
	}
	if derr.Code != domain.ErrPayloadTooLarge {
		t.Errorf("expected ErrPayloadTooLarge for oversized trailing body, got %s", derr.Code)
	}
}

// TestDecodeJSON_OversizedBody asserts the MaxBytesReader gate maps
// to a 413 ErrPayloadTooLarge — not the generic 400 invalid_params we
// would emit for any other decode error.
func TestDecodeJSON_OversizedBody(t *testing.T) {
	// Build a JSON value just over the 1 MiB cap.
	big := strings.Repeat("a", maxBodySize+1024)
	body := `{"name":"` + big + `"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	var v decodeTarget
	err := decodeJSON(req, &v)
	if err == nil {
		t.Fatal("expected error for oversized body")
	}
	var derr *domain.SpineError
	if !errors.As(err, &derr) {
		t.Fatalf("expected domain.SpineError, got %T", err)
	}
	if derr.Code != domain.ErrPayloadTooLarge {
		t.Errorf("expected ErrPayloadTooLarge (413), got %s", derr.Code)
	}
}

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
