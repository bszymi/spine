package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

var (
	dGo  = "sha256:" + strings.Repeat("a", 64)
	dGo2 = "sha256:" + strings.Repeat("b", 64)
	dDeb = "sha256:" + strings.Repeat("c", 64)
	dPg  = "sha256:" + strings.Repeat("d", 64)
	dPg2 = "sha256:" + strings.Repeat("e", 64)
	sha  = strings.Repeat("1", 40)
	sha2 = strings.Repeat("2", 40)
)

// goodTree is a minimal repository whose pins and toolchain parity hold:
// the shape of Spine's own go.mod, Dockerfile, ci.yml, Makefile and compose
// file, plus a composite action.
func goodTree() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/x\n\ngo 1.26.0\n\ntoolchain go1.26.10\n",
		"Dockerfile": "# golang:1.26.10-bookworm\n" + // 1: a comment, not a directive
			"FROM golang:1.26.10-bookworm@" + dGo + " AS builder\n" + // 2
			"RUN go build -o /x .\n" + // 3
			"FROM debian:bookworm-slim@" + dDeb + "\n" + // 4
			"COPY --from=builder /x /usr/local/bin/x\n", // 5
		".github/workflows/ci.yml": "permissions:\n  contents: read\n" + // 1-2
			"jobs:\n" + // 3
			"  a:\n" + // 4
			"    runs-on: ubuntu-24.04\n" + // 5
			"    services:\n" + // 6
			"      postgres:\n" + // 7
			"        image: postgres:18-bookworm@" + dPg + " # PG 18.6\n" + // 8
			"    container:\n" + // 9
			"      image: golang:1.26.10-bookworm@" + dGo + "\n" + // 10
			"    steps:\n" + // 11
			"      - uses: actions/checkout@" + sha + " # v4.4.0\n" + // 12
			"      - uses: \"actions/setup-go@" + sha2 + "\" # v5.6.0\n" + // 13
			"        with:\n" + // 14
			"          go-version: '1.26.10'\n" + // 15
			"      - uses: ./.github/actions/local\n" + // 16
			"      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...\n" + // 17
			"      - run: |\n" + // 18
			"          docker build --tag spine:ci .\n" + // 19
			"          docker run --rm --entrypoint git spine:ci --version\n", // 20
		".github/actions/local/action.yml": "name: local\nruns:\n  using: composite\n  steps:\n" +
			"    - uses: actions/cache@" + sha + " # v4.2.0\n" +
			"    - run: echo hi\n      shell: bash\n",
		"Makefile": "DOCKER_GO = docker run --rm \\\n" + // 1
			"\t-v \"$(CURDIR)\":/app \\\n" + // 2
			"\t-w /app golang:1.26.10-bookworm@" + dGo + "\n" + // 3
			"test-db-up:\n\tdocker compose -f docker-compose.yaml up -d\n",
		"docker-compose.yaml": "services:\n  db:\n    image: postgres:18-bookworm@" + dPg + "\n",
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const ciFile = ".github/workflows/ci.yml"

