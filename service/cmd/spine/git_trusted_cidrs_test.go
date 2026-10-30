package main

// INIT-024 TASK-006 (review finding N-2): SPINE_GIT_HTTP_TRUSTED_CIDRS
// binds runner addresses to workspaces. The bare list names no workspace
// and is accepted only in single mode, where it is bound to the one
// workspace; shared mode refuses it at startup, before any registry,
// secret or database is opened, and takes the per-workspace form
// `ws-a=172.28.1.10/32;ws-b=172.28.1.11/32`. A malformed value refuses
// startup in every mode.

import (
	"reflect"
	"strings"
	"testing"
)

// AC 4: both forms parse; every malformed entry is an error.
func TestParseGitHTTPTrustedCIDRs_PerWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name        string
		raw         string
		wantUnbound []string
		wantBy      map[string][]string
	}{
		{name: "unset", raw: ""},
		{name: "bare list", raw: "172.28.1.10/32, 172.28.1.11/32", wantUnbound: []string{"172.28.1.10/32", "172.28.1.11/32"}},
		{name: "bare ipv6", raw: "fd00:28::/64", wantUnbound: []string{"fd00:28::/64"}},
		{
			name:   "per-workspace, one each",
			raw:    "ws-a=172.28.1.10/32;ws-b=172.28.1.11/32",
			wantBy: map[string][]string{"ws-a": {"172.28.1.10/32"}, "ws-b": {"172.28.1.11/32"}},
		},
		{
			name:   "per-workspace, several CIDRs, whitespace and empty entries",
			raw:    " ws-a = 172.28.1.10/32 , 172.28.1.12/32 ;; ws_B=fd00:28::b/128 ; ",
			wantBy: map[string][]string{"ws-a": {"172.28.1.10/32", "172.28.1.12/32"}, "ws_B": {"fd00:28::b/128"}},
		},
		{
			name:   "per-workspace, trailing comma",
			raw:    "default=172.28.1.10/32,",
			wantBy: map[string][]string{"default": {"172.28.1.10/32"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGitHTTPTrustedCIDRs(tc.raw)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got.Unbound, tc.wantUnbound) {
				t.Errorf("parse %q: Unbound = %v, want %v", tc.raw, got.Unbound, tc.wantUnbound)
			}
			if len(tc.wantBy) == 0 {
				if len(got.ByWorkspace) != 0 {
					t.Errorf("parse %q: ByWorkspace = %v, want none", tc.raw, got.ByWorkspace)
				}
			} else if !reflect.DeepEqual(got.ByWorkspace, tc.wantBy) {
				t.Errorf("parse %q: ByWorkspace = %v, want %v", tc.raw, got.ByWorkspace, tc.wantBy)
			}
		})
	}

	for _, tc := range []struct {
		name, raw, want string
	}{
		{"bare, unparsable CIDR", "172.28.1.10/32,not-a-cidr", `invalid CIDR "not-a-cidr"`},
		{"bare, address without prefix", "172.28.1.10", `invalid CIDR "172.28.1.10"`},
		{"per-workspace, unparsable CIDR", "ws-a=not-a-cidr", `invalid CIDR "not-a-cidr"`},
		{"per-workspace, workspace bound to nothing", "ws-a=", `workspace "ws-a" is bound to no CIDR`},
		{"per-workspace, blank list", "ws-a= , ", `workspace "ws-a" is bound to no CIDR`},
		{"per-workspace, empty workspace ID", "=172.28.1.10/32", "workspace_id is empty"},
		{"per-workspace, workspace ID with a space", "ws a=172.28.1.10/32", "must match"},
		{"per-workspace, traversal-shaped workspace ID", "../ws-b=172.28.1.10/32", "must match"},
		{"per-workspace, URL-encoded workspace ID", "ws%2Db=172.28.1.10/32", "must match"},
		{"per-workspace, workspace named twice", "ws-a=172.28.1.10/32;ws-a=172.28.1.12/32", `workspace "ws-a" is named twice`},
		{"bare entry mixed into the per-workspace form", "172.28.1.10/32;ws-a=172.28.1.11/32", `entry "172.28.1.10/32" names no workspace`},
		{"bare list ahead of a per-workspace entry", "172.28.1.10/32,ws-a=172.28.1.11/32", "must match"},
		{"comma between workspaces", "ws-a=172.28.1.10/32,ws-b=172.28.1.11/32", `invalid CIDR "ws-b=172.28.1.11/32"`},
		{"second '=' in an entry", "ws-a=172.28.1.10/32=x", `invalid CIDR "172.28.1.10/32=x"`},
	} {
		got, err := parseGitHTTPTrustedCIDRs(tc.raw)
		if err == nil {
			t.Errorf("%s: parse %q = %+v, want an error", tc.name, tc.raw, got)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: parse %q: error %q, want it to contain %q", tc.name, tc.raw, err, tc.want)
		}
	}
}

