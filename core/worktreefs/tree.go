package worktreefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
)

// Test hooks. Package-internal and nil/false in production; tests that set
// them must not run in parallel with each other.
var (
	// testHookSkipComponentChecks disables every walk check — the symlink
	// and not-a-directory checks on each component, the SameFile identity
	// checks, and the leaf Lstat checks (symlink, regular, size) — so a test
	// can prove the os.Root layer underneath confines on its own (AC5).
	testHookSkipComponentChecks bool

	// testHookAfterComponentLstat runs between a component's Lstat and its
	// OpenRoot: the window in which swapping the component for a symlink
	// would redirect the descent if the SameFile check were missing (AC4).
	testHookAfterComponentLstat func(op, prefix string)

	// testHookBeforeLeafOp runs after the walk and the leaf checks, right
	// before the leaf primitive (O_EXCL open, O_RDONLY open, rename,
	// unlink).
	testHookBeforeLeafOp func(op, rel string)

	// testHookAfterLeafOpen runs after Create / Replace opened their file;
	// a non-nil return is injected as a write failure.
	testHookAfterLeafOpen func(op, rel string) error

	// testHookObserveRead receives the number of bytes ReadFileLimit pulled
	// from the file (before the limit check).
	testHookObserveRead func(rel string, n int64)
)

// openHandles counts the directory and file handles this package holds
// open. It exists so tests can prove I8 (no handle leaks).
var openHandles atomic.Int64

func openRootDir(dir string) (*os.Root, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	openHandles.Add(1)
	return r, nil
}

func openSubRoot(parent *os.Root, name string) (*os.Root, error) {
	r, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openHandles.Add(1)
	return r, nil
}

func closeRoot(r *os.Root) {
	openHandles.Add(-1)
	_ = r.Close()
}

func openFileIn(parent *os.Root, name string, flag int, perm fs.FileMode) (*os.File, error) {
	f, err := parent.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	openHandles.Add(1)
	return f, nil
}

func closeFile(f *os.File) error {
	openHandles.Add(-1)
	return f.Close()
}

// relErr rewrites an os error so that it names rel instead of whatever the
// os package put there (a file opened through an os.Root carries the
// root's absolute name). Errors from this package keep their type, and the
// wrapped errno is preserved so errors.Is(err, fs.ErrNotExist) and friends
// still hold.
func relErr(err error, rel string) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return &fs.PathError{Op: pe.Op, Path: rel, Err: pe.Err}
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return &fs.PathError{Op: le.Op, Path: rel, Err: le.Err}
	}
	return err
}

func isSymlink(fi fs.FileInfo) bool { return fi.Mode()&fs.ModeSymlink != 0 }

// Tree is a git working tree opened for confined I/O. All methods take a
// repo-relative path (validated with Clean) and are safe for concurrent
// use; the Tree must not be used after Close.
type Tree struct {
	root *os.Root
}

// Open opens dir as a Tree. dir is trusted (I5): symbolic links in its own
// path are followed. The error, which names dir, is for server-side logs.
func Open(dir string) (*Tree, error) {
	r, err := openRootDir(dir)
	if err != nil {
		return nil, err
	}
	return &Tree{root: r}, nil
}

// Close releases the tree's root handle. It is idempotent. An Undo returned
// by Create stays usable after Close: it holds its own handles.
func (t *Tree) Close() error {
	if t == nil || t.root == nil {
		return nil
	}
	closeRoot(t.root)
	t.root = nil
	return nil
}

var errTreeClosed = errors.New("worktreefs: tree is closed")

// walk is the result of a checked descent. dirs[0] is a private duplicate
// of the tree root's handle; dirs[i] is the handle for the i-th descended
// component. Every handle in dirs is owned by the walk.
type walk struct {
	rel     string
	leaf    string // final component; "" for a directory walk
	dirs    []*os.Root
	created []createdDir // directories this walk made, shallowest first
}

