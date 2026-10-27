package main

// INIT-024 / EPIC-004 / TASK-009 (review findings SC-08, SC-09): the
// supply-chain pin and toolchain-parity guard.
//
// Why parity matters. The official golang image sets GOTOOLCHAIN=local, so
// the Go that compiles the standalone image is the image's own GOLANG_VERSION
// — go.mod's `toolchain` line is ignored there. Bumping go.mod alone (say,
// for a stdlib security release) would therefore keep shipping the old
// compiler and its advisories while host builds (GOTOOLCHAIN=auto) used the
// new one, with nothing red anywhere. This check makes that drift a failure:
// every golang image reference in the repository must name the toolchain's
// exact patch version and all of them must be one tag and one digest, and
// every actions/setup-go step must install exactly that version. With
// -resolve it also reads the digest's image config from the registry
// (manifest and config only — nothing is pulled) and refuses unless every
// platform's GOLANG_VERSION is the toolchain version, so a tag edited without
// its digest cannot pass.
//
// Why pins matter. Spine is a public repository: a moved action tag or base
// image tag reaches every contributor's fork and CI run. Every image is
// name:tag@sha256:<digest> (the tag stays for the reader and for
// Dependabot), every `uses:` is a 40-hex commit SHA followed by a
// `# vX.Y.Z` comment, and no job runs on a floating `*-latest` runner label.
// With -resolve every action's `# vX.Y.Z` tag is looked up in its own
// repository and must name exactly the pinned commit, so a SHA from another
// release (or another repository's fork network) cannot hide behind a
// truthful-looking comment.
//
// Where it looks (see discover): go.mod; every Dockerfile, Dockerfile.* and
// *.Dockerfile, every compose file, every Makefile and *.mk, and every
// .github/actions/**/action.y{a,}ml anywhere in the tree (vendor/,
// node_modules/, testdata/ and .git/ skipped); and .github/workflows/*.y{a,}ml.
// Workflow, action and compose files are parsed as YAML (flow style, quoted
// keys, anchors and merge keys included); a file that does not parse is a
// refusal. Images named in `docker run|create|pull` commands inside workflow
// `run:` scripts and Makefiles are found by a best-effort shell scan
// (shell.go); KNOWN-LIMITATIONS.md lists what it cannot see.
//
// To move the toolchain: change go.mod's `toolchain` line, every
// golang:<version>-bookworm@sha256:<digest> reference (the Dockerfile
// builder, the ci.yml git-floor container, the Makefile's DOCKER_GO) to the
// new tag and its index digest (`docker buildx imagetools inspect <tag>`),
// and every setup-go `go-version`; then run `go run ./dev/pincheck -resolve`.

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// problem is one refusal, located at the file and line that caused it
// (line 0 for a whole-file or whole-repository refusal).
type problem struct {
	file string
	line int
	msg  string
}

func (p problem) String() string {
	switch {
	case p.file == "":
		return p.msg
	case p.line == 0:
		return p.file + ": " + p.msg
	default:
		return fmt.Sprintf("%s:%d: %s", p.file, p.line, p.msg)
	}
}

// Where an image reference was found. Compose and workflow references of
// one repository must agree (consistency rule).
const (
	originDockerfile = "dockerfile"
	originWorkflow   = "workflow"
	originAction     = "action"
	originCompose    = "compose"
	originMakefile   = "makefile"
)

// imageRef is one container image reference found in an image position.
type imageRef struct {
	file   string
	line   int
	ref    string
	origin string
}

// actionPin is one SHA-pinned `uses:` with its version comment, kept for
// -resolve's tag lookup.
type actionPin struct {
	file    string
	line    int
	repo    string // owner/repo
	sha     string
	version string // vX.Y.Z from the comment
}

// result is what a clean check found; main prints it and -resolve reads it.
type result struct {
	toolchain string     // "1.26.10" (no "go" prefix)
	golang    []imageRef // every golang reference
	images    int        // image references checked
	actions   []actionPin
}

