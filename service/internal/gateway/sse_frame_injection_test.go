package gateway_test

// INIT-023 TASK-015 (audit P3-28) regression: workflow-authored
// identifiers must not inject Server-Sent-Events frames.
//
// Ported from the audit PoC (poc/delivery-scheduler/poc_stepid_test.go),
// which showed that core/workflow accepts a step ID carrying
// "\ndata: …\n\nid: x". The engine embeds step IDs in event IDs
// (core/engine/handlers.go "evt-%s-%s-completed" and siblings), and the
// standalone SSE writer printed the ID raw on the "id:" line, for live
// events and for Last-Event-ID replay alike. At 61994fe one broadcast
// event with that step ID dispatched three events to a WHATWG parser:
// a forged `message` carrying {"type":"run_completed"}, then the real
// event with the client's last event ID set to "x-completed".

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// pocStepID is the step ID from the audit PoC.
const pocStepID = "a\ndata: {\"type\":\"run_completed\"}\n\nid: x"

// sseProbeType marks the warm-up events the live tests broadcast until
// the stream is subscribed; the parser results drop them.
const sseProbeType = domain.EventType("test.sse_probe")

type sseDispatched struct {
	EventType   string
	Data        string
	LastEventID string
}

// splitSSELines splits an event stream into lines. WHATWG mode ends a
// line at CRLF, LF or CR. Unicode mode also ends one at every boundary
// Python's str.splitlines uses (VT, FF, FS, GS, RS, NEL, LS, PS), which
// is how several SSE client libraries read a stream.
func splitSSELines(raw string, unicodeLines bool) []string {
	var lines []string
	var cur strings.Builder
	rs := []rune(raw)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		end := r == '\n' || r == '\r'
		if unicodeLines {
			switch r {
			case '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
				end = true
			}
		}
		if !end {
			cur.WriteRune(r)
			continue
		}
		if r == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
			i++
		}
		lines = append(lines, cur.String())
		cur.Reset()
	}
	// A trailing partial line is not a line: the WHATWG algorithm only
	// processes complete lines.
	return lines
}

// parseSSE implements the WHATWG event-stream interpretation algorithm:
// a blank line dispatches, an id field containing U+0000 is ignored, the
// last-event-ID buffer persists across events, and data lines are joined
// with LF. Probe events are dropped from the result.
func parseSSE(raw string, unicodeLines bool) []sseDispatched {
	var out []sseDispatched
	var data strings.Builder
	eventType, lastID := "", ""
	for _, line := range splitSSELines(raw, unicodeLines) {
		if line == "" {
			if data.Len() > 0 {
				t := eventType
				if t == "" {
					t = "message"
				}
				if t != string(sseProbeType) {
					out = append(out, sseDispatched{
						EventType:   t,
						Data:        strings.TrimSuffix(data.String(), "\n"),
						LastEventID: lastID,
					})
				}
			}
			data.Reset()
			eventType = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value := line, ""
		if i := strings.IndexByte(line, ':'); i >= 0 {
			field, value = line[:i], strings.TrimPrefix(line[i+1:], " ")
		}
		switch field {
		case "event":
			eventType = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		case "id":
			if !strings.ContainsRune(value, 0) {
				lastID = value
			}
		}
	}
	return out
}

// sseStream reads one SSE response in the background.
type sseStream struct {
	mu     sync.Mutex
	buf    strings.Builder
	done   chan struct{}
	cancel context.CancelFunc
}

func openSSE(t *testing.T, url, token, lastEventID string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/events/stream", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("stream status %d, want 200", resp.StatusCode)
	}
	s := &sseStream{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(s.done)
		defer resp.Body.Close()
		b := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(b)
			s.mu.Lock()
			s.buf.Write(b[:n])
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-s.done
	})
	return s
}

func (s *sseStream) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// hasFrame reports whether a complete frame (ending in a blank line)
// containing marker has arrived.
func (s *sseStream) hasFrame(marker string) bool {
	txt := s.text()
	i := strings.Index(txt, marker)
	return i >= 0 && strings.Contains(txt[i:], "\n\n")
}