type createdDir struct {
	parent *os.Root
	name   string
	info   fs.FileInfo
}

func (w *walk) parent() *os.Root { return w.dirs[len(w.dirs)-1] }

func (w *walk) close() {
	for i := len(w.dirs) - 1; i >= 0; i-- {
		closeRoot(w.dirs[i])
	}
	w.dirs = nil
}

// rollbackDirs removes, deepest first, every directory this walk created,
// through the parent handle it was created in. A directory that is no
// longer the one created, or is not empty, is left alone.
func (w *walk) rollbackDirs() error {
	var first error
	for i := len(w.created) - 1; i >= 0; i-- {
		d := w.created[i]
		if err := removeIfSame(d.parent, d.name, d.info); err != nil && first == nil {
			first = err
		}
	}
	w.created = nil
	return first
}

// removeIfSame unlinks name from parent if it is still the file or
// directory described by info. A directory that is not empty is left in
// place without an error.
func removeIfSame(parent *os.Root, name string, info fs.FileInfo) error {
	fi, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(fi, info) {
		return nil
	}
	err = parent.Remove(name)
	if err != nil && info.IsDir() && (errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)) {
		return nil
	}
	return err
}

// descend walks comps (each a directory) from the tree root, enforcing I2
// and I3 on every component and binding each descent to the inode its
// Lstat saw. With mkdirs it creates missing components (mkdirat never
// follows a link and fails EEXIST on a planted one) and records them so a
// failure can remove them again.
//
// On error every handle the walk opened is closed and every directory it
// created is removed before returning.
func (t *Tree) descend(op, rel string, comps []string, mkdirs bool) (_ *walk, err error) {
	if t == nil || t.root == nil {
		return nil, errTreeClosed
	}
	start, err := openSubRoot(t.root, ".")
	if err != nil {
		return nil, relErr(err, ".")
	}
	w := &walk{rel: rel, dirs: []*os.Root{start}}
	defer func() {
		if err != nil {
			_ = w.rollbackDirs()
			w.close()
		}
	}()

	skip := testHookSkipComponentChecks
	prefix := ""
	for _, c := range comps {
		prefix = joinRel(prefix, c)
		cur := w.parent()

		fi, lerr := cur.Lstat(c)
		if mkdirs && errors.Is(lerr, fs.ErrNotExist) {
			merr := cur.Mkdir(c, 0o755)
			switch {
			case merr == nil:
				fi, lerr = cur.Lstat(c)
				if lerr == nil {
					w.created = append(w.created, createdDir{parent: cur, name: c, info: fi})
				}
			case errors.Is(merr, fs.ErrExist):
				// Lost a race with a concurrent creator, or a link was
				// planted after the Lstat: re-check what is there now.
				fi, lerr = cur.Lstat(c)
			default:
				return nil, relErr(merr, prefix)
			}
		}
		if lerr != nil {
			return nil, relErr(lerr, prefix)
		}
		if !skip {
			if isSymlink(fi) {
				return nil, &PathError{Path: rel, Component: prefix, Reason: ReasonSymlink}
			}
			if !fi.IsDir() {
				return nil, &PathError{Path: rel, Component: prefix, Reason: ReasonNotDir}
			}
		}
		if hook := testHookAfterComponentLstat; hook != nil {
			hook(op, prefix)
		}
		next, oerr := openSubRoot(cur, c)
		if oerr != nil {
			// The component passed its Lstat as a directory, so a failed
			// open means it changed in between (removed, or swapped for a
			// link the confined open refused). Report a swap as such.
			if !skip {
				if fi2, e := cur.Lstat(c); e == nil && (isSymlink(fi2) || !os.SameFile(fi, fi2)) {
					return nil, &PathError{Path: rel, Component: prefix, Reason: ReasonChanged}
				}
			}
			return nil, relErr(oerr, prefix)
		}
		w.dirs = append(w.dirs, next)
		if !skip {
			// Bind the descent to the inode the Lstat saw. A component
			// swapped for a link between the Lstat and the OpenRoot is
			// followed by OpenRoot (when the link stays inside the
			// directory) and lands on a different inode.
			nfi, serr := next.Stat(".")
			if serr != nil {
				return nil, relErr(serr, prefix)
			}
			if !os.SameFile(fi, nfi) {
				return nil, &PathError{Path: rel, Component: prefix, Reason: ReasonChanged}
			}
		}
	}
	return w, nil
}

