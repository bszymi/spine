package delivery

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// SubscriptionTLSConfig captures optional TLS hardening settings for a
// single webhook subscription. Both fields are optional; when neither
// is set the dispatcher uses its shared default http.Client.
//
// Operators persist these in the subscription's Metadata JSONB under
// the "tls" key (see parseSubscriptionTLS). That keeps the schema
// unchanged — a subsequent migration can promote them to first-class
// columns if desired.
type SubscriptionTLSConfig struct {
	// PinnedSPKISHA256 is the hex-encoded SHA-256 of the expected
	// leaf certificate's SubjectPublicKeyInfo (DER). When set, any
	// connection whose leaf SPKI doesn't match is rejected.
	PinnedSPKISHA256 string `json:"pinned_spki_sha256,omitempty"`

	// CABundlePEM is an optional custom trust store (PEM). When
	// provided, the system roots are ignored for this subscription —
	// only certificates signed by CAs in the bundle are accepted.
	CABundlePEM string `json:"ca_bundle_pem,omitempty"`
}

// parseSubscriptionTLS extracts SubscriptionTLSConfig from a
// subscription's Metadata JSONB blob. Returns nil when no TLS block is
// present; returns a non-nil *SubscriptionTLSConfig and nil error only
// when at least one field is populated. A malformed Metadata blob is
// reported so operators can fix the misconfiguration rather than
// silently falling back to the default client.
//
// It delegates to parseSubscriptionMetadata (INIT-023 TASK-010), the
// single parser the dispatcher's resolver uses, so the two can never
// disagree about a row. That parser also validates signature_version,
// so metadata with an unknown version is now an error here too.
func parseSubscriptionTLS(metadata []byte) (*SubscriptionTLSConfig, error) {
	md, err := parseSubscriptionMetadata(metadata)
	if err != nil {
		return nil, err
	}
	return md.TLS, nil
}

// buildTLSClient constructs an http.Client that honors a subscription's
// TLS configuration. Both PinnedSPKISHA256 and CABundlePEM are honored
// independently: a pinned cert does NOT imply a custom CA (the pin
// alone is authoritative), and a custom CA does NOT imply pinning (a
// legitimately-signed new cert is still acceptable). Timeout matches
// the dispatcher's HTTP timeout for consistency.
//
// If targets is non-nil, the transport dials through its
// SafeDialContext so per-subscription TLS clients are subject to the
// same SSRF / DNS-rebinding protection as the default client.
func buildTLSClient(cfg *SubscriptionTLSConfig, timeout time.Duration, targets *TargetValidator) (*http.Client, error) {
	tlsCfg := &tls.Config{}
	if cfg.CABundlePEM != "" {
		pool := x509.NewCertPool()
		if ok := pool.AppendCertsFromPEM([]byte(cfg.CABundlePEM)); !ok {
			return nil, fmt.Errorf("no certificates parsed from ca_bundle_pem")
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.PinnedSPKISHA256 != "" {
		expected := strings.ToLower(cfg.PinnedSPKISHA256)
		tlsCfg.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("no peer certificate")
			}
			spki := state.PeerCertificates[0].RawSubjectPublicKeyInfo
			sum := sha256.Sum256(spki)
			got := hex.EncodeToString(sum[:])
			if got != expected {
				return fmt.Errorf("SPKI pin mismatch: got %s, want %s", got, expected)
			}
			return nil
		}
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsCfg,
	}
	if targets != nil {
		transport.DialContext = targets.SafeDialContext(&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		})
		// Disable HTTP proxies so the safe dialer validates the real
		// webhook destination, not the proxy. Without this, a URL
		// like https://public.example.com/ could be tunneled to a
		// private host via a configured proxy.
		transport.Proxy = nil
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
	if targets != nil {
		client.CheckRedirect = targets.CheckRedirect
	}
	return client, nil
}
