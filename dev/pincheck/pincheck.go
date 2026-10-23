package main

// INIT-024 / EPIC-004 / TASK-009 (review findings SC-08, SC-09): the
// supply-chain pin and toolchain-parity guard.
//
// Why parity matters. The official golang image sets GOTOOLCHAIN=local, so
// the Go that compiles the standalone image is the image's own GOLANG_VERSION
// — go.mod's `toolchain` line is ignored there. Bumping go.mod alone (say,
// for a stdlib security release) would therefore keep shipping the old
// compiler and its advisories while every host build and every setup-go CI
// job (GOTOOLCHAIN=auto) used the new one, with nothing red anywhere. This
// check makes that drift a failure: every golang image reference in the
// repository must name the toolchain's exact patch version, and all of them
// must be the same digest. With -resolve it also reads the digest's image
// config from the registry (manifest and config only — nothing is pulled)
// and refuses unless every platform's GOLANG_VERSION is the toolchain
// version, so a tag that was edited without its digest cannot pass.
//
// Why pins matter. Spine is a public repository: a moved action tag or base
// image tag reaches every contributor's fork and CI run. Every Dockerfile
// FROM, every workflow and compose image is name:tag@sha256:<digest> (the
// tag stays for the reader and for Dependabot), and every workflow `uses:`
// is a 40-hex commit SHA followed by a `# vX.Y.Z` comment.
//
// Files scanned, relative to -root: go.mod, Dockerfile, Dockerfile.*,
// *.Dockerfile, .github/workflows/*.y{a,}ml, docker-compose*.y{a,}ml,
// compose*.y{a,}ml, Makefile and *.mk. Comment lines are skipped.
//
// To move the toolchain: change go.mod's `toolchain` line, then every
// golang:<version>-bookworm@sha256:<digest> reference (Dockerfile builder,
// the ci.yml git-floor container, the Makefile's DOCKER_GO) to the new tag
// and its index digest (`docker buildx imagetools inspect <tag>`), and run
// `go run ./dev/pincheck -resolve`.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// imageRef is one container image reference found in an image position
// (Dockerfile FROM / COPY --from, workflow container or service image,
// compose image) or, for golang, anywhere in a scanned file.
type imageRef struct {
	file string
	line int
	ref  string
}

// result is what a clean check found; main prints it.
type result struct {
	toolchain string     // "1.26.10" (no "go" prefix)
	golang    []imageRef // every golang reference
	images    int        // pinned image references in image positions
	actions   int        // pinned `uses:` references
}

var (
	// toolchainVersion accepts only a release triple: GOLANG_VERSION is
	// always one, and an rc or custom toolchain has no image to pin.
	toolchainVersion = regexp.MustCompile(`^go(\d+\.\d+\.\d+)$`)

	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

	// actionRef is owner/repo[/path]@<full commit SHA>. A short SHA is
	// refused: it is ambiguous and GitHub resolves it like a branch name.
	actionRef = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[^@\s]+)?@[0-9a-f]{40}$`)

	// versionComment is the `# vX.Y.Z` a reviewer reads instead of the SHA.
	versionComment = regexp.MustCompile(`^#\s*v\d+\.\d+\.\d+(?:\s|$)`)

	yamlImage     = regexp.MustCompile(`^\s*(?:-\s+)?image:\s*(.*)$`)
	yamlContainer = regexp.MustCompile(`^\s*(?:-\s+)?container:\s*(.*)$`)
	yamlUses      = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(.*)$`)

	// golangToken finds a golang image reference anywhere in a line (a
	// Makefile `docker run`, a workflow `run:` script, an image position).
	// golang.org/... module paths have no ':' after "golang" and never match.
	golangToken = regexp.MustCompile(`(?:^|[\s"'=(:])((?:(?:index\.)?docker\.io/)?(?:library/)?golang:[^\s"'()]+)`)
)

// golangNames are the spellings of the official golang image repository.
var golangNames = map[string]bool{
	"golang":                         true,
	"library/golang":                 true,
	"docker.io/library/golang":       true,
	"index.docker.io/library/golang": true,
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
		return "", errors.New("go.mod has no `toolchain goX.Y.Z` line: under GOTOOLCHAIN=local (the golang image's default) nothing then ties the shipped compiler to a version")
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

// splitTrailingComment splits `value # comment`. YAML and Makefiles start a
// comment at a '#' preceded by whitespace; values here never contain one.
func splitTrailingComment(s string) (value, comment string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
		}
	}
	return strings.TrimSpace(s), ""
}

func unquote(s string) string {
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func isCommentLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#")
}

// scanned collects what one pass over the repository found.
type scanned struct {
	images   []imageRef // image positions (pin rule)
	golang   []imageRef // golang references anywhere (parity rule)
	actions  int
	problems []problem
}

