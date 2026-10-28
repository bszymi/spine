package githttp

// The case-sensitivity probe of a served repository (INIT-024 TASK-005,
// review finding N-8).
//
// Branch protection and ref admission judge the ref name a client sends,
// byte for byte (INIT-023 TASK-001). On storage that folds names — a
// case-insensitive or normalization-insensitive filesystem: native
// APFS/HFS+, a Docker Desktop bind mount of a macOS or Windows directory,
// SMB, an ext4 casefold directory — git then writes a loose ref file whose
// name differs from a protected ref's only in case (or in Unicode
// normalization) onto THAT ref's file: a push to refs/heads/MAIN matches
// no rule and moves main. KNOWN-LIMITATIONS documented the storage
// requirement; this probe enforces it.
//
// The probe asks the filesystem the question directly, in the directory
// git keeps loose refs in: it creates, with O_EXCL, three pairs of names
// that a folding filesystem identifies — ASCII case, Unicode NFC/NFD, and
// the Kelvin sign against 'k' — reads every file back and lists the
// directory. Any pair that collides (the second create finds the first
// file, a read returns the other name's content, or the listing lacks a
// name) is folding storage. The files live in a fresh directory under
// <git-common-dir>/refs/heads — the directory the protected branches'
// loose ref files live in, so per-directory case folding there is seen
// too (review round 1, P4-10) — whose name starts with '.', which git's
// loose-ref iteration (for-each-ref, pack-refs, the ref advertisement)
// skips and no valid ref name can address; the directory is removed
// before the probe returns. It needs no git process, no object and works
// in an empty repository. A reftable repository has no refs/heads
// directory (git keeps a placeholder file there); the probe then runs in
// refs/, and on folding storage refuses that repository too, although
// reftable does not store ref names as files (fail closed).
//
// It runs once per repository and handler lifetime, from
// ensurePushStorage, on the first push request with receive-pack enabled
// (owner question 2: a repository that fails it has receive-pack
// refused — http.receivepack=false and a 403 naming the requirement —
// while clones and fetches are served as before).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// storageProbePairs are the names a folding filesystem identifies. The
// first of each pair is created first.
var storageProbePairs = [][2]string{
	{"spine-probe-case", "SPINE-PROBE-CASE"},
	{"spine-probe-\u00e9", "spine-probe-e\u0301"}, // NFC e-acute, NFD e + combining acute (escaped: an editor must not normalise them)
	{"spine-probe-k", "spine-probe-\u212a"},       // 'k', KELVIN SIGN
}

// storageProbe reports whether the storage of a repository's refs folds
// names (true: receive-pack must be refused). An error means the question
// could not be answered; the caller refuses pushes (fail closed) and asks
// again on the next request.
type storageProbe func(ctx context.Context, repoPath string) (folds bool, err error)

