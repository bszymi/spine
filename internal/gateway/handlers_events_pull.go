package gateway

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/bszymi/spine/internal/domain"
)

// GET /api/v1/events — paginated pull-based event log
func (s *Server) handleEventList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "events.read") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	afterCursor := r.URL.Query().Get("after")

	var typeFilter []string
	if types := r.URL.Query().Get("types"); types != "" {
		for _, t := range strings.Split(types, ",") {
			if trimmed := strings.TrimSpace(t); trimmed != "" {
				typeFilter = append(typeFilter, trimmed)
			}
		}
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}

	// Fetch limit+1 to determine has_more
	entries, err := st.ListEventsAfter(r.Context(), afterCursor, typeFilter, limit+1)
	if err != nil {
		WriteError(w, err)
		return
	}

	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}

	var nextCursor string
	if len(entries) > 0 {
		nextCursor = entries[len(entries)-1].EventID
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"events":      entries,
		"next_cursor": nextCursor,
		"has_more":    hasMore,
	})
}