// addJob appends a job to the fixture workflow.
func addJob(f map[string]string, job string) {
	f[ciFile] += job
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(files map[string]string)
		want   []string // substrings of the refusals; none means the tree passes
	}{
		{name: "real pair passes", mutate: func(map[string]string) {}},

		// ── toolchain parity ──
		{
			// AC 5: go.mod moved to a new patch, the images and setup-go did
			// not. Under GOTOOLCHAIN=local the image keeps compiling 1.26.10.
			name: "mismatched fixture: toolchain bumped without the images",
			mutate: func(f map[string]string) {
				f["go.mod"] = strings.Replace(f["go.mod"], "go1.26.10", "go1.26.11", 1)
			},
			want: []string{
				"Dockerfile:2: golang image \"golang:1.26.10-bookworm@" + dGo + "\" names Go 1.26.10 but go.mod's toolchain is go1.26.11",
				ciFile + ":10: golang image",
				"Makefile:3: golang image",
				ciFile + ":15: actions/setup-go go-version \"1.26.10\" is not go.mod's toolchain 1.26.11",
			},
		},
		{
			name: "mismatched fixture: Dockerfile image bumped without go.mod",
			mutate: func(f map[string]string) {
				f["Dockerfile"] = strings.Replace(f["Dockerfile"], "FROM golang:1.26.10-bookworm@"+dGo, "FROM golang:1.26.11-bookworm@"+dGo2, 1)
			},
			want: []string{"Dockerfile:2: golang image \"golang:1.26.11-bookworm@" + dGo2 + "\" names Go 1.26.11 but go.mod's toolchain is go1.26.10"},
		},
		{
			name:   "no toolchain line",
			mutate: func(f map[string]string) { f["go.mod"] = "module example.com/x\n\ngo 1.26.0\n" },
			want:   []string{"go.mod: go.mod has no `toolchain goX.Y.Z` line"},
		},
		{
			name: "rc toolchain is not a release",
			mutate: func(f map[string]string) {
				f["go.mod"] = strings.Replace(f["go.mod"], "go1.26.10", "go1.27rc1", 1)
			},
			want: []string{"toolchain \"go1.27rc1\" is not a release goX.Y.Z"},
		},
		{
			name:   "two toolchain lines",
			mutate: func(f map[string]string) { f["go.mod"] += "toolchain go1.26.10\n" },
			want:   []string{"go.mod has 2 toolchain lines"},
		},
		{
			name: "floating golang tag in the Dockerfile",
			mutate: func(f map[string]string) {
				f["Dockerfile"] = strings.Replace(f["Dockerfile"], "golang:1.26.10-bookworm@"+dGo+" AS", "golang:1.26-bookworm AS", 1)
			},
			want: []string{
				"Dockerfile:2: image \"golang:1.26-bookworm\" is not pinned by digest",
				"Dockerfile:2: golang image \"golang:1.26-bookworm\" is not pinned as golang:1.26.10-<variant>@sha256:<digest>",
			},
		},
		{
			name: "minor-only golang tag with a digest",
			mutate: func(f map[string]string) {
				f["Dockerfile"] = strings.Replace(f["Dockerfile"], "golang:1.26.10-bookworm@"+dGo+" AS", "golang:1.26-bookworm@"+dGo+" AS", 1)
			},
			want: []string{"names Go 1.26 but go.mod's toolchain is go1.26.10"},
		},
		{
			name: "two golang digests for one tag",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "golang:1.26.10-bookworm@"+dGo, "golang:1.26.10-bookworm@"+dGo2, 1)
			},
			want: []string{ciFile + ":10: golang image \"golang:1.26.10-bookworm@" + dGo2 + "\" differs from"},
		},
		{
			// One digest under two tags: the tag a reviewer reads is not the
			// image every reference names.
			name: "one golang digest under two tags",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "golang:1.26.10-bookworm@"+dGo, "golang:1.26.10-trixie@"+dGo, 1)
			},
			want: []string{ciFile + ":10: golang image \"golang:1.26.10-trixie@" + dGo + "\" differs from \"golang:1.26.10-bookworm@" + dGo + "\" at Dockerfile:2"},
		},
		{
			name:   "unpinned golang in the Makefile",
			mutate: func(f map[string]string) { f["Makefile"] = "DOCKER_GO = docker run --rm golang:1.26-bookworm\n" },
			want: []string{
				"Makefile:1: image \"golang:1.26-bookworm\" is not pinned by digest",
				"Makefile:1: golang image \"golang:1.26-bookworm\" is not pinned",
			},
		},
		{
			name: "docker.io spelling of golang is still golang",
			mutate: func(f map[string]string) {
				f["Makefile"] = "X = docker run docker.io/library/golang:1.26.8-bookworm@" + dGo + "\n"
			},
			want: []string{"Makefile:1: golang image \"docker.io/library/golang:1.26.8-bookworm@" + dGo + "\" names Go 1.26.8"},
		},
		{
			name: "a mirror path of golang is still golang",
			mutate: func(f map[string]string) {
				f["Makefile"] = "X = docker run mirror.gcr.io/library/golang:1.26.8-bookworm@" + dGo + "\n"
			},
			want: []string{"Makefile:1: golang image \"mirror.gcr.io/library/golang:1.26.8-bookworm@" + dGo + "\" names Go 1.26.8"},
		},
		{
			name: "builder moved off the golang image",
			mutate: func(f map[string]string) {
				f["Dockerfile"] = "FROM debian:bookworm-slim@" + dDeb + "\n"
			},
			want: []string{"no golang image reference in any Dockerfile"},
		},

		// ── setup-go ──
		{
			name: "setup-go installs another Go",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "go-version: '1.26.10'", "go-version: '1.26.8'", 1)
			},
			want: []string{ciFile + ":15: actions/setup-go go-version \"1.26.8\" is not go.mod's toolchain 1.26.10"},
		},
		{
			name: "setup-go reads go-version-file",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "go-version: '1.26.10'", "go-version-file: go.mod", 1)
			},
			want: []string{
				ciFile + ":15: actions/setup-go uses go-version-file",
				ciFile + ":13: actions/setup-go has no `with.go-version`",
			},
		},
		{
			name: "setup-go without with",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "        with:\n          go-version: '1.26.10'\n", "", 1)
			},
			want: []string{ciFile + ":13: actions/setup-go has no `with.go-version`"},
		},

		// ── runner labels ──
		{
			name:   "ubuntu-latest floats",
			mutate: func(f map[string]string) { f[ciFile] = strings.Replace(f[ciFile], "ubuntu-24.04", "ubuntu-latest", 1) },
			want:   []string{ciFile + ":5: runs-on \"ubuntu-latest\" floats"},
		},
		{
			name: "-latest in a label list and a labels mapping",
			mutate: func(f map[string]string) {
				addJob(f, "  b:\n    runs-on: [self-hosted, macos-latest]\n    steps: []\n"+
					"  c:\n    runs-on:\n      group: g\n      labels: windows-latest\n    steps: []\n")
			},
			want: []string{ciFile + ":22: runs-on \"macos-latest\" floats", ciFile + ":27: runs-on \"windows-latest\" floats"},
		},
		{
			name:   "computed runner label",
			mutate: func(f map[string]string) { addJob(f, "  b:\n    runs-on: ${{ matrix.os }}\n    steps: []\n") },
			want:   []string{ciFile + ":22: runs-on \"${{ matrix.os }}\" is computed"},
		},

		// ── compose and workflow consistency ──
		{
			name: "CI postgres is not the compose postgres",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "postgres:18-bookworm@"+dPg, "postgres:18-bookworm@"+dPg2, 1)
			},
			want: []string{ciFile + ":8: image \"postgres:18-bookworm@" + dPg2 + "\" differs from the compose stack's \"postgres:18-bookworm@" + dPg + "\" at docker-compose.yaml:3"},
		},
		{
			name: "two compose files disagree",
			mutate: func(f map[string]string) {
				f["docker-compose.test.yaml"] = "services:\n  db:\n    image: postgres:17-bookworm@" + dPg2 + "\n"
			},
			want: []string{"docker-compose.yaml:3: image \"postgres:18-bookworm@" + dPg + "\" differs from the compose stack's \"postgres:17-bookworm@" + dPg2 + "\" at docker-compose.test.yaml:3"},
		},

		// ── image pins ──
		{
			name: "unpinned runtime base",
			mutate: func(f map[string]string) {
				f["Dockerfile"] = strings.Replace(f["Dockerfile"], "debian:bookworm-slim@"+dDeb, "debian:bookworm-slim", 1)
			},
			want: []string{"Dockerfile:4: image \"debian:bookworm-slim\" is not pinned by digest"},
		},
		{
			name: "digest without a tag",
			mutate: func(f map[string]string) {
				f["Dockerfile"] = strings.Replace(f["Dockerfile"], "debian:bookworm-slim@"+dDeb, "debian@"+dDeb, 1)
			},
			want: []string{"Dockerfile:4: image \"debian@" + dDeb + "\" has no tag"},
		},
		{
			name: "malformed digest",
			mutate: func(f map[string]string) {
				f["docker-compose.yaml"] = "services:\n  db:\n    image: postgres:18-bookworm@sha256:abc\n"
			},
			want: []string{"docker-compose.yaml:3: image \"postgres:18-bookworm@sha256:abc\" has a malformed digest"},
		},
		{
			name: "registry port is not a tag",
			mutate: func(f map[string]string) {
				f["docker-compose.yaml"] = "services:\n  db:\n    image: registry.local:5000/postgres@" + dPg + "\n"
			},
			want: []string{"has no tag"},
		},
		{
			name: "unpinned workflow service image",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "postgres:18-bookworm@"+dPg, "postgres:18-bookworm", 1)
			},
			want: []string{ciFile + ":8: image \"postgres:18-bookworm\" is not pinned by digest"},
		},
		{
			name: "short-form container image",
			mutate: func(f map[string]string) {
				addJob(f, "  b:\n    runs-on: ubuntu-24.04\n    container: golang:1.26-bookworm\n")
			},
			want: []string{
				ciFile + ":23: image \"golang:1.26-bookworm\" is not pinned by digest",
				ciFile + ":23: golang image \"golang:1.26-bookworm\" is not pinned",
			},
		},
		{
			name: "COPY --from an external image must be pinned",
			mutate: func(f map[string]string) {
				f["Dockerfile"] += "COPY --from=alpine/git:latest /usr/bin/git /usr/bin/git\n"
			},
			want: []string{"Dockerfile:6: image \"alpine/git:latest\" is not pinned by digest"},
		},
		{
			name:   "ONBUILD COPY --from an external image must be pinned",
			mutate: func(f map[string]string) { f["Dockerfile"] += "ONBUILD COPY --from=alpine:3 /a /b\n" },
			want:   []string{"Dockerfile:6: image \"alpine:3\" is not pinned by digest"},
		},

		// ── action pins ──
		{
			name: "action on a mutable tag",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "actions/checkout@"+sha+" # v4.4.0", "actions/checkout@v4", 1)
			},
			want: []string{ciFile + ":12: action \"actions/checkout@v4\" is not pinned to a full 40-hex commit SHA"},
		},
		{
			name: "short SHA",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "actions/checkout@"+sha, "actions/checkout@"+sha[:7], 1)
			},
			want: []string{"is not pinned to a full 40-hex commit SHA"},
		},
		{
			name: "SHA without a version comment",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], " # v4.4.0", "", 1)
			},
			want: []string{ciFile + ":12: action \"actions/checkout@" + sha + "\" has no `# vX.Y.Z` comment"},
		},
		{
			name: "major-only version comment",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], " # v4.4.0", " # v4", 1)
			},
			want: []string{"has no `# vX.Y.Z` comment"},
		},
		{
			name:   "docker:// action must be digest-pinned",
			mutate: func(f map[string]string) { f[ciFile] += "      - uses: docker://alpine:3.20\n" },
			want:   []string{ciFile + ":21: image \"alpine:3.20\" is not pinned by digest"},
		},
		{
			name: "reusable workflow on a tag",
			mutate: func(f map[string]string) {
				addJob(f, "  b:\n    uses: org/repo/.github/workflows/w.yml@v1\n")
			},
			want: []string{ciFile + ":22: action \"org/repo/.github/workflows/w.yml@v1\" is not pinned"},
		},

		// ── the line-scanner bypasses (review round 1): each is now refused ──
		{
			name:   "flow-style step",
			mutate: func(f map[string]string) { f[ciFile] += "      - {uses: actions/checkout@v4}\n" },
			want:   []string{ciFile + ":21: action \"actions/checkout@v4\" is not pinned"},
		},
		{
			name:   "quoted uses key",
			mutate: func(f map[string]string) { f[ciFile] += "      - \"uses\": actions/checkout@v4\n" },
			want:   []string{ciFile + ":21: action \"actions/checkout@v4\" is not pinned"},
		},
		{
			name:   "space before the colon",
			mutate: func(f map[string]string) { f[ciFile] += "      - uses : actions/checkout@v4\n" },
			want:   []string{ciFile + ":21: action \"actions/checkout@v4\" is not pinned"},
		},
		{
			name: "flow-style container mapping",
			mutate: func(f map[string]string) {
				addJob(f, "  b:\n    runs-on: ubuntu-24.04\n    container: {image: \"golang:1.26-bookworm\"}\n")
			},
			want: []string{ciFile + ":23: image \"golang:1.26-bookworm\" is not pinned by digest"},
		},
		{
			name: "flow-style services",
			mutate: func(f map[string]string) {
				addJob(f, "  b:\n    runs-on: ubuntu-24.04\n    services: {pg: {image: postgres:16}}\n")
			},
			want: []string{ciFile + ":23: image \"postgres:16\" is not pinned by digest"},
		},
		{
			name: "images in a run: script",
			mutate: func(f map[string]string) {
				f[ciFile] += "      - run: |\n" +
					"          docker run --rm -v \"$PWD\":/w -w /w alpine:3 sh -c 'true' && docker pull -q busybox:1.36\n" +
					"          docker container create --name x redis:7\n"
			},
			want: []string{
				ciFile + ":22: image \"alpine:3\" is not pinned by digest",
				ciFile + ":22: image \"busybox:1.36\" is not pinned by digest",
				ciFile + ":23: image \"redis:7\" is not pinned by digest",
			},
		},
		{
			// Codex round 3: jobs run on separate runners, so a tag one job
			// builds is pulled by another.
			name: "a tag built in another job is pulled there",
			mutate: func(f map[string]string) {
				addJob(f, "  b:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: docker build -t alpine:latest .\n"+
					"  c:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: docker run --rm alpine:latest\n")
			},
			want: []string{ciFile + ":28: image \"alpine:latest\" is not pinned by digest"},
		},
		{
			name: "run before the build, and a pull after it, in one job",
			mutate: func(f map[string]string) {
				addJob(f, "  b:\n    runs-on: ubuntu-24.04\n    steps:\n"+
					"      - run: docker run --rm local:ci\n"+
					"      - run: docker build -t local:ci . && docker pull local:ci\n")
			},
			want: []string{
				ciFile + ":24: image \"local:ci\" is not pinned by digest",
				ciFile + ":25: image \"local:ci\" is not pinned by digest",
			},
		},
		{
			name: "a Docker Hub spelling of the compose postgres must match it too",
			mutate: func(f map[string]string) {
				f[ciFile] = strings.Replace(f[ciFile], "image: postgres:18-bookworm@"+dPg, "image: docker.io/library/postgres:18-bookworm@"+dPg2, 1)
			},
			want: []string{ciFile + ":8: image \"docker.io/library/postgres:18-bookworm@" + dPg2 + "\" differs from the compose stack's"},
		},
		{
			name:   "an image in a Makefile recipe",
			mutate: func(f map[string]string) { f["Makefile"] += "shell:\n\tdocker run -it --rm alpine:3 sh\n" },
			want:   []string{"Makefile:7: image \"alpine:3\" is not pinned by digest"},
		},
		{
			name: "RUN --mount from an external image",
			mutate: func(f map[string]string) {
				f["Dockerfile"] += "RUN --mount=type=bind,from=alpine:3,source=/bin,target=/x true\n"
			},
			want: []string{"Dockerfile:6: image \"alpine:3\" is not pinned by digest"},
		},
		{
			name:   "syntax directive",
			mutate: func(f map[string]string) { f["Dockerfile"] = "# syntax=docker/dockerfile:1\n" + f["Dockerfile"] },
			want:   []string{"Dockerfile:1: image \"docker/dockerfile:1\" is not pinned by digest"},
		},
		{
			name: "a Dockerfile in a subdirectory",
			mutate: func(f map[string]string) {
				f["docker/runner/Dockerfile"] = "FROM alpine:3\n"
				f["build/tools.Dockerfile"] = "FROM busybox:1.36\n"
			},
			want: []string{
				"docker/runner/Dockerfile:1: image \"alpine:3\" is not pinned by digest",
				"build/tools.Dockerfile:1: image \"busybox:1.36\" is not pinned by digest",
			},
		},
		{
			name: "a composite or Docker action",
			mutate: func(f map[string]string) {
				f[".github/actions/x/action.yml"] = "runs:\n  using: composite\n  steps:\n    - uses: actions/cache@v4\n    - run: docker pull alpine:3\n      shell: bash\n"
				f[".github/actions/y/sub/action.yaml"] = "runs:\n  using: docker\n  image: docker://alpine:3.20\n"
			},
			want: []string{
				".github/actions/x/action.yml:4: action \"actions/cache@v4\" is not pinned",
				".github/actions/x/action.yml:5: image \"alpine:3\" is not pinned by digest",
				".github/actions/y/sub/action.yaml:3: image \"alpine:3.20\" is not pinned by digest",
			},
		},
		{
			name: "a continued FROM line",
			mutate: func(f map[string]string) {
				f["Dockerfile"] += "FROM \\\n    alpine:3 AS tail\n"
			},
			want: []string{"Dockerfile:6: image \"alpine:3\" is not pinned by digest"},
		},
		{
			name:   "a compose file in a subdirectory",
			mutate: func(f map[string]string) { f["deploy/compose.yaml"] = "services:\n  web:\n    image: nginx:1\n" },
			want:   []string{"deploy/compose.yaml:3: image \"nginx:1\" is not pinned by digest"},
		},

		// ── shapes that were already fail-closed stay fail-closed ──
		{
			name:   "folded uses scalar",
			mutate: func(f map[string]string) { f[ciFile] += "      - uses: >-\n          actions/checkout@v4\n" },
			want:   []string{"action \"actions/checkout@v4\" is not pinned"},
		},
		{
			name:   "a branch under a version comment",
			mutate: func(f map[string]string) { f[ciFile] += "      - uses: actions/checkout@main # v4.4.0\n" },
			want:   []string{ciFile + ":21: action \"actions/checkout@main\" is not pinned"},
		},
		{
			name: "ARG-computed FROM",
			mutate: func(f map[string]string) {
				f["Dockerfile"] += "ARG BASE=golang:1.26.10-bookworm@" + dGo + "\nFROM ${BASE}\n"
			},
			want: []string{"Dockerfile:7: image \"${BASE}\" is computed"},
		},
		{
			name: "expression-computed container image",
			mutate: func(f map[string]string) {
				addJob(f, "  b:\n    runs-on: ubuntu-24.04\n    container:\n      image: ${{ env.IMG }}\n")
			},
			want: []string{ciFile + ":24: image \"${{ env.IMG }}\" is computed"},
		},
		{
			name: "anchors, aliases and merge keys",
			mutate: func(f map[string]string) {
				f["docker-compose.yaml"] = "x-pg: &pg postgres:16\n" +
					"x-base: &base\n  image: redis:7\n" +
					"services:\n" +
					"  db:\n    image: *pg\n" +
					"  cache:\n    <<: *base\n"
			},
			want: []string{
				"docker-compose.yaml:1: image \"postgres:16\" is not pinned by digest",
				"docker-compose.yaml:3: image \"redis:7\" is not pinned by digest",
			},
		},
		{
			name: "FROM --platform",
			mutate: func(f map[string]string) {
				f["Dockerfile"] += "FROM --platform=$BUILDPLATFORM alpine:3 AS cross\n"
			},
			want: []string{"Dockerfile:6: image \"alpine:3\" is not pinned by digest"},
		},
		{
			name:   "a workflow that does not parse",
			mutate: func(f map[string]string) { f[".github/workflows/bad.yml"] = "jobs: [\n" },
			want:   []string{".github/workflows/bad.yml: does not parse as YAML"},
		},
		{
			name:   "a non-literal image",
			mutate: func(f map[string]string) { f["docker-compose.yaml"] = "services:\n  db:\n    image: [a, b]\n" },
			want:   []string{"docker-compose.yaml:3: image is not a literal string"},
		},

		// ── shapes that are allowed ──
		{
			name: "comments, stage names, scratch, stage indexes, local builds, pinned directives",
			mutate: func(f map[string]string) {
				f["Dockerfile"] = "# syntax=docker/dockerfile:1.7@" + dDeb + "\n" + f["Dockerfile"] +
					"# FROM golang:1.26-bookworm\n" +
					"FROM builder AS tested\n" +
					"RUN --mount=type=cache,target=/root/.cache --mount=from=builder,source=/x,target=/y true\n" +
					"FROM scratch\n" +
					"COPY --from=0 /x /x\n" +
					"COPY --from=tested /x /y\n"
				f[ciFile] += "      # - uses: actions/checkout@v4\n" +
					"      # image: postgres:18-bookworm\n" +
					"      - {uses: actions/checkout@" + sha + "} # v4.4.0\n" +
					"      - run: docker compose run --rm db psql   # compose images are checked as compose\n"
				f["Makefile"] += "# docker run golang:1.25-bookworm\n"
				f["vendor/x/Dockerfile"] = "FROM alpine:3\n"
				f["web/node_modules/y/Dockerfile"] = "FROM alpine:3\n"
				f["dev/pincheck/testdata/Dockerfile"] = "FROM alpine:3\n"
				f["docker-compose.override.yaml.example"] = "services:\n  db:\n    image: postgres:16\n"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := goodTree()
			tt.mutate(files)
			_, problems := check(writeTree(t, files))
			var got []string
			for _, p := range problems {
				got = append(got, p.String())
			}
			if len(tt.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("want no problems, got:\n%s", strings.Join(got, "\n"))
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("want refusals containing %q, got none", tt.want)
			}
			joined := strings.Join(got, "\n")
			for _, w := range tt.want {
				if !strings.Contains(joined, w) {
					t.Errorf("no refusal contains %q; got:\n%s", w, joined)
				}
			}
		})
	}
}