// probeRefStorage is the built-in storageProbe.
func probeRefStorage(ctx context.Context, repoPath string) (bool, error) {
	common, err := gitCommonDir(ctx, repoPath)
	if err != nil {
		return false, err
	}
	parent := filepath.Join(common, "refs", "heads")
	if fi, err := os.Stat(parent); err != nil || !fi.IsDir() {
		parent = filepath.Join(common, "refs") // reftable, or no loose branch directory
	}
	dir, err := os.MkdirTemp(parent, ".spine-storage-probe-")
	if err != nil {
		return false, fmt.Errorf("case-sensitivity probe: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	folds, err := probeDirFolds(dir)
	if probeDirHookForTest != nil {
		probeDirHookForTest(dir)
	}
	return folds, err
}

// probeDirHookForTest, when set, is called with the probe's directory
// (its files in place) before it is removed.
var probeDirHookForTest func(dir string)

// probeDirFolds runs the probe in dir, an empty directory the caller owns.
func probeDirFolds(dir string) (bool, error) {
	want := map[string][]byte{}
	for i, pair := range storageProbePairs {
		for j, name := range pair {
			content := []byte(fmt.Sprintf("%d.%d\n", i, j))
			f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: fixed names in a directory this process created
			if errors.Is(err, fs.ErrExist) {
				return true, nil // the second name opened the first one's file
			}
			if err != nil {
				return false, fmt.Errorf("case-sensitivity probe: %w", err)
			}
			_, werr := f.Write(content)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				return false, fmt.Errorf("case-sensitivity probe: %w", werr)
			}
			want[name] = content
		}
	}
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // G304: as above
		if err != nil {
			return false, fmt.Errorf("case-sensitivity probe: %w", err)
		}
		if !bytes.Equal(got, content) {
			return true, nil
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("case-sensitivity probe: %w", err)
	}
	listed := map[string]bool{}
	for _, e := range ents {
		listed[e.Name()] = true
	}
	for name := range want {
		if !listed[name] {
			return true, nil // stored under another spelling (a normalizing filesystem)
		}
	}
	return false, nil
}

// gitCommonDir is the absolute <git-common-dir> of the repository at
// repoPath (a bare repository or a work tree).
func gitCommonDir(ctx context.Context, repoPath string) (string, error) {
	// G702: repoPath is server-resolved from the workspace registry.
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-common-dir") //nolint:gosec // G204: fixed argv
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("locate the git directory: %w", err)
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoPath, common)
	}
	return filepath.Abs(common)
}

// caseFoldingStorageMessage is the push refusal for a repository whose
// storage folds names.
const caseFoldingStorageMessage = "git push is refused for this repository: it is stored on a case- or " +
	"normalization-insensitive filesystem, where two ref names that differ only in case or in Unicode " +
	"normalization are one ref, so a push could move a protected branch under another spelling. " +
	"Serve it from a case-sensitive, normalization-sensitive filesystem (KNOWN-LIMITATIONS: \"Served " +
	"Repositories Need Case- and Normalization-Sensitive Storage\"). Clones and fetches are not affected."

// isTestBinary reports whether this process is a `go test` binary; a
// variable so the refusal can be tested.
var isTestBinary = testing.Testing

// errNotATestBinary is FakeCaseSensitiveStorageOnFoldingHostForTests'
// refusal outside a test binary.
var errNotATestBinary = errors.New("githttp: the case-sensitivity probe can be faked in a test binary only")

// storageProbeOverride, when set, is the probe of every handler NewHandler
// builds instead of probeRefStorage (FakeCaseSensitiveStorageOnFoldingHostForTests).
var storageProbeOverride atomic.Pointer[storageProbe]

// defaultStorageProbe is the probe NewHandler installs.
func defaultStorageProbe() storageProbe {
	if p := storageProbeOverride.Load(); p != nil {
		return *p
	}
	return probeRefStorage
}

// FakeCaseSensitiveStorageOnFoldingHostForTests is for the TestMain of
// Spine's own test packages that push through this handler. When the
// host's temporary directory — where tests create their repositories —
// folds names (a macOS laptop: APFS is case-insensitive), the built-in
// probe correctly refuses every push, so it makes every handler built
// afterwards treat storage as case-sensitive and reports true. On a
// case-sensitive host (Linux, CI) it changes nothing, and every test runs
// the real probe. Production code never calls it, and the package is
// internal, so no embedder can; and it refuses — an error, nothing
// changed — outside a test binary (testing.Testing; review round 1,
// P4-5), so no `spine` command can switch the probe off.
func FakeCaseSensitiveStorageOnFoldingHostForTests() (bool, error) {
	if !isTestBinary() {
		return false, errNotATestBinary
	}
	dir, err := os.MkdirTemp("", "spine-storage-probe-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	folds, err := probeDirFolds(dir)
	if err != nil || !folds {
		return false, err
	}
	p := storageProbe(func(context.Context, string) (bool, error) { return false, nil })
	storageProbeOverride.Store(&p)
	return true, nil
}
