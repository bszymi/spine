package delivery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
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
//     unspecified, RFC 1918 / ULA private, and the other non-public
//     ranges in extraNonPublicV4 / rejectedV6Prefixes
//   - classify the IPv4 address embedded in an IPv6 transition address
//     (NAT64 64:ff9b::/96, 6to4 2002::/16, IPv4-compatible ::/96, SIIT
//     ::ffff:0:0:0/96) the same way, so 64:ff9b::a9fe:a9fe (IMDS through
//     NAT64) is refused like 169.254.169.254 (INIT-023 TASK-010, P3-25)
//   - unless the hostname is in the explicit allowlist, in which case
//     the operator has opted in to a known-internal destination
//
// Caller-facing errors never name an address the caller did not supply
// (P3-25): a DNS-named target that resolves to a blocked address gets a
// generic refusal, the dialer returns ErrBlockedDestination or a
// *DialError naming the caller's host:port, read and write failures on
// the connection it returns name the caller's host:port too (safeConn),
// and DNS failures omit the resolver's address. The detail goes to the
// operator log. Otherwise create, update and the subscription test would
// be an oracle on the operator's internal DNS and network.
//
// The zero-value validator (nil) permits every URL. Callers must always
// route webhook creation and dispatch through a non-nil validator.
type TargetValidator struct {
	// allowedHosts holds lowercased hostnames that are exempt from both
	// the scheme restriction and the private-address rejection. Empty
	// entries are ignored at construction.
	allowedHosts map[string]struct{}

	// lookupIPAddr resolves host names; nil means
	// net.DefaultResolver.LookupIPAddr. A field so tests can drive the
	// resolver-failure and private-answer paths deterministically.
	lookupIPAddr func(ctx context.Context, host string) ([]net.IPAddr, error)
}

// lookup resolves host through the configured resolver.
func (v *TargetValidator) lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	if v != nil && v.lookupIPAddr != nil {
		return v.lookupIPAddr(ctx, host)
	}
	return net.DefaultResolver.LookupIPAddr(ctx, host)
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
		// Never echo the URL: its path or query may carry a secret
		// token (Slack/Discord-style webhooks) and this message reaches
		// API responses, error columns and logs (P3-26).
		return domain.NewError(domain.ErrInvalidParams, "target_url: parse error: "+urlParseReason(err))
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
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return domain.NewError(domain.ErrInvalidParams, fmt.Sprintf("target_url: invalid port %q", p))
		}
	}
	if scheme == "http" && !v.isAllowedHost(host) {
		return domain.NewError(domain.ErrInvalidParams, "target_url: http scheme only permitted for allowlisted hosts")
	}
	if v.isAllowedHost(host) {
		// Allowlisted destinations intentionally opt out of the
		// private-range guard; bail before the IP/DNS checks.
		return nil
	}
	// If the host is a literal IP, reject the unsafe ranges at create
	// time so the row never makes it into the subscriptions table —
	// otherwise a row like https://127.0.0.1/ would persist and only
	// fail at dial time with a connection error (not invalid_params).
	// A literal-IP refusal names the class (loopback, private, ...) but
	// not the address — the caller supplied the address themselves.
	if ip := net.ParseIP(host); ip != nil {
		if err := v.CheckAddr(host, ip); err != nil {
			return domain.NewError(domain.ErrInvalidParams, err.Error())
		}
		return nil
	}
	// DNS hostname — resolve with a bounded timeout so `https://localhost/`
	// or a hostname pointing at an internal service is rejected at
	// create/update/test time instead of only at dial time. DNS failures
	// are not fatal: rebinding and offline-resolvers are handled by
	// SafeDialContext on the actual delivery path; we only act on
	// resolver answers we got.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := v.lookup(ctx, host)
	if err != nil {
		return nil
	}
	for _, ipa := range ips {
		if err := v.CheckAddr(host, ipa.IP); err != nil {
			// Neither the resolved address nor even its class goes back
			// to the caller: both are bits of the operator's internal
			// DNS. The operator log keeps the detail.
			observe.Logger(ctx).Info("webhook target host resolves to a blocked address",
				"host", host, "detail", blockedDetail(err))
			return domain.NewError(domain.ErrInvalidParams, msgNonPublicHost)
		}
	}
	return nil
}

// msgNonPublicHost is ValidateURL's refusal for a DNS-named target that
// resolves to a blocked address. It deliberately names no address and no
// address class.
const msgNonPublicHost = "target_url: host resolves to a non-public address (allowlist it via SPINE_WEBHOOK_ALLOWED_HOSTS if intended)"