func TestParseToolchain(t *testing.T) {
	tests := []struct {
		gomod   string
		want    string
		wantErr string
	}{
		{gomod: "go 1.26.0\ntoolchain go1.26.10\n", want: "1.26.10"},
		{gomod: "go 1.26.0\r\ntoolchain go1.26.10 // security release\r\n", want: "1.26.10"},
		{gomod: "go 1.26.0\n", wantErr: "no `toolchain goX.Y.Z` line"},
		{gomod: "toolchain default\n", wantErr: "not a release"},
		{gomod: "toolchain go1.26\n", wantErr: "not a release"},
		{gomod: "toolchain go1.26.10 go1.26.11\n", wantErr: "malformed toolchain line"},
		{gomod: "// toolchain go1.26.10\ngo 1.26.0\n", wantErr: "no `toolchain goX.Y.Z` line"},
	}
	for _, tt := range tests {
		got, err := parseToolchain([]byte(tt.gomod))
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("parseToolchain(%q) = %q, %v; want error containing %q", tt.gomod, got, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("parseToolchain(%q) = %q, %v; want %q", tt.gomod, got, err, tt.want)
		}
	}
}

func TestShellScan(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   []string // image refs, with their line
	}{
		{name: "plain run", script: "docker run alpine:3 sh", want: []string{"1 alpine:3"}},
		{name: "boolean and valued flags", script: "docker run -it --rm -e A=1 -v x:/y --entrypoint git --name n alpine:3 --version", want: []string{"1 alpine:3"}},
		{name: "flag=value", script: "docker run --network=host --platform=linux/amd64 alpine:3", want: []string{"1 alpine:3"}},
		{name: "global flags", script: "docker --context ci -D run alpine:3", want: []string{"1 alpine:3"}},
		{name: "pull with -a", script: "docker pull -a busybox", want: []string{"1 busybox"}},
		{name: "image pull and container create", script: "docker image pull a:1; docker container create b:2", want: []string{"1 a:1", "1 b:2"}},
		{name: "continuation", script: "docker run --rm \\\n  -w /app \\\n  golang:1.26", want: []string{"3 golang:1.26"}},
		{name: "quoted words", script: `docker run -v "$PWD:/w" 'alpine:3' sh`, want: []string{"1 alpine:3"}},
		{name: "separators end commands", script: "true && docker run a:1 | tee x || docker pull b:2", want: []string{"1 a:1", "1 b:2"}},
		{name: "comments", script: "# docker run a:1\necho x # docker run b:2", want: nil},
		{name: "compose is not docker run", script: "docker compose run --rm db psql", want: nil},
		{name: "locally built tag is exempt", script: "docker build -t local:ci .\ndocker run --rm local:ci", want: nil},
		{name: "a run before the build is not exempt", script: "docker run local:ci\ndocker build -t local:ci .", want: []string{"1 local:ci"}},
		{name: "a pull of a built tag is not exempt", script: "docker build -t local:ci .\ndocker pull local:ci", want: []string{"2 local:ci"}},
		{name: "buildx --tag= is exempt", script: "docker buildx build --tag=local:ci .\ndocker run local:ci", want: nil},
		{name: "computed image is reported", script: "docker run $(IMAGE)", want: []string{"1 $(IMAGE)"}},
		{name: "absolute docker path", script: "/usr/bin/docker run a:1", want: []string{"1 a:1"}},
		{
			// An unknown boolean flag is read as taking a value: the next
			// word is then taken for the image, and that refuses.
			name: "unknown boolean flag fails closed", script: "docker run --future-bool alpine:3 sh", want: []string{"1 sh"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := newShellScan("f", originWorkflow)
			sh.scan(1, tt.script)
			var got []string
			for _, img := range sh.images() {
				got = append(got, strconv.Itoa(img.line)+" "+img.ref)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("images = %q; want %q", got, tt.want)
			}
		})
	}
}

