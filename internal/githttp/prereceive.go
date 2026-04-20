package githttp

import (
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// RefUpdate is one ref change in a git push — the triple the client
// sends in the receive-pack request body before the PACK. The handler's
// pre-receive gate evaluates each triple against branchprotect.Policy.
type RefUpdate struct {
	OldSHA string
	NewSHA string
	Ref    string
}

// IsDelete reports whether the update deletes the ref (new SHA is all
// zeros). Classification follows ADR-009 §3: delete → OpDelete,
// otherwise → OpDirectWrite (every git push is a direct write from the
// policy's perspective; governed merges happen inside Spine, not over
// the wire).
func (u RefUpdate) IsDelete() bool {
	return u.NewSHA == zeroSHA
}

// zeroSHA is Git's "empty" object id — 40 zeros for SHA-1. Pushes that
// encode a ref deletion send this as the new SHA.
const zeroSHA = "0000000000000000000000000000000000000000"

// flushPkt is the Git flush packet that terminates the ref-update
// section of a receive-pack request before the PACK data.
const flushPkt = "0000"

// parseRefUpdates reads the command section of a receive-pack request
// body and returns the list of ref updates and the capability set
// advertised by the client on the first frame. The body starts with
// one or more pkt-line frames carrying "<old> <new> <ref>[\0caps]\n"
// and ends with a flush-pkt ("0000"); what follows is the PACK and
// is not consumed here (unless caps include "push-options", in which
// case an options section sits between the flush and the PACK — see
// readPreamble).
//
// Pkt-line format (Git protocol): each frame is a 4-byte hex length
// (including the 4 length bytes themselves) followed by that many
// bytes of payload. A length of "0000" is the flush packet and has no
// payload.
//
// The parser is deliberately strict: a malformed length, a frame that
// does not match the command shape, or EOF before the flush returns an
// error. The caller must treat a parse error as a hard reject — we
// cannot evaluate ref updates we could not parse.
func parseRefUpdates(r io.Reader) ([]RefUpdate, map[string]struct{}, error) {
	var updates []RefUpdate
	caps := map[string]struct{}{}
	first := true
	for {
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return nil, nil, fmt.Errorf("read pkt-line length: %w", err)
		}
		if string(lenBuf) == flushPkt {
			return updates, caps, nil
		}
		length, err := parseHex16(lenBuf)
		if err != nil {
			return nil, nil, fmt.Errorf("parse pkt-line length %q: %w", lenBuf, err)
		}
		if length < 4 {
			return nil, nil, fmt.Errorf("pkt-line length %d below minimum (4)", length)
		}
		payload := make([]byte, length-4)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, nil, fmt.Errorf("read pkt-line payload: %w", err)
		}
		line := string(payload)
		// First frame may carry capabilities after a NUL byte.
		// Capabilities are space-separated; we keep them as a set
		// so `push-options` presence is an O(1) lookup in the
		// caller.
		if first {
			if idx := strings.IndexByte(line, 0); idx >= 0 {
				for _, c := range strings.Fields(line[idx+1:]) {
					caps[c] = struct{}{}
				}
				line = line[:idx]
			}
			first = false
		}
		line = strings.TrimRight(line, "\n")

		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			return nil, nil, fmt.Errorf("malformed ref update %q", line)
		}
		updates = append(updates, RefUpdate{
			OldSHA: parts[0],
			NewSHA: parts[1],
			Ref:    parts[2],
		})
	}
}

// parsePushOptions reads the options section of a receive-pack body.
// Each option is one pkt-line of "key=value" or bare "key"; the
// section ends with a flush-pkt. Unknown keys are returned verbatim
// so the caller can decide which ones it honours (TASK-003: only
// `spine.override=true` today).
func parsePushOptions(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	for {
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return nil, fmt.Errorf("read push-option pkt-line length: %w", err)
		}
		if string(lenBuf) == flushPkt {
			return out, nil
		}
		length, err := parseHex16(lenBuf)
		if err != nil {
			return nil, fmt.Errorf("parse push-option length %q: %w", lenBuf, err)
		}
		if length < 4 {
			return nil, fmt.Errorf("push-option pkt-line length %d below minimum (4)", length)
		}
		payload := make([]byte, length-4)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("read push-option payload: %w", err)
		}
		line := strings.TrimRight(string(payload), "\n")
		if idx := strings.IndexByte(line, '='); idx >= 0 {
			out[line[:idx]] = line[idx+1:]
			continue
		}
		out[line] = ""
	}
}

