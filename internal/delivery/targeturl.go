package delivery

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

// TargetValidator enforces the rules webhook target URLs must satisfy
// before the Spine server will open an outbound connection to them.
// It exists to prevent SSRF against cloud metadata endpoints, loopback
// services, and private-network services when the server is running in
// a multi-tenant environment with admin-level actors whose only
// "privilege" is configuring a subscription.
//
// Rules applied in ValidateURL:
//   - parseable URL with a supported scheme (https, or http iff the
//     host is in the explicit allowlist)
//   - non-empty host, no userinfo (`https://user:pass@host/` is always
//     rejected — credentials in URLs are never desired for webhooks)
//
// Rules applied in CheckAddr / SafeDialContext (re-evaluated at connect
// time so DNS rebinding cannot slip past the ValidateURL step):
//   - reject loopback, link-local (includes 169.254.169.254), multicast,
//     unspecified, and RFC 1918 / ULA private addresses
//   - unless the hostname is in the explicit allowlist, in which case
//     the operator has opted in to a known-internal destination
//
// The zero-value validator (nil) permits every URL. Callers must always
// route webhook creation and dispatch through a non-nil validator.
type TargetValidator struct {
	// allowedHosts holds lowercased hostnames that are exempt from both
	// the scheme restriction and the private-address rejection. Empty
	// entries are ignored at construction.
	allowedHosts map[string]struct{}
}

// NewTargetValidator builds a validator with an optional allowlist of
// hostnames whose connections may resolve to private IPs and may be
// reached over plain http. Hostnames are case-insensitive.
func NewTargetValidator(allowedHosts []string) *TargetValidator {
	m := make(map[string]struct{}, len(allowedHosts))
	for _, h := range allowedHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			m[h] = struct{}{}
		}
	}
	return &TargetValidator{allowedHosts: m}
}

// isAllowedHost reports whether the given hostname is on the explicit
// allowlist. Comparison is case-insensitive.
func (v *TargetValidator) isAllowedHost(host string) bool {
	if v == nil {
		return true
	}
	_, ok := v.allowedHosts[strings.ToLower(host)]
	return ok
}

// ValidateURL parses rawURL and applies the syntactic rules. It returns
// a *domain.SpineError with ErrInvalidParams so the gateway surfaces a
// 400 with a stable message.
//
// A nil receiver is permissive by design so tests and bootstrap paths
// that haven't wired a validator keep working; call sites are
// responsible for wiring a real validator in production.
func (v *TargetValidator) ValidateURL(rawURL string) error {
	if v == nil {
		if rawURL == "" {
			return domain.NewError(domain.ErrInvalidParams, "target_url required")
		}
		return nil
	}
	if rawURL == "" {
		return domain.NewError(domain.ErrInvalidParams, "target_url: empty")
	}
	// url.Parse accepts relative refs; require a scheme explicitly.
	u, err := url.Parse(rawURL)
	if err != nil {
		return domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("target_url: parse error: %v", err))
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		return domain.NewError(domain.ErrInvalidParams, "target_url: missing scheme")
	}
	// Check the scheme before host/userinfo so operators get a
	// precise "unsupported scheme" message for obviously-wrong URLs
	// like file:// or gopher:// instead of a generic "missing host".
	if scheme != "https" && scheme != "http" {
		return domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("target_url: unsupported scheme %q", u.Scheme))
	}
	if u.User != nil {
		return domain.NewError(domain.ErrInvalidParams, "target_url: userinfo not permitted")
	}
	if u.Host == "" {
		return domain.NewError(domain.ErrInvalidParams, "target_url: missing host")
	}
	host := u.Hostname()
	if host == "" {
		return domain.NewError(domain.ErrInvalidParams, "target_url: missing host")
	}
	if scheme == "http" && !v.isAllowedHost(host) {
		return domain.NewError(domain.ErrInvalidParams, "target_url: http scheme only permitted for allowlisted hosts")
	}
	// If the host is a literal IP, reject the unsafe ranges at create
	// time so the row never makes it into the subscriptions table —
	// otherwise a row like https://127.0.0.1/ would persist and only
	// fail at dial time with a connection error (not invalid_params).
	// DNS hostnames are checked at connect time in SafeDialContext
	// since we can't resolve them from a pure URL parser.
	if ip := net.ParseIP(host); ip != nil {
		if err := v.CheckAddr(host, ip); err != nil {
			return domain.NewError(domain.ErrInvalidParams, "target_url: "+err.Error())
		}
	}
	return nil
}