// bind gives the bare list to the one workspace in single mode, passes the
// per-workspace form through in both modes, and repeats the shared-mode
// refusal (the backstop against an environment check that disagreed with
// the resolver actually built).
func TestGitTrustedCIDRs_Bind(t *testing.T) {
	bare := gitTrustedCIDRs{Unbound: []string{"172.28.1.10/32"}}
	perWS := gitTrustedCIDRs{ByWorkspace: map[string][]string{"ws-a": {"172.28.1.10/32"}}}

	got, err := bare.bind(false, "default")
	if err != nil || !reflect.DeepEqual(got, map[string][]string{"default": {"172.28.1.10/32"}}) {
		t.Fatalf("single mode, bare list: %v, %v; want it bound to the served workspace", got, err)
	}
	if _, err := bare.bind(true, "default"); err == nil || !strings.Contains(err.Error(), "per-workspace form") {
		t.Fatalf("shared mode, bare list: %v; want the refusal naming the per-workspace form", err)
	}
	for _, shared := range []bool{false, true} {
		got, err := perWS.bind(shared, "default")
		if err != nil || !reflect.DeepEqual(got, perWS.ByWorkspace) {
			t.Fatalf("shared=%v, per-workspace form: %v, %v", shared, got, err)
		}
		got, err = gitTrustedCIDRs{}.bind(shared, "default")
		if err != nil || len(got) != 0 {
			t.Fatalf("shared=%v, unset: %v, %v; want no trust", shared, got, err)
		}
	}
	if got := unservedTrustedWorkspaces(map[string][]string{"ws-b": nil, "default": nil, "ws-a": nil}, "default"); !reflect.DeepEqual(got, []string{"ws-a", "ws-b"}) {
		t.Fatalf("unservedTrustedWorkspaces = %v", got)
	}
	if got := (gitTrustedCIDRs{ByWorkspace: map[string][]string{"ws-b": {"10.0.0.2/32"}, "ws-a": {"10.0.0.1/32"}}}).all(); !reflect.DeepEqual(got, []string{"10.0.0.1/32", "10.0.0.2/32"}) {
		t.Fatalf("all() = %v", got)
	}
}

// trustedCIDRServeEnv makes spine serve's startup hermetic up to the
// trusted-CIDR gate and sets SPINE_STEP_ENTRY_POLICY to a refused value:
// that refusal is a later startup gate (still before any registry,
// secret or database), so a serve that got past the trusted-CIDR gate
// stops there with an error that does not mention trusted CIDRs, instead
// of starting a listener.
func trustedCIDRServeEnv(t *testing.T, resolver, trusted string) {
	t.Helper()
	t.Setenv("SPINE_ENV", "development")
	t.Setenv("SPINE_DEV_MODE", "")
	t.Setenv("SPINE_GIT_HTTP_DEV_NO_AUTH", "")
	t.Setenv("SPINE_OPERATOR_TOKEN", "")
	t.Setenv("SMP_ADMIN_TOKEN", "")
	t.Setenv("SPINE_CODE_REPO_BASE", "")
	t.Setenv("SPINE_WORKSPACE_MODE", "")
	t.Setenv("SPINE_REGISTRY_DATABASE_URL", "")
	t.Setenv("SPINE_PLATFORM_URL", "")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("WORKSPACE_RESOLVER", resolver)
	t.Setenv("SPINE_GIT_HTTP_TRUSTED_CIDRS", trusted)
	t.Setenv(stepEntryPolicyEnv, "bogus-later-gate")
}