// parseHex16 decodes a 4-byte uppercase-or-lowercase hex slice to an
// int. Tight helper so parseRefUpdates can keep error messages terse.
func parseHex16(b []byte) (int, error) {
	var dst [2]byte
	n, err := hex.Decode(dst[:], b)
	if err != nil {
		return 0, err
	}
	if n != 2 {
		return 0, fmt.Errorf("expected 2 decoded bytes, got %d", n)
	}
	return int(dst[0])<<8 | int(dst[1]), nil
}

// buildReceivePackDenial renders the body Git clients expect when a
// pre-receive reject prevents the push from landing. The shape mirrors
// what real git-http-backend emits when a pre-receive hook exits
// non-zero:
//
//   - One or more side-band 2 frames carrying the remote error
//     messages, which Git displays as "remote: <msg>" on the client.
//   - One or more side-band 1 frames carrying the receive-pack
//     result stream: "unpack ok\n" followed by "ng <ref> pre-receive
//     hook declined\n" per ref update, then a flush-pkt. Every ref
//     is marked `ng` because pre-receive semantics reject
//     all-or-nothing.
//   - A final flush-pkt outside the side-band to close the response.
//
// Content-Type must be application/x-git-receive-pack-result. Callers
// are expected to set that header before writing the returned bytes.
func buildReceivePackDenial(messages []string, updates []RefUpdate) []byte {
	var buf []byte

	// Side-band 2: remote-facing error lines. Git prefixes each with
	// "remote: " when it renders them, so we do not add that prefix
	// here — doing so would surface as "remote: remote: ..."
	for _, m := range messages {
		buf = append(buf, sidebandChunks(2, m+"\n")...)
	}

	// Side-band 1: the receive-pack result stream itself, wrapped in
	// its own pkt-line frames. Chunked across multiple side-band
	// frames when large — pkt-line length is 4 hex digits (max
	// 0xFFFF = 65535 bytes, including the 5-byte side-band header).
	// A push with thousands of refs would overflow a single frame;
	// sidebandChunks keeps every emitted frame under the limit.
	var inner []byte
	inner = append(inner, pktLine("unpack ok\n")...)
	for _, u := range updates {
		inner = append(inner, pktLine(fmt.Sprintf("ng %s pre-receive hook declined\n", u.Ref))...)
	}
	inner = append(inner, []byte(flushPkt)...)
	buf = append(buf, sidebandChunks(1, string(inner))...)

	// Outer flush to end the response.
	buf = append(buf, []byte(flushPkt)...)
	return buf
}

// maxSidebandPayload is the largest payload we emit in one side-band
// pkt-line. The Git pkt-line length prefix is 4 hex digits, so the
// total frame (header + payload) must fit in 0xFFFF bytes. Subtract 4
// bytes for the pkt-line header and 1 byte for the side-band channel
// byte; round down further to leave a safety margin for clients and
// intermediaries that dislike frames at the exact limit.
const maxSidebandPayload = 65000

// sidebandChunks splits payload across one or more side-band
// pkt-lines so no emitted frame exceeds pkt-line's length limit.
// Every chunk carries the same channel byte.
func sidebandChunks(band byte, payload string) []byte {
	if len(payload) == 0 {
		return nil
	}
	var out []byte
	for i := 0; i < len(payload); i += maxSidebandPayload {
		end := i + maxSidebandPayload
		if end > len(payload) {
			end = len(payload)
		}
		out = append(out, sidebandPkt(band, payload[i:end])...)
	}
	return out
}

// pktLine wraps payload in a Git pkt-line frame (4-hex-digit length
// prefix including the header). Callers pass the full payload
// including any trailing newline Git expects.
func pktLine(payload string) []byte {
	length := len(payload) + 4
	return []byte(fmt.Sprintf("%04x%s", length, payload))
}

// sidebandPkt wraps payload in a side-band frame on the given band
// (1 = data, 2 = progress/stderr, 3 = fatal) and then in a pkt-line.
func sidebandPkt(band byte, payload string) []byte {
	framed := string([]byte{band}) + payload
	return pktLine(framed)
}
