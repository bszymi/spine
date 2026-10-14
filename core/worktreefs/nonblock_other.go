//go:build !unix

package worktreefs

import "os"

// openNoBlock: outside unix there is no FIFO inside a directory tree
// (Windows named pipes live in their own namespace), so an O_RDONLY open
// of a leaf cannot wait for a writer.
const openNoBlock = 0

func clearNoBlock(*os.File) error { return nil }
