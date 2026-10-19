package projection

import (
	"context"
	"errors"
	"fmt"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/observe"
)

// Tree-entry descriptions for a read failure the projection has proved
// deterministic (logged, and named in a not_a_file rejection).
const (
	entryGitlink        = "gitlink"         // mode 160000: no content in this repository
	entryTree           = "tree"            // a directory
	entryNone           = "none"            // the commit's tree has no entry at that exact path
	entryContentRefused = "content_refused" // isContentReadError: the blob exists but its content is refused
	// entryNonBlobObject: a file entry (a blob mode) whose recorded object
	// is not a blob — a tag, a tree or a commit (git.NotBlobError with
	// FileEntry; adversarial review of the wave 4 follow-up). The mode is
	// all TreeEntryType sees, so only the git client's read, which types
	// the object itself, can prove it.
	entryNonBlobObject = "non_blob_object"
)

// classifyReadFailure decides whether a failed ReadFile of path at ref is
// a property of the COMMIT — so retrying can never succeed and holding
// the sync on it would wedge it until someone changes the file — or of
// git or the host, which must hold the sync (status error,
// last_synced_commit unchanged) until a retry succeeds.
//
// The deterministic class is proved POSITIVELY, never inferred from what
// is left over (INIT-023 TASK-011 review round 1):
//
//   - git's stderr is not consulted. `git show <sha>:<path>` echoes the
//     committed path, which the author chooses, and the git client's Kind
//     comes from stderr substrings ("lock", "timeout", "Could not
//     resolve", …). A gitlink named docs/blocked.md was classified
//     transient — a plantable wedge — and a git killed by a signal, or
//     one that could not be spawned, has empty stderr and was classified
//     permanent — a silent skip that advanced the sync past a change it
//     never applied.
//   - Instead the commit's tree is asked what is at that path
//     (git.TreeEntryTyper, which reads tree objects only). No file (blob)
//     there — a gitlink, a directory, or no entry at that exact
//     path (a quoted or normalised path the diff produced) — is
//     deterministic: there is no content to read at that commit, ever.
//     A blob there means the content exists and git failed to read it
//     (a lock, a killed or unspawnable process, a missing object):
//     infrastructure. Pushes cannot plant a missing blob — receive-pack's
//     connectivity check refuses it; a gitlink is the one non-file entry
//     at a file path a push can carry (its recorded commit is never
//     checked). Workflow files and the branch-protection config are
//     typed BEFORE they are read (readFileEntry), so with the capability
//     they never get here; this classifier serves changed-artifact reads
//     and readFileEntry's fallback for a client without it.
//   - isContentReadError (a blob whose content is refused by a size bound,
//     registered by the bounded-read work) is deterministic too, and so
//     is the git client's own not-a-file refusal (git.ErrNotBlob, wave 4
//     follow-up), which is described as the entry it names (notBlobEntry).
//     That refusal is consulted BEFORE the tree: a file entry recording a
//     tree or a commit (or a tag) is a "blob" to TreeEntryType, which reads
//     only the mode, and taking that answer would make the failed read
//     infrastructure — a wedge any push could plant (adversarial review of
//     the follow-up: `cat-file blob` failed such an entry with a plain
//     "bad file"; ReadFile now types the object and refuses it).
//   - Anything that cannot be proved — a cancelled context, a git client
//     without the TreeEntryTyper capability, a failure to read the tree —
//     is infrastructure. Holding the sync on an unproved failure is
//     recoverable; skipping it silently is not.
//
// It returns the tree-entry description and a nil error for a
// deterministic failure, or a non-nil infrastructure error.
func (s *Service) classifyReadFailure(ctx context.Context, ref, path string, readErr error) (string, error) {
	if readErr == nil {
		readErr = errors.New("read failed with no error detail")
	}
	if ctx.Err() != nil {
		return "", readErr
	}
	if entry, ok := notBlobEntry(readErr); ok {
		return entry, nil
	}
	if isContentReadError(readErr) {
		return entryContentRefused, nil
	}
	typer, ok := s.git.(git.TreeEntryTyper)
	if !ok {
		return "", readErr
	}
	typ, err := typer.TreeEntryType(ctx, ref, path)
	if err != nil {
		return "", errors.Join(readErr, fmt.Errorf("type the tree entry: %w", err))
	}
	entry, err := nonFileEntry(typ)
	if err != nil {
		return "", errors.Join(readErr, err)
	}
	if entry == "" {
		// A blob: the content exists, so the read failure is git's or
		// the host's.
		return "", readErr
	}
	return entry, nil
}

// nonFileEntry maps a TreeEntryType answer to the description of a
// deterministic "no file here" (entryGitlink, entryTree, entryNone), or ""
// for a blob. An answer outside the documented set — only a foreign
// TreeEntryTyper can give one — proves nothing and is an error.
func nonFileEntry(typ string) (string, error) {
	switch typ {
	case git.TreeEntryBlob:
		return "", nil
	case git.TreeEntryCommit:
		return entryGitlink, nil
	case git.TreeEntryTree:
		return entryTree, nil
	case "":
		return entryNone, nil
	default:
		return "", fmt.Errorf("unexpected tree entry type %q", truncateForLog(typ, 40))
	}
}

// notBlobEntry maps the git client's not-a-file refusal (a
// *git.NotBlobError: ReadFile typed the entry, or the object a file entry
// records, and refused it unread) to entryGitlink, entryTree or
// entryNonBlobObject. ok is false for any other error, and for a refusal
// of a type it does not document — that one is still isContentReadError,
// so it stays deterministic as refused content.
func notBlobEntry(err error) (string, bool) {
	var nb *git.NotBlobError
	if !errors.As(err, &nb) {
		return "", false
	}
	if nb.FileEntry {
		switch nb.Type {
		case git.ObjectTag, git.TreeEntryTree, git.TreeEntryCommit:
			return entryNonBlobObject, true
		}
		return "", false
	}
	switch nb.Type {
	case git.TreeEntryCommit:
		return entryGitlink, true
	case git.TreeEntryTree:
		return entryTree, true
	}
	return "", false
}