// walkToLeaf cleans rel and descends to its parent directory.
func (t *Tree) walkToLeaf(op, rel string, mkdirs bool) (*walk, error) {
	clean, err := Clean(rel)
	if err != nil {
		return nil, err
	}
	comps := strings.Split(clean, "/")
	w, err := t.descend(op, clean, comps[:len(comps)-1], mkdirs)
	if err != nil {
		return nil, err
	}
	w.leaf = comps[len(comps)-1]
	return w, nil
}

// lstatLeaf returns the leaf's Lstat. A missing leaf wraps fs.ErrNotExist;
// a symbolic link is refused (I2), dangling or not.
func (w *walk) lstatLeaf() (fs.FileInfo, error) {
	fi, err := w.parent().Lstat(w.leaf)
	if err != nil {
		return nil, relErr(err, w.rel)
	}
	if !testHookSkipComponentChecks && isSymlink(fi) {
		return nil, &PathError{Path: w.rel, Component: w.rel, Reason: ReasonSymlink}
	}
	return fi, nil
}

// regularLeaf is lstatLeaf plus the regular-file requirement.
func (w *walk) regularLeaf() (fs.FileInfo, error) {
	fi, err := w.lstatLeaf()
	if err != nil {
		return nil, err
	}
	if !testHookSkipComponentChecks && !fi.Mode().IsRegular() {
		return nil, &PathError{Path: w.rel, Component: w.rel, Reason: ReasonNotRegular}
	}
	return fi, nil
}

// Lstat checks rel and returns the leaf's file info without following
// anything. A missing leaf or directory wraps fs.ErrNotExist. A leaf that
// exists and is not a regular file is refused (ReasonNotRegular).
func (t *Tree) Lstat(rel string) (fs.FileInfo, error) {
	w, err := t.walkToLeaf("lstat", rel, false)
	if err != nil {
		return nil, err
	}
	defer w.close()
	return w.regularLeaf()
}

// ReadFile reads a regular file, capped at DefaultMaxReadBytes.
func (t *Tree) ReadFile(rel string) ([]byte, error) {
	return t.ReadFileLimit(rel, DefaultMaxReadBytes)
}

// ReadFileLimit reads a regular file of at most limit bytes (a negative
// limit is treated as 0). A leaf whose Lstat size exceeds limit is refused
// with *SizeError before it is opened; the read itself goes through an
// io.LimitReader(limit+1), so a file that grows after the check is still
// refused after reading at most limit+1 bytes.
func (t *Tree) ReadFileLimit(rel string, limit int64) ([]byte, error) {
	if limit < 0 {
		limit = 0
	}
	w, err := t.walkToLeaf("read", rel, false)
	if err != nil {
		return nil, err
	}
	defer w.close()
	lfi, err := w.regularLeaf()
	if err != nil {
		return nil, err
	}
	if !testHookSkipComponentChecks && lfi.Size() > limit {
		return nil, &SizeError{Path: w.rel, Size: lfi.Size(), Limit: limit}
	}
	if hook := testHookBeforeLeafOp; hook != nil {
		hook("read", w.rel)
	}
	// O_RDONLY on the held parent: a single component, so nothing above
	// the leaf is resolved again. os.Root would follow a link swapped in at
	// the leaf only if it stays inside this directory; the SameFile check
	// below refuses that case.
	f, err := openFileIn(w.parent(), w.leaf, os.O_RDONLY, 0)
	if err != nil {
		return nil, relErr(err, w.rel)
	}
	defer func() { _ = closeFile(f) }()
	if !testHookSkipComponentChecks {
		ffi, err := f.Stat()
		if err != nil {
			return nil, relErr(err, w.rel)
		}
		if !os.SameFile(lfi, ffi) {
			return nil, &PathError{Path: w.rel, Component: w.rel, Reason: ReasonChanged}
		}
	}
	return readCapped(f, w.rel, lfi.Size(), limit)
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func readCapped(f io.Reader, rel string, sizeHint, limit int64) ([]byte, error) {
	src := f
	if hook := testHookObserveRead; hook != nil {
		cr := &countingReader{r: f}
		src = cr
		defer func() { hook(rel, cr.n) }()
	}
	lr := io.LimitReader(src, limit+1)
	n := sizeHint
	if n < 0 {
		n = 0
	}
	if n > limit {
		n = limit
	}
	// One spare byte so an exact-size file reaches EOF without growing.
	buf := make([]byte, 0, n+1)
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		m, err := lr.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+m]
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, relErr(err, rel)
		}
	}
	if int64(len(buf)) > limit {
		return nil, &SizeError{Path: rel, Size: int64(len(buf)), Limit: limit}
	}
	return buf, nil
}

