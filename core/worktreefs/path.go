// Package worktreefs confines file I/O inside a git working tree
// (INIT-023 TASK-004, security audit 2026-10-06 finding F5).
//
// Every byte Spine writes into a working tree, reads back for rewriting, or
// removes from it goes through a Tree. A Tree enforces four invariants:
//
//   - I1. Every operation is anchored to a directory handle that was opened
//     without following a symbolic link below the tree root. No
//     multi-component path is resolved again after it has been checked.
//   - I2. No path component below the root may be a symbolic link — the
//     final component included, dangling or not, whatever it points at.
//     Such a path is refused with a typed *PathError before any filesystem
//     change.
//   - I3. No path component may name the git directory: ".git", or any of
//     the case, NTFS and HFS spellings git itself treats as ".git". The rule
//     is lexical, so it holds the same way in a main working tree (".git" is
//     a directory) and in a linked worktree (".git" is a file).
//   - I4. New files are created O_CREAT|O_EXCL, so the final component is
//     never followed; updates write a fresh O_EXCL temp file and rename it
//     over the leaf, so they never open the existing file for writing.
//
// Two more bound the resources a hostile tree can consume:
//
//   - I7. Reads are capped (DefaultMaxReadBytes) and refused with *SizeError
//     beyond it; paths are capped at MaxPathBytes / MaxComponentBytes.
//   - I8. Every directory and file handle an operation opens is closed on
//     every return path; Undo.Rollback and Undo.Release are idempotent.
//
// The tree root itself is trusted: it is chosen by the server (the
// repository path, a `git worktree` directory, an embedder's scope dir), and
// symbolic links in the root's own path are followed (macOS /var ->
// /private/var, an operator-symlinked clone root).
//
// The defence is layered. The per-component Lstat walk enforces I2/I3 and an
// os.SameFile identity check binds each descent to the inode that passed the
// Lstat. Underneath, every handle is an os.Root confined to its directory,
// so even a bypassed or buggy walk cannot leave the tree: os.Root refuses
// absolute links and links that climb out of the directory, and the leaf
// primitives (O_EXCL create, renameat, unlinkat) never follow a leaf link.
// os.Root alone is not enough — it follows relative links that stay inside
// the root, including one into ".git/" — which is why the walk exists.
//
// The package imports only the standard library so that core/, adapters/
// and embedders (the Spine Management Platform) can all use it.
package worktreefs

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnsafePath is the sentinel every path refusal unwraps to. Callers use
// errors.Is(err, ErrUnsafePath) to tell a refused path from an I/O failure.
var ErrUnsafePath = errors.New("worktreefs: unsafe path")

// ErrTooLarge is the sentinel a read-limit refusal unwraps to. It is
// deliberately not ErrUnsafePath: an oversized file is a resource limit
// (HTTP 413), not an unsafe path (HTTP 400).
var ErrTooLarge = errors.New("worktreefs: file exceeds the read limit")

const (
	// DefaultMaxReadBytes caps every Tree.ReadFile (I7). It is the same
	// value as the git-blob read cap INIT-023 TASK-003 introduces
	// (`git.DefaultMaxBlobBytes`, governed by TASK-003 owner question 1):
	// an oversized artifact gets the same refusal whether it is read
	// through `git show` or from the worktree. worktreefs is stdlib-only
	// and cannot import that constant, so whichever of the two tasks
	// lands second adds a test asserting the two are equal.
	DefaultMaxReadBytes int64 = 4 << 20

	// MaxPathBytes caps a cleaned path (Linux PATH_MAX). The old
	// os.*-on-a-joined-path code hit this limit implicitly; an
	// fd-anchored walk does not, so the cap is explicit.
	MaxPathBytes = 4096

	// MaxComponentBytes caps one path component (Linux NAME_MAX).
	MaxComponentBytes = 255
)

// Reason classifies a path refusal.
type Reason string

const (
	// ReasonInvalid: empty, NUL, absolute, non-canonical, a "." or ".."
	// component, the root itself, or over a length cap.
	ReasonInvalid Reason = "invalid"
	// ReasonGitDir: a component names the git directory.
	ReasonGitDir Reason = "git_dir"
	// ReasonSymlink: a component (including the leaf) is a symbolic link.
	ReasonSymlink Reason = "symlink"
	// ReasonNotDir: a non-final component exists and is not a directory.
	ReasonNotDir Reason = "not_directory"
	// ReasonNotRegular: the leaf exists and is not a regular file.
	ReasonNotRegular Reason = "not_regular"
	// ReasonChanged: a component changed between its check and its open.
	ReasonChanged Reason = "changed"
)

// PathError is a path refusal. It carries only repo-relative, slash-form
// strings — never an absolute path — so it is safe to return to an API
// caller (INIT-008 TASK-023: errors do not leak filesystem detail).
//
// Error() never echoes Path, the caller's raw input: for ReasonInvalid it
// names only the offending component (or describes the defect), and for
// every other reason it names Component, the validated repo-relative prefix
// that was refused. Path stays on the struct for server-side logs.
type PathError struct {
	Path      string // the cleaned (or, for ReasonInvalid, raw) path that was refused
	Component string // the repo-relative prefix that was refused; for ReasonInvalid the offending component, if any
	Reason    Reason

	detail string // ReasonInvalid only: what is wrong, without the raw input
}

