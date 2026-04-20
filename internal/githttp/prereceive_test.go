package githttp

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestParseRefUpdates_SingleRefWithCapabilities(t *testing.T) {
	// A real push advertises capabilities after a NUL on the first
	// frame. Parser must strip them so the ref is not polluted.
	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	payload := old + " " + new + " refs/heads/main\x00report-status side-band-64k\n"
	frame := pktLine(payload)
	body := append([]byte{}, frame...)
	body = append(body, []byte(flushPkt)...)

	updates, err := parseRefUpdates(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parseRefUpdates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d: %+v", len(updates), updates)
	}
	got := updates[0]
	if got.OldSHA != old || got.NewSHA != new || got.Ref != "refs/heads/main" {
		t.Errorf("ref update mismatch: %+v", got)
	}
	if got.IsDelete() {
		t.Error("expected IsDelete=false for advance")
	}
}

func TestParseRefUpdates_MultiRef(t *testing.T) {
	// Subsequent frames carry no capabilities — parser must handle
	// both shapes in the same stream.
	old := strings.Repeat("0", 40)
	new1 := strings.Repeat("a", 40)
	new2 := strings.Repeat("b", 40)
	body := []byte{}
	body = append(body, pktLine(old+" "+new1+" refs/heads/main\x00report-status\n")...)
	body = append(body, pktLine(old+" "+new2+" refs/heads/feature\n")...)
	body = append(body, []byte(flushPkt)...)

	updates, err := parseRefUpdates(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parseRefUpdates: %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
	if updates[0].Ref != "refs/heads/main" || updates[1].Ref != "refs/heads/feature" {
		t.Errorf("ref order/names wrong: %+v", updates)
	}
}

func TestParseRefUpdates_DeleteRef(t *testing.T) {
	old := strings.Repeat("a", 40)
	// New SHA = 40 zeros → delete.
	body := pktLine(old + " " + zeroSHA + " refs/heads/main\n")
	body = append(body, []byte(flushPkt)...)

	updates, err := parseRefUpdates(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parseRefUpdates: %v", err)
	}
	if !updates[0].IsDelete() {
		t.Error("expected IsDelete=true when new SHA is all zeros")
	}
}

func TestParseRefUpdates_MalformedLengthRejected(t *testing.T) {
	body := []byte("XXXXnot a pkt-line")
	if _, err := parseRefUpdates(bytes.NewReader(body)); err == nil {
		t.Error("expected error for non-hex length prefix")
	}
}

func TestParseRefUpdates_MalformedFrameRejected(t *testing.T) {
	// A frame containing only two space-separated fields is not a
	// valid ref update command.
	body := pktLine("0000 refs/heads/main\n")
	body = append(body, []byte(flushPkt)...)
	if _, err := parseRefUpdates(bytes.NewReader(body)); err == nil {
		t.Error("expected error for malformed ref update")
	}
}

func TestParseRefUpdates_EOFBeforeFlushRejected(t *testing.T) {
	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	body := pktLine(old + " " + new + " refs/heads/main\n")
	// No flush-pkt appended.
	if _, err := parseRefUpdates(bytes.NewReader(body)); err == nil {
		t.Error("expected error when stream ends before flush")
	}
}

func TestBuildReceivePackDenial_ContainsExpectedFrames(t *testing.T) {
	updates := []RefUpdate{
		{Ref: "refs/heads/main"},
		{Ref: "refs/heads/release/1.x"},
	}
	msgs := []string{
		"branch-protection: no-direct-write denies main",
		"branch-protection: no-delete denies release/1.x",
	}
	body := buildReceivePackDenial(msgs, updates)
	s := string(body)

	// Each message must appear verbatim (Git renders them as
	// "remote: <msg>" client-side).
	for _, m := range msgs {
		if !strings.Contains(s, m) {
			t.Errorf("expected body to contain %q, got:\n%s", m, s)
		}
	}
	// Unpack-ok sideband payload.
	if !strings.Contains(s, "unpack ok") {
		t.Errorf("expected body to contain unpack ok frame, got:\n%s", s)
	}
	// Per-ref ng lines.
	for _, u := range updates {
		want := fmt.Sprintf("ng %s pre-receive hook declined", u.Ref)
		if !strings.Contains(s, want) {
			t.Errorf("expected body to contain %q, got:\n%s", want, s)
		}
	}
	// Outer flush.
	if !strings.HasSuffix(s, flushPkt) {
		t.Errorf("expected body to end with flush-pkt, got last 12 bytes: %q", s[max(0, len(s)-12):])
	}
}

func TestBuildReceivePackDenial_SplitsLargeSidebandFrames(t *testing.T) {
	// A push with thousands of refs generates a receive-pack result
	// stream that overflows a single pkt-line (max 0xFFFF bytes).
	// Each emitted pkt-line's length prefix must stay 4 hex digits;
	// if we fail to split, `pktLine` emits a 5-digit length like
	// "10001" which Git clients reject as a protocol error.
	updates := make([]RefUpdate, 3000)
	for i := range updates {
		updates[i] = RefUpdate{Ref: fmt.Sprintf("refs/heads/bulk-%04d", i)}
	}
	body := buildReceivePackDenial([]string{"bulk denial"}, updates)

	// Walk every pkt-line in the response and assert the 4-byte
	// length header is always valid hex < 0x10000 (i.e. the length
	// field parses into a single byte pair).
	idx := 0
	for idx < len(body) {
		if idx+4 > len(body) {
			t.Fatalf("dangling bytes at %d: %q", idx, body[idx:])
		}
		lenHdr := body[idx : idx+4]
		if string(lenHdr) == flushPkt {
			idx += 4
			continue
		}
		length, err := parseHex16(lenHdr)
		if err != nil {
			t.Fatalf("non-hex pkt-line length at %d: %q (%v)", idx, lenHdr, err)
		}
		if length < 4 || idx+length > len(body) {
			t.Fatalf("invalid pkt-line length %d at offset %d", length, idx)
		}
		idx += length
	}
}

func TestBuildReceivePackDenial_StillValidWithNoUpdates(t *testing.T) {
	// When parse fails we reject with no per-ref ng lines. The
	// response must still be well-formed so Git clients show the
	// remote message instead of silently failing.
	body := buildReceivePackDenial([]string{"malformed push"}, nil)
	s := string(body)
	if !strings.Contains(s, "malformed push") {
		t.Error("expected remote message in body")
	}
	if !strings.HasSuffix(s, flushPkt) {
		t.Error("expected outer flush-pkt at end")
	}
}

// max is defined in Go 1.21+; keep a local shim for readability in the
// test's slicing math above.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
