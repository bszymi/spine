package delivery

// INIT-023 TASK-010 regression tests for audit P3-25 (the target
// validator is a DNS oracle and misses IPv6 transition addresses), plus
// the design check's connect-error leak.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTransitionAddressesBlocked (AC15): an IPv6 transition address is
// classified by the IPv4 address it embeds; the prefixes whose embedding
// cannot be read are refused outright; public translations stay allowed.
func TestTransitionAddressesBlocked(t *testing.T) {
	v := NewTargetValidator(nil)
	blocked := []string{
		"64:ff9b::a00:1",                       // NAT64 -> 10.0.0.1
		"64:ff9b::7f00:1",                      // NAT64 -> 127.0.0.1
		"64:ff9b::a9fe:a9fe",                   // NAT64 -> 169.254.169.254 (IMDS)
		"2002:a00:1::1",                        // 6to4 -> 10.0.0.1
		"2002:7f00:1::",                        // 6to4 -> 127.0.0.1
		"2002:a9fe:a9fe::1",                    // 6to4 -> 169.254.169.254
		"::10.0.0.1",                           // IPv4-compatible
		"::127.0.0.1",                          // IPv4-compatible loopback
		"::a9fe:a9fe",                          // IPv4-compatible IMDS
		"::ffff:10.0.0.1",                      // IPv4-mapped (folded by To4; guard)
		"64:ff9b:1::a00:1",                     // RFC 8215 local-use NAT64
		"2001:0:4136:e378:8000:63bf:f5ff:fffe", // Teredo
		"0.1.2.3",                              // 0.0.0.0/8
		"::ffff:0:a00:1",                       // SIIT -> 10.0.0.1
		"::ffff:0:7f00:1",                      // SIIT -> 127.0.0.1
		"::ffff:0:a9fe:a9fe",                   // SIIT -> 169.254.169.254
		"fec0::1",                              // deprecated site-local
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test address %s", s)
		}
		err := v.CheckAddr("example.net", ip)
		if err == nil {
			t.Errorf("CheckAddr(%s) = nil, want blocked", s)
			continue
		}
		var be *BlockedAddressError
		if !errors.As(err, &be) || strings.Contains(err.Error(), s) {
			t.Errorf("CheckAddr(%s) = %v, want a *BlockedAddressError without the address", s, err)
		}
	}
	public := []string{"8.8.8.8", "64:ff9b::808:808", "2002:808:808::1", "2606:4700:4700::1111", "::ffff:0:808:808"}
	for _, s := range public {
		if err := v.CheckAddr("example.net", net.ParseIP(s)); err != nil {
			t.Errorf("CheckAddr(%s) = %v, want allowed", s, err)
		}
	}
	// The literal-IP path of ValidateURL refuses the same forms.
	if err := v.ValidateURL("https://[64:ff9b::a9fe:a9fe]/latest/meta-data/"); err == nil {
		t.Error("ValidateURL accepted a NAT64 literal for the IMDS address")
	}
}

// TestValidatorDoesNotReportResolvedAddresses (AC16): no caller-facing
// error names an address the caller did not supply.
func TestValidatorDoesNotReportResolvedAddresses(t *testing.T) {
	v := NewTargetValidator(nil)
	err := v.ValidateURL("https://localhost/hook")
	if err == nil {
		t.Fatal("localhost accepted")
	}
	for _, leak := range []string{"127.0.0.1", "::1", "loopback"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("ValidateURL error %q reveals %q", err, leak)
		}
	}
	if err := v.CheckAddr("example.net", net.ParseIP("10.11.12.13")); err == nil || strings.Contains(err.Error(), "10.11.12.13") {
		t.Errorf("CheckAddr error %v reveals the address", err)
	}
	dial := v.SafeDialContext(&net.Dialer{Timeout: time.Second})
	_, derr := dial(context.Background(), "tcp", "localhost:443")
	if derr == nil {
		t.Fatal("dial to localhost allowed")
	}
	if !errors.Is(derr, ErrBlockedDestination) {
		t.Errorf("dial error %v, want ErrBlockedDestination", derr)
	}
	for _, leak := range []string{"127.0.0.1", "::1", "loopback"} {
		if strings.Contains(derr.Error(), leak) {
			t.Errorf("dial error %q reveals %q", derr, leak)
		}
	}
}

