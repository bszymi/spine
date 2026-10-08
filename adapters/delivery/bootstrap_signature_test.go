package delivery

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/bszymi/spine/adapters/store"
)

// INIT-023 TASK-010 AC6: BootstrapConfig.SignatureVersion opts the
// internal subscription into a signature version. The existing
// bootstrap_test.go cases (SignatureVersion "") run unmodified.

func v2Config() BootstrapConfig {
	cfg := canonicalConfig()
	cfg.SignatureVersion = SignatureVersionV2
	return cfg
}

func TestBootstrapSignatureVersion_FreshRowCarriesVersion(t *testing.T) {
	st := &fakeSubscriptionStore{}
	if err := BootstrapInternalSubscription(context.Background(), st, v2Config()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if st.lastCreated == nil {
		t.Fatal("no row created")
	}
	if got := string(st.lastCreated.Metadata); got != `{"source":"bootstrap","signature_version":"v2"}` {
		t.Fatalf("metadata = %s", got)
	}

	// An unchanged config is a no-op: no UpdateSubscription.
	if err := BootstrapInternalSubscription(context.Background(), st, v2Config()); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if st.updateCalls != 0 || st.createCalls != 1 {
		t.Fatalf("second call: create=%d update=%d, want 1/0", st.createCalls, st.updateCalls)
	}
}

func TestBootstrapSignatureVersion_SwitchRewritesOnlyTheKey(t *testing.T) {
	existing := canonicalExisting()
	existing.Metadata = []byte(`{"source":"bootstrap","tls":{"pinned_spki_sha256":"ab"},"note":"keep me"}`)
	st := &fakeSubscriptionStore{subs: []store.EventSubscription{existing}}

	if err := BootstrapInternalSubscription(context.Background(), st, v2Config()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if st.updateCalls != 1 {
		t.Fatalf("updateCalls = %d, want 1 (v1 -> v2 is drift)", st.updateCalls)
	}
	var got map[string]any
	if err := json.Unmarshal(st.lastUpdated.Metadata, &got); err != nil {
		t.Fatalf("metadata not JSON: %v", err)
	}
	want := map[string]any{
		"source":            "bootstrap",
		"tls":               map[string]any{"pinned_spki_sha256": "ab"},
		"note":              "keep me",
		"signature_version": "v2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %s, want only the version key added", st.lastUpdated.Metadata)
	}
	// Every other field is as before.
	if st.lastUpdated.TargetURL != existing.TargetURL || st.lastUpdated.SigningSecret != existing.SigningSecret ||
		st.lastUpdated.SubscriptionID != existing.SubscriptionID {
		t.Fatalf("unexpected field change: %+v", st.lastUpdated)
	}

	// Switching back to v1 is drift too and rewrites only the key.
	if err := BootstrapInternalSubscription(context.Background(), st, canonicalConfig()); err != nil {
		t.Fatalf("empty version: %v", err)
	}
	if st.updateCalls != 1 {
		t.Fatalf("empty SignatureVersion touched the row (updateCalls=%d)", st.updateCalls)
	}
	cfgV1 := canonicalConfig()
	cfgV1.SignatureVersion = SignatureVersionV1
	if err := BootstrapInternalSubscription(context.Background(), st, cfgV1); err != nil {
		t.Fatalf("v1: %v", err)
	}
	if v, _ := SignatureVersionOf(st.lastUpdated.Metadata); st.updateCalls != 2 || v != SignatureVersionV1 {
		t.Fatalf("v2 -> v1: updateCalls=%d version=%q", st.updateCalls, v)
	}
}

// An empty SignatureVersion never reads or rewrites metadata — not even
// metadata the dispatcher would refuse.
func TestBootstrapSignatureVersion_EmptyLeavesMetadataAlone(t *testing.T) {
	existing := canonicalExisting()
	existing.Metadata = []byte(`{"signature_version":"v3"}`)
	existing.TargetURL = "https://stale.example.com/old" // force an update for another reason
	st := &fakeSubscriptionStore{subs: []store.EventSubscription{existing}}
	if err := BootstrapInternalSubscription(context.Background(), st, canonicalConfig()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if string(st.lastUpdated.Metadata) != `{"signature_version":"v3"}` {
		t.Fatalf("metadata rewritten without an opt-in: %s", st.lastUpdated.Metadata)
	}
}

// Design-check addition: unparseable metadata on the internal row is
// replaced, and bootstrap returns nil (an error would wedge SMP's
// blocking pre-dispatch ensure on a row that never self-heals).
func TestBootstrapSignatureVersion_HealsUnparseableMetadata(t *testing.T) {
	for _, bad := range []string{`{not json`, `[]`, `"v2"`} {
		existing := canonicalExisting()
		existing.Metadata = []byte(bad)
		st := &fakeSubscriptionStore{subs: []store.EventSubscription{existing}}
		if err := BootstrapInternalSubscription(context.Background(), st, v2Config()); err != nil {
			t.Fatalf("metadata %s: bootstrap returned %v, want nil", bad, err)
		}
		if st.updateCalls != 1 || string(st.lastUpdated.Metadata) != `{"source":"bootstrap","signature_version":"v2"}` {
			t.Fatalf("metadata %s: updates=%d metadata=%s", bad, st.updateCalls, st.lastUpdated.Metadata)
		}
	}
	// A bad version value inside a valid object is healed in place.
	existing := canonicalExisting()
	existing.Metadata = []byte(`{"source":"bootstrap","signature_version":"v9"}`)
	st := &fakeSubscriptionStore{subs: []store.EventSubscription{existing}}
	if err := BootstrapInternalSubscription(context.Background(), st, v2Config()); err != nil {
		t.Fatalf("bad version: %v", err)
	}
	if string(st.lastUpdated.Metadata) != `{"signature_version":"v2","source":"bootstrap"}` {
		t.Fatalf("bad version healed to %s", st.lastUpdated.Metadata)
	}
}

func TestBootstrapSignatureVersion_RejectsUnknownVersion(t *testing.T) {
	for _, v := range []string{"v3", "V2", " v2"} {
		cfg := canonicalConfig()
		cfg.SignatureVersion = v
		st := &fakeSubscriptionStore{}
		if err := BootstrapInternalSubscription(context.Background(), st, cfg); err == nil {
			t.Fatalf("SignatureVersion %q accepted", v)
		}
		if st.listCalls != 0 || st.createCalls != 0 {
			t.Fatalf("SignatureVersion %q: store touched before validation", v)
		}
	}
}