func (e *PathError) Error() string {
	switch e.Reason {
	case ReasonInvalid:
		if e.detail != "" {
			return "unsafe path: " + e.detail
		}
		return "unsafe path: invalid path"
	case ReasonGitDir:
		return fmt.Sprintf("unsafe path: component %q names the git directory", e.Component)
	case ReasonSymlink:
		return fmt.Sprintf("unsafe path: component %q is a symbolic link", e.Component)
	case ReasonNotDir:
		return fmt.Sprintf("unsafe path: component %q is not a directory", e.Component)
	case ReasonNotRegular:
		return fmt.Sprintf("unsafe path: %q is not a regular file", e.Component)
	case ReasonChanged:
		return fmt.Sprintf("unsafe path: component %q changed while it was being checked", e.Component)
	default:
		return fmt.Sprintf("unsafe path: component %q refused (%s)", e.Component, e.Reason)
	}
}

// Unwrap makes every refusal match errors.Is(err, ErrUnsafePath).
func (e *PathError) Unwrap() error { return ErrUnsafePath }

// SizeError is a read refused by the read limit (I7). Size is the leaf's
// size when it was refused before reading, or the number of bytes seen
// (limit+1) when the file grew past the limit while it was being read.
type SizeError struct {
	Path  string // repo-relative, slash form
	Size  int64
	Limit int64
}

func (e *SizeError) Error() string {
	return fmt.Sprintf("file %q is at least %d bytes, over the %d-byte read limit", e.Path, e.Size, e.Limit)
}

// Unwrap makes a size refusal match errors.Is(err, ErrTooLarge).
func (e *SizeError) Unwrap() error { return ErrTooLarge }

func invalid(path, component, detail string) *PathError {
	return &PathError{Path: path, Component: component, Reason: ReasonInvalid, detail: detail}
}

// Clean validates rel lexically and returns its canonical slash form.
//
// rel may use "/" or the OS separator. Clean refuses, with ReasonInvalid:
// an empty path; "." (the tree root itself); an absolute path; a NUL byte;
// any empty, "." or ".." component — even where filepath.Clean would fold
// it away, because a path that needs folding is not one the caller meant —
// a path over MaxPathBytes; and a component over MaxComponentBytes. It
// refuses, with ReasonGitDir, any component that names the git directory
// (see isGitDirName). It never touches the filesystem.
//
// ".gitignore", ".gitattributes", ".github/…", "a.git/…" and "git/…" are
// accepted: they are tracked content an author can push anyway (INIT-023
// TASK-004 owner question 2; the merge-time guard for git-meaningful
// dotfiles belongs to TASK-006).
func Clean(rel string) (string, error) {
	if rel == "" {
		return "", invalid(rel, "", "path is empty")
	}
	if strings.IndexByte(rel, 0) >= 0 {
		return "", invalid(rel, "", "path contains a NUL byte")
	}
	slashed := filepath.ToSlash(rel)
	if strings.HasPrefix(slashed, "/") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", invalid(rel, "", "path must be relative")
	}
	if len(slashed) > MaxPathBytes {
		return "", invalid(rel, "", fmt.Sprintf("path exceeds %d bytes", MaxPathBytes))
	}
	if slashed == "." {
		// Written without a dot: the message must not contain the input.
		return "", invalid(rel, "", "path names the tree root")
	}
	prefix := ""
	for _, c := range strings.Split(slashed, "/") {
		switch c {
		case "":
			return "", invalid(rel, "", "path has an empty component")
		case ".", "..":
			return "", invalid(rel, c, fmt.Sprintf("component %q is not allowed", c))
		}
		if len(c) > MaxComponentBytes {
			return "", invalid(rel, "", fmt.Sprintf("a path component exceeds %d bytes", MaxComponentBytes))
		}
		prefix = joinRel(prefix, c)
		if isGitDirName(c) {
			return "", &PathError{Path: rel, Component: prefix, Reason: ReasonGitDir}
		}
	}
	return slashed, nil
}

// isGitDirName reports whether a single path component names the git
// directory. It mirrors git's own verify_path `.git` handling
// (is_hfs_dotgit / is_ntfs_dotgit), in this order:
//
//  1. drop the code points HFS+ ignores when it compares names
//     (U+200C–U+200F, U+202A–U+202E, U+206A–U+206F, U+FEFF);
//  2. cut at the first ':' (an NTFS alternate data stream: ".git:x" is the
//     git dir on NTFS), then strip trailing '.' and ' ' (NTFS drops them);
//  3. compare ".git" case-insensitively; separately, compare "git~1" (the
//     8.3 short name of ".git") case-insensitively.
//
// On Linux and macOS some of these names are not the git directory, but git
// refuses to check them out anyway (core.protectNTFS / core.protectHFS), so
// refusing them here costs nothing and keeps the rule a faithful mirror.
// Applying both folds to every component makes the rule a superset of
// git's, never a subset.
func isGitDirName(c string) bool {
	var b strings.Builder
	for _, r := range c {
		if !hfsIgnorable(r) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, ". ")
	return strings.EqualFold(s, ".git") || strings.EqualFold(s, "git~1")
}

func hfsIgnorable(r rune) bool {
	switch {
	case r >= 0x200C && r <= 0x200F,
		r >= 0x202A && r <= 0x202E,
		r >= 0x206A && r <= 0x206F,
		r == 0xFEFF:
		return true
	}
	return false
}

func joinRel(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}
