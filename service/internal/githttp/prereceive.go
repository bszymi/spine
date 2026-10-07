package githttp

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bszymi/spine/adapters/git/refname"
	"github.com/bszymi/spine/core/domain"
)

// RefUpdate is one ref change in a git push — the triple the client
// sends in the receive-pack request body before the PACK. The handler's
// pre-receive gate evaluates each triple against branchprotect.Policy.
type RefUpdate struct {
	OldSHA string
	NewSHA string
	Ref    string
}

// IsDelete reports whether the update deletes the ref (new SHA is the
// null object id of either hash algorithm). Classification follows
// ADR-009 §3: delete → OpDelete, otherwise → OpDirectWrite (every git
// push is a direct write from the policy's perspective; governed merges
// happen inside Spine, not over the wire).
//
// Both null ids must be recognised: a SHA-256 repository sends 64 zeros
// for a delete, and classifying that as a direct write would let a
// no-delete-only rule wave the delete through (INIT-023 TASK-001).
func (u RefUpdate) IsDelete() bool {
	return u.NewSHA == zeroSHA || u.NewSHA == zeroSHA256
}

// zeroSHA is Git's "empty" object id — 40 zeros for SHA-1. Pushes that
// encode a ref deletion send this as the new SHA.
const zeroSHA = "0000000000000000000000000000000000000000"

// zeroSHA256 is the null object id of a SHA-256 repository (64 zeros),
// which a delete sends as the new SHA there.
const zeroSHA256 = zeroSHA + "000000000000000000000000"

// flushPkt is the Git flush packet that terminates the ref-update
// section of a receive-pack request before the PACK data.
const flushPkt = "0000"

// parseRefUpdates reads the command section of a receive-pack request
// body and returns the list of ref updates and the capability set
// advertised by the client on the first command. The body starts with
// one or more pkt-line frames carrying "<old> <new> <ref>[\0caps]\n"
// and ends with a flush-pkt ("0000"); what follows is the PACK and is
// not consumed here (unless caps include "push-options", in which case
// an options section sits between the flush and the PACK — see
// Handler.prereceive).
//
// Pkt-line format (Git protocol): each frame is a 4-byte hex length
// (including the 4 length bytes themselves) followed by that many
// bytes of payload. A length of "0000" is the flush packet and has no
// payload.
//
// The parser is deliberately strict, because its one job is to hand the
// policy exactly the refs git will update (INIT-023 TASK-001, audit F1):
//
//   - Exactly one trailing LF is stripped per frame, as git's
//     PACKET_READ_CHOMP_NEWLINE does; anything else stays in the line.
//   - Capabilities are accepted on the first command only, after its
//     first NUL, and must be printable ASCII (isCapabilityList). git
//     send-pack never sends them anywhere else. git receive-pack, by
//     contrast, ends EVERY command's ref at its first NUL and reads the
//     rest as a feature list, so a NUL tolerated on a later line would
//     let the gate evaluate "refs/heads/main\x00" while git updates
//     "refs/heads/main". On lines 2+ a NUL therefore stays in the ref
//     and validateRefUpdate refuses it.
//   - Every command's object ids and ref are validated
//     (validateRefUpdate), so nothing after the parser ever sees a ref
//     git could read differently.
//
// A malformed length, a frame that does not match the command shape, a
// command that fails validation, or EOF before the flush returns an
// error naming the 1-based command number. The caller must treat a
// parse error as a hard reject of the whole push — we cannot evaluate
// ref updates we could not parse. Error text quotes untrusted bytes
// with %q so no raw control byte reaches a log or the client.
func parseRefUpdates(r io.Reader) ([]RefUpdate, map[string]struct{}, error) {
	var updates []RefUpdate
	caps := map[string]struct{}{}
	n := 0 // 1-based index of the command being parsed
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
		n++
		// git's packet reader strips exactly one trailing LF
		// (PACKET_READ_CHOMP_NEWLINE). TrimSuffix, not TrimRight: a
		// second LF stays in the ref, where validateRefUpdate refuses
		// it, instead of being silently folded into the same ref.
		line := strings.TrimSuffix(string(payload), "\n")
		// Capabilities ride after a NUL on the FIRST command only. They
		// are space-separated; we keep them as a set so `push-options`
		// presence is an O(1) lookup in the caller. Split them off line
		// 1 and nowhere else: on a later line the NUL stays in the ref
		// and is refused below (see the doc comment for why).
		if n == 1 {
			if idx := strings.IndexByte(line, 0); idx >= 0 {
				capList := line[idx+1:]
				// git reads the feature list as a C string (it ends at
				// a second NUL) and splits it on its own isspace;
				// strings.Fields also splits on \v, \f and Unicode
				// spaces. Refusing anything but printable ASCII makes
				// the two token sets identical, so the gate and git
				// agree on whether a push-options section follows (and
				// so on whether `spine.override` was requested).
				if !isCapabilityList(capList) {
					return nil, nil, fmt.Errorf("command %d: malformed capability list %q", n, capList)
				}
				for _, c := range strings.Fields(capList) {
					caps[c] = struct{}{}
				}
				line = line[:idx]
			}
		}

		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			return nil, nil, fmt.Errorf("command %d: malformed ref update %q", n, line)
		}
		u := RefUpdate{OldSHA: parts[0], NewSHA: parts[1], Ref: parts[2]}
		if err := validateRefUpdate(u); err != nil {
			return nil, nil, fmt.Errorf("command %d: %w", n, err)
		}
		updates = append(updates, u)
	}
}

