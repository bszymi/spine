package delivery

import (
	"encoding/json"
	"strings"
	"testing"
)

// INIT-024 TASK-014. Go 1.27 runs the v1 encoding/json API on the v2
// engine. The dispatcher's TLS decision (pin, CA bundle) rests on two
// properties of the engine, which this test checks against whichever
// engine the binary is built with, so a change in a later Go release
// fails here and not in production:
//
//  1. foldedKey treats a key as the tls block, or as one of its fields,
//     exactly when strings.EqualFold matches it, because that is the set
//     encoding/json matches a struct field against (Unicode simple
//     folding; '_' and '-' are significant).
//  2. A repeated key keeps the last value, as jsonb does.
func TestMetadataParseMatchesTheJSONEngine(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	for _, key := range []string{
		"pinned_spki_sha256",
		"PINNED_SPKI_SHA256",
		"Pinned_Spki_Sha256",
		"pinned_\u017fpki_sha256", // U+017F LATIN SMALL LETTER LONG S folds to s
		"pinned_spki_\u017fha256",
		"pinned-spki-sha256",       // a delimiter differs: no match
		"pinnedspkisha256",         // delimiters dropped: no match
		"pinned_spki_sha256 ",      // trailing space: no match
		"pinned_spki_sha25\uff16",  // FULLWIDTH DIGIT SIX: no match
		"pinned_spki_sha256\u0000", // NUL: no match
	} {
		want := strings.EqualFold(key, "pinned_spki_sha256")
		block, err := json.Marshal(map[string]string{key: pin})
		if err != nil {
			t.Fatal(err)
		}
		var cfg SubscriptionTLSConfig
		if err := json.Unmarshal(block, &cfg); err != nil {
			t.Fatalf("key %q: %v", key, err)
		}
		if engine := cfg.PinnedSPKISHA256 == pin; engine != want {
			t.Errorf("key %q: encoding/json matched=%v, strings.EqualFold=%v: foldedKey no longer describes the engine", key, engine, want)
		}
		md, err := parseSubscriptionMetadata([]byte(`{"tls":` + string(block) + `}`))
		if err != nil {
			t.Fatalf("key %q: parse: %v", key, err)
		}
		if got := md.TLS != nil && md.TLS.PinnedSPKISHA256 == pin; got != want {
			t.Errorf("key %q: the dispatcher pins=%v, want %v", key, got, want)
		}
	}

	// U+017F folds the block's own key too: one such key is the block, and
	// two spellings of it are ambiguous.
	md, err := parseSubscriptionMetadata([]byte(`{"tl\u017f":{"pinned_spki_sha256":"` + pin + `"}}`))
	if err != nil || md.TLS == nil || md.TLS.PinnedSPKISHA256 != pin {
		t.Errorf(`{"tl\u017f":{pin}}: md=%+v err=%v, want the pin`, md, err)
	}
	if _, err := parseSubscriptionMetadata([]byte(`{"tls":null,"tl\u017f":{"pinned_spki_sha256":"` + pin + `"}}`)); err == nil {
		t.Error(`{"tls":null,"tl\u017f":{pin}} parsed; want ErrInvalidSubscriptionMetadata (ambiguous key)`)
	}

	// A repeated key keeps the last value, at the top level and inside
	// the block, for the version key too.
	for _, tc := range []struct {
		doc, wantPin, wantVersion string
	}{
		{`{"tls":{"pinned_spki_sha256":"zz"},"tls":{"pinned_spki_sha256":"` + pin + `"}}`, pin, SignatureVersionV1},
		{`{"tls":{"pinned_spki_sha256":"` + pin + `"},"tls":null}`, "", SignatureVersionV1},
		{`{"tls":{"pinned_spki_sha256":"zz","pinned_spki_sha256":"` + pin + `"}}`, pin, SignatureVersionV1},
		{`{"signature_version":"v2","signature_version":"v1"}`, "", SignatureVersionV1},
		{`{"signature_version":"v1","signature_version":"v2"}`, "", SignatureVersionV2},
	} {
		md, err := parseSubscriptionMetadata([]byte(tc.doc))
		if err != nil {
			t.Errorf("%s: %v", tc.doc, err)
			continue
		}
		gotPin := ""
		if md.TLS != nil {
			gotPin = md.TLS.PinnedSPKISHA256
		}
		if gotPin != tc.wantPin || md.SignatureVersion != tc.wantVersion {
			t.Errorf("%s: pin %q version %q, want pin %q version %q", tc.doc, gotPin, md.SignatureVersion, tc.wantPin, tc.wantVersion)
		}
	}
}
