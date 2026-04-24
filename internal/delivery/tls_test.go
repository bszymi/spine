package delivery

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseSubscriptionTLS(t *testing.T) {
	cases := []struct {
		name      string
		meta      string
		wantNil   bool
		wantError bool
	}{
		{name: "empty metadata", meta: "", wantNil: true},
		{name: "metadata without tls block", meta: `{"other":"value"}`, wantNil: true},
		{name: "tls block with empty fields", meta: `{"tls":{}}`, wantNil: true},
		{name: "pinned spki only", meta: `{"tls":{"pinned_spki_sha256":"abcd1234"}}`},
		{name: "ca bundle only", meta: `{"tls":{"ca_bundle_pem":"-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----"}}`},
		{name: "both fields", meta: `{"tls":{"pinned_spki_sha256":"abcd1234","ca_bundle_pem":"pem"}}`},
		{name: "invalid json", meta: `{not json`, wantError: true},
		{name: "pin is not hex", meta: `{"tls":{"pinned_spki_sha256":"zzz"}}`, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseSubscriptionTLS([]byte(tc.meta))
			if tc.wantError {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if cfg != nil {
					t.Fatalf("expected nil, got %+v", cfg)
				}
				return
			}
			if cfg == nil {
				t.Fatal("expected non-nil config")
			}
		})
	}
}

// generateSelfSignedCert creates a fresh ECDSA key + self-signed
// certificate for use in TLS handshake tests.
func generateSelfSignedCert(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "spine-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(pemCert, pemKey)
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}
	return cert, parsed
}

func spkiSHA256Hex(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

func TestBuildTLSClient_PinnedSPKI_Match(t *testing.T) {
	cert, parsed := generateSelfSignedCert(t)

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	defer ts.Close()

	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	pem := pemEncode(parsed)

	client, err := buildTLSClient(&SubscriptionTLSConfig{
		PinnedSPKISHA256: spkiSHA256Hex(parsed),
		CABundlePEM:      string(pem),
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("expected success with matching pin, got %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestBuildTLSClient_PinnedSPKI_Mismatch(t *testing.T) {
	cert, parsed := generateSelfSignedCert(t)

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	defer ts.Close()

	bogusPin := strings.Repeat("a", 64)
	pem := pemEncode(parsed)

	client, err := buildTLSClient(&SubscriptionTLSConfig{
		PinnedSPKISHA256: bogusPin,
		CABundlePEM:      string(pem),
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	_, err = client.Get(ts.URL)
	if err == nil {
		t.Fatal("expected TLS handshake failure with mismatched pin")
	}
	if !strings.Contains(err.Error(), "SPKI pin mismatch") {
		t.Fatalf("expected SPKI mismatch error, got %v", err)
	}
}

func TestBuildTLSClient_CustomCA_Accepts(t *testing.T) {
	cert, parsed := generateSelfSignedCert(t)

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	defer ts.Close()

	pem := pemEncode(parsed)

	client, err := buildTLSClient(&SubscriptionTLSConfig{
		CABundlePEM: string(pem),
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("expected success with custom CA, got %v", err)
	}
	defer resp.Body.Close()
}

func pemEncode(cert *x509.Certificate) []byte {
	var buf bytes.Buffer
	_ = pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	return buf.Bytes()
}
