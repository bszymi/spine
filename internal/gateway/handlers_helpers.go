package gateway

import (
	"encoding/json"
	"fmt"
	"io"
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
//
// Bodies must contain exactly one JSON value. A second top-level value
// (e.g. `{...}{...}` or `{...} 42`) is rejected as invalid_params so
// request semantics stay unambiguous and malformed clients fail loudly
// instead of silently dropping their second payload. Trailing
// whitespace is allowed.
func decodeJSON(r *http.Request, v any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return domain.NewError(domain.ErrInvalidParams, "Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodySize)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return mapDecodeErr(err, "invalid request body")
	}
	// Reject any non-whitespace tail after the first JSON value. We
	// can't use `dec.More()` here — it returns false on `]` and `}`
	// because those terminate an array/object context — so a body
	// like `{"name":"a"}]` would slip through. A second Decode that
	// must return io.EOF catches every malformed tail (extra value,
	// stray scalar, stray closing brace) while still accepting
	// trailing whitespace, which the underlying scanner skips before
	// looking for the next token.
	var trailing json.RawMessage
	switch err := dec.Decode(&trailing); {
	case err == io.EOF:
		return nil
	case err == nil:
		// Second value decoded — a duplicate JSON payload, the
		// classic case (e.g. `{...}{...}`). Distinct from the
		// trailing-garbage case below: there's no decode error to
		// inspect, just a successful second Decode that proves
		// extra content existed.
		return domain.NewError(domain.ErrInvalidParams, "request body must contain a single JSON value")
	default:
		return mapDecodeErr(err, "request body must contain a single JSON value")
	}
}

// mapDecodeErr maps a json.Decoder error to a domain error. The
// MaxBytesReader limit is enforced by both the first body decode and
// the trailing-content check, so a body whose tail pushes total bytes
// over the cap must still surface as 413 ErrPayloadTooLarge instead
// of being demoted to a generic 400 invalid_params.
func mapDecodeErr(err error, fallbackMsg string) error {
	if err == nil {
		return nil
	}
	if err.Error() == "http: request body too large" {
		return domain.NewError(domain.ErrPayloadTooLarge, "request body too large (max 1MB)")
	}
	return domain.NewError(domain.ErrInvalidParams, fallbackMsg)
}

// parsePagination extracts limit and cursor from query params.
// Defaults: limit=50, min=10, max=200. The floor prevents
// `limit=1` loops from amplifying DB queries against large result
// sets; clients that genuinely want tiny pages gain nothing over
// clients that ask for 10.
func parsePagination(r *http.Request) (int, string) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit < 10 {
		limit = 10
	}
	if limit > 200 {
		limit = 200
	}
	cursor := r.URL.Query().Get("cursor")
	return limit, cursor
}
