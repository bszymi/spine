package delivery

// INIT-023 TASK-010 regression tests for audit P3-25 (the target
// validator is a DNS oracle and misses IPv6 transition addresses), plus
// the design check's connect-error leak.

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	// End to end through the dialer: an unresolvable name.
	dial := NewTargetValidator(nil).SafeDialContext(&net.Dialer{Timeout: time.Second})
	_, err := dial(context.Background(), "tcp", "nonexistent.invalid:443")
	if err == nil || !strings.HasPrefix(err.Error(), "target_url: resolve nonexistent.invalid: ") || strings.Contains(err.Error(), " on ") {
		t.Errorf("unresolvable host error = %v", err)
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
