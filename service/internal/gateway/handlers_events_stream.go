package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
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

	eventBroadcaster := s.eventBroadcasterFrom(r.Context())
	if eventBroadcaster == nil {
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
		for t := range typeFilter {
			typeList = append(typeList, t)
		}
		missed, err := st.ListEventsAfter(r.Context(), lastEventID, typeList, sseReplayCap)
		if err != nil {
			log.Error("failed to replay missed events", "error", err)
		} else {
			for _, entry := range missed {
				warnUnsafeSSEFields(log, entry.EventID, entry.EventType)
				if werr := writeSSEEvent(rc, w, flusher, entry.EventID, entry.EventType, entry.Payload); werr != nil {
					log.Warn("sse replay write failed, dropping connection", "error", werr)
					return
				}
			}
			log.Info("replayed missed events", "count", len(missed), "after", lastEventID, "cap", sseReplayCap)
		}
	}

	// Capture the workspace's eviction signal BEFORE releasing the pool
	// ref. After release the pool may evict the ServiceSet at any time;
	// reading ss.Done from the (still pointer-valid) ServiceSet object
	// lets the loop below disconnect promptly so the client reconnects
	// against fresh per-workspace services. ssDone is nil in
	// single-workspace mode (no ServiceSet on the request), where a
	// nil channel selects-blocks forever — the desired behavior:
	// nothing to react to.
	var ssDone <-chan struct{}
	if ss := serviceSetFromContext(r.Context()); ss != nil {
		ssDone = ss.Done
	}

	// Release the workspace pool ref before entering the live loop. The
	// rest of the handler operates on the captured eventBroadcaster
	// reference only, never on ss.Store, so we no longer need to pin
	// the pool entry. Without this, an open SSE stream would hold the
	// pool entry's refCount > 0 for the entire stream lifetime; a
	// concurrent ServicePool.Evict (binding invalidation, idle reaper)
	// would mark the entry evicting and leave subsequent pool.Get
	// calls for the same workspace blocked on the gone channel until
	// the stream disconnects. Releasing here lets eviction complete
	// promptly; the ssDone select branch above ensures we don't keep
	// streaming heartbeats from a halted broadcaster after the
	// workspace is rebuilt. The middleware release is sync.Once-
	// protected, so its defer remains a safe no-op for this request.
	releaseWorkspaceRef(r.Context())

	// Subscribe to broadcaster — automatically cleaned up on disconnect
	events := make(chan domain.Event, 100)
	subID := eventBroadcaster.Subscribe(events)
	defer eventBroadcaster.Unsubscribe(subID)

	// Stream loop
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			log.Info("SSE stream disconnected")
			return
		case <-ssDone:
			log.Info("SSE stream closing: workspace evicted")
			return
		case evt := <-events:
			if typeFilter != nil && !typeFilter[string(evt.Type)] {
				continue
			}
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			warnUnsafeSSEFields(log, evt.EventID, string(evt.Type))
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

// sseFieldSafe reports whether v can be written as the value of a
// single-line SSE field (id: or event:). INIT-023 TASK-015, audit P3-28.
//
// Event IDs embed workflow-authored identifiers — step IDs
// (core/engine "evt-<trace>-<step>-completed" and siblings), divergence-
// point IDs ("<run>-div-<dp>", then "div-started-…", "conv-completed-…")
// and divergence-branch IDs (execution IDs, then "evt-<exec>-started").
// core/workflow only checks that a step ID is non-empty, and a workflow
// reaches the authoritative branch by API, by git push and by governed
// merge, so the writer is the one place that sees every source,
// including Last-Event-ID replay from runtime.event_log. A value is
// refused when it holds:
//
//   - any byte below 0x20 or DEL. The event-stream grammar ends a line at
//     CR, LF or CRLF (so a raw newline starts a new field: a forged
//     `data:` frame and an attacker-chosen `id:`), and a client ignores
//     an id containing NUL;
//   - invalid UTF-8, which a client decodes with replacement characters,
//     so the id it would echo as Last-Event-ID is not the stored one;
//   - U+0085 (NEL), U+2028 or U+2029. A WHATWG parser ignores them, but
//     clients that split lines by Unicode rules (Python str.splitlines,
//     which several SSE client libraries use) treat each as a line end.
//
// Every other non-ASCII character is allowed.
func sseFieldSafe(v string) bool {
	for i := 0; i < len(v); i++ {
		if b := v[i]; b < 0x20 || b == 0x7f {
			return false
		}
	}
	if !utf8.ValidString(v) {
		return false
	}
	return !strings.ContainsAny(v, "\u0085\u2028\u2029")
}

