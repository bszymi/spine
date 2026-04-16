package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/bszymi/spine/internal/domain"
)

const (
	maxBodySize            = 1 << 20 // 1MB
	maxArtifactContentSize = 512 * 1024
)

// validateArtifactContent enforces an explicit cap on the `content`
// field of artifact write requests. The outer body already has a
// MaxBytesReader gate, but clients can still ship a 1 MiB JSON wrapper
// containing a YAML-rich payload that bypasses the artifact-level
// bounds we'd like to preserve. Returns a domain.ErrPayloadTooLarge
// (HTTP 413) when the cap is exceeded.
func validateArtifactContent(content string) error {
	if len(content) > maxArtifactContentSize {
		return domain.NewError(domain.ErrPayloadTooLarge, fmt.Sprintf("artifact content exceeds %d byte cap (got %d)", maxArtifactContentSize, len(content)))
	}
	return nil
}

// decodeJSON reads and decodes a JSON request body with a size limit.
// Validates Content-Type and returns a 413 if the body exceeds maxBodySize.
func decodeJSON(r *http.Request, v any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return domain.NewError(domain.ErrInvalidParams, "Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodySize)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		if err.Error() == "http: request body too large" {
			return domain.NewError(domain.ErrPayloadTooLarge, "request body too large (max 1MB)")
		}
		return domain.NewError(domain.ErrInvalidParams, "invalid request body")
	}
	return nil
}

// parsePagination extracts limit and cursor from query params.
// Defaults: limit=50, max=200.
func parsePagination(r *http.Request) (int, string) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 200 {
		limit = 200
	}
	cursor := r.URL.Query().Get("cursor")
	return limit, cursor
}
