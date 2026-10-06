package githttp

import (
	"bytes"
	"strings"
	"testing"
)

// rawControlBytes are bytes a parse error must never carry raw: the
// error text is echoed to the client's side-band ("malformed push: …")
// and logged, so every untrusted byte is %q-escaped.
const rawControlBytes = "\x00\r\v\x7f"

// TestParseRefUpdates_RejectsMalformedCommandOnAnyLine pins INIT-023
// TASK-001 (audit F1): capabilities are split off line 1 only, every
// line's ref and object ids are validated, and the error names the
// command that failed. Before the fix a NUL after the ref on line 2+
// parsed as part of the ref, so the policy evaluated
// "refs/heads/main\x00" (no rule matches) while git updated
// "refs/heads/main".
func TestParseRefUpdates_RejectsMalformedCommandOnAnyLine(t *testing.T) {
	o := strings.Repeat("1", 40)
	n := strings.Repeat("a", 40)
	line1 := o + " " + n + " refs/heads/feature\x00report-status side-band-64k\n"
	cases := []struct {
		name    string
		frames  []string // raw pkt-line payloads; nil-terminated by a flush
		raw     []byte   // used instead of frames when set
		wantErr string   // substring; "" = must parse
	}{
		// Parsed.
		{name: "caps on line 1 still parsed", frames: []string{line1, o + " " + n + " refs/heads/other\n"}},
		{name: "sha256 oids", frames: []string{strings.Repeat("1", 64) + " " + strings.Repeat("a", 64) + " refs/heads/x\x00report-status\n"}},
		{name: "uppercase hex oids", frames: []string{strings.ToUpper(o) + " " + strings.ToUpper(n) + " refs/heads/x\x00report-status\n"}},
		// git send-pack sends no trailing LF on any command line.
		{name: "no trailing LF (what git send-pack sends)", frames: []string{
			o + " " + n + " refs/heads/feature\x00report-status side-band-64k",
			o + " " + n + " refs/heads/other",
		}},
		{name: "real-shaped capability list", frames: []string{
			o + " " + n + " refs/heads/x\x00report-status report-status-v2 side-band-64k quiet atomic push-options object-format=sha1 agent=git/2.39.5\n",
		}},

		// Refused: NUL on a later line (the F1 bypass shapes).
		{name: "line 2 NUL after ref", frames: []string{line1, o + " " + n + " refs/heads/main\x00\n"}, wantErr: "command 2"},
		{name: "line 2 NUL then caps", frames: []string{line1, o + " " + n + " refs/heads/main\x00report-status\n"}, wantErr: "command 2"},
		{name: "line 2 NUL without trailing LF", frames: []string{line1, o + " " + n + " refs/heads/main\x00"}, wantErr: "command 2"},
		{name: "line 3 NUL after ref", frames: []string{line1, o + " " + n + " refs/heads/x\n", o + " " + n + " refs/heads/main\x00\n"}, wantErr: "command 3"},
		{name: "line 3 NUL then caps", frames: []string{line1, o + " " + n + " refs/heads/x\n", o + " " + n + " refs/heads/main\x00report-status push-options\n"}, wantErr: "command 3"},

		// Refused: other bytes git reads differently or refuses.
		{name: "line 2 CR", frames: []string{line1, o + " " + n + " refs/heads/main\r\n"}, wantErr: "command 2"},
		{name: "line 2 DEL", frames: []string{line1, o + " " + n + " refs/heads/main\x7f\n"}, wantErr: "command 2"},
		{name: "line 2 space in ref", frames: []string{line1, o + " " + n + " refs/heads/ma in\n"}, wantErr: "command 2"},
		{name: "line 2 tab in ref", frames: []string{line1, o + " " + n + " refs/heads/main\t\n"}, wantErr: "command 2"},
		{name: "line 2 second LF kept", frames: []string{line1, o + " " + n + " refs/heads/main\n\n"}, wantErr: "command 2"},
		{name: "line 1 control byte before caps", frames: []string{o + " " + n + " refs/heads/ma\rin\x00report-status\n"}, wantErr: "command 1"},
		{name: "bare branch name", frames: []string{o + " " + n + " main\x00report-status\n"}, wantErr: "command 1: invalid ref name"},
		{name: "bare spine/run name", frames: []string{line1, o + " " + n + " spine/run/x\n"}, wantErr: "command 2: invalid ref name"},
		{name: "dot-dot", frames: []string{line1, o + " " + n + " refs/heads/a..b\n"}, wantErr: "command 2"},
		{name: "reflog syntax", frames: []string{line1, o + " " + n + " refs/heads/a@{1}\n"}, wantErr: "command 2"},
		{name: ".lock component", frames: []string{line1, o + " " + n + " refs/heads/a.lock\n"}, wantErr: "command 2"},
		{name: "trailing slash", frames: []string{line1, o + " " + n + " refs/heads/a/\n"}, wantErr: "command 2"},
		{name: "short old oid", frames: []string{line1, "abc " + n + " refs/heads/x\n"}, wantErr: "command 2: malformed object ids"},
		{name: "non-hex new oid", frames: []string{line1, o + " " + strings.Repeat("g", 40) + " refs/heads/x\n"}, wantErr: "command 2: malformed object ids"},
		{name: "mixed oid lengths", frames: []string{line1, o + " " + strings.Repeat("a", 64) + " refs/heads/x\n"}, wantErr: "command 2: malformed object ids"},

		// Refused: the line-1 capability list must be printable ASCII, so
		// the gate's token set equals the feature list git reads (a C
		// string split on git's isspace). Parity with SMP INIT-027
		// EPIC-004 TASK-001 §1 step 3a.
		{name: "caps: second NUL hides push-options from git", frames: []string{o + " " + n + " refs/heads/x\x00report-status\x00 push-options\n"}, wantErr: "command 1: malformed capability list"},
		{name: "caps: second NUL hides push-options from the gate", frames: []string{o + " " + n + " refs/heads/x\x00report-status push-options\x00x\n"}, wantErr: "command 1: malformed capability list"},
		{name: "caps: vertical tab", frames: []string{o + " " + n + " refs/heads/x\x00report-status\vpush-options\n"}, wantErr: "command 1: malformed capability list"},
		{name: "caps: CR", frames: []string{o + " " + n + " refs/heads/x\x00report-status\rpush-options\n"}, wantErr: "command 1: malformed capability list"},
		{name: "caps: TAB", frames: []string{o + " " + n + " refs/heads/x\x00report-status\tpush-options\n"}, wantErr: "command 1: malformed capability list"},
		{name: "caps: DEL", frames: []string{o + " " + n + " refs/heads/x\x00report-status\x7fpush-options\n"}, wantErr: "command 1: malformed capability list"},
		{name: "caps: UTF-8 NEL", frames: []string{o + " " + n + " refs/heads/x\x00report-status\xc2\x85push-options\n"}, wantErr: "command 1: malformed capability list"},

		// Still refused (pinned; shapes the gate never parsed as commands).
		{name: "shallow line ahead of the first command", frames: []string{"shallow " + o + "\n", line1}, wantErr: "command 1: malformed ref update"},
		{name: "shallow line with a third field", frames: []string{"shallow " + o + " refs/heads/x\n", line1}, wantErr: "command 1: malformed object ids"},
		{name: "push-cert frame", frames: []string{"push-cert\x00report-status\n", line1}, wantErr: "command 1: malformed ref update"},
		{name: "empty pkt-line", frames: []string{line1, ""}, wantErr: "command 2: malformed ref update"},
		{name: "0001 delim packet", raw: append(append(pktLine(line1), "0001"...), flushPkt...), wantErr: "below minimum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.raw
			if body == nil {
				for _, f := range tc.frames {
					body = append(body, pktLine(f)...)
				}
				body = append(body, flushPkt...)
			}
			updates, caps, err := parseRefUpdates(bytes.NewReader(body))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(updates) != len(tc.frames) {
					t.Fatalf("got %d updates, want %d", len(updates), len(tc.frames))
				}
				if _, ok := caps["report-status"]; !ok {
					t.Errorf("line-1 caps lost: %v", caps)
				}
				for _, u := range updates {
					if strings.ContainsAny(u.Ref, "\x00\n") {
						t.Errorf("ref carries a NUL or LF after parsing: %q", u.Ref)
					}
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got updates %+v", tc.wantErr, updates)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
			if strings.ContainsAny(err.Error(), rawControlBytes) {
				t.Errorf("error message carries a raw control byte: %q", err)
			}
		})
	}
}

