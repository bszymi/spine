package gateway

import (
	"net"
	"net/http/httptest"
	"testing"
)

func mustCIDRs(t *testing.T, raw string) []*net.IPNet {
	t.Helper()
	nets, err := ParseTrustedProxyCIDRs(raw)
	if err != nil {
		t.Fatalf("ParseTrustedProxyCIDRs(%q): %v", raw, err)
	}
	return nets
}

func TestParseTrustedProxyCIDRs(t *testing.T) {
	if nets, err := ParseTrustedProxyCIDRs(""); err != nil || nets != nil {
		t.Fatalf("empty input: got %v, %v", nets, err)
	}
	if nets, err := ParseTrustedProxyCIDRs(" 10.0.0.0/8 , 192.168.1.0/24 "); err != nil || len(nets) != 2 {
		t.Fatalf("expected 2 nets, got %v, %v", nets, err)
	}
	if _, err := ParseTrustedProxyCIDRs("not-a-cidr"); err == nil {
		t.Fatal("expected parse error for invalid CIDR")
	}
}

func TestClientIPForRateLimit_NoTrustedProxies(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")

	// Without trusted proxies, XFF must be ignored.
	got := clientIPForRateLimit(r, nil)
	if got != "203.0.113.5" {
		t.Fatalf("expected 203.0.113.5 (RemoteAddr), got %q", got)
	}
}

func TestClientIPForRateLimit_UntrustedRemoteAddr(t *testing.T) {
	// RemoteAddr is a real client, not a proxy — XFF must not be trusted
	// even when trusted CIDRs are configured.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")

	got := clientIPForRateLimit(r, mustCIDRs(t, "10.0.0.0/8"))
	if got != "203.0.113.5" {
		t.Fatalf("expected RemoteAddr when client is not a trusted proxy, got %q", got)
	}
}

func TestClientIPForRateLimit_TrustedProxySingleHop(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:443"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")

	got := clientIPForRateLimit(r, mustCIDRs(t, "10.0.0.0/8"))
	if got != "198.51.100.9" {
		t.Fatalf("expected 198.51.100.9 from XFF, got %q", got)
	}
}

func TestClientIPForRateLimit_MultipleProxyHops(t *testing.T) {
	// Chain: client 198.51.100.9 -> proxy 10.0.0.1 -> proxy 10.0.0.2 (edge) -> server.
	// Walking right-to-left we skip the trusted proxies and stop at the first
	// untrusted entry, which is the real client.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:443"
	r.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.1")

	got := clientIPForRateLimit(r, mustCIDRs(t, "10.0.0.0/8"))
	if got != "198.51.100.9" {
		t.Fatalf("expected 198.51.100.9 from XFF after skipping trusted proxies, got %q", got)
	}
}

func TestClientIPForRateLimit_XFFEmptyFallsBackToRemote(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:443"
	// No XFF header set.

	got := clientIPForRateLimit(r, mustCIDRs(t, "10.0.0.0/8"))
	if got != "10.0.0.2" {
		t.Fatalf("expected RemoteAddr fallback when XFF missing, got %q", got)
	}
}

func TestClientIPForRateLimit_ForgedXFFFromUntrustedClient(t *testing.T) {
	// A direct (untrusted) client forges XFF to impersonate another IP —
	// the limiter must ignore the header and bucket on RemoteAddr.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")

	got := clientIPForRateLimit(r, mustCIDRs(t, "10.0.0.0/8"))
	if got != "203.0.113.5" {
		t.Fatalf("forged XFF must be ignored; expected RemoteAddr, got %q", got)
	}
}
