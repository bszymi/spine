package git

// Neutral checkout attributes (INIT-024 TASK-003, review round 2).
//
// The checkout budget sums the blob bytes git stores. What a checkout
// writes can be more: attributes rewrite content on the way out — `ident`,
// `text` / `eol=crlf` (and the host's core.autocrlf / core.eol, which act
// through `text`), `working-tree-encoding`, a `filter=` smudge driver —
// and a conflicted merge writes markers `conflict-marker-size` long. Those
// attributes come from the tree's own .gitattributes, its macros, the
// host's core.attributesFile and the system attributes file. Predicting
// git's attribute parser from outside proved brittle (an attribute scanner
// kept yielding inputs git and it read differently), so a budgeted
// repository turns them off instead:
//
//	* -text -ident -filter !eol !working-tree-encoding !conflict-marker-size
//
// as the last line of $GIT_COMMON_DIR/info/attributes. That file has the
// highest precedence of every attribute source, and within a file a later
// line wins, so the line overrides every other setting of these
// attributes, macros included, for every checkout, merge, restore and
// linked worktree of the repository (worktrees share the common dir). A
// checkout then writes exactly the bytes the listing sums, and a conflict
// marker is 7 characters. `merge` is deliberately left alone: `!merge`
// would change what `binary` and `merge=union` do to a merge.
//
// What it changes for the repository (CHANGELOG, "Changed (operators)"):
// checkouts write stored bytes (an LFS pointer stays a pointer, CRLF is
// not added), `git add` no longer normalises (CRLF content stays CRLF,
// `$Id: … $` is not collapsed), conflict markers are always 7 characters,
// and a tree checked out with conversions before looks modified once,
// until the shared tree's restore resets it.
//
// The writer is opt-in like the budget: `spine serve` calls
// EnsureNeutralCheckoutAttributes for each repository it serves, and a
// budgeted measure refuses (fail closed) on a repository whose attributes
// file does not end in the line (requireNeutralCheckoutAttributes).
// Operator lines in the file are kept.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// NeutralCheckoutAttributes is the attributes line a budgeted repository
// carries last in $GIT_COMMON_DIR/info/attributes. It is the one source
// of the line: an embedder that writes it with its own writer should use
// this constant (and must not manage the same file with a second writer).
const NeutralCheckoutAttributes = "* -text -ident -filter !eol !working-tree-encoding !conflict-marker-size"

// ErrNeutralAttributesMissing is what a budgeted measure on a repository
// without the neutral attributes line matches with errors.Is.
var ErrNeutralAttributesMissing = errors.New("git: the repository's info/attributes does not end in the neutral checkout attributes line")

// NeutralAttributesMissingMessage is the GitError.Message of that refusal.
const NeutralAttributesMissingMessage = "the checkout budget needs the neutral checkout attributes as the last line of info/attributes"

// maxInfoAttributesBytes bounds the read of an existing info/attributes.
const maxInfoAttributesBytes = 1 << 20

// neutralWriteMu serialises this process's writers of info/attributes.
var neutralWriteMu sync.Mutex

// infoAttributesPath returns $GIT_COMMON_DIR/info/attributes of repoDir.
func infoAttributesPath(ctx context.Context, repoDir string) (string, error) {
	cmd := newGitCmd(ctx, "rev-parse", "--git-common-dir")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
	out, err := cmd.Output()
	if err != nil {
		return "", &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot locate the git directory", Err: err}
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoDir, common)
	}
	return filepath.Join(common, "info", "attributes"), nil
}

// readInfoAttributes reads path ("" content when it does not exist). It
// refuses anything but a regular file: a link is never written through.
func readInfoAttributes(path string) ([]byte, bool, error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	case !fi.Mode().IsRegular():
		return nil, true, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path) //nolint:gosec // G304: the repository's own git-administrative file
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxInfoAttributesBytes+1))
	if err != nil {
		return nil, true, err
	}
	if len(b) > maxInfoAttributesBytes {
		return nil, true, fmt.Errorf("%s is larger than %d bytes", path, maxInfoAttributesBytes)
	}
	return b, true, nil
}