var (
	// toolchainVersion accepts only a release triple: GOLANG_VERSION is
	// always one, and an rc or custom toolchain has no image to pin.
	toolchainVersion = regexp.MustCompile(`^go(\d+\.\d+\.\d+)$`)

	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

	// actionRef is owner/repo[/path]@<full commit SHA>. A short SHA is
	// refused: it is ambiguous and GitHub resolves it like a branch name.
	actionRef = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:/[^@\s]+)?@([0-9a-f]{40})$`)

	// versionComment is the `# vX.Y.Z` a reviewer reads instead of the SHA.
	versionComment = regexp.MustCompile(`^#\s*(v\d+\.\d+\.\d+)(?:\s|$)`)

	composeName = regexp.MustCompile(`^(?:docker-)?compose(?:[.-][^/]*)?\.ya?ml$`)
)

// hubRepo folds the Docker Hub spellings of a repository into one, so
// `postgres`, `library/postgres` and `docker.io/library/postgres` are the
// same repository.
func hubRepo(name string) string {
	name = strings.TrimPrefix(name, "index.docker.io/")
	name = strings.TrimPrefix(name, "docker.io/")
	return strings.TrimPrefix(name, "library/")
}

// isGolangRepo reports whether an image repository is the official golang
// image under any registry or mirror path (golang, library/golang,
// docker.io/library/golang, mirror.gcr.io/library/golang, …). A mirror
// spelling must not drop a reference out of the parity rule.
func isGolangRepo(name string) bool {
	return name == "golang" || strings.HasSuffix(name, "/golang")
}

// parseToolchain returns the release version ("1.26.10") of go.mod's single
// `toolchain goX.Y.Z` line, refusing a missing, repeated or non-release one.
func parseToolchain(gomod []byte) (string, error) {
	var found []string
	for _, raw := range strings.Split(string(gomod), "\n") {
		line := stripGoModComment(strings.TrimSpace(raw))
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "toolchain" {
			continue
		}
		if len(fields) != 2 {
			return "", fmt.Errorf("malformed toolchain line %q", strings.TrimSpace(raw))
		}
		found = append(found, fields[1])
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("go.mod has no `toolchain goX.Y.Z` line: under GOTOOLCHAIN=local (the golang image's default) nothing then ties the shipped compiler to a version")
	case 1:
	default:
		return "", fmt.Errorf("go.mod has %d toolchain lines; want exactly one", len(found))
	}
	m := toolchainVersion.FindStringSubmatch(found[0])
	if m == nil {
		return "", fmt.Errorf("toolchain %q is not a release goX.Y.Z: no golang image carries it", found[0])
	}
	return m[1], nil
}

func stripGoModComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		return strings.TrimSpace(line[:i])
	}
	return line
}

// splitImageRef splits name[:tag][@digest]. The tag is the part after the
// last ':' that follows the last '/', so a registry port is not a tag.
func splitImageRef(ref string) (name, tag, digest string) {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		ref, digest = ref[:i], ref[i+1:]
	}
	name = ref
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	return name, tag, digest
}

// pinProblem explains why ref is not name:tag@sha256:<digest>, or returns "".
func pinProblem(ref string) string {
	if strings.ContainsAny(ref, "${}") {
		return fmt.Sprintf("image %q is computed (a variable or expression): only a literal name:tag@sha256:<digest> can be verified", ref)
	}
	name, tag, digest := splitImageRef(ref)
	switch {
	case digest == "":
		return fmt.Sprintf("image %q is not pinned by digest: write it as name:tag@sha256:<digest>", ref)
	case !digestPattern.MatchString(digest):
		return fmt.Sprintf("image %q has a malformed digest: want sha256 and 64 lowercase hex digits", ref)
	case name == "" || tag == "":
		return fmt.Sprintf("image %q has no tag: keep the tag next to the digest (name:tag@sha256:...) so reviewers and Dependabot can read the version", ref)
	}
	return ""
}

// golangTagVersion is the Go version a golang image tag names: the part
// before the first '-' ("1.26.10" of "1.26.10-bookworm").
func golangTagVersion(tag string) string {
	v, _, _ := strings.Cut(tag, "-")
	return v
}

