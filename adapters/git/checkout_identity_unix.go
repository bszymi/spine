//go:build unix

package git

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity names the file at path by its (device, inode), following
// a symbolic link (INIT-024 TASK-011 review round 1): two spellings of
// one directory — a symbolic link, a case variant on case-insensitive
// storage — give the same identity. ok is false when path cannot be
// stat'ed; the caller then keys by the path.
func fileIdentity(path string) (string, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	// Formatted as reported (their integer types differ by platform);
	// no conversion, so nothing can wrap.
	return fmt.Sprintf("id:%d:%d", st.Dev, st.Ino), true
}