func indexJSON(versions map[string]string) string {
	var parts []string
	for platform, v := range versions {
		env := `"PATH=/go/bin:/usr/local/go/bin","GOTOOLCHAIN=local"`
		if v != "" {
			env += `,"GOLANG_VERSION=` + v + `"`
		}
		parts = append(parts, `"`+platform+`":{"architecture":"x","config":{"Env":[`+env+`]}}`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestResolveGolang(t *testing.T) {
	res := result{
		toolchain: "1.26.10",
		golang: []imageRef{
			{file: "Dockerfile", line: 2, ref: "golang:1.26.10-bookworm@" + dGo},
			{file: ciFile, line: 9, ref: "golang:1.26.10-bookworm@" + dGo},
		},
	}
	tests := []struct {
		name    string
		reply   string
		err     error
		wantErr string
	}{
		{name: "every platform is the toolchain", reply: indexJSON(map[string]string{"linux/amd64": "1.26.10", "linux/arm64": "1.26.10"})},
		{name: "single manifest", reply: `{"architecture":"amd64","config":{"Env":["GOLANG_VERSION=1.26.10"]}}`},
		{
			// The tag text says 1.26.10 but the digest is an older build.
			name:    "digest is another Go version",
			reply:   indexJSON(map[string]string{"linux/amd64": "1.26.10", "linux/arm64": "1.26.8"}),
			wantErr: `(linux/arm64) has GOLANG_VERSION="1.26.8", not go.mod's toolchain 1.26.10`,
		},
		{name: "not a golang image", reply: indexJSON(map[string]string{"linux/amd64": ""}), wantErr: "linux/amd64: image config has no GOLANG_VERSION"},
		{name: "empty index", reply: `{}`, wantErr: "no image config"},
		{name: "garbage", reply: `not json`, wantErr: "unparseable image config"},
		{name: "registry unreachable", err: errors.New("dial tcp: no route"), wantErr: "cannot read docker.io/library/golang@" + dGo + " from the registry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var asked []string
			inspect := func(_ context.Context, ref string) ([]byte, error) {
				asked = append(asked, ref)
				return []byte(tt.reply), tt.err
			}
			report, problems := resolveGolang(context.Background(), res, inspect)
			// One distinct digest is inspected once, by digest alone: the
			// tag is never what decides which image is verified.
			if len(asked) != 1 || asked[0] != "docker.io/library/golang@"+dGo {
				t.Fatalf("inspected %q; want exactly the digest reference", asked)
			}
			var got []string
			for _, p := range problems {
				got = append(got, p.String())
			}
			if tt.wantErr == "" {
				if len(got) != 0 || len(report) != 1 {
					t.Fatalf("want a clean resolve, got problems %q report %q", got, report)
				}
				return
			}
			if !strings.Contains(strings.Join(got, "\n"), tt.wantErr) {
				t.Fatalf("want a refusal containing %q, got %q", tt.wantErr, got)
			}
		})
	}
}

func TestResolveGolangInspectsAMirrorAsWritten(t *testing.T) {
	res := result{toolchain: "1.26.10", golang: []imageRef{{file: "Makefile", line: 1, ref: "mirror.gcr.io/library/golang:1.26.10-bookworm@" + dGo}}}
	var asked []string
	_, _ = resolveGolang(context.Background(), res, func(_ context.Context, ref string) ([]byte, error) {
		asked = append(asked, ref)
		return []byte(indexJSON(map[string]string{"linux/amd64": "1.26.10"})), nil
	})
	if len(asked) != 1 || asked[0] != "mirror.gcr.io/library/golang@"+dGo {
		t.Fatalf("inspected %q; want the mirror reference by digest", asked)
	}
}

func TestResolveActions(t *testing.T) {
	pins := []actionPin{
		{file: ciFile, line: 12, repo: "actions/checkout", sha: sha, version: "v4.4.0"},
		{file: ciFile, line: 30, repo: "actions/checkout", sha: sha, version: "v4.4.0"},
		{file: ciFile, line: 13, repo: "actions/setup-go", sha: sha2, version: "v5.6.0"},
	}
	tests := []struct {
		name    string
		tags    map[string]string // repo@tag -> commit
		err     error
		wantErr []string
	}{
		{name: "every pin is its tag's commit", tags: map[string]string{"actions/checkout@v4.4.0": sha, "actions/setup-go@v5.6.0": sha2}},
		{
			// A real SHA under a comment naming another release.
			name:    "the tag names another commit",
			tags:    map[string]string{"actions/checkout@v4.4.0": sha, "actions/setup-go@v5.6.0": sha},
			wantErr: []string{ciFile + ":13: actions/setup-go@" + sha2 + " is not v5.6.0: that tag is commit " + sha},
		},
		{
			name:    "the tag does not exist",
			tags:    map[string]string{"actions/setup-go@v5.6.0": sha2},
			wantErr: []string{ciFile + ":12: actions/checkout has no tag v4.4.0", ciFile + ":30: actions/checkout has no tag v4.4.0"},
		},
		{
			name:    "the remote cannot be read",
			err:     errors.New("exit status 128"),
			wantErr: []string{ciFile + ":12: cannot resolve actions/checkout tag v4.4.0: exit status 128"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := map[string]int{}
			lookup := func(_ context.Context, repo, tag string) (string, error) {
				calls[repo+"@"+tag]++
				return tt.tags[repo+"@"+tag], tt.err
			}
			report, problems := resolveActions(context.Background(), result{actions: pins}, lookup)
			for k, n := range calls {
				if n != 1 {
					t.Errorf("%s looked up %d times; want once", k, n)
				}
			}
			var got []string
			for _, p := range problems {
				got = append(got, p.String())
			}
			if len(tt.wantErr) == 0 {
				if len(got) != 0 || len(report) != 1 || !strings.HasPrefix(report[0], "3 of 3 action pin(s)") {
					t.Fatalf("want a clean resolve, got problems %q report %q", got, report)
				}
				return
			}
			joined := strings.Join(got, "\n")
			for _, w := range tt.wantErr {
				if !strings.Contains(joined, w) {
					t.Errorf("no refusal contains %q; got:\n%s", w, joined)
				}
			}
		})
	}
}

func TestParseLsRemote(t *testing.T) {
	tests := []struct {
		name, out, want string
	}{
		{name: "lightweight tag", out: sha + "\trefs/tags/v4.4.0\n", want: sha},
		{name: "annotated tag: the peeled commit", out: sha2 + "\trefs/tags/v4.4.0\n" + sha + "\trefs/tags/v4.4.0^{}\n", want: sha},
		{name: "a longer tag is not this one", out: sha + "\trefs/tags/v4.4.0.1\n", want: ""},
		{name: "absent", out: "", want: ""},
	}
	for _, tt := range tests {
		if got := parseLsRemote([]byte(tt.out), "v4.4.0"); got != tt.want {
			t.Errorf("%s: parseLsRemote = %q; want %q", tt.name, got, tt.want)
		}
	}
}

// TestRepository is the regression test for SC-08/SC-09: Spine's own go.mod,
// Dockerfile, workflows, compose files and Makefile pass the static rules.
// At the INIT-024 scaffold base it fails (no toolchain line, floating golang,
// debian and postgres tags, actions on @v4/@v5).
func TestRepository(t *testing.T) {
	res, problems := check(filepath.Join("..", ".."))
	for _, p := range problems {
		t.Error(p.String())
	}
	if res.toolchain == "" || len(res.golang) == 0 || len(res.actions) == 0 {
		t.Fatalf("the repository check found nothing to compare: %+v", res)
	}
}