// urlParseReason renders a url.Parse failure without the URL: the
// *url.Error wrapper quotes the whole input, and an escape error quotes
// the bytes after a '%', which may be part of a secret path segment.
func urlParseReason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var esc url.EscapeError
	if errors.As(err, &esc) {
		return "invalid URL escape"
	}
	return err.Error()
}

// BlockedAddressError is CheckAddr's refusal. Error() names the address
// class only ("target_url: private address not permitted"); the address
// itself is kept for operator logs and never rendered to a caller.
type BlockedAddressError struct {
	// Class is "loopback", "unspecified", "multicast", "link-local",
	// "private" or "non-public".
	Class string
	ip    net.IP
}

func (e *BlockedAddressError) Error() string {
	return "target_url: " + e.Class + " address not permitted"
}

// detail renders class and address for the operator log.
func (e *BlockedAddressError) detail() string {
	return e.Class + " " + e.ip.String()
}

func blockedDetail(err error) string {
	var be *BlockedAddressError
	if errors.As(err, &be) {
		return be.detail()
	}
	return err.Error()
}

// ErrBlockedDestination is what SafeDialContext returns when a resolved
// address is refused: no address, no class. It is the text that reaches
// last_error, the delivery log and the subscription-test response.
var ErrBlockedDestination = errors.New("target_url: destination address not permitted")

// DialError is SafeDialContext's error when every validated address
// failed to connect. It names the caller's own host:port, never the
// resolved address (the dialer's *net.OpError reads
// "dial tcp <resolved-ip>:<port>: ...", which hands the private address
// of an allowlisted internal host to anyone who can read last_error or
// the subscription-test response). Err is the OpError's inner error
// ("connect: connection refused", "i/o timeout"); Timeout, Temporary and
// Unwrap delegate to it, so net.Error semantics hold for callers.
type DialError struct {
	HostPort string
	Err      error
}

func (e *DialError) Error() string {
	return "target_url: connect " + e.HostPort + ": " + e.Err.Error()
}

func (e *DialError) Unwrap() error { return e.Err }

// Timeout reports whether the underlying dial failure was a timeout.
func (e *DialError) Timeout() bool {
	var t interface{ Timeout() bool }
	return errors.As(e.Err, &t) && t.Timeout()
}

// Temporary delegates to the underlying error (net.Error).
func (e *DialError) Temporary() bool {
	var t interface{ Temporary() bool }
	return errors.As(e.Err, &t) && t.Temporary()
}

// resolveError is SafeDialContext's error for a failed lookup. Error()
// reads "target_url: resolve <host>: no such host|timeout|temporary
// failure|lookup failed" and never includes the resolver's address
// (*net.DNSError.Error() reads "lookup <host> on <server>: ...", and
// <server> is the operator's internal resolver). Unwrap still returns the
// resolver's error, so errors.As(err, &*net.DNSError) keeps working for
// callers that branch on it, and Timeout/Temporary delegate to it, so
// url.Error.Timeout() still reports a DNS timeout. Code that renders the
// unwrapped *net.DNSError itself reintroduces the server address; nothing
// in Spine does.
type resolveError struct {
	host string
	err  error
}

func (e *resolveError) Error() string {
	return "target_url: resolve " + e.host + ": " + dnsFailureReason(e.err)
}

func (e *resolveError) Unwrap() error { return e.err }

// Timeout reports whether the lookup timed out.
func (e *resolveError) Timeout() bool {
	var t interface{ Timeout() bool }
	return errors.As(e.err, &t) && t.Timeout()
}

// Temporary delegates to the resolver's error (net.Error).
func (e *resolveError) Temporary() bool {
	var t interface{ Temporary() bool }
	return errors.As(e.err, &t) && t.Temporary()
}

// safeConn is the net.Conn SafeDialContext returns. A read or write
// failure after the connection is up is a *net.OpError whose text names
// both ends — "read tcp <local-ip:port>-><resolved-ip:port>: read:
// connection reset by peer" — so a target that resets the connection
// (or an allowlisted internal host restarting mid-request) would hand its
// resolved address, and Spine's own source address, to anyone who reads
// last_error, the delivery log or the subscription-test response
// (INIT-023 TASK-010 I5, review round 1; DialError covers the connect
// step). safeConn re-renders such errors as a *net.OpError with no Source
// and with Addr set to the caller's own host:port ("read tcp
// hooks.example:443: read: connection reset by peer"). The type, Op, Net
// and inner Err are kept, so Timeout(), errors.Is(err,
// syscall.ECONNRESET) and errors.As(err, &*net.OpError) behave as before.
// io.EOF and other errors pass through unchanged. LocalAddr/RemoteAddr
// are not rewritten: they are not error text.
type safeConn struct {
	net.Conn
	addr net.Addr
}

