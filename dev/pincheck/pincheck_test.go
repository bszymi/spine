package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	dGo  = "sha256:" + strings.Repeat("a", 64)
	dGo2 = "sha256:" + strings.Repeat("b", 64)
	dDeb = "sha256:" + strings.Repeat("c", 64)
	dPg  = "sha256:" + strings.Repeat("d", 64)
	sha  = strings.Repeat("1", 40)
)

// goodTree is a minimal repository whose pins and toolchain parity hold:
// the shape of Spine's own go.mod, Dockerfile, ci.yml, Makefile and compose
// file after TASK-009.
func goodTree() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/x\n\ngo 1.26.0\n\ntoolchain go1.26.10\n",
		"Dockerfile": "# golang:1.26.10-bookworm\n" +
			"FROM golang:1.26.10-bookworm@" + dGo + " AS builder\n" +
			"RUN go build -o /x .\n" +
			"FROM debian:bookworm-slim@" + dDeb + "\n" +
			"COPY --from=builder /x /usr/local/bin/x\n",
		".github/workflows/ci.yml": "permissions:\n  contents: read\n" +
			"jobs:\n" +
			"  a:\n" +
			"    services:\n" +
			"      postgres:\n" +
			"        image: postgres:18-bookworm@" + dPg + " # PG 18.6\n" +
			"    container:\n" +
			"      image: golang:1.26.10-bookworm@" + dGo + "\n" +
			"    steps:\n" +
			"      - uses: actions/checkout@" + sha + " # v4.4.0\n" +
			"      - uses: \"actions/setup-go@" + sha + "\" # v5.6.0\n" +
			"      - uses: ./.github/actions/local\n" +
			"      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...\n",
		"Makefile":            "DOCKER_GO = docker run --rm -w /app golang:1.26.10-bookworm@" + dGo + "\n",
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

