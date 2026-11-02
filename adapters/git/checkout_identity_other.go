//go:build !unix

package git

// fileIdentity has no (device, inode) to report on this platform; the
// caller keys by the symlink-free path.
func fileIdentity(string) (string, bool) { return "", false }