func (c *safeConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	return n, scrubOpError(err, c.addr)
}

func (c *safeConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	return n, scrubOpError(err, c.addr)
}

func (c *safeConn) Close() error {
	return scrubOpError(c.Conn.Close(), c.addr)
}

// scrubOpError returns err with the addresses of a *net.OpError replaced:
// Source dropped, Addr set to addr (which may be nil). A net.Conn returns
// its *net.OpError unwrapped, so only that exact value is rewritten.
func scrubOpError(err error, addr net.Addr) error {
	oe, ok := err.(*net.OpError)
	if !ok || (oe.Source == nil && oe.Addr == addr) {
		return err
	}
	cp := *oe
	cp.Source = nil
	cp.Addr = addr
	return &cp
}

// callerAddr is a net.Addr that renders the caller's own host:port.
type callerAddr struct{ network, hostPort string }

func (a callerAddr) Network() string { return a.network }
func (a callerAddr) String() string  { return a.hostPort }

// dnsFailureReason renders a resolver failure without the resolver's
// address: net.DNSError.Error() reads "lookup <host> on <server>: ...",
// and <server> is the operator's internal resolver.
func dnsFailureReason(err error) string {
	var de *net.DNSError
	if errors.As(err, &de) {
		switch {
		case de.IsNotFound:
			return "no such host"
		case de.IsTimeout:
			return "timeout"
		case de.IsTemporary:
			return "temporary failure"
		}
	}
	return "lookup failed"
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
	if class := blockedClass(ip); class != "" {
		return &BlockedAddressError{Class: class, ip: ip}
	}
	return nil
}

// blockedClass returns the non-public class of ip, or "" when ip is a
// permitted destination. It checks, in order:
//
//  1. ip itself (classifyOne);
//  2. for an IPv6 address, prefixes refused outright because the IPv4
//     address they lead to cannot be read off the address
//     (rejectedV6Prefixes);
//  3. the IPv4 address an IPv6 transition mechanism embeds in ip
//     (embeddedIPv4), classified like a native IPv4 address.
//
// Public NAT64 and 6to4 targets (64:ff9b::808:808, 2002:808:808::1) stay
// allowed: IPv6-only deployments reach IPv4 webhooks through NAT64.
func blockedClass(ip net.IP) string {
	if c := classifyOne(ip); c != "" {
		return c
	}
	if ip.To4() != nil {
		return ""
	}
	for _, n := range rejectedV6Prefixes {
		if n.Contains(ip) {
			return "non-public"
		}
	}
	if v4 := embeddedIPv4(ip); v4 != nil {
		return classifyOne(v4)
	}
	return ""
}

// classifyOne classifies one address by the native ranges.
func classifyOne(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast():
		return "multicast"
	// IsLinkLocalUnicast covers IPv4 169.254.0.0/16 (including the AWS
	// IMDS address) and IPv6 fe80::/10.
	case ip.IsLinkLocalUnicast():
		return "link-local"
	// IsPrivate covers RFC 1918 IPv4 and RFC 4193 IPv6 ULA.
	case ip.IsPrivate():
		return "private"
	}
	// IsPrivate does not include several non-public IPv4 ranges that
	// are routinely used for internal services. Reject them explicitly
	// so the allowlist remains the only way in.
	for _, cidr := range extraNonPublicV4 {
		if cidr.Contains(ip) {
			return "non-public"
		}
	}
	return ""
}

