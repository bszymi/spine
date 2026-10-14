//go:build linux || darwin

package git_test

import (
	"runtime"
	"syscall"
)

// childPeakRSS returns the peak resident set size, in bytes, of the
// largest child process this process has waited for (RUSAGE_CHILDREN),
// or -1 when it cannot be read.
func childPeakRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_CHILDREN, &ru); err != nil {
		return -1
	}
	rss := int64(ru.Maxrss)
	if runtime.GOOS == "linux" {
		rss *= 1024 // KiB on Linux, bytes on darwin
	}
	return rss
}
