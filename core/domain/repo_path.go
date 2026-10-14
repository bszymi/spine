package domain

import (
	"unicode"
	"unicode/utf8"
)

// UnsafeRepositoryPath reports whether p is a repository path Spine
// refuses to project or list (INIT-023 TASK-003, owner question 2,
// default pending the owner: refuse and record). It is true when p is not
// valid UTF-8, or holds:
//
//   - a control character (unicode.IsControl: C0 — tab and newline
//     included — DEL, and C1 U+0080–U+009F; U+0085 NEL is a line break
//     for several parsers);
//   - U+2028 LINE SEPARATOR or U+2029 PARAGRAPH SEPARATOR, which
//     JavaScript and JSON-in-HTML consumers treat as line terminators;
//   - a bidi embedding, override or isolate control (U+202A–U+202E,
//     U+2066–U+2069), which can make a displayed path — a protected-path
//     warning, say — read as a different path.
//
// Invalid UTF-8 is refused whatever the owner decides:
// projection.artifacts.artifact_path is a text primary key, and a UTF8
// PostgreSQL rejects such a parameter (SQLSTATE 22021), so one such path
// would fail every incremental sync and wedge the workspace's
// projection. Before TASK-003 git C-quoted all of these (every byte
// >= 0x80, every control byte) under its default core.quotePath=true,
// and the quoted form never ended in ".md" or ".yaml", so no such path
// was projected or listed; with raw NUL-delimited listings it would be.
// Under core.quotePath=false git listed bytes >= 0x80 raw, so a
// valid-UTF-8 one could be projected then: discovery and the projection
// retire such rows (artifact.StoredUnsafePath; wave 3 integration).
//
// artifact.Slugify keeps only letters, digits and "-", and none of these
// are letters, so no path the platform creates is refused — "wdrożenie"
// or "résumé" directories are projected. Unicode normalisation (NFC vs
// NFD spellings of one name) is not an integrity issue and is not
// refused.
func UnsafeRepositoryPath(p string) bool {
	if !utf8.ValidString(p) {
		return true
	}
	for _, r := range p {
		switch {
		case unicode.IsControl(r):
			return true
		case r == '\u2028' || r == '\u2029':
			return true
		case r >= '\u202a' && r <= '\u202e':
			return true
		case r >= '\u2066' && r <= '\u2069':
			return true
		}
	}
	return false
}
