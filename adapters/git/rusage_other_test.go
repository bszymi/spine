//go:build !linux && !darwin

package git_test

// childPeakRSS is unavailable on this platform; the git-memory tests run
// their error assertions only.
func childPeakRSS() int64 { return -1 }