// scanned collects what one pass over the repository found.
type scanned struct {
	toolchain string
	images    []imageRef
	actions   []actionPin
	problems  []problem
}

func (s *scanned) refuse(file string, line int, format string, args ...any) {
	s.problems = append(s.problems, problem{file: file, line: line, msg: fmt.Sprintf(format, args...)})
}

func (s *scanned) addImage(file string, line int, ref, origin string) {
	s.images = append(s.images, imageRef{file: file, line: line, ref: ref, origin: origin})
}

// checkUses applies the action pin rule to one `uses:` value.
func (s *scanned) checkUses(file string, line int, uses, comment, origin string) {
	switch {
	case strings.HasPrefix(uses, "./"):
		// A local action or reusable workflow is this repository's own
		// reviewed code (its own file is scanned when it is an action).
		return
	case strings.HasPrefix(uses, "docker://"):
		s.addImage(file, line, strings.TrimPrefix(uses, "docker://"), origin)
		return
	}
	m := actionRef.FindStringSubmatch(uses)
	if m == nil {
		s.refuse(file, line, "action %q is not pinned to a full 40-hex commit SHA: a tag or branch can be moved under every run", uses)
		return
	}
	v := versionComment.FindStringSubmatch(comment)
	if v == nil {
		s.refuse(file, line, "action %q has no `# vX.Y.Z` comment naming the release its SHA is", uses)
		return
	}
	s.actions = append(s.actions, actionPin{file: file, line: line, repo: m[1], sha: m[2], version: v[1]})
}

// The kinds of file the check reads.
type fileSet struct {
	dockerfiles, workflows, actions, composes, makefiles []string
}

// skipDirs are never descended into: vendored or installed third-party
// trees and test fixtures are not this repository's build inputs.
var skipDirs = map[string]bool{".git": true, "vendor": true, "node_modules": true, "testdata": true}

// discover walks the tree once and classifies every file the rules read.
// Workflows are only .github/workflows/*.y{a,}ml (GitHub reads no
// subdirectory); everything else is found at any depth. A symlinked file is
// read through its link, as a builder or compose would.
func discover(root string) (fileSet, error) {
	var fsys fileSet
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// A dangling link or a special file is not a build input.
		if info, statErr := os.Stat(p); statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		base := d.Name()
		switch {
		case base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile.") || strings.HasSuffix(base, ".Dockerfile"):
			fsys.dockerfiles = append(fsys.dockerfiles, rel)
		case path.Dir(rel) == ".github/workflows" && (strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")):
			fsys.workflows = append(fsys.workflows, rel)
		case strings.HasPrefix(rel, ".github/actions/") && (base == "action.yml" || base == "action.yaml"):
			fsys.actions = append(fsys.actions, rel)
		case composeName.MatchString(base):
			fsys.composes = append(fsys.composes, rel)
		case base == "Makefile" || base == "makefile" || base == "GNUmakefile" || strings.HasSuffix(base, ".mk"):
			fsys.makefiles = append(fsys.makefiles, rel)
		}
		return nil
	})
	for _, l := range [][]string{fsys.dockerfiles, fsys.workflows, fsys.actions, fsys.composes, fsys.makefiles} {
		sort.Strings(l)
	}
	return fsys, err
}