// TestDNSFailureOmitsResolverAddress (AC16): net.DNSError.Error() names
// the resolver ("lookup x on 10.0.0.2:53: ..."); the rendered reason
// does not.
func TestDNSFailureOmitsResolverAddress(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "no such host", Name: "x.example", Server: "10.0.0.2:53", IsNotFound: true}, "no such host"},
		{&net.DNSError{Err: "i/o timeout", Name: "x.example", Server: "10.0.0.2:53", IsTimeout: true}, "timeout"},
		{&net.DNSError{Err: "server misbehaving", Name: "x.example", Server: "10.0.0.2:53", IsTemporary: true}, "temporary failure"},
		{&net.DNSError{Err: "odd", Name: "x.example", Server: "10.0.0.2:53"}, "lookup failed"},
		{fmt.Errorf("wrapped: %w", &net.DNSError{Err: "no such host", Server: "10.0.0.2:53", IsNotFound: true}), "no such host"},
		{errors.New("not a dns error 10.0.0.2"), "lookup failed"},
	}
	for _, tc := range cases {
		got := dnsFailureReason(tc.err)
		if got != tc.want || strings.Contains(got, "10.0.0.2") {
			t.Errorf("dnsFailureReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	// End to end through the dialer, with a resolver failure that names
	// the resolver (as Go's pure-Go resolver does).
	v := NewTargetValidator(nil)
	v.lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		return nil, &net.DNSError{Err: "no such host", Name: host, Server: "10.0.0.2:53", IsNotFound: true}
	}
	_, err := v.SafeDialContext(&net.Dialer{Timeout: time.Second})(context.Background(), "tcp", "x.example:443")
	if err == nil || err.Error() != "target_url: resolve x.example: no such host" {
		t.Errorf("resolver failure through the dialer = %v", err)
	}
	// And with the real resolver: an unresolvable name never echoes a
	// resolver address.
	dial := NewTargetValidator(nil).SafeDialContext(&net.Dialer{Timeout: time.Second})
	_, err = dial(context.Background(), "tcp", "nonexistent.invalid:443")
	if err == nil || !strings.HasPrefix(err.Error(), "target_url: resolve nonexistent.invalid: ") || strings.Contains(err.Error(), " on ") {
		t.Errorf("unresolvable host error = %v", err)
	}
}

// A DNS-named target whose answer is private is refused without the
// address or its class, at create time and at connect time.
func TestPrivateAnswerNotReported(t *testing.T) {
	v := NewTargetValidator(nil)
	v.lookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("10.1.2.3")}}, nil
	}
	err := v.ValidateURL("https://internal.example/hook")
	if err == nil || err.Error() != "invalid_params: "+msgNonPublicHost {
		t.Fatalf("ValidateURL = %v, want the generic refusal", err)
	}
	_, derr := v.SafeDialContext(&net.Dialer{Timeout: time.Second})(context.Background(), "tcp", "internal.example:443")
	if !errors.Is(derr, ErrBlockedDestination) {
		t.Fatalf("dial = %v, want ErrBlockedDestination", derr)
	}
	for _, e := range []error{err, derr} {
		for _, leak := range []string{"10.1.2.3", "private"} {
			if strings.Contains(e.Error(), leak) {
				t.Fatalf("%q reveals %q", e, leak)
			}
		}
	}
}

// closedPort returns a loopback port with nothing listening.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "i/o timeout" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

