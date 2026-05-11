package githttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestServeHTTP_CGIEnvWhitelist locks the cgi.Handler.Env whitelist in
// place. ServeHTTP must NOT inherit host process environment — only
// GIT_PROJECT_ROOT, GIT_HTTP_EXPORT_ALL, and the CGI-standard variables
// Go's net/http/cgi always sets may reach git-http-backend.
//
// The original 2026-05-07 review finding ("CGI inherits server env")
// was incorrect — the handler already sets a minimal Env without
// InheritEnv. This regression test exists so a future refactor (adding
// an InheritEnv entry, switching to os.Environ()-derived setup,
// growing the Env list) cannot silently regress that protection.
func TestServeHTTP_CGIEnvWhitelist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CGI stub relies on POSIX shell")
	}

	// Stub host-process env with sensitive sentinels that must NOT
	// reach the CGI subprocess. t.Setenv restores prior state at
	// teardown so this test cannot leak the sentinels to siblings.
	const adminSentinel = "host-sentinel-admin-token"
	const awsSentinel = "host-sentinel-aws-key"
	t.Setenv("SMP_ADMIN_TOKEN", adminSentinel)
	t.Setenv("AWS_SECRET_ACCESS_KEY", awsSentinel)

	// Stub binary that dumps its environment to a fixed capture path
	// before producing a minimal valid CGI response. The path is
	// baked into the script itself: GIT_PROJECT_ROOT is the only
	// whitelisted variable that points at a writable location, and
	// piggy-backing on it would couple the capture to the repo dir.
	// A hard-coded absolute path keeps the stub independent of the
	// env it is asserting on.
	//
	// `env` is found via PATH, which net/http/cgi always sets from
	// the host's PATH (or a POSIX fallback) — that is a CGI-standard
	// variable, not an inherited host secret, so the test relies on
	// it without coupling to the whitelist under test.
	stubDir := t.TempDir()
	capturePath := filepath.Join(stubDir, "env-capture.txt")
	stubPath := filepath.Join(stubDir, "fake-git-http-backend")
	stubSource := fmt.Sprintf(
		"#!/bin/sh\nenv > %q\nprintf 'Content-Type: text/plain\\n\\n'\n",
		capturePath,
	)
	if err := os.WriteFile(stubPath, []byte(stubSource), 0o755); err != nil { //nolint:gosec // G306: test stub must be executable
		t.Fatalf("write stub: %v", err)
	}

	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	// NewHandler discovers the real git-http-backend so the
	// production code path remains exercised by other tests. Here we
	// swap the resolved path for the stub so CGI dispatches into a
	// process whose env we can capture.
	h.gitBackendPath = stubPath

	repoDir := initBareIshRepo(t)
	req := httptest.NewRequest("GET", "/info/refs?service=git-upload-pack", http.NoBody)
	req = req.WithContext(WithRepoPath(req.Context(), repoDir))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	// 200 from the stub indicates CGI dispatch reached the
	// subprocess and the response parser was happy with the headers.
	// A 500 here usually means the stub never ran (path/permission)
	// or its output was malformed.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from CGI stub, got %d (body: %s)", w.Code, w.Body.String())
	}

	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read env capture: %v", err)
	}
	cgiEnv := parseEnvDump(raw)

	// Whitelisted variables must be present with the expected values.
	if got := cgiEnv["GIT_PROJECT_ROOT"]; got != repoDir {
		t.Errorf("GIT_PROJECT_ROOT = %q, want %q", got, repoDir)
	}
	if got := cgiEnv["GIT_HTTP_EXPORT_ALL"]; got != "1" {
		t.Errorf("GIT_HTTP_EXPORT_ALL = %q, want %q", got, "1")
	}

	// Spot-check that Go's net/http/cgi standard variables flow
	// through. We do not exhaustively list them (the stdlib owns
	// the canonical set); REQUEST_METHOD and QUERY_STRING are
	// load-bearing for any real git-http-backend interaction, so
	// their presence is a useful smoke test that the dispatch path
	// is intact.
	if got := cgiEnv["REQUEST_METHOD"]; got != "GET" {
		t.Errorf("REQUEST_METHOD = %q, want GET", got)
	}
	if got := cgiEnv["QUERY_STRING"]; got != "service=git-upload-pack" {
		t.Errorf("QUERY_STRING = %q, want service=git-upload-pack", got)
	}

	// Sensitive host vars must NOT have leaked through to the
	// subprocess. Catches broad regressions (Env: os.Environ()) where
	// the specific sentinel values are visible end-to-end.
	for _, key := range []string{
		"SMP_ADMIN_TOKEN",
		"AWS_SECRET_ACCESS_KEY",
	} {
		if v, ok := cgiEnv[key]; ok {
			t.Errorf("host env %s leaked into CGI subprocess: %q (must not be inherited)", key, v)
		}
	}

	// Strict-subset check: the captured environment must contain ONLY
	// the documented whitelist. This catches narrower regressions like
	// `InheritEnv: []string{"AWS_SESSION_TOKEN"}` that a sentinel-name
	// list cannot see. The allowed set is:
	//   - The handler's explicit Env entries (GIT_PROJECT_ROOT,
	//     GIT_HTTP_EXPORT_ALL).
	//   - The CGI-standard variables Go's net/http/cgi always sets —
	//     see net/http/cgi/host.go for the canonical list. PATH is
	//     populated from the host's PATH (or a POSIX fallback) by the
	//     stdlib unconditionally; the test relies on it to find `env`.
	//   - HTTP_HOST — the only HTTP_* variable Go's net/http/cgi
	//     emits for this request (httptest.NewRequest sets a default
	//     req.Host and leaves req.Header empty). We deliberately do
	//     NOT blanket-allow `HTTP_*` because a future
	//     `InheritEnv: []string{"HTTP_PROXY"}` would leak credentials
	//     into the CGI subprocess under that namespace; the strict
	//     enumeration here catches that regression.
	//   - osDefaultInheritEnv entries the stdlib whitelists by
	//     platform (Linux: LD_LIBRARY_PATH; macOS/iOS:
	//     DYLD_LIBRARY_PATH). These are dynamic-loader paths
	//     scoped to runtime linking, not application secrets, and
	//     are out of scope for the application-env attack surface
	//     this whitelist is defending against.
	//   - Shell-self-injected variables (PWD, SHLVL, _, OLDPWD).
	//     The CGI subprocess is a POSIX shell that auto-populates
	//     these from its own runtime state — PWD from getcwd() at
	//     startup, SHLVL/_ in bash. They come from the shell, not
	//     from the host Go process env, so they don't represent a
	//     whitelist regression. dash (Debian default /bin/sh) only
	//     sets PWD; bash (macOS /bin/sh) adds SHLVL and _. Listing
	//     all four keeps the test stable across host shells.
	allowed := map[string]bool{
		"SERVER_SOFTWARE":     true,
		"SERVER_PROTOCOL":     true,
		"GATEWAY_INTERFACE":   true,
		"REQUEST_METHOD":      true,
		"QUERY_STRING":        true,
		"REQUEST_URI":         true,
		"PATH_INFO":           true,
		"SCRIPT_NAME":         true,
		"SCRIPT_FILENAME":     true,
		"SERVER_PORT":         true,
		"SERVER_NAME":         true,
		"REMOTE_ADDR":         true,
		"REMOTE_HOST":         true,
		"REMOTE_PORT":         true,
		"PATH":                true,
		"GIT_PROJECT_ROOT":    true,
		"GIT_HTTP_EXPORT_ALL": true,
		"LD_LIBRARY_PATH":     true,
		"DYLD_LIBRARY_PATH":   true,
		"PWD":                 true,
		"SHLVL":               true,
		"_":                   true,
		"OLDPWD":              true,
		"HTTP_HOST":           true,
	}
	for k := range cgiEnv {
		if allowed[k] {
			continue
		}
		t.Errorf("CGI env contains unexpected key %q (=%q) — handler whitelist may have regressed; "+
			"if this key is now intentional, add it to the allowed set with a comment", k, cgiEnv[k])
	}
}

// parseEnvDump parses `env(1)` output into a map. Each KEY=VALUE line
// is split on the first `=`. Values containing newlines are not
// supported; the test does not inject such values.
func parseEnvDump(raw []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i < 0 {
			continue
		}
		out[line[:i]] = line[i+1:]
	}
	return out
}
