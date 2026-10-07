package worktreefs

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// TestClean pins the lexical rules (INIT-023 TASK-004 AC2).
//
// Mutation checks: dropping the ".git" comparison in isGitDirName fails the
// ReasonGitDir rows; dropping the HFS fold fails the ".g\u200cit/x" row;
// dropping the ':' cut or the trailing-dot/space strip fails the NTFS rows;
// dropping the backslash split fails the backslash rows, and cutting at ':'
// before splitting fails the `a:b\.git` row.
func TestClean(t *testing.T) {
	long := strings.Repeat("a/", MaxPathBytes/2) + "b" // 4097 bytes
	longComp := strings.Repeat("c", MaxComponentBytes+1)

	tests := []struct {
		in     string
		want   string // cleaned form when accepted
		reason Reason // "" = accepted
		// unix: a backslash is an ordinary byte only off Windows (Clean's
		// filepath.ToSlash turns it into a separator there).
		unix bool
	}{
		// ReasonInvalid.
		{in: "", reason: ReasonInvalid},
		{in: ".", reason: ReasonInvalid},
		{in: "/abs.md", reason: ReasonInvalid},
		{in: "a/../b.md", reason: ReasonInvalid},
		{in: "../x.md", reason: ReasonInvalid},
		{in: "a//b.md", reason: ReasonInvalid},
		{in: "a/./b.md", reason: ReasonInvalid},
		{in: "a\x00b.md", reason: ReasonInvalid},
		{in: "a/", reason: ReasonInvalid},
		{in: long, reason: ReasonInvalid},
		{in: "a/" + longComp, reason: ReasonInvalid},
		{in: longComp, reason: ReasonInvalid},

		// ReasonGitDir: git's verify_path ".git" spellings.
		{in: ".git", reason: ReasonGitDir},
		{in: ".git/x", reason: ReasonGitDir},
		{in: "a/.git/x", reason: ReasonGitDir},
		{in: ".GIT/x", reason: ReasonGitDir},
		{in: ".Git/x", reason: ReasonGitDir},
		{in: ".git./x", reason: ReasonGitDir},
		{in: ".git /x", reason: ReasonGitDir},
		{in: "git~1/x", reason: ReasonGitDir},
		{in: "GIT~1/x", reason: ReasonGitDir},
		{in: ".g\u200cit/x", reason: ReasonGitDir},
		{in: "\ufeff.git/x", reason: ReasonGitDir},
		{in: ".git:x/y", reason: ReasonGitDir},
		{in: ".git. :z/y", reason: ReasonGitDir},
		// git's NTFS rule also ends a name at a backslash and re-checks after
		// every backslash (core.protectNTFS, on by default everywhere):
		// `git add` refuses each of these on Linux and macOS.
		{in: `.git\x`, reason: ReasonGitDir, unix: true},
		{in: `.git\info\attributes`, reason: ReasonGitDir, unix: true},
		{in: `git~1\x`, reason: ReasonGitDir, unix: true},
		{in: `GIT~1\x.md`, reason: ReasonGitDir, unix: true},
		{in: `governance/.git\x.md`, reason: ReasonGitDir, unix: true},
		{in: `a\.git`, reason: ReasonGitDir, unix: true},
		{in: `a:b\.git`, reason: ReasonGitDir, unix: true},
		{in: `x\git~1.`, reason: ReasonGitDir, unix: true},
		{in: `x\.git.\y`, reason: ReasonGitDir, unix: true},
		{in: `x\.GIT :s`, reason: ReasonGitDir, unix: true},

		// Accepted: tracked dotfiles and look-alikes are content.
		{in: ".gitignore", want: ".gitignore"},
		{in: ".gitattributes", want: ".gitattributes"},
		{in: ".github/x.md", want: ".github/x.md"},
		{in: "a.git/x.md", want: "a.git/x.md"},
		{in: "git/x.md", want: "git/x.md"},
		{in: ".git .x/y", want: ".git .x/y"},
		{in: `a\b.md`, want: `a\b.md`, unix: true},
		{in: `.github\x.md`, want: `.github\x.md`, unix: true},
		{in: `a\.gitignore`, want: `a\.gitignore`, unix: true},
		{in: "governance/charter.md", want: "governance/charter.md"},
		{in: strings.Repeat("c", MaxComponentBytes), want: strings.Repeat("c", MaxComponentBytes)},
	}
	for _, tc := range tests {
		name := tc.in
		if len(name) > 40 {
			name = name[:40] + "…"
		}
		t.Run(name, func(t *testing.T) {
			if tc.unix && runtime.GOOS == "windows" {
				t.Skip("a backslash is a separator on Windows")
			}
			got, err := Clean(tc.in)
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("Clean(%q) = %v, want accepted", tc.in, err)
				}
				if got != tc.want {
					t.Fatalf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("Clean(%q) accepted (%q), want %s", tc.in, got, tc.reason)
			}
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Clean(%q) error %v does not unwrap to ErrUnsafePath", tc.in, err)
			}
			var pe *PathError
			if !errors.As(err, &pe) || pe.Reason != tc.reason {
				t.Fatalf("Clean(%q) = %v, want reason %s", tc.in, err, tc.reason)
			}
			// Error text never echoes the raw input (INIT-008 TASK-023;
			// design-check note on PathError). "" is a substring of
			// everything, so it is exempt.
			if tc.reason == ReasonInvalid && tc.in != "" && strings.Contains(err.Error(), tc.in) {
				t.Fatalf("Clean(%q) error %q echoes the input", tc.in, err.Error())
			}
		})
	}
}

