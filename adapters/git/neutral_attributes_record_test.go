package git_test

// INIT-024 integration A (codex, phase 3; phase 4's record): the
// renormalisation marker's record parser trusted its counts, so a damaged
// marker could panic the server (a count past int64's range made the field
// slice's capacity overflow) or make it allocate far past the 64 MiB
// record limit, instead of falling back to the quarantine. The marker is
// untrusted input. Since phase 4 the record holds only the real changes.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

// TestNeutralAttributes_RecordParserRefusesDamage: a damaged marker — a
// count past the file or past the bound, a count that is not plain
// digits, an empty field, a torn record, trailing bytes, the second
// section an earlier build wrote — is "no record", never a panic and
// never a partial record.
func TestNeutralAttributes_RecordParserRefusesDamage(t *testing.T) {
	const h = "spine-renormalise-record v1\n"
	maxN := git.MaxRenormaliseRecordEntries
	cases := []struct {
		name, content string
		ok            bool
	}{
		{"valid", h + "changes 2\na\x00b\x00", true},
		{"valid, empty", h + "changes 0\n", true},
		{"count past int64", h + "changes 9223372036854775807\n", false},
		{"count past the file", h + "changes 1000\na\x00", false},
		{"count past the bound", h + fmt.Sprintf("changes %d\n", maxN+1) + strings.Repeat("a\x00", 4), false},
		{"negative count", h + "changes -1\n", false},
		{"signed count", h + "changes +1\na\x00", false},
		{"leading zero", h + "changes 01\na\x00", false},
		{"no count", h + "changes \n", false},
		{"no space", h + "changes1\na\x00", false},
		{"wrong label", h + "change 1\na\x00", false},
		{"no newline after the count", h + "changes 1", false},
		{"empty path", h + "changes 1\n\x00", false},
		{"field without its NUL", h + "changes 1\na", false},
		{"an earlier build's proof section", h + "changes 0\nproof 1\np\x00blob 1\x00", false},
		{"a proof count that overflows", h + "changes 0\nproof 4611686018427387904\n", false},
		{"trailing bytes", h + "changes 1\na\x00junk", false},
		{"no header", "changes 1\na\x00", false},
	}
	dir := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_"))
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			var ok bool
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("the parser panicked: %v", r)
					}
				}()
				_, ok, err = git.ReadRenormaliseRecordForTest(path)
			}()
			if err != nil || ok != tc.ok {
				t.Fatalf("ok=%v err=%v; want ok=%v", ok, err, tc.ok)
			}
		})
	}

	// The bound itself is accepted, one more is not, and the writer
	// refuses what the reader would.
	at := filepath.Join(dir, "at-the-bound")
	paths := make([]string, maxN)
	for i := range paths {
		paths[i] = "a"
	}
	if err := git.WriteRenormaliseRecordForTest(at, paths); err != nil {
		t.Fatalf("writing %d paths: %v", maxN, err)
	}
	if changes, ok, err := git.ReadRenormaliseRecordForTest(at); err != nil || !ok || len(changes) != maxN {
		t.Fatalf("reading %d paths: ok=%v err=%v n=%d", maxN, ok, err, len(changes))
	}
	if err := git.WriteRenormaliseRecordForTest(filepath.Join(dir, "past-the-bound"), append(paths, "a")); err == nil {
		t.Fatalf("writing %d paths succeeded; want a refusal", maxN+1)
	}
}

// TestNeutralAttributes_DamagedMarkerFallsBackToTheQuarantine: end to end,
// a marker whose count would have panicked the parser makes the next call
// quarantine the tracked state first and then renormalise.
func TestNeutralAttributes_DamagedMarkerFallsBackToTheQuarantine(t *testing.T) {
	ctx := context.Background()
	logs := rnLogs(t)
	repo, _ := rpConverted(t)
	git.SetRenormaliseAfterLineHookForTest(func() error { return errors.New("simulated crash after the line") })
	err := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{})).EnsureNeutralWorkTree(ctx)
	git.SetRenormaliseAfterLineHookForTest(nil)
	if !errors.Is(err, git.ErrRenormalisePending) {
		t.Fatalf("first call: %v; want ErrRenormalisePending", err)
	}
	gd := tbGit(t, repo, nil, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(gd, git.RenormaliseMarkerName), []byte("spine-renormalise-record v1\nchanges 9223372036854775807\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("EnsureNeutralWorkTree panicked on a damaged marker: %v", r)
			}
		}()
		if err := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{})).EnsureNeutralWorkTree(ctx); err != nil {
			t.Fatalf("call over a damaged marker: %v", err)
		}
	}()
	if b := rnRead(t, repo, "a.txt"); b != rpStored {
		t.Errorf("a.txt = %q; want its stored bytes", b)
	}
	if rnMarked(t, repo) {
		t.Error("the marker is still there")
	}
	if !strings.Contains(logs.String(), "quarantined first") || !strings.Contains(logs.String(), "quarantine_sha=") || !strings.Contains(logs.String(), "record=false") {
		t.Errorf("no quarantine Warn for the unreadable record:\n%s", logs.String())
	}
}
