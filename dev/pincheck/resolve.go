package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// -resolve is the only part of the check that uses the network. It reads
// registry manifests and image configs (never layers) and remote git refs
// (never objects), so a run cannot change what a later build uses.

// inspector returns `docker buildx imagetools inspect --format '{{json .Image}}'`
// output for ref.
type inspector func(ctx context.Context, ref string) ([]byte, error)

// tagResolver returns the commit a repository's tag names (the peeled
// commit of an annotated tag), or "" when the tag does not exist.
type tagResolver func(ctx context.Context, repo, tag string) (string, error)

// canonicalRepo spells the Docker Hub official image in full, so the
// inspection is the same one whichever short form the file used; a mirror
// path is inspected as written.
func canonicalRepo(name string) string {
	if hubRepo(name) == "golang" {
		return "docker.io/library/golang"
	}
	return name
}

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
		byDigest := canonicalRepo(name) + "@" + digest
		if digest == "" || done[byDigest] {
			continue
		}
		done[byDigest] = true
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

// resolveActions looks each action's `# vX.Y.Z` up in its own repository and
// refuses unless that tag names exactly the pinned commit. A comment is
// otherwise only a claim: a SHA from another release, or a commit that
// exists only in a fork (GitHub resolves fork-network commits through the
// parent repository's name), would pass with a truthful-looking version
// beside it.
func resolveActions(ctx context.Context, res result, lookup tagResolver) ([]string, []problem) {
	type key struct{ repo, tag string }
	type answer struct {
		sha string
		err error
	}
	cache := map[key]answer{}
	var out []problem
	checked := 0
	for _, a := range res.actions {
		k := key{a.repo, a.version}
		ans, ok := cache[k]
		if !ok {
			ans.sha, ans.err = lookup(ctx, a.repo, a.version)
			cache[k] = ans
		}
		switch {
		case ans.err != nil:
			out = append(out, problem{a.file, a.line, fmt.Sprintf("cannot resolve %s tag %s: %v", a.repo, a.version, ans.err)})
		case ans.sha == "":
			out = append(out, problem{a.file, a.line, fmt.Sprintf("%s has no tag %s: the version comment names no release", a.repo, a.version)})
		case ans.sha != a.sha:
			out = append(out, problem{a.file, a.line, fmt.Sprintf("%s@%s is not %s: that tag is commit %s", a.repo, a.sha, a.version, ans.sha)})
		default:
			checked++
		}
	}
	var report []string
	if len(res.actions) > 0 {
		report = append(report, fmt.Sprintf("%d of %d action pin(s) equal their tag's commit (%d distinct tag(s) resolved)", checked, len(res.actions), len(cache)))
	}
	return report, out
}

// parseLsRemote picks a tag's commit out of `git ls-remote` output for
// refs/tags/<tag> and refs/tags/<tag>^{}: the peeled line when the tag is
// annotated, the tag line itself when it is lightweight.
func parseLsRemote(out []byte, tag string) string {
	direct, peeled := "", ""
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[1] {
		case "refs/tags/" + tag:
			direct = fields[0]
		case "refs/tags/" + tag + "^{}":
			peeled = fields[0]
		}
	}
	if peeled != "" {
		return peeled
	}
	return direct
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
