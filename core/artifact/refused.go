package artifact

import (
	"context"
	"errors"
	"fmt"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// RefusalReason says why discovery declined to project a file
// (INIT-023 TASK-003).
type RefusalReason string

const (
	// RefusedOversized: the blob exceeds the git client's per-blob read
	// limit (git.ErrBlobTooLarge). It can never become readable at that
	// commit, so discovery skips it, records it and moves on.
	RefusedOversized RefusalReason = "oversized"
	// RefusedUnsafePath: the path is not one the projection stores
	// (UnsafePath). The file is never read.
	RefusedUnsafePath RefusalReason = "unsafe_path"
)

// RefusedFile is a file discovery declined to project. Refusals are not
// errors: they never make a rebuild or a sync fail, and every one is
// reported (the projection logs a Warn line, counts
// spine_projection_files_skipped_total and emits projection_file_skipped).
type RefusedFile struct {
	// Path is the repository-relative path, exactly as git lists it
	// (never stripped of artifacts_dir). For RefusedUnsafePath it may
	// hold invalid UTF-8 or control characters: quote it before display
	// (strconv.Quote) and never pass it to the store.
	Path   string
	Reason RefusalReason
	// Size and Limit are set for RefusedOversized: the blob's size (-1
	// when the read overran the limit rather than being refused by the
	// size probe) and the limit it exceeded (-1 when the client did not
	// report it). Both are 0 for RefusedUnsafePath.
	Size  int64
	Limit int64
	// GitAllocLimit is set for RefusedOversized when git itself refused
	// an allocation over this GIT_ALLOC_LIMIT while producing the content
	// (git.BlobTooLargeError.GitAllocLimit; Size is then -1). The file
	// may be under Limit: what is too large is a delta base it is stored
	// on, or a gitlink's patch. 0 otherwise.
	GitAllocLimit int64
}

// UnsafePath reports whether p is a repository path discovery refuses to
// project: not valid UTF-8, or holding a control character (C0, DEL, C1),
// U+2028/U+2029, or a bidi embedding, override or isolate control. Such
// a candidate is never read; it is recorded as RefusedUnsafePath. See
// domain.UnsafeRepositoryPath, which it is, for why each class is refused
// (owner question 2). No path artifact.Slugify produces is refused.
func UnsafePath(p string) bool { return domain.UnsafeRepositoryPath(p) }

// readForDiscovery reads one candidate. It returns exactly one of: the
// content; a RefusedOversized record when the git client refused the
// blob for its size (git.ErrBlobTooLarge — any GitClient whose refusal
// matches the sentinel, not only *git.CLIClient); or the read error.
func readForDiscovery(ctx context.Context, gitClient git.GitClient, ref, path string) ([]byte, *RefusedFile, error) {
	content, err := gitClient.ReadFile(ctx, ref, path)
	if err == nil {
		return content, nil, nil
	}
	if errors.Is(err, git.ErrBlobTooLarge) {
		rf := &RefusedFile{Path: path, Reason: RefusedOversized, Size: -1, Limit: -1}
		var tl *git.BlobTooLargeError
		if errors.As(err, &tl) {
			rf.Size, rf.Limit, rf.GitAllocLimit = tl.Size, tl.Limit, tl.GitAllocLimit
		}
		return nil, rf, nil
	}
	return nil, nil, err
}

func unsafeRefusal(path string) RefusedFile {
	return RefusedFile{Path: path, Reason: RefusedUnsafePath}
}

// ReadLimitPhrase renders a size refusal for an API message: "is <n>
// bytes, over the <limit>-byte read limit", "cannot be read within the
// read limits: git needed an allocation over …" when git refused an
// allocation (the file itself may be small), or "exceeds the read limit"
// when the sizes are unknown. err should match git.ErrBlobTooLarge.
func ReadLimitPhrase(err error) string {
	var tl *git.BlobTooLargeError
	if errors.As(err, &tl) {
		if tl.GitAllocLimit > 0 {
			return fmt.Sprintf("cannot be read within the read limits: git needed an allocation over the "+
				"%d-byte git allocation limit to produce it (a delta on a large base, or a gitlink rendered "+
				"with its patch)", tl.GitAllocLimit)
		}
		if tl.Size >= 0 {
			return fmt.Sprintf("is %d bytes, over the %d-byte read limit", tl.Size, tl.Limit)
		}
		return fmt.Sprintf("exceeds the %d-byte read limit", tl.Limit)
	}
	return "exceeds the read limit"
}
