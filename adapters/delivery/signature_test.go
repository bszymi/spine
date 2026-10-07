package delivery

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestSignatureGoldenVectors pins both wire formats (INIT-023 TASK-010
// AC2). The values were computed independently of this package (Python
// hmac) in the design phase; v2 is SMP's receiver format byte for byte.
func TestSignatureGoldenVectors(t *testing.T) {
	body := []byte(`{"event_id":"evt-1"}`)
	if got, want := SignV2("whsec_test", "1700000000", "000102030405060708090a0b0c0d0e0f", body),
		"v2=7259d9ee00d6dfc7d6cd0be62fac13dada610df1455f5bf2b23866fa5949b288"; got != want {
		t.Fatalf("SignV2 golden = %s, want %s", got, want)
	}
	if got, want := SignV1("whsec_test", body),
		"sha256=b5fb135192a36cba13b173dac6aadcc588b98253b1f45e016139e5208503c61e"; got != want {
		t.Fatalf("SignV1 golden = %s, want %s", got, want)
	}
	if got := computeHMAC(body, "whsec_test"); "sha256="+got != SignV1("whsec_test", body) {
		t.Fatalf("computeHMAC drifted from SignV1: %s", got)
	}
}

func TestSignatureVersionOf(t *testing.T) {
	cases := []struct {
		name    string
		meta    string
		want    string
		wantErr bool
	}{
		{name: "nil", meta: "", want: SignatureVersionV1},
		{name: "whitespace", meta: "  \n", want: SignatureVersionV1},
		{name: "json null", meta: "null", want: SignatureVersionV1},
		{name: "empty object", meta: `{}`, want: SignatureVersionV1},
		{name: "bootstrap source only", meta: `{"source":"bootstrap"}`, want: SignatureVersionV1},
		{name: "explicit empty", meta: `{"signature_version":""}`, want: SignatureVersionV1},
		{name: "explicit null", meta: `{"signature_version":null}`, want: SignatureVersionV1},
		{name: "explicit v1", meta: `{"signature_version":"v1"}`, want: SignatureVersionV1},
		{name: "v2", meta: `{"source":"bootstrap","signature_version":"v2"}`, want: SignatureVersionV2},
		{name: "unknown v3", meta: `{"signature_version":"v3"}`, wantErr: true},
		{name: "upper-case V2", meta: `{"signature_version":"V2"}`, wantErr: true},
		{name: "non-string", meta: `{"signature_version":2}`, wantErr: true},
		{name: "object value", meta: `{"signature_version":{"v":"v2"}}`, wantErr: true},
		{name: "case-variant key", meta: `{"Signature_Version":"v2"}`, wantErr: true},
		{name: "array", meta: `[]`, wantErr: true},
		{name: "string", meta: `"v2"`, wantErr: true},
		{name: "not json", meta: `{not json`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SignatureVersionOf([]byte(tc.meta))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SignatureVersionOf(%s) = %q, want error", tc.meta, got)
				}
				if !errors.Is(err, ErrInvalidSubscriptionMetadata) {
					t.Fatalf("error %v does not wrap ErrInvalidSubscriptionMetadata", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("SignatureVersionOf(%s) = %q, %v; want %q", tc.meta, got, err, tc.want)
			}
		})
	}
}

