package gateway

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// INIT-023 TASK-015 (audit P3-28): the unit contract of the SSE frame
// writer (AC 2, AC 19).
func TestP328_FormatSSEEvent(t *testing.T) {
	// A safe frame is byte-identical to the pre-fix Fprintf format.
	for _, tc := range []struct{ id, typ, data string }{
		{"evt-1", "run_completed", `{"a":1}`},
		{"evt-0123456789ab-execute-completed", "step_completed", `{"event_id":"evt-0123456789ab-execute-completed","type":"step_completed"}`},
		{"0123456789ab-div-dp_1", "divergence_started", `{"k":"ż"}`},
	} {
		got := string(formatSSEEvent(tc.id, tc.typ, []byte(tc.data)))
		want := fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", tc.id, tc.typ, tc.data)
		if got != want {
			t.Errorf("safe frame changed:\n got %q\nwant %q", got, want)
		}
	}

	for _, tc := range []struct {
		name, id, typ, data, want string
	}{
		{"lf in id omitted", "a\nid: x", "t", "{}", "event: t\ndata: {}\n\n"},
		{"cr in id omitted", "a\rid: x", "t", "{}", "event: t\ndata: {}\n\n"},
		{"crlf in id omitted", "a\r\nid: x", "t", "{}", "event: t\ndata: {}\n\n"},
		{"nul in id omitted", "a\x00b", "t", "{}", "event: t\ndata: {}\n\n"},
		{"tab in id omitted", "a\tb", "t", "{}", "event: t\ndata: {}\n\n"},
		{"del in id omitted", "a\x7fb", "t", "{}", "event: t\ndata: {}\n\n"},
		{"nel in id omitted", "a\u0085id: x", "t", "{}", "event: t\ndata: {}\n\n"},
		{"line separator in id omitted", "a\u2028id: x", "t", "{}", "event: t\ndata: {}\n\n"},
		{"paragraph separator in id omitted", "a\u2029id: x", "t", "{}", "event: t\ndata: {}\n\n"},
		{"invalid utf-8 in id omitted", "a\xffb", "t", "{}", "event: t\ndata: {}\n\n"},
		{"empty id omitted", "", "t", "{}", "event: t\ndata: {}\n\n"},
		{"lf in event omitted", "i", "t\ndata: x", "{}", "id: i\ndata: {}\n\n"},
		{"nel in event omitted", "i", "t\u0085data: x", "{}", "id: i\ndata: {}\n\n"},
		{"line separator in event omitted", "i", "t\u2028x", "{}", "id: i\ndata: {}\n\n"},
		{"invalid utf-8 in event omitted", "i", "t\xc3", "{}", "id: i\ndata: {}\n\n"},
		{"multi-line data split", "i", "t", "a\nb\r\nc\rd", "id: i\nevent: t\ndata: a\ndata: b\ndata: c\ndata: d\n\n"},
		{"trailing newline in data", "i", "t", "a\n", "id: i\nevent: t\ndata: a\ndata: \n\n"},
		{"empty data still one line", "i", "t", "", "id: i\nevent: t\ndata: \n\n"},
		{"non-ascii utf-8 id kept", "evt-ż-完", "t", "{}", "id: evt-ż-完\nevent: t\ndata: {}\n\n"},
		{"data separators escaped", "i", "t", "{\"k\":\"a\u0085b\u2028c\u2029d\"}", "id: i\nevent: t\ndata: {\"k\":\"a\\u0085b\\u2028c\\u2029d\"}\n\n"},
	} {
		if got := string(formatSSEEvent(tc.id, tc.typ, []byte(tc.data))); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// AC 19: the data escapes keep the JSON value. The re-joined data lines
// (one data: line per line, joined with LF) decode to the input value.
func TestP328_FormatSSEEvent_DataEscapesKeepJSONValue(t *testing.T) {
	for _, in := range []string{
		"{\"k\":\"a\u0085b\"}",
		"{\"k\":\"\u2028\u2029\",\"n\":[\"x\u0085\u2028\"]}",
		"{\"\u2028key\":1}",
		`{"k":"plain"}`,
	} {
		frame := string(formatSSEEvent("i", "t", []byte(in)))
		if strings.ContainsAny(frame, "\u0085\u2028\u2029") {
			t.Fatalf("raw line separator left in frame %q", frame)
		}
		var data []string
		for _, line := range strings.Split(frame, "\n") {
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				data = append(data, v)
			}
		}
		var got, want any
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &got); err != nil {
			t.Fatalf("re-joined data of %q is not JSON: %v", in, err)
		}
		if err := json.Unmarshal([]byte(in), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: JSON value changed: got %#v want %#v", in, got, want)
		}
	}
}

func TestP328_SSEFieldSafe(t *testing.T) {
	for v, want := range map[string]bool{
		"evt-0123456789ab-execute-completed": true,
		"evt-ż":                              true,
		"a b:c":                              true,
		"a\nb":                               false,
		"a\rb":                               false,
		"a\x00b":                             false,
		"a\x1fb":                             false,
		"a\x7fb":                             false,
		"a\u0085b":                           false,
		"a\u2028b":                           false,
		"a\u2029b":                           false,
		"a\xe2\x80":                          false,
	} {
		if got := sseFieldSafe(v); got != want {
			t.Errorf("sseFieldSafe(%q) = %v, want %v", v, got, want)
		}
	}
}