// sseDataEscaper rewrites the three Unicode line separators that
// Unicode-aware SSE clients split on as JSON \uXXXX escapes. Every SSE
// data payload is JSON (json.Marshal live, runtime.event_log jsonb text on
// replay), and in valid JSON these code points can occur only inside a
// string, where the escape denotes the same character: the decoded value
// is unchanged. json.Marshal already escapes U+2028 and U+2029 but not
// U+0085; jsonb text escapes none of them.
var sseDataEscaper = strings.NewReplacer(
	"\u0085", `\u0085`,
	"\u2028", `\u2028`,
	"\u2029", `\u2029`,
)

// formatSSEEvent renders one event record (INIT-023 TASK-015, P3-28).
//
//   - The id: line is written only for a non-empty id that passes
//     sseFieldSafe, and the event: line only for a non-empty, safe type.
//     An unsafe value is omitted, not escaped or percent-encoded: an
//     escaped id is unknown to the replay query (ListEventsAfter), and an
//     unknown cursor replays nothing, so one bad event would become a
//     silent gap. An omitted id leaves the client's last-event-ID buffer
//     unchanged, so a reconnect replays from the previous event
//     (at-least-once). The JSON data still carries the true event_id.
//   - data is normalised (CRLF and CR become LF) and written as one data:
//     line per line, which the client re-joins with LF, so a payload line
//     break can never start a new field. U+0085, U+2028 and U+2029 are
//     written as JSON escapes (sseDataEscaper).
//
// A frame whose id and type are safe and whose data holds no line break
// and none of those three code points — every engine event — is
// byte-identical to the "id: %s\nevent: %s\ndata: %s\n\n" format the
// writer used before.
func formatSSEEvent(id, eventType string, data []byte) []byte {
	var b bytes.Buffer
	b.Grow(len(id) + len(eventType) + len(data) + 32)
	if id != "" && sseFieldSafe(id) {
		b.WriteString("id: ")
		b.WriteString(id)
		b.WriteByte('\n')
	}
	if eventType != "" && sseFieldSafe(eventType) {
		b.WriteString("event: ")
		b.WriteString(eventType)
		b.WriteByte('\n')
	}
	d := string(data)
	if strings.ContainsAny(d, "\r\u0085\u2028\u2029") {
		d = strings.ReplaceAll(d, "\r\n", "\n")
		d = strings.ReplaceAll(d, "\r", "\n")
		d = sseDataEscaper.Replace(d)
	}
	for {
		line, rest, more := strings.Cut(d, "\n")
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
		if !more {
			break
		}
		d = rest
	}
	b.WriteByte('\n')
	return b.Bytes()
}

// warnUnsafeSSEFields logs one Warn per field formatSSEEvent is about to
// omit. slog quotes the value, so the log line cannot be split either.
func warnUnsafeSSEFields(log *slog.Logger, id, eventType string) {
	if id != "" && !sseFieldSafe(id) {
		log.Warn("sse: omitting unsafe event id", "event_id", id, "event_type", eventType)
	}
	if eventType != "" && !sseFieldSafe(eventType) {
		log.Warn("sse: omitting unsafe event type", "event_id", id, "event_type", eventType)
	}
}

// writeSSEEvent writes a single SSE event record (formatSSEEvent) with one
// Write. It applies a per-write deadline via http.ResponseController so a
// stalled client cannot hold the goroutine indefinitely. Returns the
// first write error, if any.
func writeSSEEvent(rc *http.ResponseController, w http.ResponseWriter, flusher http.Flusher, id, eventType string, data []byte) error {
	setSSEDeadline(rc)
	if _, err := w.Write(formatSSEEvent(id, eventType, data)); err != nil {
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
