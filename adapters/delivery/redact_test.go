package delivery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRedactURL(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{name: "path only", in: "https://hooks.example/services/T/B/secret", want: "https://hooks.example/[redacted]"},
		{name: "query only", in: "https://hooks.example?token=secret", want: "https://hooks.example/[redacted]"},
		{name: "query with root path", in: "https://hooks.example/?token=secret", want: "https://hooks.example/[redacted]"},
		{name: "fragment only", in: "https://hooks.example#secret", want: "https://hooks.example/[redacted]"},
		{name: "userinfo dropped", in: "https://user:pass@hooks.example", want: "https://hooks.example"},
		{name: "userinfo and path", in: "https://user:pass@hooks.example/x", want: "https://hooks.example/[redacted]"},
		{name: "port kept", in: "http://hooks.example:8443/x?y=1", want: "http://hooks.example:8443/[redacted]"},
		{name: "ipv6 host kept", in: "https://[2001:db8::1]:8443/x", want: "https://[2001:db8::1]:8443/[redacted]"},
		{name: "no path", in: "https://hooks.example", want: "https://hooks.example"},
		{name: "root path kept", in: "https://hooks.example/", want: "https://hooks.example/"},
		{name: "escaped path", in: "https://hooks.example/a%2Fsecret", want: "https://hooks.example/[redacted]"},
		{name: "unparseable", in: "https://exa mple.com/secret", want: "[unparseable-url]"},
		{name: "no host", in: "/relative/secret", want: "[unparseable-url]"},
		{name: "empty", in: "", want: "[unparseable-url]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactURL(tc.in); got != tc.want {
				t.Fatalf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactError(t *testing.T) {
	const target = "https://hooks.example/services/T/B/s3cr3tPath?token=s3cr3tQuery"
	secrets := []string{"s3cr3tPath", "s3cr3tQuery"}
	check := func(t *testing.T, got string) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(got, s) {
				t.Fatalf("redacted text leaks %q: %q", s, got)
			}
		}
	}

	if RedactError(nil, target) != "" {
		t.Fatal("RedactError(nil) must be empty")
	}

	// A client.Do-shaped error: Op + quoted URL + inner.
	ue := &url.Error{Op: "Post", URL: target, Err: errors.New("dial tcp: connection refused")}
	got := RedactError(ue, target)
	check(t, got)
	if want := `Post "https://hooks.example/[redacted]": dial tcp: connection refused`; got != want {
		t.Fatalf("RedactError = %q, want %q", got, want)
	}

	// A redirect target is a different URL: redacted by the chain walk.
	redirect := &url.Error{Op: "Post", URL: "https://elsewhere.example/r/s3cr3tPath", Err: errors.New("stopped")}
	check(t, RedactError(redirect, target))

	// A wrapped *url.Error keeps the wrapper text, URL redacted.
	wrapped := fmt.Errorf("deliver: %w", ue)
	got = RedactError(wrapped, target)
	check(t, got)
	if !strings.HasPrefix(got, "deliver: ") {
		t.Fatalf("wrapper text lost: %q", got)
	}

	// Backstop: a plain error quoting the raw target or its parts.
	for _, e := range []error{
		errors.New("failed for " + target),
		fmt.Errorf("failed for %q", target),
		errors.New("path /services/T/B/s3cr3tPath rejected"),
		errors.New("query token=s3cr3tQuery rejected"),
	} {
		check(t, RedactError(e, target))
	}
}

// TestSecretURLPartsRedacted (AC17): a delivery whose target URL carries
// secrets in its path and query fails (connection refused); neither
// secret appears in last_error, the delivery log or the operator log.
// Before the fix the *url.Error text — the full URL — was stored and
// logged verbatim.
func TestSecretURLPartsRedacted(t *testing.T) {
	logs := captureLogs(t)
	const target = "http://127.0.0.1:1/services/T000/B000/s3cr3tPathToken?token=s3cr3tQueryToken"
	st := newQueueStore()
	st.add("d1", "sub-1", st.now().Add(-time.Minute))
	res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-1": {SubscriptionID: "sub-1", TargetURL: target}}}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{HTTPTimeout: 2 * time.Second})
	sharedClock(d, st)
	d.poll(context.Background())

	r := st.row("d1")
	if r.LastError == "" || r.Status != "failed" {
		t.Fatalf("row = %+v, want a recorded connect failure", r)
	}
	if !strings.Contains(r.LastError, "http://127.0.0.1:1/[redacted]") {
		t.Fatalf("last_error %q lost the endpoint's scheme/host/port", r.LastError)
	}
	haystacks := []string{r.LastError, logs.String()}
	for _, l := range st.logsFor("d1") {
		haystacks = append(haystacks, l.Error)
	}
	for _, h := range haystacks {
		for _, secret := range []string{"s3cr3tPathToken", "s3cr3tQueryToken"} {
			if strings.Contains(h, secret) {
				t.Errorf("secret %q leaked into %q", secret, h)
			}
		}
	}
	// Parse errors must not echo the URL either.
	if err := NewTargetValidator(nil).ValidateURL("https://exa mple.com/s3cr3tPathToken"); err == nil || strings.Contains(err.Error(), "s3cr3tPathToken") {
		t.Errorf("parse error %v echoes the URL", err)
	}
	if err := NewTargetValidator(nil).ValidateURL("https://example.com/%zzs3cr3t"); err == nil || strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "%zz") {
		t.Errorf("escape error %v echoes the path", err)
	}
}

// TestRedirectTargetRedacted: an unsafe redirect's Location (another
// URL that may carry a secret) is redacted in the stored error.
func TestRedirectTargetRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/s3cr3tRedirect", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	host = host[:strings.LastIndex(host, ":")]
	st := newQueueStore()
	st.add("d1", "sub-1", st.now())
	res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-1": {SubscriptionID: "sub-1", TargetURL: srv.URL + "/hook/s3cr3tPath"}}}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{Targets: NewTargetValidator([]string{host})})
	sharedClock(d, st)
	d.poll(context.Background())
	r := st.row("d1")
	if r.LastError == "" {
		t.Fatalf("redirect to IMDS was followed: %+v", r)
	}
	for _, s := range []string{"s3cr3tRedirect", "s3cr3tPath", "latest"} {
		if strings.Contains(r.LastError, s) {
			t.Fatalf("last_error %q leaks %q", r.LastError, s)
		}
	}
}

// The bootstrap's creation log line names the endpoint, not its path.
func TestBootstrapLogRedactsTargetURL(t *testing.T) {
	logs := captureLogs(t)
	cfg := canonicalConfig()
	cfg.EventURL = "https://internal.example.com/internal/step-events/ws-1?token=s3cr3tQueryToken"
	if err := BootstrapInternalSubscription(context.Background(), &fakeSubscriptionStore{}, cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "s3cr3tQueryToken") || !strings.Contains(logs.String(), "https://internal.example.com/[redacted]") {
		t.Fatalf("bootstrap log = %q", logs.String())
	}
}
