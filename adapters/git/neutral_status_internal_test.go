package git

// INIT-024 integration A, phase 5: the renormalisation's status is read
// through the listings' record reader (statusCollector) and held to a
// tree listing's bounds, whatever the pipe's chunking.

import (
	"errors"
	"strings"
	"testing"
)

func feedStatus(s *statusCollector, out string, chunk int) error {
	for out != "" {
		n := min(chunk, len(out))
		if _, err := s.Write([]byte(out[:n])); err != nil {
			return err
		}
		out = out[n:]
	}
	return nil
}

func TestStatusCollector_ParsesAndBounds(t *testing.T) {
	valid := " M a.txt\x00MM dir/b c\x00A  new\x00 D gone\x00"
	for _, chunk := range []int{1, 3, 7, len(valid)} {
		s := newStatusCollector("neutral_attributes", listingBounds{bytes: 1 << 20, records: 100})
		if err := feedStatus(s, valid, chunk); err != nil || s.refusal() != nil || s.unterminated() {
			t.Fatalf("chunk %d: %v %v unterminated=%v", chunk, err, s.refusal(), s.unterminated())
		}
		want := []trackedChange{{' ', 'M', "a.txt"}, {'M', 'M', "dir/b c"}, {'A', ' ', "new"}, {' ', 'D', "gone"}}
		if len(s.changes) != len(want) {
			t.Fatalf("chunk %d: %d changes", chunk, len(s.changes))
		}
		for i := range want {
			if s.changes[i] != want[i] {
				t.Errorf("chunk %d: change %d = %+v; want %+v", chunk, i, s.changes[i], want[i])
			}
		}
	}

	refused := func(name, out string, b listingBounds, wantRecords, wantSize bool) {
		t.Helper()
		for _, chunk := range []int{1, 5, len(out)} {
			s := newStatusCollector("neutral_attributes", b)
			err := feedStatus(s, out, chunk)
			r := s.refusal()
			if err == nil || r == nil {
				t.Errorf("%s (chunk %d): not refused", name, chunk)
				continue
			}
			var ote *OutputTooLargeError
			isSize := errors.As(r, &ote)
			if isSize != (wantRecords || wantSize) || (isSize && ote.Records != wantRecords) {
				t.Errorf("%s (chunk %d): refusal %v", name, chunk, r)
			}
		}
	}
	refused("no space after the status", " Ma.txt\x00", listingBounds{bytes: 1 << 20, records: 100}, false, false)
	refused("too short", " M\x00", listingBounds{bytes: 1 << 20, records: 100}, false, false)
	refused("past the record ceiling", strings.Repeat(" M a\x00", 4), listingBounds{bytes: 1 << 20, records: 3}, true, false)
	refused("past the kept bytes", " M "+strings.Repeat("p", 100)+"\x00 M "+strings.Repeat("q", 100)+"\x00", listingBounds{bytes: 150, records: 100}, false, true)
	refused("an unfinished path past the room", " M "+strings.Repeat("p", 8000), listingBounds{bytes: 5000, records: 100}, false, true)

	s := newStatusCollector("neutral_attributes", listingBounds{bytes: 1 << 20, records: 100})
	if err := feedStatus(s, " M a.txt\x00 M b", 4); err != nil || !s.unterminated() {
		t.Errorf("a status ending inside a record: err=%v unterminated=%v; want it unterminated", err, s.unterminated())
	}
}