func TestValidateSubscriptionMetadata(t *testing.T) {
	_, cert := generateSelfSignedCert(t)
	pemBundle := string(pemEncode(cert))
	valid := []string{
		"", `{}`, `{"source":"bootstrap"}`, `{"signature_version":"v2"}`,
		`{"tls":{"pinned_spki_sha256":"abcd1234"}}`,
		`{"signature_version":"v1","tls":{"pinned_spki_sha256":"ab"},"note":"x"}`,
	}
	ca, _ := json.Marshal(map[string]any{"tls": map[string]string{"ca_bundle_pem": pemBundle}})
	valid = append(valid, string(ca))
	for _, m := range valid {
		if err := ValidateSubscriptionMetadata([]byte(m)); err != nil {
			t.Errorf("ValidateSubscriptionMetadata(%s) = %v, want nil", m, err)
		}
	}
	invalid := []string{
		`{"signature_version":"v3"}`,
		`{"signature_version":true}`,
		`{"tls":{"pinned_spki_sha256":"zz"}}`,
		`{"tls":{"ca_bundle_pem":"not a pem"}}`,
		`{"tls":"x"}`,
		`[1]`,
		`{`,
	}
	for _, m := range invalid {
		err := ValidateSubscriptionMetadata([]byte(m))
		if err == nil || !errors.Is(err, ErrInvalidSubscriptionMetadata) {
			t.Errorf("ValidateSubscriptionMetadata(%s) = %v, want ErrInvalidSubscriptionMetadata", m, err)
		}
	}
}

