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
	return nil
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
		// Dial the first resolved address explicitly so the kernel/OS
		// resolver doesn't re-query and race us into a newly-poisoned
		// answer between our validation and the connect() call.
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// HTTPClient returns an *http.Client whose transport dials through
// SafeDialContext. Use this for any outbound request built from a
// subscription's target_url — the gateway subscription-test handler
// and the webhook dispatcher both route through it.
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
		Timeout:   timeout,
		Transport: v.Transport(nil),
	}
}

// Transport wraps base (or a fresh default transport) so the final
// dial goes through SafeDialContext. It leaves every other transport
// field (TLS config, proxy, etc.) untouched.
//
// A nil receiver returns base (or a cloned default transport) without
// injecting the safe dialer.
func (v *TargetValidator) Transport(base *http.Transport) *http.Transport {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	if v == nil {
		return base
	}
	base.DialContext = v.SafeDialContext(&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second})
	return base
}