// CheckAddr reports whether the given resolved IP is a safe
// destination for the supplied host. Used by SafeDialContext after DNS
// resolution, and exported so callers can check IP literals directly
// in tests.
func (v *TargetValidator) CheckAddr(host string, ip net.IP) error {
	if v == nil {
		return nil
	}
	if ip == nil {
		return fmt.Errorf("target_url: resolved to nil address")
	}
	if v.isAllowedHost(host) {
		return nil
	}
	if ip.IsLoopback() {
		return fmt.Errorf("target_url: loopback address %s not permitted", ip)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("target_url: unspecified address %s not permitted", ip)
	}
	if ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return fmt.Errorf("target_url: multicast address %s not permitted", ip)
	}
	// IsLinkLocalUnicast covers IPv4 169.254.0.0/16 (including the AWS
	// IMDS address) and IPv6 fe80::/10.
	if ip.IsLinkLocalUnicast() {
		return fmt.Errorf("target_url: link-local address %s not permitted", ip)
	}
	// IsPrivate covers RFC 1918 IPv4 and RFC 4193 IPv6 ULA.
	if ip.IsPrivate() {
		return fmt.Errorf("target_url: private address %s not permitted", ip)
	}
	// IsPrivate does not include several non-public IPv4 ranges that
	// are routinely used for internal services:
	//   - 100.64.0.0/10 (RFC 6598 shared address space, aka CGNAT)
	//   - 198.18.0.0/15 (RFC 2544 benchmarking, also used internally)
	// Reject them explicitly so the allowlist remains the only way in.
	for _, cidr := range extraNonPublicV4 {
		if cidr.Contains(ip) {
			return fmt.Errorf("target_url: non-public address %s not permitted", ip)
		}
	}
	return nil
}

// extraNonPublicV4 are IPv4 ranges that net.IP.IsPrivate does not
// classify as private but that routinely host internal services, so
// the SSRF guard must also reject them. Parsed once at init so
// CheckAddr stays allocation-free on the hot path.
var extraNonPublicV4 = []*net.IPNet{
	mustParseCIDR("100.64.0.0/10"), // RFC 6598 shared address space (CGNAT)
	mustParseCIDR("198.18.0.0/15"), // RFC 2544 benchmarking
}

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("invalid CIDR in extraNonPublicV4: " + s + ": " + err.Error())
	}
	return n
}

// SafeDialContext wraps a *net.Dialer so every dial re-validates the
// resolved address. Returning an error aborts the connection before
// any bytes leave the process, closing the DNS-rebinding window that
// a create-time-only URL check would leave open.
func (v *TargetValidator) SafeDialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if d == nil {
		d = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("target_url: split host/port: %w", err)
		}
		// Resolve all addresses so we reject any that point at private
		// space — for hosts with mixed answers we fail closed rather
		// than racing the dialer to pick a public one.
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("target_url: resolve %s: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("target_url: %s resolved to no addresses", host)
		}
		for _, ipa := range ips {
			if err := v.CheckAddr(host, ipa.IP); err != nil {
				return nil, err
			}
		}
		// Dial the already-validated addresses in order so we still
		// connect to the host's actual answer set (preserving
		// multi-A/AAAA reachability and IPv4 fallback when IPv6 is
		// broken), while never letting the kernel/OS resolver
		// re-query and race us into a newly-poisoned answer.
		var lastErr error
		for _, ipa := range ips {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// HTTPClient returns an *http.Client whose transport dials through
// SafeDialContext. Use this for any outbound request built from a
// subscription's target_url — the gateway subscription-test handler
// and the webhook dispatcher both route through it.
//
// Every redirect the client would follow is re-validated against
// ValidateURL. Without this a validated https:// webhook that
// responds with "Location: http://169.254.169.254/" would be
// followed to a cleartext private host using the default http redirect
// policy, bypassing the whole guard.
//
// A nil receiver returns a plain *http.Client with the supplied
// timeout, preserving prior behaviour for tests that haven't wired
// a validator.
func (v *TargetValidator) HTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if v == nil {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     v.Transport(nil),
		CheckRedirect: v.CheckRedirect,
	}
}

// CheckRedirect is an http.Client.CheckRedirect that re-applies
// ValidateURL to every redirect target, so a validated webhook cannot
// bounce a delivery onto an unsafe URL via a 30x response. The
// request's target_url is also the thing the dialer sees, so this is
// the only place the scheme / IP / allowlist contract can still be
// enforced once the response is in-flight.
func (v *TargetValidator) CheckRedirect(req *http.Request, via []*http.Request) error {
	if v == nil {
		return nil
	}
	if len(via) >= 10 {
		return fmt.Errorf("target_url: too many redirects")
	}
	return v.ValidateURL(req.URL.String())
}

// Transport wraps base (or a fresh default transport) so the final
// dial goes through SafeDialContext. It also disables HTTP proxies for
// the returned transport: if HTTP_PROXY / HTTPS_PROXY were inherited
// from http.DefaultTransport, the dialer would validate the proxy
// address rather than the webhook destination, letting a
// public-looking URL tunnel to a private host. Webhooks should
// connect to their target directly.
//
// A nil receiver returns base (or a cloned default transport) without
// injecting the safe dialer or clearing proxies.
func (v *TargetValidator) Transport(base *http.Transport) *http.Transport {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	if v == nil {
		return base
	}
	base.Proxy = nil
	base.DialContext = v.SafeDialContext(&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second})
	return base
}