// TestDialErrorOmitsResolvedAddress (AC16, design check): a connect
// failure to an allowlisted DNS-named host names the caller's host:port,
// never the resolved address, in the dispatcher's last_error and
// delivery log; net.Error timeout semantics survive the re-rendering.
func TestDialErrorOmitsResolvedAddress(t *testing.T) {
	port := closedPort(t)
	v := NewTargetValidator([]string{"localhost"})
	target := fmt.Sprintf("http://localhost:%d/hook", port)

	st := newQueueStore()
	st.add("d1", "sub-x", st.now())
	res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: target}}}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{Targets: v, HTTPTimeout: 5 * time.Second})
	sharedClock(d, st)
	d.poll(context.Background())

	r := st.row("d1")
	if r.Status != "failed" || r.LastError == "" {
		t.Fatalf("row = %+v, want a retryable connect failure", r)
	}
	texts := []string{r.LastError}
	for _, l := range st.logsFor("d1") {
		texts = append(texts, l.Error)
	}
	for _, s := range texts {
		if strings.Contains(s, "127.0.0.1") || strings.Contains(s, "[::1]") {
			t.Fatalf("error text names the resolved address: %q", s)
		}
		if !strings.Contains(s, fmt.Sprintf("localhost:%d", port)) {
			t.Fatalf("error text %q does not name the caller's host:port", s)
		}
	}

	// Direct dial: *DialError, Unwrap reaches the syscall error.
	dial := v.SafeDialContext(&net.Dialer{Timeout: time.Second})
	_, err := dial(context.Background(), "tcp", fmt.Sprintf("localhost:%d", port))
	var de *DialError
	if !errors.As(err, &de) || de.HostPort != fmt.Sprintf("localhost:%d", port) {
		t.Fatalf("dial error = %#v, want *DialError for localhost:%d", err, port)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("dial error %v does not unwrap to ECONNREFUSED", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || ne.Timeout() {
		t.Fatalf("connection refused reported as net.Error timeout=%v", ne != nil && ne.Timeout())
	}

	// A dial timeout keeps Timeout() == true through the re-rendering.
	timeoutDial := v.SafeDialContext(&net.Dialer{Timeout: time.Second,
		Control: func(string, string, syscall.RawConn) error { return fakeTimeout{} }})
	_, err = timeoutDial(context.Background(), "tcp", fmt.Sprintf("localhost:%d", port))
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("dial timeout lost its Timeout(): %v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "[::1]") {
		t.Fatalf("timeout error names the resolved address: %v", err)
	}
}

// resetServer accepts connections, reads what the client sends first
// (an HTTP request, or a TLS ClientHello) and then resets the connection
// (SO_LINGER 0), so the client's next read fails with ECONNRESET after a
// successful connect. It returns the port.
func resetServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 4096)
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				_, _ = c.Read(buf)
				if tc, ok := c.(*net.TCPConn); ok {
					_ = tc.SetLinger(0)
				}
				c.Close()
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// leaksAddress reports whether s names a loopback address in any form.
func leaksAddress(s string) bool {
	return strings.Contains(s, "127.0.0.1") || strings.Contains(s, "[::1]") || strings.Contains(s, "::1]")
}