// AC 3: shared mode (db or platform-binding) with a bare-list
// SPINE_GIT_HTTP_TRUSTED_CIDRS refuses to start, naming the per-workspace
// form, before the registry or the platform is touched (with neither
// configured, a serve that skipped the gate would stop at "...is
// required for WORKSPACE_RESOLVER=..." or the step-entry gate instead).
// Single mode with the same value gets past the gate, as does shared mode
// with the per-workspace form.
func TestServe_SharedModeRefusesBareTrustedList(t *testing.T) {
	const bare = "172.28.1.10/32,172.28.1.11/32"
	for _, resolver := range []string{"db", "platform-binding", " DB "} {
		trustedCIDRServeEnv(t, resolver, bare)
		err := serveCmd().RunE(serveCmd(), nil)
		if err == nil {
			t.Fatalf("WORKSPACE_RESOLVER=%q with a bare trusted list: spine serve started", resolver)
		}
		msg := err.Error()
		if !strings.Contains(msg, "SPINE_GIT_HTTP_TRUSTED_CIDRS is a bare CIDR list") ||
			!strings.Contains(msg, "per-workspace form") ||
			!strings.Contains(msg, "ws-a=172.28.1.10/32;ws-b=172.28.1.11/32") {
			t.Fatalf("WORKSPACE_RESOLVER=%q: err = %v, want the trusted-CIDR refusal naming the per-workspace form", resolver, err)
		}
	}

	for _, tc := range []struct{ resolver, trusted string }{
		{"", bare},
		{"file", bare},
		{"db", "ws-a=172.28.1.10/32;ws-b=172.28.1.11/32"},
		{"platform-binding", "ws-a=172.28.1.10/32"},
		{"db", ""},
	} {
		trustedCIDRServeEnv(t, tc.resolver, tc.trusted)
		err := serveCmd().RunE(serveCmd(), nil)
		if err == nil || !strings.Contains(err.Error(), stepEntryPolicyEnv) || strings.Contains(err.Error(), "TRUSTED_CIDRS") {
			t.Fatalf("WORKSPACE_RESOLVER=%q SPINE_GIT_HTTP_TRUSTED_CIDRS=%q: err = %v, want it past the trusted-CIDR gate (at the %s gate)",
				tc.resolver, tc.trusted, err, stepEntryPolicyEnv)
		}
	}
}

// AC 4: a malformed value is a startup error in every mode (it used to
// reach githttp.NewHandler, whose error disabled /git with a log line).
func TestServe_MalformedTrustedCIDRsRefuseStartup(t *testing.T) {
	for _, tc := range []struct{ resolver, trusted string }{
		{"", "172.28.1.10"},
		{"file", "ws-a=not-a-cidr"},
		{"db", "ws-a=172.28.1.10/32;172.28.1.11/32"},
		{"platform-binding", "=172.28.1.10/32"},
	} {
		trustedCIDRServeEnv(t, tc.resolver, tc.trusted)
		err := serveCmd().RunE(serveCmd(), nil)
		if err == nil || !strings.HasPrefix(err.Error(), "SPINE_GIT_HTTP_TRUSTED_CIDRS: ") {
			t.Fatalf("WORKSPACE_RESOLVER=%q SPINE_GIT_HTTP_TRUSTED_CIDRS=%q: err = %v, want the parse refusal", tc.resolver, tc.trusted, err)
		}
	}
}

// Review round 1 (P3): CIDRs bound to different workspaces that overlap
// trust the overlap for each of them; serve warns once, listing every
// pair. Overlap inside one workspace, and disjoint bindings, are silent.
func TestCrossWorkspaceCIDROverlaps(t *testing.T) {
	for _, tc := range []struct {
		name string
		by   map[string][]string
		want []string
	}{
		{"disjoint", map[string][]string{"ws-a": {"172.28.1.10/32"}, "ws-b": {"172.28.1.11/32"}}, nil},
		{"same workspace", map[string][]string{"ws-a": {"172.28.1.0/24", "172.28.1.10/32"}}, nil},
		{
			"range typo covers another workspace's runner",
			map[string][]string{"ws-a": {"172.28.1.0/24"}, "ws-b": {"172.28.1.11/32"}},
			[]string{"ws-a=172.28.1.0/24 overlaps ws-b=172.28.1.11/32"},
		},
		{
			"shared runner address, and an IPv6 overlap",
			map[string][]string{"ws-a": {"172.28.1.10/32", "fd00:28::/64"}, "ws-b": {"172.28.1.10/32"}, "ws-c": {"fd00:28::c/128"}},
			[]string{"ws-a=172.28.1.10/32 overlaps ws-b=172.28.1.10/32", "ws-a=fd00:28::/64 overlaps ws-c=fd00:28::c/128"},
		},
		{"none", nil, nil},
	} {
		if got := crossWorkspaceCIDROverlaps(tc.by); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: crossWorkspaceCIDROverlaps = %q, want %q", tc.name, got, tc.want)
		}
	}
}