// validateRefUpdate refuses a command the gate could evaluate
// differently from git receive-pack:
//
//   - object ids that are not hex of one hash length (40 for SHA-1, 64
//     for SHA-256; either case, as git's parser accepts both), or whose
//     lengths differ;
//   - a ref that fails git's refname grammar (refname.ValidateQualified):
//     control bytes including NUL, a missing refs/ prefix, "..", "@{",
//     ".lock" components, and the rest.
//
// Every such command is one git would refuse itself ("funny refname",
// or a protocol error on the ids), or one git would read as a different
// ref; refusing it never refuses a push git would have applied as
// written.
func validateRefUpdate(u RefUpdate) error {
	if !isHexOID(u.OldSHA) || !isHexOID(u.NewSHA) || len(u.OldSHA) != len(u.NewSHA) {
		return fmt.Errorf("malformed object ids in ref update for %q", u.Ref)
	}
	if err := refname.ValidateQualified(u.Ref); err != nil {
		// Surface the grammar's message without its error-code prefix
		// so the wire text matches SMP's copy of this gate. The
		// message already %q-quotes the ref.
		msg := err.Error()
		var se *domain.SpineError
		if errors.As(err, &se) {
			msg = se.Message
		}
		return fmt.Errorf("invalid ref name: %s", msg)
	}
	return nil
}

// isHexOID reports whether s is a SHA-1 (40) or SHA-256 (64) object id
// in hex. Either case is accepted, as git's own parser accepts both.
func isHexOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case '0' <= c && c <= '9', 'a' <= c && c <= 'f', 'A' <= c && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// isCapabilityList reports whether s is printable ASCII (0x20-0x7e), as
// every git client's capability list is (git sanitises agent= and
// session-id= to printable ASCII; JGit, libgit2 and go-git send ASCII
// tokens). A second NUL, a control byte or a non-ASCII space would make
// this gate's token set differ from the feature list git receive-pack
// reads — a C string split on git's isspace.
//
// This is the one place the gate is deliberately stricter than git:
// receive-pack itself accepts a TAB separator or a byte >= 0x80 in the
// list and applies the push. Refusing those costs no real client and is
// what keeps the token-set proof simple; the exception is documented
// (CHANGELOG) and pinned against real git by
// TestPreReceive_CapabilityListStricterThanGit.
func isCapabilityList(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
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
