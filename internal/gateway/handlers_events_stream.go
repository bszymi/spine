package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

const (
	sseReplayCap     = 100
	sseHeartbeat     = 30 * time.Second
	sseWriteDeadline = 5 * time.Second
)

// GET /api/v1/events/stream — SSE event stream
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "events.read") {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	if s.eventBroadcaster == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "event delivery not configured"))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "streaming not supported"))
		return
	}
	rc := http.NewResponseController(w)

	// Cap concurrent SSE streams per actor to prevent resource exhaustion.
	var heldActorID string
	if actor := actorFromContext(r.Context()); actor != nil {
		if !s.sseLimiter.Acquire(actor.ActorID) {
			observe.Logger(r.Context()).Warn("sse connection limit reached", "actor_id", actor.ActorID)
			WriteError(w, domain.NewError(domain.ErrRateLimited, "concurrent SSE connections exceeded for this actor"))
			return
		}
		heldActorID = actor.ActorID
		defer s.sseLimiter.Release(heldActorID)
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

	// Replay missed events if Last-Event-ID is provided. Cap the replay at
	// sseReplayCap so that a stale client reconnecting with an old cursor
	// can't force a synchronous flood before live streaming begins.
	lastEventID := r.Header.Get("Last-Event-ID")
	if lastEventID != "" {
		var typeList []string
		if typeFilter != nil {
			for t := range typeFilter {
				typeList = append(typeList, t)
			}
		}
		missed, err := st.ListEventsAfter(r.Context(), lastEventID, typeList, sseReplayCap)
		if err != nil {
			log.Error("failed to replay missed events", "error", err)
		} else {
			for _, entry := range missed {
				if werr := writeSSEEvent(rc, w, flusher, entry.EventID, entry.EventType, entry.Payload); werr != nil {
					log.Warn("sse replay write failed, dropping connection", "error", werr)
					return
				}
			}
			log.Info("replayed missed events", "count", len(missed), "after", lastEventID, "cap", sseReplayCap)
		}
	}

	// Subscribe to broadcaster — automatically cleaned up on disconnect
	events := make(chan domain.Event, 100)
	subID := s.eventBroadcaster.Subscribe(events)
	defer s.eventBroadcaster.Unsubscribe(subID)

	// Stream loop
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
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
			if err := writeSSEEvent(rc, w, flusher, evt.EventID, string(evt.Type), payload); err != nil {
				log.Warn("sse write failed, dropping connection", "error", err)
				return
			}
		case <-heartbeat.C:
			if err := writeSSEHeartbeat(rc, w, flusher); err != nil {
				log.Warn("sse heartbeat failed, dropping connection", "error", err)
				return
			}
		}
	}
}

// writeSSEEvent writes a single SSE event record. It applies a per-write
// deadline via http.ResponseController so a stalled client cannot hold
// the goroutine indefinitely. Returns the first write error, if any.
func writeSSEEvent(rc *http.ResponseController, w http.ResponseWriter, flusher http.Flusher, id, eventType string, data []byte) error {
	setSSEDeadline(rc)
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, eventType, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// writeSSEHeartbeat emits an SSE comment line to keep the connection
// alive and exercise the write path so a stalled client errors out.
func writeSSEHeartbeat(rc *http.ResponseController, w http.ResponseWriter, flusher http.Flusher) error {
	setSSEDeadline(rc)
	if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// setSSEDeadline applies a short write deadline. Handlers served behind
// wrappers that don't expose SetWriteDeadline (e.g. some recorders in
// tests) are tolerated — the error is ignored when unsupported.
func setSSEDeadline(rc *http.ResponseController) {
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		// Non-fatal. A nil response controller or an intermediate wrapper
		// without deadline support still leaves the heartbeat loop as a
		// weaker liveness guard.
		_ = err
	}
}
