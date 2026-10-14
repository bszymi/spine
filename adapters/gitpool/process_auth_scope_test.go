package gitpool_test

// INIT-023 TASK-015 (the audit's latent gitpool item: refuted as
// unreachable at 61994fe, P1 the moment a code-repo catalog or a
// RepositoryManager is wired): a code repository with no per-binding
// credential must never authenticate with the deployment's credentials
// — the process-wide push token (SPINE_GIT_PUSH_TOKEN) or the process
// credential helper (SPINE_GIT_CREDENTIAL_HELPER) — against the host its
// clone_url or its clone's origin names. Owner question 2 was ruled
// "never" (pending the owner), so no host is exempt.
//
// Ported from the audit PoCs with the assertion inverted:
//   - poc/crypto-secrets-auth/pushtoken_exfil_test.go (pool, any resolver)
//   - poc/gitpool_tokenleak/                          (pool, real registry + catalog)
//   - poc/verify-r1-3/poc_gitpool_test.go             (cloner, zero Credential)
// plus the factory push path, a reused clone whose origin is foreign,
// and the process credential helper (AC 11, 12, 21, 22).

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
)

const t015ProcessToken = "ghp_PROCESS_WIDE_PUSH_TOKEN_shared_by_all_workspaces"

// t015Capture records every request a test git host receives.
type t015Capture struct {
	mu    sync.Mutex
	auths []string
	hits  int
}

func (c *t015Capture) hitCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits
}

func (c *t015Capture) authCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.auths)
}

func (c *t015Capture) passwords() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, a := range c.auths {
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(a, "Basic "))
		if _, pw, ok := strings.Cut(string(raw), ":"); ok {
			out = append(out, pw)
		}
	}
	return out
}

func (c *t015Capture) sawPassword(p string) bool {
	for _, got := range c.passwords() {
		if got == p {
			return true
		}
	}
	return false
}