// Undo reverts a Create. It holds the directory handles the Create walked,
// so it stays usable after the Tree is closed. Exactly one of Rollback or
// Release should be called; both are idempotent and nil-safe, so
// `defer undo.Release()` after an explicit Rollback is safe.
type Undo struct {
	w        *walk
	leafInfo fs.FileInfo
}

// Rollback removes the created leaf, then each directory the Create made
// (deepest first, only while empty), through the held handles, and
// releases the handles. A leaf or directory that has since been replaced
// by something else is left alone.
func (u *Undo) Rollback() error {
	if u == nil || u.w == nil {
		return nil
	}
	var first error
	if u.leafInfo != nil {
		first = removeIfSame(u.w.parent(), u.w.leaf, u.leafInfo)
		if first != nil {
			first = relErr(first, u.w.rel)
		}
	}
	if err := u.w.rollbackDirs(); err != nil && first == nil {
		first = relErr(err, u.w.rel)
	}
	u.w.close()
	u.w = nil
	return first
}

// Release closes the held handles without removing anything.
func (u *Undo) Release() {
	if u == nil || u.w == nil {
		return
	}
	u.w.close()
	u.w = nil
}

// Create writes a new regular file at rel with O_CREAT|O_EXCL (I4),
// creating missing parent directories. An existing leaf that is not a
// symbolic link wraps fs.ErrExist; a symbolic link is refused (I2). On any
// failure the leaf and every directory this call made are removed before
// returning. On success the caller resolves the returned Undo: Rollback
// when the commit that follows fails, Release otherwise.
func (t *Tree) Create(rel string, data []byte, perm fs.FileMode) (_ *Undo, err error) {
	w, err := t.walkToLeaf("create", rel, true)
	if err != nil {
		return nil, err
	}
	u := &Undo{w: w}
	defer func() {
		if err != nil {
			_ = u.Rollback()
		}
	}()

	parent := w.parent()
	if !testHookSkipComponentChecks {
		lfi, lerr := parent.Lstat(w.leaf)
		switch {
		case lerr == nil && isSymlink(lfi):
			return nil, &PathError{Path: w.rel, Component: w.rel, Reason: ReasonSymlink}
		case lerr == nil && !lfi.Mode().IsRegular():
			return nil, &PathError{Path: w.rel, Component: w.rel, Reason: ReasonNotRegular}
		case lerr == nil:
			return nil, &fs.PathError{Op: "create", Path: w.rel, Err: fs.ErrExist}
		case !errors.Is(lerr, fs.ErrNotExist):
			return nil, relErr(lerr, w.rel)
		}
	}
	if hook := testHookBeforeLeafOp; hook != nil {
		hook("create", w.rel)
	}
	// O_EXCL: the kernel never follows the final component, and os.Root
	// reports EEXIST for every leaf link (dangling or not).
	f, err := openFileIn(parent, w.leaf, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm.Perm())
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			if !testHookSkipComponentChecks {
				if lfi, lerr := parent.Lstat(w.leaf); lerr == nil && isSymlink(lfi) {
					return nil, &PathError{Path: w.rel, Component: w.rel, Reason: ReasonSymlink}
				}
			}
			return nil, &fs.PathError{Op: "create", Path: w.rel, Err: fs.ErrExist}
		}
		return nil, relErr(err, w.rel)
	}
	// Record the created file's identity first, so a failure from here on
	// removes exactly this file.
	info, serr := f.Stat()
	if serr != nil {
		_ = closeFile(f)
		// Without the identity the file cannot be removed safely by
		// SameFile; it was created O_EXCL by this call, so remove by name.
		_ = parent.Remove(w.leaf)
		return nil, relErr(serr, w.rel)
	}
	u.leafInfo = info
	if err := writeAndClose(f, data, "create", w.rel, 0, false); err != nil {
		return nil, err
	}
	return u, nil
}