// check runs the static rules over the repository at root.
func check(root string) (result, []problem) {
	var s scanned
	var res result

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return res, []problem{{file: "go.mod", msg: err.Error()}}
	}
	toolchain, err := parseToolchain(gomod)
	if err != nil {
		s.refuse("go.mod", 0, "%v", err)
	}
	s.toolchain = toolchain
	res.toolchain = toolchain

	files, err := discover(root)
	if err != nil {
		return res, []problem{{msg: "walking the repository: " + err.Error()}}
	}
	read := func(rel string) ([]byte, bool) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			s.refuse(rel, 0, "%v", err)
			return nil, false
		}
		return b, true
	}
	for _, f := range files.dockerfiles {
		if b, ok := read(f); ok {
			s.scanDockerfile(f, b)
		}
	}
	for _, f := range files.workflows {
		if b, ok := read(f); ok {
			s.scanWorkflow(f, b)
		}
	}
	for _, f := range files.actions {
		if b, ok := read(f); ok {
			s.scanAction(f, b)
		}
	}
	for _, f := range files.composes {
		if b, ok := read(f); ok {
			s.scanCompose(f, b)
		}
	}
	for _, f := range files.makefiles {
		if b, ok := read(f); ok {
			s.scanShellImages(f, 1, string(b), originMakefile)
		}
	}

	for _, img := range s.images {
		if msg := pinProblem(img.ref); msg != "" {
			s.refuse(img.file, img.line, "%s", msg)
		}
	}

	var golang []imageRef
	dockerGolang := 0
	for _, img := range s.images {
		if name, _, _ := splitImageRef(img.ref); isGolangRepo(name) {
			golang = append(golang, img)
			if img.origin == originDockerfile {
				dockerGolang++
			}
		}
	}
	if dockerGolang == 0 {
		s.refuse("", 0, "no golang image reference in any Dockerfile: the standalone image's compiler cannot be compared with go.mod's toolchain (was the builder stage moved off the official golang image?)")
	}
	if toolchain != "" {
		s.problems = append(s.problems, golangParity(toolchain, golang)...)
	}
	s.problems = append(s.problems, composeConsistency(s.images)...)

	res.golang = golang
	res.images = len(s.images)
	res.actions = s.actions
	return res, s.problems
}

// golangParity refuses a golang reference that is unpinned, names another
// Go version than the toolchain, or is a different image than the others
// (another digest, or the same digest under another tag).
func golangParity(toolchain string, refs []imageRef) []problem {
	var out []problem
	var firstDigest, firstTag string
	var first imageRef
	for _, r := range refs {
		_, tag, digest := splitImageRef(r.ref)
		if digest == "" || !digestPattern.MatchString(digest) || tag == "" {
			out = append(out, problem{r.file, r.line, fmt.Sprintf("golang image %q is not pinned as golang:%s-<variant>@sha256:<digest>: under GOTOOLCHAIN=local a moving tag decides the compiler", r.ref, toolchain)})
			continue
		}
		if v := golangTagVersion(tag); v != toolchain {
			out = append(out, problem{r.file, r.line, fmt.Sprintf("golang image %q names Go %s but go.mod's toolchain is go%s: the image's own GOLANG_VERSION is what compiles here (GOTOOLCHAIN=local)", r.ref, v, toolchain)})
			continue
		}
		if firstDigest == "" {
			firstDigest, firstTag, first = digest, tag, r
			continue
		}
		if digest != firstDigest || tag != firstTag {
			out = append(out, problem{r.file, r.line, fmt.Sprintf("golang image %q differs from %q at %s:%d: every golang reference must be the same tag and digest", r.ref, first.ref, first.file, first.line)})
		}
	}
	return out
}

// composeConsistency refuses a workflow or compose image whose repository a
// compose file also uses under another tag or digest. CI's postgres service
// stands in for the compose stack's database; once Dependabot moves the
// compose digest, a CI image left behind would test a database nobody runs.
func composeConsistency(images []imageRef) []problem {
	first := map[string]imageRef{}
	for _, img := range images {
		if img.origin != originCompose || pinProblem(img.ref) != "" {
			continue
		}
		name, _, _ := splitImageRef(img.ref)
		if _, ok := first[hubRepo(name)]; !ok {
			first[hubRepo(name)] = img
		}
	}
	var out []problem
	for _, img := range images {
		if img.origin != originCompose && img.origin != originWorkflow {
			continue
		}
		name, _, _ := splitImageRef(img.ref)
		want, ok := first[hubRepo(name)]
		if !ok || img.ref == want.ref || pinProblem(img.ref) != "" {
			continue
		}
		out = append(out, problem{img.file, img.line, fmt.Sprintf("image %q differs from the compose stack's %q at %s:%d: one repository, one tag and digest across compose and workflow files", img.ref, want.ref, want.file, want.line)})
	}
	return out
}
