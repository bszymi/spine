package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Object types a tree entry can record, as `git ls-tree` prints them.
const (
	TreeEntryBlob   = "blob"   // a regular file or a symlink
	TreeEntryTree   = "tree"   // a directory
	TreeEntryCommit = "commit" // a gitlink (submodule entry, mode 160000)
)

// TreeEntryTyper is an optional GitClient capability: report the object
// type a commit's tree records for one path, WITHOUT reading that object.
// *CLIClient implements it. It is not part of GitClient, so embedders and
// test doubles that implement the full interface are unaffected; a
// decorator that wraps a GitClient should forward it.
//
// The projection uses it to tell a read failure that is a property of
// the commit (there is no file at that path — a gitlink, a directory, no
// entry at all) from one that is a property of git or the host (a lock,
// a killed or unspawnable process, a missing object). It must not decide
// that from git's stderr: `git show <sha>:<path>` echoes the committed,
// attacker-chosen path, so stderr substrings such as "lock" or "timeout"
// in a file name would steer the classification (INIT-023 TASK-011
// review round 1).
//
// The projection also asks it BEFORE reading a governance file (review
// round 2): `git show <sha>:<path>` prints whatever object the entry's
// recorded id names, so a gitlink recording the id of a blob in this
// repository reads successfully — content no diff shows. A successful
// read therefore does not prove the path holds a file; the entry type
// does.
type TreeEntryTyper interface {
	// TreeEntryType returns TreeEntryBlob, TreeEntryTree or
	// TreeEntryCommit for the entry at exactly path in ref's tree, or ""
	// when the tree has no entry at that path. An error means the
	// question could not be answered (the tree is unreadable, git could
	// not run); it never means "absent".
	TreeEntryType(ctx context.Context, ref, path string) (string, error)
}

var _ TreeEntryTyper = (*CLIClient)(nil)

// TreeEntryType implements TreeEntryTyper with
// `git --literal-pathspecs ls-tree -z --full-tree <ref> -- <path>`:
//   - ls-tree reads only tree objects, so a gitlink whose commit is not
//     in this repository, or a blob that is missing, is still typed;
//   - --literal-pathspecs keeps `*`, `?` and `:(magic)` in a committed
//     path literal;
//   - -z prints paths raw (no core.quotePath quoting) and NUL-terminated;
//   - --full-tree makes the path repository-relative wherever the
//     command runs.
//
// git normalises the pathspec (a `..` component, for example), so only
// an output record whose path is byte-equal to the requested one counts;
// anything else is "no entry at that path".
func (c *CLIClient) TreeEntryType(ctx context.Context, ref, path string) (string, error) {
	if ref == "" {
		return "", errors.New("tree entry: empty ref")
	}
	if err := ValidateRef(ref); err != nil {
		return "", fmt.Errorf("tree entry: %w", err)
	}
	if path == "" {
		return "", errors.New("tree entry: empty path")
	}
	output, err := c.run(ctx, "tree_entry", "--literal-pathspecs", "ls-tree", "-z", "--full-tree", ref, "--", path)
	if err != nil {
		return "", err
	}
	return parseTreeEntryType(output, path)
}

// parseTreeEntryType reads `ls-tree -z` records ("<mode> SP <type> SP
// <object> TAB <path> NUL") and returns the type of the record whose path
// equals want, or "" when there is none. A record that does not have the
// documented shape is an error, not "absent": a misread listing must not
// be taken as proof that no file exists.
func parseTreeEntryType(output, want string) (string, error) {
	for _, rec := range strings.Split(output, "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			return "", fmt.Errorf("tree entry: unexpected ls-tree record %q", truncateRecord(rec))
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return "", fmt.Errorf("tree entry: unexpected ls-tree record %q", truncateRecord(rec))
		}
		switch fields[1] {
		case TreeEntryBlob, TreeEntryTree, TreeEntryCommit:
		default:
			return "", fmt.Errorf("tree entry: unexpected object type %q", truncateRecord(fields[1]))
		}
		if p == want {
			return fields[1], nil
		}
	}
	return "", nil
}

func truncateRecord(s string) string {
	const n = 120
	if len(s) <= n {
		return s
	}
	return s[:n]
}