// t015Host is a TLS git host that answers every request with a Basic
// challenge and records any Authorization header git sends back.
func t015Host(t *testing.T) (*httptest.Server, *t015Capture) {
	t.Helper()
	c := &t015Capture{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.hits++
		if a := r.Header.Get("Authorization"); a != "" {
			c.auths = append(c.auths, a)
		}
		c.mu.Unlock()
		w.Header().Set("WWW-Authenticate", `Basic realm="t015"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

// t015Isolate keeps the developer's git config, keychain helpers and
// askpass programs out of the test, and returns the isolated HOME.
func t015Isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GIT_SSL_NO_VERIFY", "1") // the httptest certificate is self-signed
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_ASKPASS", "")
	t.Setenv("SSH_ASKPASS", "")
	return home
}

func t015Git(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// t015LocalRepo makes a repository with one commit on main and the
// given origin (and, when pushURL is set, remote.origin.pushurl).
func t015LocalRepo(t *testing.T, dir, origin, pushURL string) {
	t.Helper()
	t015Git(t, "init", "-q", "-b", "main", dir)
	t015Git(t, "-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c")
	t015Git(t, "-C", dir, "remote", "add", "origin", origin)
	if pushURL != "" {
		t015Git(t, "-C", dir, "config", "remote.origin.pushurl", pushURL)
	}
}

type t015Resolver struct{ repo *repository.Repository }

func (r t015Resolver) Lookup(context.Context, string) (*repository.Repository, error) {
	cp := *r.repo
	return &cp, nil
}

func (r t015Resolver) ListActive(context.Context) ([]repository.Repository, error) {
	return []repository.Repository{*r.repo}, nil
}

// t015Pool builds the production pool shape (cloner + factory with the
// process push token in their base options, the secret credential
// resolver) over one credential-less code repository.
func t015Pool(t *testing.T, cloneURL, localPath string, baseOpts ...git.CLIOption) *gitpool.Pool {
	t.Helper()
	base := filepath.Dir(localPath)
	if len(baseOpts) == 0 {
		baseOpts = []git.CLIOption{git.WithPushToken(t015ProcessToken, "")} // = git.PushAuthOpts() with SPINE_GIT_PUSH_TOKEN set
	}
	repo := &repository.Repository{
		ID: "evil", WorkspaceID: "tenant-b", Kind: repository.KindCode,
		CloneURL:  cloneURL,
		LocalPath: localPath,
		Status:    store.RepositoryBindingStatusActive,
		UpdatedAt: time.Now(),
	}
	p, err := gitpool.New(git.NewCLIClient(base), t015Resolver{repo}, gitpool.NewCLIClientFactory(baseOpts...),
		gitpool.WithCloner(gitpool.NewCLICloner(baseOpts...)),
		gitpool.WithRepoBase(base),
		gitpool.WithCredentialResolver(&gitpool.SecretCredentialResolver{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func t015AssertNoProcessToken(t *testing.T, c *t015Capture) {
	t.Helper()
	if c.hitCount() == 0 {
		t.Fatal("the test did not reach the git host")
	}
	if c.sawPassword(t015ProcessToken) {
		t.Fatalf("the process-wide push token was sent to the code repository's host")
	}
	if n := c.authCount(); n != 0 {
		t.Fatalf("a credential-less code repository sent %d Authorization header(s)", n)
	}
}

// AC 11 (pushtoken_exfil PoC, inverted): the pool path a
// /git/{ws}/{repo} request triggers — lazy clone of a credential-less
// code repository.
func TestLatentGitpool_PoolNeverOffersProcessTokenToCodeRepoHost(t *testing.T) {
	t015Isolate(t)
	srv, c := t015Host(t)
	p := t015Pool(t, srv.URL+"/acme/evil.git", filepath.Join(t.TempDir(), "evil"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := p.RepositoryPath(ctx, "evil"); err == nil {
		t.Fatal("clone of a 401 remote unexpectedly succeeded")
	}
	t015AssertNoProcessToken(t, c)
}

type t015Bindings struct{ b store.RepositoryBinding }

func (s *t015Bindings) GetRepositoryBinding(_ context.Context, _, _ string) (*store.RepositoryBinding, error) {
	c := s.b
	return &c, nil
}

func (s *t015Bindings) ListRepositoryBindings(context.Context, string) ([]store.RepositoryBinding, error) {
	return []store.RepositoryBinding{s.b}, nil
}

// AC 11 (gitpool_tokenleak PoC, inverted): the real registry with a
// catalog that lists the code repository — the wiring that would make
// the leak reachable.
func TestLatentGitpool_RegistryCatalogPathDoesNotLeak(t *testing.T) {
	t015Isolate(t)
	srv, c := t015Host(t)
	cat, err := repository.ParseCatalog([]byte("- id: spine\n  kind: spine\n  name: s\n  default_branch: main\n- id: evil\n  kind: code\n  name: e\n  default_branch: main\n"), repository.PrimarySpec{})
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	bindings := &t015Bindings{b: store.RepositoryBinding{
		RepositoryID: "evil", WorkspaceID: "ws-a", CloneURL: srv.URL + "/x.git",
		LocalPath: filepath.Join(base, "evil"), DefaultBranch: "main",
		Status: store.RepositoryBindingStatusActive, UpdatedAt: time.Now(),
	}}
	reg := repository.New("ws-a", repository.PrimarySpec{LocalPath: t.TempDir()},
		func(context.Context) (*repository.Catalog, error) { return cat, nil }, bindings)
	baseOpts := []git.CLIOption{git.WithPushToken(t015ProcessToken, "")}
	p, err := gitpool.New(git.NewCLIClient(base), reg, gitpool.NewCLIClientFactory(baseOpts...),
		gitpool.WithCloner(gitpool.NewCLICloner(baseOpts...)), gitpool.WithRepoBase(base),
		gitpool.WithCredentialResolver(&gitpool.SecretCredentialResolver{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Client(context.Background(), "evil"); err == nil {
		t.Fatal("clone of a 401 remote unexpectedly succeeded")
	}
	t015AssertNoProcessToken(t, c)
}

// AC 11 (verify-r1-3 PoC, inverted): the cloner itself honours "an empty
// Credential means no auth".
func TestLatentGitpool_ClonerZeroCredentialSendsNoProcessToken(t *testing.T) {
	t015Isolate(t)
	srv, c := t015Host(t)
	cloner := gitpool.NewCLICloner(git.WithPushToken(t015ProcessToken, ""))
	if err := cloner.Clone(context.Background(), srv.URL+"/acme/code.git", filepath.Join(t.TempDir(), "clone"), gitpool.Credential{}); err == nil {
		t.Fatal("clone of a 401 remote unexpectedly succeeded")
	}
	t015AssertNoProcessToken(t, c)
}

// AC 11 (new): a factory-built (cached) code-repo client pushing to its
// origin.
func TestLatentGitpool_FactoryClientPushSendsNoProcessToken(t *testing.T) {
	t015Isolate(t)
	srv, c := t015Host(t)
	dir := filepath.Join(t.TempDir(), "code")
	t015LocalRepo(t, dir, srv.URL+"/acme/evil.git", "")
	client := gitpool.NewCLIClientFactory(git.WithPushToken(t015ProcessToken, ""))(dir, gitpool.Credential{})
	if cl, ok := client.(*git.CLIClient); ok {
		defer cl.Close()
	}
	if err := client.Push(context.Background(), "origin", "main"); err == nil {
		t.Fatal("push to a 401 remote unexpectedly succeeded")
	}
	t015AssertNoProcessToken(t, c)
}

// AC 21: a clone that already exists at the binding's local_path is
// reused as is, so pushes go to ITS origin (or remote.origin.pushurl),
// whatever the binding's clone_url says — for example after an operator
// updated clone_url but kept local_path, or when a second binding points
// at the first one's clone. No process credential reaches that host.
func TestLatentGitpool_ReusedCloneForeignOriginNeverInherits(t *testing.T) {
	for _, tc := range []struct{ name, origin, pushURL string }{
		{name: "origin"},
		{name: "pushurl only", origin: "https://primary.example/acme/primary.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t015Isolate(t)
			srv, c := t015Host(t)
			local := filepath.Join(t.TempDir(), "evil")
			origin, pushURL := srv.URL+"/acme/evil.git", ""
			if tc.origin != "" {
				origin, pushURL = tc.origin, srv.URL+"/acme/evil.git"
			}
			t015LocalRepo(t, local, origin, pushURL)
			// The binding names a different host than the reused clone.
			p := t015Pool(t, "https://primary.example/acme/primary.git", local)
			client, err := p.Client(context.Background(), "evil")
			if err != nil {
				t.Fatalf("Client: %v", err)
			}
			if err := client.Push(context.Background(), "origin", "main"); err == nil {
				t.Fatal("push to a 401 remote unexpectedly succeeded")
			}
			t015AssertNoProcessToken(t, c)
		})
	}
}

// t015StoreCredential writes a credential for host into the `store`
// helper's file under home. Rewritten before each attempt: git erases a
// stored credential the host rejects.
func t015StoreCredential(t *testing.T, home, hostURL string) {
	t.Helper()
	u := strings.Replace(hostURL, "https://", "https://store-user:store-secret@", 1)
	if err := os.WriteFile(filepath.Join(home, ".git-credentials"), []byte(u+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// AC 22: the process credential helper (SPINE_GIT_CREDENTIAL_HELPER) is
// a platform credential too. In shared mode a host-keyed helper answers
// for the platform's own host, so a tenant binding naming
// github.com/<other-org>/private would otherwise clone with the
// platform's stored credential. A credential-less code repository's
// cloner and factory client drop it.
func TestLatentGitpool_NonInheritingCodeRepoDropsProcessHelper(t *testing.T) {
	home := t015Isolate(t)
	helperOpts := []git.CLIOption{git.WithCredentialHelper("store")}

	// Positive control: the helper setup answers for the test host, so
	// the assertions below are not vacuous.
	t.Run("control: a client with the helper sends the stored credential", func(t *testing.T) {
		srv, c := t015Host(t)
		t015StoreCredential(t, home, srv.URL)
		dir := filepath.Join(t.TempDir(), "control")
		t015LocalRepo(t, dir, srv.URL+"/acme/x.git", "")
		cl := git.NewCLIClient(dir, helperOpts...)
		defer cl.Close()
		_ = cl.Push(context.Background(), "origin", "main")
		if !c.sawPassword("store-secret") {
			t.Fatalf("the store helper did not answer for the test host; passwords seen: %d", len(c.passwords()))
		}
	})

	t.Run("cloner", func(t *testing.T) {
		srv, c := t015Host(t)
		t015StoreCredential(t, home, srv.URL)
		cloner := gitpool.NewCLICloner(helperOpts...)
		_ = cloner.Clone(context.Background(), srv.URL+"/acme/x.git", filepath.Join(t.TempDir(), "clone"), gitpool.Credential{})
		t015AssertNoProcessToken(t, c)
		if c.sawPassword("store-secret") {
			t.Fatal("the cloner consulted the process credential helper for a credential-less code repo")
		}
	})

	t.Run("factory push", func(t *testing.T) {
		srv, c := t015Host(t)
		t015StoreCredential(t, home, srv.URL)
		dir := filepath.Join(t.TempDir(), "code")
		t015LocalRepo(t, dir, srv.URL+"/acme/x.git", "")
		client := gitpool.NewCLIClientFactory(helperOpts...)(dir, gitpool.Credential{})
		if cl, ok := client.(*git.CLIClient); ok {
			defer cl.Close()
		}
		_ = client.Push(context.Background(), "origin", "main")
		t015AssertNoProcessToken(t, c)
		if c.sawPassword("store-secret") {
			t.Fatal("the factory client consulted the process credential helper for a credential-less code repo")
		}
	})

	t.Run("pool lazy clone", func(t *testing.T) {
		srv, c := t015Host(t)
		t015StoreCredential(t, home, srv.URL)
		p := t015Pool(t, srv.URL+"/acme/x.git", filepath.Join(t.TempDir(), "evil"), helperOpts...)
		_, _ = p.RepositoryPath(context.Background(), "evil")
		t015AssertNoProcessToken(t, c)
	})
}

// AC 12 (what must still work): a per-binding credential is sent — by
// the cloner and by the factory client — and the process token is not.
func TestLatentGitpool_PerBindingCredentialStillUsed(t *testing.T) {
	t015Isolate(t)
	binding := gitpool.Credential{Username: "x-access-token", Token: secrets.NewSecretValue([]byte("binding-token"))}
	for _, base := range []struct {
		name string
		opts []git.CLIOption
	}{
		{"process token in base options", []git.CLIOption{git.WithPushToken(t015ProcessToken, "")}},
		{"process helper in base options", []git.CLIOption{git.WithCredentialHelper("store")}},
	} {
		t.Run(base.name+"/cloner", func(t *testing.T) {
			srv, c := t015Host(t)
			_ = gitpool.NewCLICloner(base.opts...).Clone(context.Background(), srv.URL+"/b.git", filepath.Join(t.TempDir(), "b"), binding)
			if !c.sawPassword("binding-token") || c.sawPassword(t015ProcessToken) {
				t.Fatalf("per-binding credential path changed: binding token sent=%v, process token sent=%v",
					c.sawPassword("binding-token"), c.sawPassword(t015ProcessToken))
			}
		})
		t.Run(base.name+"/factory push", func(t *testing.T) {
			srv, c := t015Host(t)
			dir := filepath.Join(t.TempDir(), "code")
			t015LocalRepo(t, dir, srv.URL+"/b.git", "")
			client := gitpool.NewCLIClientFactory(base.opts...)(dir, binding)
			if cl, ok := client.(*git.CLIClient); ok {
				defer cl.Close()
			}
			_ = client.Push(context.Background(), "origin", "main")
			if !c.sawPassword("binding-token") || c.sawPassword(t015ProcessToken) {
				t.Fatalf("per-binding credential path changed: binding token sent=%v, process token sent=%v",
					c.sawPassword("binding-token"), c.sawPassword(t015ProcessToken))
			}
		})
	}
}

// t015AmbientHeader is an http.extraHeader carrying the ambient
// credential (Basic ambient-user:ambient-secret).
const t015AmbientHeader = "Authorization: Basic YW1iaWVudC11c2VyOmFtYmllbnQtc2VjcmV0"

// t015AmbientAskpass writes an askpass program that answers every prompt
// with the ambient credential and returns its path.
func t015AmbientAskpass(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ambient-askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n  Username*) echo ambient-user ;;\n  *) echo ambient-secret ;;\nesac\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// Codex review (P1): stripping Spine's own options is not enough. A host
// whose global or system git config names a credential helper (Apple
// git's system config ships osxkeychain), or whose environment carries
// GIT_ASKPASS, would still hand its credential to the host a
// credential-less binding names. The cloner, the factory client and the
// pool switch every ambient credential source off for such a binding.
func TestLatentGitpool_CredentialLessCodeRepoIgnoresHostCredentials(t *testing.T) {
	for _, src := range []struct {
		name  string
		setup func(t *testing.T, home, hostURL string)
	}{
		{"global credential.helper", func(t *testing.T, home, hostURL string) {
			t015StoreCredential(t, home, hostURL)
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[credential]\n\thelper = store\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"URL-scoped global helper", func(t *testing.T, home, hostURL string) {
			t015StoreCredential(t, home, hostURL)
			cfg := "[credential \"" + hostURL + "\"]\n\thelper = store\n"
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"inherited GIT_ASKPASS", func(t *testing.T, _, _ string) {
			t.Setenv("GIT_ASKPASS", t015AmbientAskpass(t))
		}},
		// Codex review round 3 (P2): an http.extraHeader is a credential
		// too, generic or URL-scoped, from a config file or injected
		// through the environment.
		{"global http.extraHeader", func(t *testing.T, home, _ string) {
			cfg := "[http]\n\textraHeader = " + t015AmbientHeader + "\n"
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"URL-scoped global http.extraHeader", func(t *testing.T, home, hostURL string) {
			cfg := "[http \"" + hostURL + "\"]\n\textraHeader = " + t015AmbientHeader + "\n"
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"system credential.helper", func(t *testing.T, home, hostURL string) {
			t015StoreCredential(t, home, hostURL)
			sys := filepath.Join(t.TempDir(), "gitconfig")
			if err := os.WriteFile(sys, []byte("[credential]\n\thelper = store\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_SYSTEM", sys)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "")
			if err := os.Unsetenv("GIT_CONFIG_NOSYSTEM"); err != nil {
				t.Fatal(err)
			}
		}},
		{"system URL-scoped http.extraHeader", func(t *testing.T, _, hostURL string) {
			sys := filepath.Join(t.TempDir(), "gitconfig")
			cfg := "[http \"" + hostURL + "\"]\n\textraHeader = " + t015AmbientHeader + "\n"
			if err := os.WriteFile(sys, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_SYSTEM", sys)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "")
			if err := os.Unsetenv("GIT_CONFIG_NOSYSTEM"); err != nil {
				t.Fatal(err)
			}
		}},
		{"GIT_CONFIG_COUNT URL-scoped http.extraHeader", func(t *testing.T, _, hostURL string) {
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "http."+hostURL+".extraHeader")
			t.Setenv("GIT_CONFIG_VALUE_0", t015AmbientHeader)
		}},
		{"core.askPass", func(t *testing.T, home, _ string) {
			// git reads core.askPass only when GIT_ASKPASS is unset (an
			// empty GIT_ASKPASS stops the lookup), so unset it here;
			// t.Setenv restores it after the test.
			t.Setenv("GIT_ASKPASS", "")
			if err := os.Unsetenv("GIT_ASKPASS"); err != nil {
				t.Fatal(err)
			}
			cfg := "[core]\n\taskPass = " + t015AmbientAskpass(t) + "\n"
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(src.name, func(t *testing.T) {
			sawAmbient := func(c *t015Capture) bool {
				return c.sawPassword("store-secret") || c.sawPassword("ambient-secret")
			}
			// Positive control: a plain client does use the ambient source,
			// so the assertions below are not vacuous.
			t.Run("control", func(t *testing.T) {
				home := t015Isolate(t)
				srv, c := t015Host(t)
				src.setup(t, home, srv.URL)
				dir := filepath.Join(t.TempDir(), "control")
				t015LocalRepo(t, dir, srv.URL+"/acme/x.git", "")
				cl := git.NewCLIClient(dir)
				defer cl.Close()
				_ = cl.Push(context.Background(), "origin", "main")
				if !sawAmbient(c) {
					t.Fatalf("the ambient source did not answer for the test host (Authorization headers: %d)", c.authCount())
				}
			})
			t.Run("cloner", func(t *testing.T) {
				home := t015Isolate(t)
				srv, c := t015Host(t)
				src.setup(t, home, srv.URL)
				_ = gitpool.NewCLICloner(git.WithPushToken(t015ProcessToken, "")).Clone(context.Background(), srv.URL+"/acme/x.git", filepath.Join(t.TempDir(), "clone"), gitpool.Credential{})
				t015AssertNoProcessToken(t, c)
			})
			t.Run("factory push", func(t *testing.T) {
				home := t015Isolate(t)
				srv, c := t015Host(t)
				src.setup(t, home, srv.URL)
				dir := filepath.Join(t.TempDir(), "code")
				t015LocalRepo(t, dir, srv.URL+"/acme/x.git", "")
				client := gitpool.NewCLIClientFactory(git.WithPushToken(t015ProcessToken, ""))(dir, gitpool.Credential{})
				if cl, ok := client.(*git.CLIClient); ok {
					defer cl.Close()
				}
				_ = client.Push(context.Background(), "origin", "main")
				t015AssertNoProcessToken(t, c)
			})
			t.Run("pool lazy clone", func(t *testing.T) {
				home := t015Isolate(t)
				srv, c := t015Host(t)
				src.setup(t, home, srv.URL)
				p := t015Pool(t, srv.URL+"/acme/x.git", filepath.Join(t.TempDir(), "evil"))
				_, _ = p.RepositoryPath(context.Background(), "evil")
				t015AssertNoProcessToken(t, c)
			})
		})
	}
}

// t015FakeSSH puts a recording `ssh` first on PATH and gives the process
// an ambient SSH identity: an agent socket and an operator
// GIT_SSH_COMMAND naming a key file. It returns a function that reads
// what the fake ssh was invoked with.
func t015FakeSSH(t *testing.T) func() string {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "ssh.record")
	script := "#!/bin/sh\n{ printf 'ARGS'; for a in \"$@\"; do printf ' [%s]' \"$a\"; done; printf '\\nSOCK=[%s]\\n' \"$SSH_AUTH_SOCK\"; } >> '" + rec + "'\nexit 128\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_AUTH_SOCK", "/tmp/t015-ambient-agent.sock")
	t.Setenv("GIT_SSH_COMMAND", "ssh -o IdentityFile=/operator/key")
	return func() string {
		b, _ := os.ReadFile(rec)
		return string(b)
	}
}

// Codex review round 2 (P1): for an ssh:// or SCP-style code repository
// the process's SSH identity (agent, default and configured keys) is an
// ambient credential too. A credential-less binding runs ssh with no
// config, no agent and no key-based authentication.
func TestLatentGitpool_CredentialLessSSHCodeRepoUsesNoSSHIdentity(t *testing.T) {
	const sshURL = "ssh://git@127.0.0.1:2222/acme/x.git"
	assertNoIdentity := func(t *testing.T, rec string) {
		t.Helper()
		if !strings.Contains(rec, "ARGS") {
			t.Fatal("git never invoked ssh")
		}
		for _, want := range []string{"[-F] [none]", "[IdentityAgent=none]", "[PubkeyAuthentication=no]", "[BatchMode=yes]", "SOCK=[]"} {
			if !strings.Contains(rec, want) {
				t.Errorf("ssh invocation lacks %s:\n%s", want, rec)
			}
		}
		if strings.Contains(rec, "/operator/key") || strings.Contains(rec, "t015-ambient-agent") {
			t.Errorf("ssh invocation carries the process SSH identity:\n%s", rec)
		}
	}
	t.Run("control: a plain client uses the process SSH identity", func(t *testing.T) {
		t015Isolate(t)
		read := t015FakeSSH(t)
		cl := git.NewCLIClient(t.TempDir())
		_ = cl.Clone(context.Background(), sshURL, filepath.Join(t.TempDir(), "c"))
		if rec := read(); !strings.Contains(rec, "/operator/key") || !strings.Contains(rec, "SOCK=[/tmp/t015-ambient-agent.sock]") {
			t.Fatalf("the fake ssh did not see the ambient identity:\n%s", rec)
		}
	})
	t.Run("cloner", func(t *testing.T) {
		t015Isolate(t)
		read := t015FakeSSH(t)
		_ = gitpool.NewCLICloner(git.WithPushToken(t015ProcessToken, "")).Clone(context.Background(), sshURL, filepath.Join(t.TempDir(), "clone"), gitpool.Credential{})
		assertNoIdentity(t, read())
	})
	t.Run("factory push", func(t *testing.T) {
		t015Isolate(t)
		read := t015FakeSSH(t)
		dir := filepath.Join(t.TempDir(), "code")
		t015LocalRepo(t, dir, sshURL, "")
		client := gitpool.NewCLIClientFactory(git.WithCredentialHelper("store"))(dir, gitpool.Credential{})
		if cl, ok := client.(*git.CLIClient); ok {
			defer cl.Close()
		}
		_ = client.Push(context.Background(), "origin", "main")
		assertNoIdentity(t, read())
	})
}
