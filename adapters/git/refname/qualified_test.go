package refname

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/bszymi/spine/core/domain"
)

// qualifiedCases is the ValidateQualified corpus (INIT-023 TASK-001).
// SMP's temporary copy of the grammar (INIT-027 EPIC-004 TASK-001,
// qualifiedRefCases) carries the same entries in the same order; keep
// the two in step until SMP calls ValidateQualified directly.
var qualifiedCases = []struct {
	ref   string
	valid bool
}{
	{"refs/heads/main", true},
	{"refs/heads/spine/run/task-001-abcd1234", true},
	{"refs/tags/v1.0.0", true},
	{"refs/foo", true}, // one-level: legal for delete; git refuses create itself
	{"refs/heads/-x", true},
	{"refs/heads/a./b", true},
	{"refs/heads/a{b", true},
	{"refs/heads/a@b", true},
	{"refs/heads/@", true},
	{"refs/heads/é", true},
	{"refs/heads/a\u00a0b", true}, // NBSP (escaped, never a literal): git accepts bytes >= 0x80
	{"main", false},
	{"heads/main", false},
	{"spine/run/x", false},
	{"refs/", false},
	{"refs/heads/main\x00", false},
	{"refs/heads/main\x00report-status", false},
	{"refs/heads/ma\x00in", false},
	{"refs/heads/main\r", false},
	{"refs/heads/main\n", false},
	{"refs/heads/main\t", false},
	{"refs/heads/main\x1f", false},
	{"refs/heads/main\x7f", false},
	{"refs/heads/a b", false},
	{"refs/heads/a~1", false},
	{"refs/heads/a^", false},
	{"refs/heads/a:b", false},
	{"refs/heads/a?", false},
	{"refs/heads/a*", false},
	{"refs/heads/a[b", false},
	{"refs/heads/a\\b", false},
	{"refs/heads/a..b", false},
	{"refs/heads/a@{1}", false},
	{"refs/heads/a.", false},
	{"refs/heads/.a", false},
	{"refs/heads/a/.b", false},
	{"refs/heads/a.lock", false},
	{"refs/heads/a.lock/b", false},
	{"refs/heads//a", false},
	{"refs/heads/a/", false},
	{"refs//heads/a", false},
}

// rawControlBytes are the bytes an error message must never carry raw:
// every one of them is %q-escaped by ValidateQualified so neither a log
// line nor a git client's side-band output can be forged or truncated.
const rawControlBytes = "\x00\r\n\t\v\x1f\x7f"

func TestValidateQualified(t *testing.T) {
	for _, tc := range qualifiedCases {
		err := ValidateQualified(tc.ref)
		if tc.valid {
			if err != nil {
				t.Errorf("ValidateQualified(%q) = %v, want nil", tc.ref, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("ValidateQualified(%q) = nil, want error", tc.ref)
			continue
		}
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrInvalidParams {
			t.Errorf("ValidateQualified(%q) error %v is not a *domain.SpineError with ErrInvalidParams", tc.ref, err)
		}
		if strings.ContainsAny(err.Error(), rawControlBytes) {
			t.Errorf("ValidateQualified(%q) error carries a raw control byte: %q", tc.ref, err)
		}
	}
}

// TestQualifiedCases_KeepNonASCIIAcceptRows pins the corpus rows that
// prove the grammar is never stricter than git on bytes >= 0x80 (AC 5):
// a precomposed é and an NBSP. The NBSP is written as an escape, never
// as a literal: an editor or formatter that folded a literal NBSP into
// an ASCII space would turn this accept row into a confusing duplicate
// of the `a b` reject row and drop the non-ASCII coverage. If either
// row is lost or rewritten, this test says so plainly.
func TestQualifiedCases_KeepNonASCIIAcceptRows(t *testing.T) {
	for _, want := range []string{"refs/heads/\u00e9", "refs/heads/a\u00a0b"} {
		found := false
		for _, tc := range qualifiedCases {
			if tc.ref != want {
				continue
			}
			found = true
			if !tc.valid {
				t.Errorf("corpus row %q must be an accept row (git accepts bytes >= 0x80)", want)
			}
		}
		if !found {
			t.Errorf("corpus lost its non-ASCII accept row %q", want)
		}
	}
}

// TestNoLiteralInvisibleCharactersInTestSources keeps the non-ASCII corpus rows
// reviewable at the byte level. An invisible character written
// literally (U+00A0 NBSP, a zero-width space, a BOM, U+2028) cannot be
// told apart from its visible look-alike, and an editor may silently
// fold it into an ASCII space, so a row could lose its coverage without
// failing. Such characters must be written as escapes (\u00a0).
// INIT-023 TASK-001 review round 1 found exactly that, twice.
func TestNoLiteralInvisibleCharactersInTestSources(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test sources found (%v)", err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			for _, r := range line {
				if r > 0x7f && (unicode.IsSpace(r) || unicode.Is(unicode.Cf, r)) {
					t.Errorf("%s:%d: literal U+%04X; write it as an escape", f, i+1, r)
				}
			}
		}
	}
}