func TestClean_GitDirComponentNamesValidatedPrefix(t *testing.T) {
	_, err := Clean("governance/sub/.GIT/info/attributes")
	var pe *PathError
	if !errors.As(err, &pe) || pe.Reason != ReasonGitDir {
		t.Fatalf("want ReasonGitDir, got %v", err)
	}
	if pe.Component != "governance/sub/.GIT" {
		t.Fatalf("Component = %q, want the prefix through the git-dir component", pe.Component)
	}
	if !strings.Contains(err.Error(), `"governance/sub/.GIT"`) || strings.Contains(err.Error(), "attributes") {
		t.Fatalf("message must name the refused prefix and nothing after it: %q", err.Error())
	}
}

// The backslash spellings name the validated component, like any other
// git-dir refusal (before the backslash split, `governance/.git\x.md` passed Clean and
// was a 500 git_error after the file was written, not a 400 before it).
func TestClean_BackslashGitDirNamesComponent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a backslash is a separator on Windows")
	}
	_, err := Clean(`governance/.git\x.md`)
	var pe *PathError
	if !errors.As(err, &pe) || pe.Reason != ReasonGitDir || pe.Component != `governance/.git\x.md` {
		t.Fatalf("want ReasonGitDir naming the component, got %v", err)
	}
}

func TestClean_OSSeparatorAccepted(t *testing.T) {
	// filepath.ToSlash is the identity on Unix; this pins that a slash
	// path round-trips unchanged and a backslash is an ordinary byte there.
	got, err := Clean("a/b.md")
	if err != nil || got != "a/b.md" {
		t.Fatalf("Clean = %q, %v", got, err)
	}
}

func TestSizeError_IsNotUnsafePath(t *testing.T) {
	err := error(&SizeError{Path: "a.md", Size: 10, Limit: 5})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatal("SizeError must unwrap to ErrTooLarge")
	}
	if errors.Is(err, ErrUnsafePath) {
		t.Fatal("SizeError must not unwrap to ErrUnsafePath")
	}
}

func TestPathError_MessagesNameOnlyTheComponent(t *testing.T) {
	for _, r := range []Reason{ReasonGitDir, ReasonSymlink, ReasonNotDir, ReasonNotRegular, ReasonChanged, Reason("other")} {
		pe := &PathError{Path: "RAW-INPUT", Component: "gov/x", Reason: r}
		msg := pe.Error()
		if strings.Contains(msg, "RAW-INPUT") {
			t.Errorf("%s: message echoes Path: %q", r, msg)
		}
		if !strings.Contains(msg, `"gov/x"`) {
			t.Errorf("%s: message does not name the component: %q", r, msg)
		}
	}
}