// scanGolangTokens records every golang image reference on a non-comment line.
func (s *scanned) scanGolangTokens(file string, content []byte) {
	for i, raw := range strings.Split(string(content), "\n") {
		if isCommentLine(raw) {
			continue
		}
		line, _ := splitTrailingComment(raw)
		for _, m := range golangToken.FindAllStringSubmatch(line, -1) {
			s.golang = append(s.golang, imageRef{file: file, line: i + 1, ref: m[1]})
		}
	}
}

// scanDockerfile records FROM and COPY --from images that are not a build
// stage, `scratch` or a stage index.
func (s *scanned) scanDockerfile(file string, content []byte) {
	stages := map[string]bool{}
	for i, raw := range strings.Split(string(content), "\n") {
		if isCommentLine(raw) {
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			rest := fields[1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				s.problems = append(s.problems, problem{file, i + 1, "FROM without an image"})
				continue
			}
			image := rest[0]
			// The stage this line declares is usable from the next line
			// on, so it is checked against the earlier stages first.
			if !strings.EqualFold(image, "scratch") && !stages[strings.ToLower(image)] {
				s.images = append(s.images, imageRef{file: file, line: i + 1, ref: image})
			}
			if len(rest) >= 3 && strings.EqualFold(rest[1], "AS") {
				stages[strings.ToLower(rest[2])] = true
			}
		case "COPY", "ADD":
			for _, f := range fields[1:] {
				from, ok := strings.CutPrefix(f, "--from=")
				if !ok {
					continue
				}
				if stages[strings.ToLower(from)] || isAllDigits(from) {
					continue
				}
				s.images = append(s.images, imageRef{file: file, line: i + 1, ref: from})
			}
		}
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// scanYAML records image (and, for workflows, container) values and checks
// every workflow `uses:`.
func (s *scanned) scanYAML(file string, content []byte, workflow bool) {
	for i, raw := range strings.Split(string(content), "\n") {
		if isCommentLine(raw) {
			continue
		}
		if m := yamlImage.FindStringSubmatch(raw); m != nil {
			if v, _ := splitTrailingComment(m[1]); v != "" {
				s.images = append(s.images, imageRef{file: file, line: i + 1, ref: unquote(v)})
			}
			continue
		}
		if !workflow {
			continue
		}
		if m := yamlContainer.FindStringSubmatch(raw); m != nil {
			// `container:` followed by a mapping has no inline value; its
			// `image:` is matched on its own line.
			if v, _ := splitTrailingComment(m[1]); v != "" && !strings.HasPrefix(v, "{") {
				s.images = append(s.images, imageRef{file: file, line: i + 1, ref: unquote(v)})
			}
			continue
		}
		if m := yamlUses.FindStringSubmatch(raw); m != nil {
			v, comment := splitTrailingComment(m[1])
			s.checkUses(file, i+1, unquote(v), comment)
		}
	}
}

func (s *scanned) checkUses(file string, line int, uses, comment string) {
	switch {
	case strings.HasPrefix(uses, "./"):
		// A local action is this repository's own reviewed code.
		return
	case strings.HasPrefix(uses, "docker://"):
		s.images = append(s.images, imageRef{file: file, line: line, ref: strings.TrimPrefix(uses, "docker://")})
		return
	case !actionRef.MatchString(uses):
		s.problems = append(s.problems, problem{file, line, fmt.Sprintf("action %q is not pinned to a full 40-hex commit SHA: a tag or branch can be moved under every run", uses)})
	case !versionComment.MatchString(comment):
		s.problems = append(s.problems, problem{file, line, fmt.Sprintf("action %q has no `# vX.Y.Z` comment naming the release its SHA is", uses)})
	default:
		s.actions++
	}
}

// globFiles returns the root-relative files matching each pattern, sorted
// and de-duplicated.
func globFiles(root string, patterns ...string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, p := range patterns {
		matches, err := filepath.Glob(filepath.Join(root, p))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			rel, err := filepath.Rel(root, m)
			if err != nil {
				return nil, err
			}
			if info, err := os.Stat(m); err == nil && info.Mode().IsRegular() && !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// check runs the static pin and parity rules over the repository at root.
func check(root string) (result, []problem) {
	var s scanned
	var res result

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return res, []problem{{file: "go.mod", msg: err.Error()}}
	}
	toolchain, err := parseToolchain(gomod)
	if err != nil {
		s.problems = append(s.problems, problem{file: "go.mod", msg: err.Error()})
	}
	res.toolchain = toolchain

	read := func(rel string) []byte {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			s.problems = append(s.problems, problem{file: rel, msg: err.Error()})
			return nil
		}
		return b
	}

	dockerfiles, err := globFiles(root, "Dockerfile", "Dockerfile.*", "*.Dockerfile")
	if err != nil {
		return res, []problem{{msg: err.Error()}}
	}
	workflows, err := globFiles(root, ".github/workflows/*.yml", ".github/workflows/*.yaml")
	if err != nil {
		return res, []problem{{msg: err.Error()}}
	}
	composes, err := globFiles(root, "docker-compose*.yml", "docker-compose*.yaml", "compose*.yml", "compose*.yaml")
	if err != nil {
		return res, []problem{{msg: err.Error()}}
	}
	makefiles, err := globFiles(root, "Makefile", "*.mk")
	if err != nil {
		return res, []problem{{msg: err.Error()}}
	}

	dockerGolang := 0
	for _, f := range dockerfiles {
		b := read(f)
		s.scanDockerfile(f, b)
		before := len(s.golang)
		s.scanGolangTokens(f, b)
		dockerGolang += len(s.golang) - before
	}
	for _, f := range workflows {
		b := read(f)
		s.scanYAML(f, b, true)
		s.scanGolangTokens(f, b)
	}
	for _, f := range composes {
		b := read(f)
		s.scanYAML(f, b, false)
		s.scanGolangTokens(f, b)
	}
	for _, f := range makefiles {
		s.scanGolangTokens(f, read(f))
	}

	for _, img := range s.images {
		if msg := pinProblem(img.ref); msg != "" {
			s.problems = append(s.problems, problem{img.file, img.line, msg})
		}
	}

	if dockerGolang == 0 {
		s.problems = append(s.problems, problem{msg: "no golang image reference in any Dockerfile: the standalone image's compiler cannot be compared with go.mod's toolchain (was the builder stage moved off the official golang image?)"})
	}
	if toolchain != "" {
		s.problems = append(s.problems, golangParity(toolchain, s.golang)...)
	}

	res.golang = s.golang
	res.images = len(s.images)
	res.actions = s.actions
	return res, s.problems
}

// golangParity refuses a golang reference that is unpinned, names another
// Go version than the toolchain, or is a different image than the others.
func golangParity(toolchain string, refs []imageRef) []problem {
	var out []problem
	var firstDigest, firstTag string
	var first imageRef
	for _, r := range refs {
		name, tag, digest := splitImageRef(r.ref)
		if !golangNames[name] {
			continue
		}
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

// inspector returns `docker buildx imagetools inspect --format '{{json .Image}}'`
// output for ref.
type inspector func(ctx context.Context, ref string) ([]byte, error)

// resolveGolang reads every distinct golang digest's image config from the
// registry and refuses unless each platform's GOLANG_VERSION is the
// toolchain version. It inspects by digest alone (the tag is dropped), so
// what is verified is exactly what a build pulls.
func resolveGolang(ctx context.Context, res result, inspect inspector) ([]string, []problem) {
	var report []string
	var out []problem
	done := map[string]bool{}
	for _, r := range res.golang {
		name, _, digest := splitImageRef(r.ref)
		if !golangNames[name] || digest == "" || done[digest] {
			continue
		}
		done[digest] = true
		byDigest := "docker.io/library/golang@" + digest
		raw, err := inspect(ctx, byDigest)
		if err != nil {
			out = append(out, problem{r.file, r.line, fmt.Sprintf("cannot read %s from the registry: %v", byDigest, err)})
			continue
		}
		platforms, err := imageGoVersions(raw)
		if err != nil {
			out = append(out, problem{r.file, r.line, fmt.Sprintf("%s: %v", byDigest, err)})
			continue
		}
		keys := make([]string, 0, len(platforms))
		for k := range platforms {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, p := range keys {
			if platforms[p] != res.toolchain {
				out = append(out, problem{r.file, r.line, fmt.Sprintf("%s (%s) has GOLANG_VERSION=%q, not go.mod's toolchain %s: the digest is not the image its tag names", byDigest, p, platforms[p], res.toolchain)})
			}
		}
		report = append(report, fmt.Sprintf("%s: GOLANG_VERSION=%s on %s", byDigest, res.toolchain, strings.Join(keys, ", ")))
	}
	return report, out
}

// imageConfig is the part of an OCI image config this check reads.
type imageConfig struct {
	Config struct {
		Env []string `json:"Env"`
	} `json:"config"`
}

// imageGoVersions parses `{{json .Image}}` output — a platform-keyed map for
// an image index, or one image config for a single manifest — into each
// platform's GOLANG_VERSION. A platform without one is an error: it is not a
// golang image, and nothing else can stand in for the version.
func imageGoVersions(raw []byte) (map[string]string, error) {
	raw = bytes.TrimSpace(raw)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("unparseable image config: %w", err)
	}
	images := map[string]json.RawMessage{}
	if _, single := top["config"]; single {
		images["(single manifest)"] = raw
	} else {
		images = top
	}
	if len(images) == 0 {
		return nil, errors.New("the registry returned no image config")
	}
	out := map[string]string{}
	for platform, msg := range images {
		var img imageConfig
		if err := json.Unmarshal(msg, &img); err != nil {
			return nil, fmt.Errorf("%s: unparseable image config: %w", platform, err)
		}
		version := ""
		for _, e := range img.Config.Env {
			if v, ok := strings.CutPrefix(e, "GOLANG_VERSION="); ok {
				version = v
			}
		}
		if version == "" {
			return nil, fmt.Errorf("%s: image config has no GOLANG_VERSION", platform)
		}
		out[platform] = version
	}
	return out, nil
}
