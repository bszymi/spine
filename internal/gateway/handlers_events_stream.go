package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// GET /api/v1/events/stream — SSE event stream
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "events.read") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	if s.events == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "event router not configured"))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "streaming not supported"))
		return
	}

	// Parse event type filters
	var typeFilter map[string]bool
	if types := r.URL.Query().Get("types"); types != "" {
		typeFilter = make(map[string]bool)
		for _, t := range strings.Split(types, ",") {
			typeFilter[strings.TrimSpace(t)] = true
		}
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	log := observe.Logger(r.Context())
	log.Info("SSE stream connected", "types", r.URL.Query().Get("types"))

	// Replay missed events if Last-Event-ID is provided
	lastEventID := r.Header.Get("Last-Event-ID")
	if lastEventID != "" {
		var typeList []string
		if typeFilter != nil {
			for t := range typeFilter {
				typeList = append(typeList, t)
			}
		}
		missed, err := st.ListEventsAfter(r.Context(), lastEventID, typeList, 1000)
		if err != nil {
			log.Error("failed to replay missed events", "error", err)
		} else {
			for _, entry := range missed {
				writeSSEEvent(w, flusher, entry.EventID, entry.EventType, entry.Payload)
			}
			log.Info("replayed missed events", "count", len(missed), "after", lastEventID)
		}
	}

	// Subscribe to live events via a channel
	events := make(chan domain.Event, 100)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Subscribe to all event types on the router.
	// The router will dispatch events to our channel.
	allTypes := allEventTypes()
	for _, et := range allTypes {
		et := et
		if err := s.events.(interface {
			Subscribe(ctx context.Context, eventType domain.EventType, handler func(context.Context, domain.Event) error) error
		}).Subscribe(ctx, et, func(_ context.Context, evt domain.Event) error {
			select {
			case events <- evt:
			default:
				// Drop if channel full — consumer too slow
			}
			return nil
		}); err != nil {
			log.Error("failed to subscribe to event type", "type", et, "error", err)
		}
	}

	// Stream loop
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("SSE stream disconnected")
			return
		case evt := <-events:
			if typeFilter != nil && !typeFilter[string(evt.Type)] {
				continue
			}
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			writeSSEEvent(w, flusher, evt.EventID, string(evt.Type), payload)
		case <-heartbeat.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, id, eventType string, data []byte) {
	fmt.Fprintf(w, "id: %s\n", id)
	fmt.Fprintf(w, "event: %s\n", eventType)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// allEventTypes returns all known event types for SSE subscription.
func allEventTypes() []domain.EventType {
	return []domain.EventType{
		domain.EventArtifactCreated, domain.EventArtifactUpdated,
		domain.EventRunStarted, domain.EventRunCompleted, domain.EventRunFailed,
		domain.EventRunCancelled, domain.EventRunPaused, domain.EventRunResumed,
		domain.EventStepAssigned, domain.EventStepStarted, domain.EventStepCompleted,
		domain.EventStepFailed, domain.EventStepTimeout, domain.EventRetryAttempted,
		domain.EventRunTimeout,
		domain.EventDivergenceStarted, domain.EventConvergenceCompleted,
		domain.EventEngineRecovered, domain.EventProjectionSynced,
		domain.EventThreadCreated, domain.EventCommentAdded, domain.EventThreadResolved,
		domain.EventValidationPassed, domain.EventValidationFailed,
		domain.EventAssignmentFailed, domain.EventTaskUnblocked, domain.EventTaskReleased,
	}
}