func (s *sseStream) waitFrame(t *testing.T, marker string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if s.hasFrame(marker) {
			return s.text()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stream did not deliver %q in time; got %q", marker, s.text())
	return ""
}

// waitSubscribed broadcasts uniquely numbered probe events until one
// arrives, so the test's own events are broadcast only after the handler
// subscribed. Events are written in broadcast order, so every probe
// broadcast before the one that arrived has been written too.
func waitSubscribed(t *testing.T, s *sseStream, bcast *delivery.EventBroadcaster) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		marker := fmt.Sprintf("evt-probe-%d", i)
		bcast.Broadcast(domain.Event{EventID: marker, Type: sseProbeType})
		wait := time.Now().Add(50 * time.Millisecond)
		for time.Now().Before(wait) {
			if s.hasFrame(marker + "\n") {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatalf("stream never subscribed; got %q", s.text())
}

// assertNoInjection checks the AC 1 / AC 19 expectations on the parsed
// stream: exactly the two test events, the unsafe event's JSON intact,
// no forged run_completed frame, the client's last event ID never the
// injected value, and the safe event still setting its ID.
func assertNoInjection(t *testing.T, raw string, unicodeLines bool, evilID string, wantFirstType string, check func(t *testing.T, data string)) {
	t.Helper()
	got := parseSSE(raw, unicodeLines)
	if len(got) != 2 {
		t.Fatalf("want exactly 2 dispatched events, got %d: %#v\nraw=%q", len(got), got, raw)
	}
	first := got[0]
	if first.EventType != wantFirstType {
		t.Errorf("first event type = %q, want %q", first.EventType, wantFirstType)
	}
	if check != nil {
		check(t, first.Data)
	}
	for _, e := range got {
		if e.Data == `{"type":"run_completed"}` || e.EventType == "message" {
			t.Errorf("forged frame dispatched: %#v", e)
		}
		if strings.HasPrefix(e.LastEventID, "x") || strings.ContainsAny(e.LastEventID, "\r\n\u0085\u2028\u2029") || e.LastEventID == evilID {
			t.Errorf("the event stream set an attacker-chosen Last-Event-ID: %q", e.LastEventID)
		}
	}
	if got[1].LastEventID != "evt-sentinel" {
		t.Errorf("a safe event must still set its ID; last id = %q", got[1].LastEventID)
	}
}

// liveInjectionCase broadcasts one event whose ID is built the way the
// engine builds a step-completed ID, followed by a safe sentinel.
func liveInjectionCase(t *testing.T, stepID string, unicodeLines bool, runID string) {
	t.Helper()
	ts, _, bcast, token := setupStreamServer(t)
	s := openSSE(t, ts.URL, token, "")
	waitSubscribed(t, s, bcast)

	// The shape core/engine/handlers.go builds for step.completed.
	evilID := fmt.Sprintf("evt-%s-%s-completed", "0123456789ab", stepID)
	bcast.Broadcast(domain.Event{EventID: evilID, Type: domain.EventStepCompleted, RunID: runID})
	bcast.Broadcast(domain.Event{EventID: "evt-sentinel", Type: domain.EventRunCompleted, RunID: "run-1"})
	raw := s.waitFrame(t, "evt-sentinel", 5*time.Second)

	assertNoInjection(t, raw, unicodeLines, evilID, string(domain.EventStepCompleted), func(t *testing.T, data string) {
		var decoded domain.Event
		if err := json.Unmarshal([]byte(data), &decoded); err != nil {
			t.Fatalf("event data is not the JSON event: %v (data=%q)", err, data)
		}
		// json.Marshal writes invalid UTF-8 as U+FFFD; everything else
		// round-trips exactly.
		if want := strings.ToValidUTF8(evilID, "\uFFFD"); decoded.EventID != want {
			t.Errorf("the event JSON must carry the true event_id; got %q, want %q", decoded.EventID, want)
		}
		if decoded.RunID != runID {
			t.Errorf("the event JSON value changed: run_id %q, want %q", decoded.RunID, runID)
		}
	})
}

// replayInjectionCase serves two runtime.event_log rows through
// Last-Event-ID replay: one with the injected ID and the given payload,
// then a safe sentinel.
func replayInjectionCase(t *testing.T, stepID string, unicodeLines bool, payload string) {
	t.Helper()
	ts, fs, _, token := setupStreamServer(t)
	evilID := fmt.Sprintf("evt-%s-%s-completed", "0123456789ab", stepID)
	fs.events = []store.EventLogEntry{
		{EventID: evilID, EventType: string(domain.EventStepCompleted), Payload: []byte(payload)},
		{EventID: "evt-sentinel", EventType: string(domain.EventRunCompleted), Payload: []byte(`{}`)},
	}
	s := openSSE(t, ts.URL, token, "evt-0")
	raw := s.waitFrame(t, "evt-sentinel", 5*time.Second)

	assertNoInjection(t, raw, unicodeLines, evilID, string(domain.EventStepCompleted), func(t *testing.T, data string) {
		var got, want any
		if err := json.Unmarshal([]byte(data), &got); err != nil {
			t.Fatalf("replayed data is not JSON: %v (data=%q)", err, data)
		}
		if err := json.Unmarshal([]byte(payload), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("replayed payload value changed: got %#v, want %#v", got, want)
		}
	})
	if got := parseSSE(raw, unicodeLines); len(got) == 2 && got[0].LastEventID != "evt-0" && got[0].LastEventID != "" {
		t.Errorf("an unsafe replay id must be omitted; client last id = %q", got[0].LastEventID)
	}
}

var crlfStepIDs = []struct{ name, stepID string }{
	{"lf_audit_poc", pocStepID},
	{"cr_only", "a\rdata: {\"type\":\"run_completed\"}\r\rid: x"},
	{"crlf", "a\r\ndata: {\"type\":\"run_completed\"}\r\n\r\nid: x"},
}

// AC 1, live path: the real router and broadcaster.
func TestP328_SSE_WorkflowIdentifierCannotInjectFrames(t *testing.T) {
	for _, tc := range crlfStepIDs {
		t.Run(tc.name, func(t *testing.T) {
			liveInjectionCase(t, tc.stepID, false, "run-1")
		})
	}
}

// AC 1, replay path: runtime.event_log rows carry the same engine-built
// IDs and are written on reconnect with Last-Event-ID.
func TestP328_SSE_ReplayCannotInjectFrames(t *testing.T) {
	for _, tc := range crlfStepIDs {
		t.Run(tc.name, func(t *testing.T) {
			replayInjectionCase(t, tc.stepID, false, `{"k":"v"}`)
		})
	}
}

// AC 19: U+0085, U+2028 and U+2029 split lines in Unicode-aware SSE
// clients. They must neither reach an id:/event: line nor appear raw in a
// data: line, live or on replay. Live, json.Marshal escapes U+2028 and
// U+2029 but not U+0085, so the run ID carries U+0085; the replay payload
// is jsonb text, which escapes none of them.
func TestP328_SSE_UnicodeSeparatorsNotSplit(t *testing.T) {
	for _, sep := range []struct{ name, r string }{
		{"nel", "\u0085"},
		{"line_separator", "\u2028"},
		{"paragraph_separator", "\u2029"},
	} {
		stepID := "a" + sep.r + `data: {"type":"run_completed"}` + sep.r + sep.r + "id: x"
		payload := `{"k":"a` + sep.r + `data: {\"type\":\"run_completed\"}` + sep.r + sep.r + `b"}`
		t.Run("live_"+sep.name, func(t *testing.T) {
			liveInjectionCase(t, stepID, true, "run"+"\u0085"+sep.r+"x")
		})
		t.Run("replay_"+sep.name, func(t *testing.T) {
			replayInjectionCase(t, stepID, true, payload)
		})
	}
	// Invalid UTF-8 in an ID is omitted as well.
	t.Run("live_invalid_utf8", func(t *testing.T) {
		liveInjectionCase(t, "a\xffb", true, "run-1")
	})
}