func TestCheck(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(files map[string]string)
		want   []string // substrings of the refusals; none means the tree passes
	}{
		{name: "real pair passes", mutate: func(map[string]string) {}},
		{
			// AC 5: go.mod moved to a new patch, the images did not. Under
			// GOTOOLCHAIN=local the image would silently keep compiling 1.26.10.
			name: "mismatched fixture: toolchain bumped without the images",
			mutate: func(f map[string]string) {
				f["go.mod"] = strings.Replace(f["go.mod"], "go1.26.10", "go1.26.11", 1)
			},
			want: []string{
				"Dockerfile:2: golang image \"golang:1.26.10-bookworm@" + dGo + "\" names Go 1.26.10 but go.mod's toolchain is go1.26.11",
				".github/workflows/ci.yml:9: golang image",
				"Makefile:1: golang image",
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
			name: "no toolchain line",
			mutate: func(f map[string]string) {
				f["go.mod"] = "module example.com/x\n\ngo 1.26.0\n"
			},
			want: []string{"go.mod: go.mod has no `toolchain goX.Y.Z` line"},
		},
		{
			name: "rc toolchain is not a release",
			mutate: func(f map[string]string) {
				f["go.mod"] = strings.Replace(f["go.mod"], "go1.26.10", "go1.27rc1", 1)
			},
			want: []string{"toolchain \"go1.27rc1\" is not a release goX.Y.Z"},
		},
		{
			name: "two toolchain lines",
			mutate: func(f map[string]string) {
				f["go.mod"] += "toolchain go1.26.10\n"
			},
			want: []string{"go.mod has 2 toolchain lines"},
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
				f[".github/workflows/ci.yml"] = strings.Replace(f[".github/workflows/ci.yml"], "golang:1.26.10-bookworm@"+dGo, "golang:1.26.10-bookworm@"+dGo2, 1)
			},
			want: []string{".github/workflows/ci.yml:9: golang image \"golang:1.26.10-bookworm@" + dGo2 + "\" differs from"},
		},
		{
			name: "unpinned golang in the Makefile",
			mutate: func(f map[string]string) {
				f["Makefile"] = "DOCKER_GO = docker run --rm golang:1.26-bookworm\n"
			},
			want: []string{"Makefile:1: golang image \"golang:1.26-bookworm\" is not pinned"},
		},
		{
			name: "docker.io spelling of golang is still golang",
			mutate: func(f map[string]string) {
				f["Makefile"] = "X = docker run docker.io/library/golang:1.26.8-bookworm@" + dGo + "\n"
			},
			want: []string{"Makefile:1: golang image \"docker.io/library/golang:1.26.8-bookworm@" + dGo + "\" names Go 1.26.8"},
		},
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
			name: "compose image from a variable",
			mutate: func(f map[string]string) {
				f["docker-compose.yaml"] = "services:\n  db:\n    image: ${PG_IMAGE:-postgres:18-bookworm}\n"
			},
			want: []string{"docker-compose.yaml:3: image \"${PG_IMAGE:-postgres:18-bookworm}\" is computed"},
		},
		{
			name: "unpinned workflow service image",
			mutate: func(f map[string]string) {
				f[".github/workflows/ci.yml"] = strings.Replace(f[".github/workflows/ci.yml"], "postgres:18-bookworm@"+dPg, "postgres:18-bookworm", 1)
			},
			want: []string{".github/workflows/ci.yml:7: image \"postgres:18-bookworm\" is not pinned by digest"},
		},
		{
			name: "short-form container image",
			mutate: func(f map[string]string) {
				f[".github/workflows/ci.yml"] += "  b:\n    container: golang:1.26-bookworm\n"
			},
			want: []string{
				".github/workflows/ci.yml:16: image \"golang:1.26-bookworm\" is not pinned by digest",
				".github/workflows/ci.yml:16: golang image \"golang:1.26-bookworm\" is not pinned",
			},
		},
		{
			name: "action on a mutable tag",
			mutate: func(f map[string]string) {
				f[".github/workflows/ci.yml"] = strings.Replace(f[".github/workflows/ci.yml"], "actions/checkout@"+sha+" # v4.4.0", "actions/checkout@v4", 1)
			},
			want: []string{".github/workflows/ci.yml:11: action \"actions/checkout@v4\" is not pinned to a full 40-hex commit SHA"},
		},
		{
			name: "short SHA",
			mutate: func(f map[string]string) {
				f[".github/workflows/ci.yml"] = strings.Replace(f[".github/workflows/ci.yml"], "actions/checkout@"+sha, "actions/checkout@"+sha[:7], 1)
			},
			want: []string{"is not pinned to a full 40-hex commit SHA"},
		},
		{
			name: "SHA without a version comment",
			mutate: func(f map[string]string) {
				f[".github/workflows/ci.yml"] = strings.Replace(f[".github/workflows/ci.yml"], " # v4.4.0", "", 1)
			},
			want: []string{".github/workflows/ci.yml:11: action \"actions/checkout@" + sha + "\" has no `# vX.Y.Z` comment"},
		},
		{
			name: "major-only version comment",
			mutate: func(f map[string]string) {
				f[".github/workflows/ci.yml"] = strings.Replace(f[".github/workflows/ci.yml"], " # v4.4.0", " # v4", 1)
			},
			want: []string{"has no `# vX.Y.Z` comment"},
		},
		{
			name: "docker:// action must be digest-pinned",
			mutate: func(f map[string]string) {
				f[".github/workflows/ci.yml"] += "      - uses: docker://alpine:3.20\n"
			},
			want: []string{".github/workflows/ci.yml:15: image \"alpine:3.20\" is not pinned by digest"},
		},
		{
			name: "COPY --from an external image must be pinned",
			mutate: func(f map[string]string) {
				f["Dockerfile"] += "COPY --from=alpine/git:latest /usr/bin/git /usr/bin/git\n"
			},
			want: []string{"Dockerfile:6: image \"alpine/git:latest\" is not pinned by digest"},
		},
		{
			name: "builder moved off the golang image",
			mutate: func(f map[string]string) {
				f["Dockerfile"] = "FROM debian:bookworm-slim@" + dDeb + "\n"
			},
			want: []string{"no golang image reference in any Dockerfile"},
		},
		{
			name: "comments, stage names, scratch and stage indexes need no digest",
			mutate: func(f map[string]string) {
				f["Dockerfile"] += "# FROM golang:1.26-bookworm\n" +
					"FROM builder AS tested\n" +
					"FROM scratch\n" +
					"COPY --from=0 /x /x\n" +
					"COPY --from=tested /x /y\n"
				f[".github/workflows/ci.yml"] += "      # - uses: actions/checkout@v4\n" +
					"      # image: postgres:18-bookworm\n"
				f["Makefile"] += "# docker run golang:1.25-bookworm\n"
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
			{file: ".github/workflows/ci.yml", line: 9, ref: "golang:1.26.10-bookworm@" + dGo},
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

// TestRepository is the regression test for SC-08/SC-09: Spine's own go.mod,
// Dockerfile, workflows, compose files and Makefile pass the static rules.
// At the INIT-024 scaffold base it fails (no toolchain line, floating golang,
// debian and postgres tags, actions on @v4/@v5).
func TestRepository(t *testing.T) {
	res, problems := check(filepath.Join("..", ".."))
	for _, p := range problems {
		t.Error(p.String())
	}
	if res.toolchain == "" || len(res.golang) == 0 || res.actions == 0 {
		t.Fatalf("the repository check found nothing to compare: %+v", res)
	}
}