// writeAndClose writes data to f, optionally fchmods it, and closes it. f
// is closed on every path.
func writeAndClose(f *os.File, data []byte, op, rel string, mode fs.FileMode, chmod bool) error {
	if hook := testHookAfterLeafOpen; hook != nil {
		if err := hook(op, rel); err != nil {
			_ = closeFile(f)
			return err
		}
	}
	if _, err := f.Write(data); err != nil {
		_ = closeFile(f)
		return relErr(err, rel)
	}
	if chmod {
		// fchmod on the open descriptor: never resolves a name.
		if err := f.Chmod(mode); err != nil {
			_ = closeFile(f)
			return relErr(err, rel)
		}
	}
	if err := closeFile(f); err != nil {
		return relErr(err, rel)
	}
	return nil
}

func tempName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return ".spine-tmp-" + hex.EncodeToString(b[:]), nil
}

// Replace rewrites an existing regular file (I4). It never opens the file
// for writing: the new bytes go to a fresh O_EXCL temp file in the same
// directory, which takes the old file's permission bits by fchmod (keeping
// git's executable bit) and is then renamed over the leaf. renameat
// replaces the directory entry, so even a link swapped in after the check
// is replaced rather than followed. On failure the temp file is removed.
func (t *Tree) Replace(rel string, data []byte) error {
	w, err := t.walkToLeaf("replace", rel, false)
	if err != nil {
		return err
	}
	defer w.close()
	lfi, err := w.regularLeaf()
	if err != nil {
		return err
	}
	return replaceIn(w, data, lfi.Mode().Perm())
}

func replaceIn(w *walk, data []byte, mode fs.FileMode) (err error) {
	parent := w.parent()
	tmp, err := tempName()
	if err != nil {
		return err
	}
	f, err := openFileIn(parent, tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return relErr(err, w.rel)
	}
	defer func() {
		if err != nil {
			_ = parent.Remove(tmp)
		}
	}()
	if err := writeAndClose(f, data, "replace", w.rel, mode, true); err != nil {
		return err
	}
	if hook := testHookBeforeLeafOp; hook != nil {
		hook("replace", w.rel)
	}
	if err := parent.Rename(tmp, w.leaf); err != nil {
		return relErr(err, w.rel)
	}
	return nil
}

// WriteFile keeps os.WriteFile's create-or-overwrite contract under the
// confinement rules: Replace when a regular leaf exists (its mode is kept;
// perm is ignored), else Create with perm (the Undo is released, so a later
// failure does not remove the file).
func (t *Tree) WriteFile(rel string, data []byte, perm fs.FileMode) error {
	_, err := t.Lstat(rel)
	switch {
	case err == nil:
		return t.Replace(rel, data)
	case errors.Is(err, fs.ErrNotExist):
		u, err := t.Create(rel, data, perm)
		if err != nil {
			return err
		}
		u.Release()
		return nil
	default:
		return err
	}
}

