//go:build unix

package worktreefs

import (
	"os"
	"syscall"
)

// openNoBlock is or-ed into the O_RDONLY opens that re-open a leaf by
// name after its check (ReadFileLimit's read, Create's pin). A leaf
// swapped for a FIFO in that window would otherwise block the open in the
// kernel until a writer appeared, before the SameFile check that refuses
// it could run. On the regular file the check admits it changes nothing
// at open time.
const openNoBlock = syscall.O_NONBLOCK

// clearNoBlock puts a descriptor opened with openNoBlock back into
// blocking mode once it is known to be the checked regular file, so the
// read that follows behaves exactly as a plain O_RDONLY descriptor's
// (O_NONBLOCK has no effect on regular-file reads, but no filesystem has
// to be trusted on that).
func clearNoBlock(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) { serr = syscall.SetNonblock(int(fd), false) }); err != nil {
		return err
	}
	return serr
}