// unreadableEntry is readFileEntry's deterministic outcome: the commit
// holds no content the projection may read at the path, ever.
type unreadableEntry struct {
	entry   string // entryGitlink, entryTree, entryNone, entryNonBlobObject or entryContentRefused
	readErr error  // the failed read, when one was attempted (entryContentRefused)
}

// cause is the error a rejection or a sync error carries.
func (u *unreadableEntry) cause() error {
	if u.entry == entryContentRefused {
		return u.readErr
	}
	return fmt.Errorf("no file at this path (tree entry: %s)", u.entry)
}

// readFileEntry reads path at ref only when ref's tree records a file (a
// blob) at exactly that path (INIT-023 TASK-011 review round 2). It is
// the read for the governance files the projection is the authority for
// — workflow definitions and the branch-protection config.
//
// The entry is typed BEFORE the read, not only after a failed one.
// `git show <ref>:<path>` resolves the path to the object id the tree
// entry records and prints that object whatever the entry's mode. A
// gitlink (mode 160000) is meant to name a commit in another repository,
// but nothing checks what it names: a push can record the id of a blob
// that is in this repository (any pushed branch can put one in the
// object store). The read then SUCCEEDS and returns content no diff or
// repository viewer shows — they show "Subproject commit <id>" — so a
// reviewed workflow could be replaced by a review-less one, or the
// branch-protection rules relaxed, with nothing visible to review.
//
// The entry's mode is not proof either (adversarial review of the wave 4
// follow-up): a file entry can record the id of a tag, a tree or a commit,
// and `git cat-file blob` peeled such a tag to its target blob — again
// content no diff shows (git diff shows the tag object's text). The git
// client's ReadFile types the recorded object itself and refuses anything
// but a blob (git.NotBlobError with FileEntry), which is an
// unreadableEntry here (entryNonBlobObject) like a gitlink.
//
// It returns exactly one of: the content of a blob; an unreadableEntry
// (a gitlink, a directory, no entry at that exact path — a quoted or
// normalised path a listing produced —, a file entry recording an object
// that is not a blob, or content isContentReadError refuses); or an
// infrastructure error (the tree could not be typed, an unexpected type,
// or git failed to read a blob that is there).
//
// A git client without git.TreeEntryTyper keeps the historical read: a
// successful read is trusted and a failure goes through
// classifyReadFailure (infrastructure, unless the content is refused).
// Refusing every read there would stop the projection for an embedder
// whose GitClient decorator does not forward the capability; the gap is
// documented in KNOWN-LIMITATIONS.md. *git.CLIClient implements it, and
// both Spine and SMP hand that to NewService.
func (s *Service) readFileEntry(ctx context.Context, ref, path string) ([]byte, *unreadableEntry, error) {
	typer, ok := s.git.(git.TreeEntryTyper)
	if !ok {
		content, err := s.git.ReadFile(ctx, ref, path)
		if err == nil {
			return content, nil, nil
		}
		entry, infraErr := s.classifyReadFailure(ctx, ref, path, err)
		if infraErr != nil {
			return nil, nil, infraErr
		}
		return nil, &unreadableEntry{entry: entry, readErr: err}, nil
	}

	typ, err := typer.TreeEntryType(ctx, ref, path)
	if err != nil {
		return nil, nil, fmt.Errorf("type the tree entry: %w", err)
	}
	entry, err := nonFileEntry(typ)
	if err != nil {
		return nil, nil, err
	}
	if entry != "" {
		return nil, &unreadableEntry{entry: entry}, nil
	}

	// A blob. The ref is a commit SHA on every projection path, so the
	// tree read above and the blob read below see the same entry.
	content, err := s.git.ReadFile(ctx, ref, path)
	if err == nil {
		return content, nil, nil
	}
	if ctx.Err() == nil {
		if entry, ok := notBlobEntry(err); ok {
			return nil, &unreadableEntry{entry: entry}, nil
		}
		if isContentReadError(err) {
			return nil, &unreadableEntry{entry: entryContentRefused, readErr: err}, nil
		}
	}
	return nil, nil, err
}

// classifyReadErrors sorts the artifact read failures discovery skipped
// past (INIT-023 TASK-011 §3, review round 1). A failure classifyReadFailure
// proves deterministic keeps the historical skip, now logged at Warn and
// counted (distinct paths) as read_skipped. The first failure it cannot
// prove deterministic is returned, and the caller holds the sync on it.
func (s *Service) classifyReadErrors(ctx context.Context, readErrors []artifact.DiscoveryError) (int, error) {
	skipped := make(map[string]struct{})
	for i := range readErrors {
		re := &readErrors[i]
		readErr := re.Err
		if readErr == nil && re.Message != "" {
			readErr = errors.New(re.Message)
		}
		entry, err := s.classifyReadFailure(ctx, re.Ref, re.Path, readErr)
		if err != nil {
			return 0, fmt.Errorf("read %s at %s: %w", re.Path, shortSHA(re.Ref), err)
		}
		observe.Logger(ctx).Warn("projection: skipped unreadable changed file",
			"path", re.Path,
			"ref", shortSHA(re.Ref),
			"entry", entry,
			"error", sanitizeForLog(re.Message),
		)
		skipped[re.Path] = struct{}{}
	}
	return len(skipped), nil
}