// Remove unlinks a regular file (unlinkat on the held parent; a link is
// never followed). A missing leaf wraps fs.ErrNotExist.
func (t *Tree) Remove(rel string) error {
	w, err := t.walkToLeaf("remove", rel, false)
	if err != nil {
		return err
	}
	defer w.close()
	if _, err := w.regularLeaf(); err != nil {
		return err
	}
	if hook := testHookBeforeLeafOp; hook != nil {
		hook("remove", w.rel)
	}
	if err := w.parent().Remove(w.leaf); err != nil {
		return relErr(err, w.rel)
	}
	return nil
}

// RemoveEmptyDir removes rel if it is an empty directory. A directory that
// is not empty is an error (the caller decides whether that matters); a
// symbolic link is refused, never followed.
func (t *Tree) RemoveEmptyDir(rel string) error {
	w, err := t.walkToLeaf("rmdir", rel, false)
	if err != nil {
		return err
	}
	defer w.close()
	fi, err := w.lstatLeaf()
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return &PathError{Path: w.rel, Component: w.rel, Reason: ReasonNotDir}
	}
	if hook := testHookBeforeLeafOp; hook != nil {
		hook("rmdir", w.rel)
	}
	if err := w.parent().Remove(w.leaf); err != nil {
		return relErr(err, w.rel)
	}
	return nil
}

// WalkRegularFiles calls fn with the repo-relative path of every regular
// file under relDir ("" is the tree root), in lexical order per directory.
// It descends only through directories that pass the same checks as every
// other operation, never yields or enters a symbolic link or anything that
// is not a regular file or directory, and never yields or enters a
// component that names the git directory (I3). The walk holds the
// directory handles it descended through; fn may use the Tree (its own
// reads are bounded by ReadFile, I7). An error from fn stops the walk and
// is returned.
func (t *Tree) WalkRegularFiles(relDir string, fn func(rel string) error) error {
	var comps []string
	if relDir != "" {
		clean, err := Clean(relDir)
		if err != nil {
			return err
		}
		relDir = clean
		comps = strings.Split(clean, "/")
	}
	w, err := t.descend("walk", relDir, comps, false)
	if err != nil {
		return err
	}
	defer w.close()
	return walkDir(w.parent(), relDir, fn)
}

func walkDir(dir *os.Root, prefix string, fn func(rel string) error) error {
	d, err := openFileIn(dir, ".", os.O_RDONLY, 0)
	if err != nil {
		return relErr(err, prefix)
	}
	entries, rerr := d.ReadDir(-1)
	_ = closeFile(d)
	if rerr != nil {
		return relErr(rerr, prefix)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		name := e.Name()
		if isGitDirName(name) {
			continue
		}
		rel := joinRel(prefix, name)
		if _, err := Clean(rel); err != nil {
			continue // over a length cap: not addressable through a Tree
		}
		// Lstat on the held handle, not the DirEntry type: authoritative
		// and never follows.
		fi, err := dir.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return relErr(err, rel)
		}
		switch {
		case isSymlink(fi):
			continue
		case fi.IsDir():
			sub, err := openSubRoot(dir, name)
			if err != nil {
				return relErr(err, rel)
			}
			nfi, serr := sub.Stat(".")
			if serr != nil || !os.SameFile(fi, nfi) {
				closeRoot(sub)
				if serr != nil {
					return relErr(serr, rel)
				}
				return &PathError{Path: rel, Component: rel, Reason: ReasonChanged}
			}
			err = walkDir(sub, rel, fn)
			closeRoot(sub)
			if err != nil {
				return err
			}
		case fi.Mode().IsRegular():
			if err := fn(rel); err != nil {
				return err
			}
		}
	}
	return nil
}