// TestWithSignatureVersion covers AC5's helper contract: the key is set,
// every other key survives untouched.
func TestWithSignatureVersion(t *testing.T) {
	out, err := WithSignatureVersion([]byte(`{"source":"bootstrap","tls":{"pinned_spki_sha256":"ab"}}`), SignatureVersionV2)
	if err != nil {
		t.Fatalf("WithSignatureVersion: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("result is not JSON: %v (%s)", err, out)
	}
	want := map[string]any{
		"source":            "bootstrap",
		"signature_version": "v2",
		"tls":               map[string]any{"pinned_spki_sha256": "ab"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithSignatureVersion = %s, want %v", out, want)
	}
	if v, err := SignatureVersionOf(out); err != nil || v != SignatureVersionV2 {
		t.Fatalf("SignatureVersionOf(result) = %q, %v", v, err)
	}

	// Switching back keeps everything else, overwrites the version.
	back, err := WithSignatureVersion(out, SignatureVersionV1)
	if err != nil {
		t.Fatalf("WithSignatureVersion back: %v", err)
	}
	if v, _ := SignatureVersionOf(back); v != SignatureVersionV1 || !bytes.Contains(back, []byte(`"pinned_spki_sha256":"ab"`)) {
		t.Fatalf("switch back = %s", back)
	}

	// Empty / null metadata become an object with only the key.
	for _, in := range []string{"", "null", "{}"} {
		got, err := WithSignatureVersion([]byte(in), SignatureVersionV2)
		if err != nil || string(got) != `{"signature_version":"v2"}` {
			t.Errorf("WithSignatureVersion(%q) = %s, %v", in, got, err)
		}
	}

	// A case-variant key (which SignatureVersionOf refuses) is replaced,
	// so the result parses.
	healed, err := WithSignatureVersion([]byte(`{"SIGNATURE_VERSION":"v9","source":"x"}`), SignatureVersionV2)
	if err != nil {
		t.Fatalf("heal case variant: %v", err)
	}
	if v, err := SignatureVersionOf(healed); err != nil || v != SignatureVersionV2 || bytes.Contains(healed, []byte("SIGNATURE_VERSION")) {
		t.Fatalf("healed = %s (%q, %v)", healed, v, err)
	}

	// Refusals: unknown version, non-object metadata.
	for _, v := range []string{"", "v3", "V2"} {
		if _, err := WithSignatureVersion([]byte(`{}`), v); err == nil {
			t.Errorf("WithSignatureVersion(version %q) = nil error", v)
		}
	}
	for _, m := range []string{`[]`, `"x"`, `{bad`} {
		if _, err := WithSignatureVersion([]byte(m), SignatureVersionV2); err == nil {
			t.Errorf("WithSignatureVersion(%s) = nil error, want refusal of non-object metadata", m)
		}
	}
}

// TestParseSubscriptionMetadata_TLSUnchanged: a tls block given under one
// key keeps its pre-TASK-010 meaning, including encoding/json's
// case-insensitive match of the key and its fields, so no stored row
// changes meaning (two spellings of one key are refused; see
// TestTLSBlockDecodingIsOrderIndependent).
func TestParseSubscriptionMetadata_TLSUnchanged(t *testing.T) {
	md, err := parseSubscriptionMetadata([]byte(`{"TLS":{"Pinned_SPKI_SHA256":"abcd"},"signature_version":"v2"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if md.TLS == nil || md.TLS.PinnedSPKISHA256 != "abcd" || md.SignatureVersion != SignatureVersionV2 {
		t.Fatalf("parse = %+v", md)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestSignRequest(t *testing.T) {
	body := []byte(`{"event_id":"evt-1"}`)
	now := time.Unix(1700000000, 0)
	nonceSrc := func() io.Reader { return bytes.NewReader([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}) }

	t.Run("v1 with secret", func(t *testing.T) {
		h := http.Header{}
		if err := signRequest(h, "", "whsec_test", body, now, nonceSrc()); err != nil {
			t.Fatal(err)
		}
		if h.Get(HeaderSignature) != SignV1("whsec_test", body) || h.Get(HeaderTimestamp) != "" || h.Get(HeaderNonce) != "" {
			t.Fatalf("v1 headers = %v", h)
		}
	})
	t.Run("v1 without secret is unsigned", func(t *testing.T) {
		h := http.Header{HeaderSignature: {"stale"}}
		if err := signRequest(h, SignatureVersionV1, "", body, now, nonceSrc()); err != nil {
			t.Fatal(err)
		}
		if len(h) != 0 {
			t.Fatalf("unsigned v1 carries headers: %v", h)
		}
	})
	t.Run("v2", func(t *testing.T) {
		h := http.Header{}
		if err := signRequest(h, SignatureVersionV2, "whsec_test", body, now, nonceSrc()); err != nil {
			t.Fatal(err)
		}
		if h.Get(HeaderTimestamp) != "1700000000" || h.Get(HeaderNonce) != "000102030405060708090a0b0c0d0e0f" {
			t.Fatalf("v2 ts/nonce = %v", h)
		}
		if got := h.Get(HeaderSignature); got != "v2=7259d9ee00d6dfc7d6cd0be62fac13dada610df1455f5bf2b23866fa5949b288" {
			t.Fatalf("v2 signature = %s (golden mismatch)", got)
		}
	})
	t.Run("v2 without secret fails closed", func(t *testing.T) {
		h := http.Header{}
		err := signRequest(h, SignatureVersionV2, "", body, now, nonceSrc())
		if !errors.Is(err, errV2WithoutSecret) || len(h) != 0 {
			t.Fatalf("err=%v headers=%v", err, h)
		}
	})
	t.Run("unknown version fails closed", func(t *testing.T) {
		h := http.Header{}
		err := signRequest(h, "v3", "whsec_test", body, now, nonceSrc())
		if !errors.Is(err, ErrInvalidSubscriptionMetadata) || len(h) != 0 {
			t.Fatalf("err=%v headers=%v", err, h)
		}
	})
	t.Run("entropy failure is transient and sends nothing", func(t *testing.T) {
		h := http.Header{}
		err := signRequest(h, SignatureVersionV2, "whsec_test", body, now, errReader{err: errors.New("no entropy")})
		if err == nil || errors.Is(err, errV2WithoutSecret) || errors.Is(err, ErrInvalidSubscriptionMetadata) || len(h) != 0 {
			t.Fatalf("err=%v headers=%v", err, h)
		}
	})
	t.Run("short entropy read is an error, never a shorter nonce", func(t *testing.T) {
		h := http.Header{}
		err := signRequest(h, SignatureVersionV2, "whsec_test", body, now, strings.NewReader("short"))
		if err == nil || len(h) != 0 {
			t.Fatalf("err=%v headers=%v", err, h)
		}
	})
}

func TestInvalidVersionMessageIsBounded(t *testing.T) {
	long := strings.Repeat("x", 10000)
	_, err := SignatureVersionOf([]byte(`{"signature_version":"` + long + `"}`))
	if err == nil || len(err.Error()) > 200 {
		t.Fatalf("error length %d: %v", len(err.Error()), err)
	}
	_, err = SignatureVersionOf([]byte(`{"` + strings.ToUpper("signature_version") + long + `":"v2"}`))
	if err != nil {
		t.Fatalf("an unrelated long key must not matter: %v", err)
	}
}

// TestTLSBlockDecodingIsOrderIndependent (review round 1): the gateway
// validates the client's key order, the dispatcher reads Postgres jsonb's
// (keys re-ordered: "TLS" before "tls"). With the old whole-document
// struct decode, {"tls":null,"TLS":{pin}} validated with a pin and was
// dispatched without one. Two keys that fold to the same name are now
// refused in either order; a single key in any letter case keeps its
// meaning (rows stored as "TLS" still work).
func TestTLSBlockDecodingIsOrderIndependent(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	refused := [][2]string{
		// {client order, jsonb order}
		{`{"tls":null,"TLS":{"pinned_spki_sha256":"` + pin + `"}}`, `{"TLS":{"pinned_spki_sha256":"` + pin + `"},"tls":null}`},
		{`{"tls":{"pinned_spki_sha256":"zz"},"TLS":null}`, `{"TLS":null,"tls":{"pinned_spki_sha256":"zz"}}`},
		{`{"tls":{"pinned_spki_sha256":"` + pin + `"},"Tls":{}}`, `{"Tls":{},"tls":{"pinned_spki_sha256":"` + pin + `"}}`},
		{`{"tls":{"pinned_spki_sha256":"` + pin + `","PINNED_SPKI_SHA256":"cd"}}`, `{"tls":{"PINNED_SPKI_SHA256":"cd","pinned_spki_sha256":"` + pin + `"}}`},
		{`{"tls":{"ca_bundle_pem":"x","CA_Bundle_PEM":"y"}}`, `{"tls":{"CA_Bundle_PEM":"y","ca_bundle_pem":"x"}}`},
	}
	for _, pair := range refused {
		for _, doc := range pair {
			if _, err := parseSubscriptionMetadata([]byte(doc)); !errors.Is(err, ErrInvalidSubscriptionMetadata) {
				t.Errorf("parse %s = %v, want ErrInvalidSubscriptionMetadata (ambiguous key)", doc, err)
			}
			if err := ValidateSubscriptionMetadata([]byte(doc)); !errors.Is(err, ErrInvalidSubscriptionMetadata) {
				t.Errorf("validate %s = %v, want ErrInvalidSubscriptionMetadata", doc, err)
			}
		}
	}
	for _, tc := range []struct {
		doc     string
		wantPin string
	}{
		{`{"TLS":{"pinned_spki_sha256":"` + pin + `"}}`, pin},
		{`{"tls":{"Pinned_SPKI_SHA256":"` + pin + `"}}`, pin},
		{`{"source":"api","tls":{"pinned_spki_sha256":"` + pin + `"},"TLSX":{}}`, pin},
		{`{"tls":null}`, ""},
		{`{"tls":{}}`, ""},
	} {
		md, err := parseSubscriptionMetadata([]byte(tc.doc))
		if err != nil {
			t.Errorf("parse %s: %v", tc.doc, err)
			continue
		}
		got := ""
		if md.TLS != nil {
			got = md.TLS.PinnedSPKISHA256
		}
		if got != tc.wantPin {
			t.Errorf("parse %s: pin %q, want %q", tc.doc, got, tc.wantPin)
		}
	}
	for _, doc := range []string{`{"tls":"x"}`, `{"tls":[1]}`, `{"tls":{"pinned_spki_sha256":5}}`} {
		if _, err := parseSubscriptionMetadata([]byte(doc)); !errors.Is(err, ErrInvalidSubscriptionMetadata) {
			t.Errorf("parse %s = %v, want ErrInvalidSubscriptionMetadata", doc, err)
		}
	}
}