// isNeutralLine reports whether line is the neutral line (blanks around it
// and a CR ignored).
func isNeutralLine(line []byte) bool {
	return string(bytes.TrimSpace(line)) == NeutralCheckoutAttributes
}

// endsNeutral reports whether the last attribute line of content — the
// last line that is neither blank nor a comment — is the neutral line.
func endsNeutral(content []byte) bool {
	lines := bytes.Split(content, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		l := bytes.TrimSpace(lines[i])
		if len(l) == 0 || l[0] == '#' {
			continue
		}
		return isNeutralLine(l)
	}
	return false
}

// EnsureNeutralCheckoutAttributes makes NeutralCheckoutAttributes the last
// line of repoDir's $GIT_COMMON_DIR/info/attributes. Every other line is
// kept in order; an earlier copy of the line is moved to the end. It is
// idempotent — a file that already ends in the line is not rewritten — and
// writes atomically (a temporary file renamed into place). A non-regular
// file at the path is refused, never followed.
func EnsureNeutralCheckoutAttributes(ctx context.Context, repoDir string) error {
	path, err := infoAttributesPath(ctx, repoDir)
	if err != nil {
		return err
	}
	neutralWriteMu.Lock()
	defer neutralWriteMu.Unlock()
	have, exists, err := readInfoAttributes(path)
	if err != nil {
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot read info/attributes", Err: err}
	}
	var want bytes.Buffer
	for _, line := range bytes.SplitAfter(have, []byte("\n")) {
		if len(line) == 0 || isNeutralLine(line) {
			continue
		}
		want.Write(line)
		if line[len(line)-1] != '\n' {
			want.WriteByte('\n')
		}
	}
	want.WriteString(NeutralCheckoutAttributes + "\n")
	if exists && bytes.Equal(have, want.Bytes()) {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot create info/", Err: err}
	}
	tmp, err := os.CreateTemp(dir, ".attributes.spine-*")
	if err != nil {
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot write info/attributes", Err: err}
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op after the rename
	if _, err := tmp.Write(want.Bytes()); err != nil {
		_ = tmp.Close()
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot write info/attributes", Err: err}
	}
	if err := tmp.Chmod(0o644); err != nil { //nolint:gosec // G302: git's own mode for info/attributes
		_ = tmp.Close()
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot write info/attributes", Err: err}
	}
	if err := tmp.Close(); err != nil {
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot write info/attributes", Err: err}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot write info/attributes", Err: err}
	}
	return nil
}

// HasNeutralCheckoutAttributes reports whether repoDir's info/attributes
// ends in NeutralCheckoutAttributes.
func HasNeutralCheckoutAttributes(ctx context.Context, repoDir string) (bool, error) {
	path, err := infoAttributesPath(ctx, repoDir)
	if err != nil {
		return false, err
	}
	have, _, err := readInfoAttributes(path)
	if err != nil {
		return false, &GitError{Kind: ErrKindPermanent, Op: "neutral_attributes", Message: "cannot read info/attributes", Err: err}
	}
	return endsNeutral(have), nil
}

// requireNeutralCheckoutAttributes is the measure's precondition: nil
// when the repository ends its info/attributes in the neutral line, the
// permanent ErrNeutralAttributesMissing refusal otherwise.
func (c *CLIClient) requireNeutralCheckoutAttributes(ctx context.Context, op string) error {
	ok, err := HasNeutralCheckoutAttributes(ctx, c.repoPath)
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) {
			cp := *ge
			cp.Op = op
			return &cp
		}
		return err
	}
	if !ok {
		return &GitError{Kind: ErrKindPermanent, Op: op, Message: NeutralAttributesMissingMessage, Err: ErrNeutralAttributesMissing}
	}
	return nil
}