// gitAccepts reports git's verdict on ref, restricted to names under
// refs/ — the oracle ValidateQualified must match ("never stricter,
// never looser than git").
func gitAccepts(t *testing.T, ref string) bool {
	t.Helper()
	ok := exec.Command("git", "check-ref-format", "--allow-onelevel", ref).Run() == nil
	return ok && strings.HasPrefix(ref, "refs/")
}

// TestValidateQualified_AgreesWithGit pins "never stricter, never looser
// than git": for every corpus entry git can take on argv (no NUL), the
// verdict must equal `git check-ref-format --allow-onelevel` restricted
// to names under refs/. It also checks the corpus itself against git, so
// a wrong expectation in the table cannot hide a wrong grammar.
func TestValidateQualified_AgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	for _, tc := range qualifiedCases {
		if strings.IndexByte(tc.ref, 0) >= 0 {
			continue // argv cannot carry a NUL; the table pins these rows
		}
		want := gitAccepts(t, tc.ref)
		if got := ValidateQualified(tc.ref) == nil; got != want {
			t.Errorf("%q: ValidateQualified ok=%v, git(+refs/ prefix) ok=%v", tc.ref, got, want)
		}
		if want != tc.valid {
			t.Errorf("%q: corpus says valid=%v but git says %v — fix the table", tc.ref, tc.valid, want)
		}
	}
}

// TestValidateQualified_ByteSweepAgreesWithGit extends the agreement
// from the corpus to every byte value: each byte 0x01-0x7f, plus 0x80,
// 0xc2 and 0xff (a lone continuation byte, a truncated lead byte, and a
// byte that never occurs in UTF-8), is placed in the middle of a
// component, at the start of a component and at the end of the ref.
// Every verdict must equal git's. 390 git spawns, a few seconds.
func TestValidateQualified_ByteSweepAgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	var sweep []byte
	for b := 0x01; b <= 0x7f; b++ {
		sweep = append(sweep, byte(b))
	}
	sweep = append(sweep, 0x80, 0xc2, 0xff)
	checked := 0
	for _, b := range sweep {
		c := string([]byte{b})
		for _, ref := range []string{
			"refs/heads/a" + c + "b", // mid-component
			"refs/heads/" + c + "x",  // component start
			"refs/heads/x" + c,       // ref end
		} {
			checked++
			want := gitAccepts(t, ref)
			err := ValidateQualified(ref)
			if got := err == nil; got != want {
				t.Errorf("byte 0x%02x in %q: ValidateQualified ok=%v, git(+refs/ prefix) ok=%v", b, ref, got, want)
			}
			if err != nil && strings.ContainsAny(err.Error(), rawControlBytes) {
				t.Errorf("byte 0x%02x: error carries a raw control byte: %q", b, err)
			}
		}
	}
	if checked != 390 {
		t.Fatalf("sweep checked %d refs, want 390", checked)
	}
}