// embeddedIPv4 extracts the IPv4 address an IPv6 transition address
// carries, or nil:
//   - NAT64 well-known prefix 64:ff9b::/96 — the last 32 bits (RFC 6052);
//   - 6to4 2002::/16 — bits 16-47 (RFC 3056);
//   - IPv4-compatible ::/96 — the last 32 bits (RFC 4291 §2.5.5.1,
//     deprecated but still routable by some stacks);
//   - IPv4-translated (SIIT) ::ffff:0:0:0/96 — the last 32 bits
//     (RFC 2765 / RFC 6145); net.IP.To4 does not fold it.
//
// IPv4-mapped ::ffff:0:0/96 is already folded by net.IP.To4, so it is
// classified as the IPv4 address it is. A NAT64 deployment with an
// operator-chosen network-specific prefix (RFC 6052 §2.2) cannot be
// recognised generically; egress policy is the control there.
func embeddedIPv4(ip net.IP) net.IP {
	ip16 := ip.To16()
	if ip16 == nil || ip.To4() != nil {
		return nil
	}
	switch {
	case nat64WellKnown.Contains(ip16), ipv4Compatible.Contains(ip16), ipv4Translated.Contains(ip16):
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15])
	case sixToFour.Contains(ip16):
		return net.IPv4(ip16[2], ip16[3], ip16[4], ip16[5])
	}
	return nil
}

var (
	nat64WellKnown = mustParseCIDR("64:ff9b::/96")
	sixToFour      = mustParseCIDR("2002::/16")
	ipv4Compatible = mustParseCIDR("::/96")
	ipv4Translated = mustParseCIDR("::ffff:0:0:0/96")

	// rejectedV6Prefixes are refused outright: the IPv4 destination
	// behind them is not readable off the address.
	rejectedV6Prefixes = []*net.IPNet{
		mustParseCIDR("64:ff9b:1::/48"), // RFC 8215 local-use NAT64 (operator-defined embedding)
		mustParseCIDR("2001::/32"),      // Teredo (RFC 4380): server/client/obfuscated IPv4
		mustParseCIDR("fec0::/10"),      // deprecated site-local (RFC 3879), still routed internally
	}
)

// extraNonPublicV4 are IPv4 ranges that net.IP.IsPrivate does not
// classify as private but that routinely host internal services, so
// the SSRF guard must also reject them. Parsed once at init so
// CheckAddr stays allocation-free on the hot path.
var extraNonPublicV4 = []*net.IPNet{
	mustParseCIDR("0.0.0.0/8"),     // RFC 1122 "this network" (0.0.0.0 itself is "unspecified")
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
		ips, err := v.lookup(ctx, host)
		if err != nil {
			observe.Logger(ctx).Debug("webhook target resolve failed", "host", host, "error", err)
			return nil, &resolveError{host: host, err: err}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("target_url: %s resolved to no addresses", host)
		}
		for _, ipa := range ips {
			if err := v.CheckAddr(host, ipa.IP); err != nil {
				observe.Logger(ctx).Warn("webhook dial blocked", "host", host, "detail", blockedDetail(err))
				return nil, ErrBlockedDestination
			}
		}
		// Dial the already-validated addresses in order so we still
		// connect to the host's actual answer set (preserving
		// multi-A/AAAA reachability and IPv4 fallback when IPv6 is
		// broken), while never letting the kernel/OS resolver
		// re-query and race us into a newly-poisoned answer. Each
		// attempt gets a bounded per-address deadline so a single
		// blackholed answer can't consume the caller's entire
		// timeout budget.
		perAddr := perAddrDialTimeout(d.Timeout, len(ips))
		var lastErr error
		for _, ipa := range ips {
			attemptCtx, cancel := context.WithTimeout(ctx, perAddr)
			conn, err := d.DialContext(attemptCtx, network, net.JoinHostPort(ipa.IP.String(), port))
			cancel()
			if err == nil {
				// Post-connect read/write errors name the addresses too;
				// safeConn re-renders them under the caller's host:port.
				return &safeConn{Conn: conn, addr: callerAddr{network: network, hostPort: addr}}, nil
			}
			observe.Logger(ctx).Debug("webhook dial failed", "host", host, "addr", ipa.IP.String(), "error", err)
			lastErr = err
		}
		// Re-render the failure under the caller's host:port: the
		// *net.OpError names the resolved address.
		inner := lastErr
		var opErr *net.OpError
		if errors.As(lastErr, &opErr) && opErr.Err != nil {
			inner = opErr.Err
		}
		return nil, &DialError{HostPort: addr, Err: inner}
	}
}

// perAddrDialTimeout divides the dialer's overall timeout across the
// resolved addresses so fallback happens within the caller's budget.
// If the dialer has no explicit timeout, we fall back to a modest
// per-address default.
func perAddrDialTimeout(total time.Duration, n int) time.Duration {
	const defaultPerAddr = 3 * time.Second
	if n <= 0 {
		return defaultPerAddr
	}
	if total <= 0 {
		return defaultPerAddr
	}
	t := total / time.Duration(n)
	if t < time.Second {
		return time.Second
	}
	return t
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