// TestPostConnectErrorOmitsAddresses (review round 1, I5): a read or
// write failure after a successful connect used to surface as
// "read tcp 127.0.0.1:<p>->127.0.0.1:<port>: read: connection reset by
// peer" — the resolved address of an allowlisted (or public) DNS-named
// target and Spine's own source address. The validator's client now
// reports the caller's host:port, keeps the error a *net.OpError that
// unwraps to ECONNRESET, and the dispatcher's last_error, delivery log
// and operator log name no address — for the default client and for a
// per-subscription TLS client.
func TestPostConnectErrorOmitsAddresses(t *testing.T) {
	port := resetServer(t)
	v := NewTargetValidator([]string{"localhost"})
	hostPort := fmt.Sprintf("localhost:%d", port)

	// The validator's client itself: this is what an embedder using
	// TargetValidator.HTTPClient sees, before any redaction.
	_, err := v.HTTPClient(5*time.Second).Post("http://"+hostPort+"/hook", "application/json", strings.NewReader(`{}`))
	if err == nil {
		t.Fatal("request to a resetting server succeeded")
	}
	if leaksAddress(err.Error()) || !strings.Contains(err.Error(), hostPort) {
		t.Fatalf("client error %q names an address or lost the caller's host:port", err)
	}
	var oe *net.OpError
	if !errors.As(err, &oe) || !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("client error %v: want a *net.OpError unwrapping to ECONNRESET", err)
	}

	for _, tc := range []struct {
		name   string
		target string
		tls    *SubscriptionTLSConfig
	}{
		{name: "default client", target: "http://" + hostPort + "/services/T/B/s3cr3tPathToken"},
		{name: "per-subscription TLS client", target: "https://" + hostPort + "/hook",
			tls: &SubscriptionTLSConfig{PinnedSPKISHA256: strings.Repeat("ab", 32)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			st := newQueueStore()
			st.add("d1", "sub-x", st.now())
			res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: tc.target, TLS: tc.tls}}}
			d := NewWebhookDispatcher(st, res, DispatcherConfig{Targets: v, HTTPTimeout: 5 * time.Second})
			sharedClock(d, st)
			d.poll(context.Background())
			r := st.row("d1")
			if r.Status != "failed" || r.LastError == "" {
				t.Fatalf("row = %+v, want a retryable transport failure", r)
			}
			// The dialer logs the resolved address at Debug for the
			// operator by design (§4); every other line carries the
			// caller-facing text and must not.
			texts := []string{r.LastError}
			for _, line := range strings.Split(logs.String(), "\n") {
				if !strings.Contains(line, "level=DEBUG") {
					texts = append(texts, line)
				}
			}
			if !strings.Contains(logs.String(), "webhook delivery failed") {
				t.Fatal("the delivery-failure log line is missing")
			}
			for _, l := range st.logsFor("d1") {
				texts = append(texts, l.Error)
			}
			for _, s := range texts {
				if leaksAddress(s) {
					t.Fatalf("error text names an address: %q", s)
				}
			}
			if !strings.Contains(r.LastError, hostPort) {
				t.Fatalf("last_error %q does not name the caller's host:port", r.LastError)
			}
		})
	}
}

// opConn is a net.Conn whose Read, Write and Close fail with the errors a
// kernel socket reports after connect.
type opConn struct {
	net.Conn
	readErr, writeErr, closeErr error
}

func (c *opConn) Read([]byte) (int, error)  { return 0, c.readErr }
func (c *opConn) Write([]byte) (int, error) { return 0, c.writeErr }
func (c *opConn) Close() error              { return c.closeErr }

// TestSafeConnRewritesOpErrors (review round 1): every *net.OpError from
// the connection loses its source and names the caller's host:port, with
// Op, Net, the inner error and Timeout() kept; other errors pass through.
func TestSafeConnRewritesOpErrors(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("10.0.0.9"), Port: 50122}
	dst := &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 443}
	reset := &net.OpError{Op: "read", Net: "tcp", Source: src, Addr: dst, Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	pipe := &net.OpError{Op: "write", Net: "tcp", Source: src, Addr: dst, Err: os.NewSyscallError("write", syscall.EPIPE)}
	deadline := &net.OpError{Op: "read", Net: "tcp", Source: src, Addr: dst, Err: os.ErrDeadlineExceeded}
	c := &safeConn{Conn: &opConn{readErr: reset, writeErr: pipe, closeErr: deadline}, addr: callerAddr{network: "tcp", hostPort: "hooks.example:443"}}

	_, rerr := c.Read(nil)
	_, werr := c.Write(nil)
	cerr := c.Close()
	for _, tc := range []struct {
		got     error
		want    string
		is      error
		timeout bool
	}{
		{rerr, "read tcp hooks.example:443: read: connection reset by peer", syscall.ECONNRESET, false},
		{werr, "write tcp hooks.example:443: write: broken pipe", syscall.EPIPE, false},
		{cerr, "read tcp hooks.example:443: i/o timeout", os.ErrDeadlineExceeded, true},
	} {
		if tc.got == nil || tc.got.Error() != tc.want {
			t.Errorf("got %v, want %q", tc.got, tc.want)
			continue
		}
		var oe *net.OpError
		if !errors.As(tc.got, &oe) || !errors.Is(tc.got, tc.is) || oe.Timeout() != tc.timeout {
			t.Errorf("%v: type/unwrap/timeout not kept", tc.got)
		}
	}
	// The originals are not mutated, and non-OpErrors pass through.
	if reset.Source != src || reset.Addr != dst {
		t.Fatal("the original *net.OpError was modified")
	}
	eof := &safeConn{Conn: &opConn{readErr: io.EOF}, addr: c.addr}
	if _, err := eof.Read(nil); err != io.EOF {
		t.Fatalf("io.EOF became %v", err)
	}
}