// TestParseRefUpdates_CapabilitiesFromLine1Only pins that the
// capability set is exactly line 1's, and an empty list ("ref\x00") is
// accepted: git clients send capabilities on the first command only.
func TestParseRefUpdates_CapabilitiesFromLine1Only(t *testing.T) {
	o, n := strings.Repeat("1", 40), strings.Repeat("a", 40)

	body := append(pktLine(o+" "+n+" refs/heads/a\x00report-status push-options\n"), pktLine(o+" "+n+" refs/heads/b\n")...)
	body = append(body, flushPkt...)
	updates, caps, err := parseRefUpdates(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(caps) != 2 {
		t.Errorf("caps = %v, want exactly report-status and push-options", caps)
	}
	if updates[0].Ref != "refs/heads/a" || updates[1].Ref != "refs/heads/b" {
		t.Errorf("refs = %+v", updates)
	}

	body = append(pktLine(o+" "+n+" refs/heads/a\x00\n"), flushPkt...)
	updates, caps, err = parseRefUpdates(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("empty capability list refused: %v", err)
	}
	if len(caps) != 0 || len(updates) != 1 || updates[0].Ref != "refs/heads/a" {
		t.Errorf("empty caps: updates=%+v caps=%v", updates, caps)
	}
}

func TestRefUpdate_IsDeleteRecognisesBothNullOIDs(t *testing.T) {
	if len(zeroSHA) != 40 || len(zeroSHA256) != 64 || strings.Trim(zeroSHA256, "0") != "" {
		t.Fatalf("null ids malformed: %q / %q", zeroSHA, zeroSHA256)
	}
	if !(RefUpdate{NewSHA: zeroSHA}).IsDelete() || !(RefUpdate{NewSHA: zeroSHA256}).IsDelete() {
		t.Fatal("40- and 64-zero new oids must both classify as delete")
	}
	for _, nonDelete := range []string{strings.Repeat("a", 40), strings.Repeat("a", 64), strings.Repeat("0", 39) + "1"} {
		if (RefUpdate{NewSHA: nonDelete}).IsDelete() {
			t.Errorf("non-null oid %q classified as delete", nonDelete)
		}
	}
}
