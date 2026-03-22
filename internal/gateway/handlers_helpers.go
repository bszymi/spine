package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/bszymi/spine/internal/domain"
)

const maxBodySize = 1 << 20 // 1MB

// decodeJSON reads and decodes a JSON request body with a size limit.
func decodeJSON(r *http.Request, v any) error {
	body := io.LimitReader(r.Body, maxBodySize)
	if err := json.NewDecoder(body).Decode(v); err != nil {
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