// TestRedactErrorRewritesOpErrorAddresses (review round 1): the
// RedactError backstop re-renders any *net.OpError in the chain that
// still names addresses, under the target URL's host[:port].
func TestRedactErrorRewritesOpErrorAddresses(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("10.0.0.9"), Port: 50122}
	dst := &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 443}
	target := "https://hooks.example/services/T/B/s3cr3tPathToken"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"read after connect", &url.Error{Op: "Post", URL: target,
			Err: &net.OpError{Op: "read", Net: "tcp", Source: src, Addr: dst, Err: os.NewSyscallError("read", syscall.ECONNRESET)}},
			`Post "https://hooks.example/[redacted]": read tcp hooks.example: read: connection reset by peer`},
		{"dial without a validator", &url.Error{Op: "Post", URL: target,
			Err: &net.OpError{Op: "dial", Net: "tcp", Addr: dst, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}},
			`Post "https://hooks.example/[redacted]": dial tcp hooks.example: connect: connection refused`},
		{"wrapped", fmt.Errorf("transport: %w", &net.OpError{Op: "write", Net: "tcp", Source: src, Addr: dst, Err: os.NewSyscallError("write", syscall.EPIPE)}),
			`transport: write tcp hooks.example: write: broken pipe`},
	} {
		got := RedactError(tc.err, target)
		if got != tc.want || strings.Contains(got, "10.0.0.9") || strings.Contains(got, "10.1.2.3") {
			t.Errorf("%s: RedactError = %q, want %q", tc.name, got, tc.want)
		}
	}
	// An error safeConn already re-rendered is left as it is.
	clean := &net.OpError{Op: "read", Net: "tcp", Addr: callerAddr{"tcp", "hooks.example:443"}, Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	if got := RedactError(clean, target); got != clean.Error() {
		t.Errorf("RedactError rewrote an already clean error: %q", got)
	}
}

// TestResolveErrorKeepsDNSErrorInChain (review round 1, CHANGELOG
// "For embedders"): the dialer's DNS-failure error omits the resolver's
// address from its text but still unwraps to the *net.DNSError, and a
// DNS timeout still reports Timeout() — directly and through the
// *url.Error a client returns.
func TestResolveErrorKeepsDNSErrorInChain(t *testing.T) {
	v := NewTargetValidator(nil)
	v.lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		return nil, &net.DNSError{Err: "i/o timeout", Name: host, Server: "10.0.0.2:53", IsTimeout: true}
	}
	_, err := v.SafeDialContext(&net.Dialer{Timeout: time.Second})(context.Background(), "tcp", "x.example:443")
	if err == nil || err.Error() != "target_url: resolve x.example: timeout" {
		t.Fatalf("dial error = %v", err)
	}
	var de *net.DNSError
	if !errors.As(err, &de) || de.Server != "10.0.0.2:53" || !de.IsTimeout {
		t.Fatalf("errors.As(*net.DNSError) lost the resolver error: %v", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("DNS timeout lost Timeout(): %v", err)
	}
	if ue := (&url.Error{Op: "Post", URL: "https://x.example/", Err: err}); !ue.Timeout() || strings.Contains(ue.Error(), "10.0.0.2") {
		t.Fatalf("through *url.Error: Timeout()=%v text %q", ue.Timeout(), ue.Error())
	}
}
