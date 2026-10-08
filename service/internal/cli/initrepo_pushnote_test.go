package cli

import (
	"strings"
	"testing"
)

// TestPushFailureNote: a Spine gateway's refusal of spine/init reaches
// the user with its cause and the operator-token remedy, quoting only
// the server's refusal lines (INIT-023 TASK-016 review round 1); any
// other push failure keeps the old "remote may not be configured" note.
func TestPushFailureNote(t *testing.T) {
	const generic = "Note: could not push to origin (remote may not be configured)"

	admission := "remote: ref-admission: spine/init is reserved for 'spine init-repo'; operator role required        \n" +
		"To http://user:s3cret@gateway.example/git/ws1\n" +
		" ! [remote rejected] spine/init -> spine/init (pre-receive hook declined)\n" +
		"error: failed to push some refs to 'http://user:s3cret@gateway.example/git/ws1'\n"
	got := pushFailureNote(admission)
	for _, want := range []string{
		"origin refused the push; spine/init exists only locally",
		"ref-admission: spine/init is reserved for 'spine init-repo'; operator role required",
		"with an operator token",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("admission refusal: note lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "s3cret") || strings.Contains(got, "gateway.example") {
		t.Errorf("note quotes the remote URL:\n%s", got)
	}

	bp := "remote: branch-protection: rule \"no-direct-write\" blocks this operation on branch \"refs/heads/spine/init\"\n"
	if got := pushFailureNote(bp); !strings.Contains(got, `branch-protection: rule "no-direct-write"`) || strings.Contains(got, "operator token") {
		t.Errorf("branch-protection refusal: got\n%s", got)
	}

	if got := pushFailureNote("remote: ref-admission: \x1b[31mred\x07\n"); strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("control bytes reached the note: %q", got)
	}

	for _, out := range []string{
		"",
		"fatal: 'origin' does not appear to be a git repository\nfatal: Could not read from remote repository.",
		"remote: Counting objects: 3, done.\nfatal: unable to access 'https://x/': Could not resolve host: x",
	} {
		if got := pushFailureNote(out); got != generic {
			t.Errorf("pushFailureNote(%q) = %q, want the generic note", out, got)
		}
	}
}
