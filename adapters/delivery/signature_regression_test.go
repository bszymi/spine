package delivery

// INIT-023 TASK-010 regression tests for audit P3-11 (a captured delivery
// replays forever), ported from the audit's dispatcher replay proof of
// concept with its assertion inverted. The opt-in goes through the real
// StoreSubscriptionResolver and the subscription's metadata, exactly as a
// deployment would configure it.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
)

// oneSubStore is a store.SubscriptionStore that serves a single row;
// only GetSubscription is used by StoreSubscriptionResolver.
type oneSubStore struct {
	store.SubscriptionStore
	sub *store.EventSubscription
}

func (s oneSubStore) GetSubscription(context.Context, string) (*store.EventSubscription, error) {
	c := *s.sub
	return &c, nil
}

// captureServer records every request's headers and body.
func captureServer(t *testing.T) (*httptest.Server, func() ([]http.Header, [][]byte)) {
	t.Helper()
	var mu sync.Mutex
	var hs []http.Header
	var bs [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hs = append(hs, r.Header.Clone())
		bs = append(bs, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() ([]http.Header, [][]byte) {
		mu.Lock()
		defer mu.Unlock()
		return append([]http.Header(nil), hs...), append([][]byte(nil), bs...)
	}
}

// smpV2MAC is an independent oracle: SMP's verifier
// (internal/customer/runner/event_receiver.go hmacMatchesAny, v2 branch)
// re-implemented here, deliberately not through SignV2.
func smpV2MAC(key, ts, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(ts))
	mac.Write([]byte{'.'})
	mac.Write([]byte(nonce))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func metadataDispatcher(srv *httptest.Server, metadata, secret string) (*WebhookDispatcher, *queueStore) {
	res := NewStoreSubscriptionResolver(oneSubStore{sub: &store.EventSubscription{
		SubscriptionID: "sub-1", TargetURL: srv.URL, SigningSecret: secret, Metadata: []byte(metadata),
	}})
	st := newQueueStore()
	return NewWebhookDispatcher(st, res, DispatcherConfig{}), st
}

func deliverOnce(d *WebhookDispatcher, st *queueStore, id string) {
	st.add(id, "sub-1", time.Now())
	entries, _ := st.ClaimDeliveries(context.Background(), 1)
	for i := range entries {
		entries[i].EventID = "evt-1"
		entries[i].Payload = []byte(`{"event_id":"evt-1","type":"step_assigned"}`)
		d.deliver(context.Background(), entries[i])
	}
}

// TestV2OptInSignsFreshTimestampAndNonce (AC1): two attempts of the same
// event, opted into v2 through metadata, each carry a send-time
// timestamp, a fresh 16-byte nonce and a MAC that SMP's v2 rule accepts.
// Dropping the nonce from the MAC, a constant nonce or a stale timestamp
// each fail it.
func TestV2OptInSignsFreshTimestampAndNonce(t *testing.T) {
	srv, captured := captureServer(t)
	const secret = "whsec_0123456789abcdef"
	d, st := metadataDispatcher(srv, `{"source":"bootstrap","signature_version":"v2"}`, secret)
	for _, id := range []string{"del-A", "del-B"} {
		deliverOnce(d, st, id)
	}
	hs, bs := captured()
	if len(hs) != 2 {
		t.Fatalf("got %d deliveries, want 2", len(hs))
	}
	var nonces, sigs []string
	for i, h := range hs {
		ts, nonce, sig := h.Get(HeaderTimestamp), h.Get(HeaderNonce), h.Get(HeaderSignature)
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || ts != strconv.FormatInt(sec, 10) {
			t.Fatalf("X-Webhook-Timestamp %q is not canonical decimal Unix seconds", ts)
		}
		if skew := time.Since(time.Unix(sec, 0)); skew < -5*time.Second || skew > 5*time.Second {
			t.Fatalf("timestamp %d is not send time (skew %s)", sec, skew)
		}
		raw, err := hex.DecodeString(nonce)
		if err != nil || len(raw) < 16 {
			t.Fatalf("X-Webhook-Nonce %q: want hex of >= 16 bytes", nonce)
		}
		if !strings.HasPrefix(sig, "v2=") {
			t.Fatalf("X-Spine-Signature %q lacks the v2= prefix", sig)
		}
		if want := "v2=" + smpV2MAC(secret, ts, nonce, bs[i]); sig != want {
			t.Fatalf("signature does not verify under SMP's v2 rule")
		}
		nonces, sigs = append(nonces, nonce), append(sigs, sig)
	}
	if nonces[0] == nonces[1] || sigs[0] == sigs[1] {
		t.Fatalf("nonce/signature reused across attempts")
	}
	for _, id := range []string{"del-A", "del-B"} {
		if r := st.row(id); r.Status != "delivered" {
			t.Fatalf("row %s status %q, want delivered", id, r.Status)
		}
	}
}

// TestV1RemainsDefault (AC3, guard): without an explicit v2 opt-in the
// wire format is exactly today's — body-only sha256= MAC, no X-Webhook-*.
func TestV1RemainsDefault(t *testing.T) {
	for _, md := range []string{``, `{}`, `{"source":"bootstrap"}`, `{"signature_version":"v1"}`} {
		srv, captured := captureServer(t)
		d, st := metadataDispatcher(srv, md, "s3cret")
		deliverOnce(d, st, "d")
		hs, bs := captured()
		if len(hs) != 1 {
			t.Fatalf("metadata %q: %d deliveries", md, len(hs))
		}
		mac := hmac.New(sha256.New, []byte("s3cret"))
		mac.Write(bs[0])
		if got := hs[0].Get(HeaderSignature); got != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Fatalf("metadata %q: v1 signature changed: %q", md, got)
		}
		if hs[0].Get(HeaderTimestamp) != "" || hs[0].Get(HeaderNonce) != "" {
			t.Fatalf("metadata %q: v1 delivery carries v2 headers", md)
		}
	}
}

// TestUnknownSignatureVersionFailsClosed (AC4): an unknown or non-string
// version is a permanent configuration error — nothing is sent and the
// row is dead-lettered with an explanatory reason.
func TestUnknownSignatureVersionFailsClosed(t *testing.T) {
	for _, md := range []string{`{"signature_version":"v3"}`, `{"signature_version":2}`} {
		srv, captured := captureServer(t)
		d, st := metadataDispatcher(srv, md, "s3cret")
		deliverOnce(d, st, "d")
		if hs, _ := captured(); len(hs) != 0 {
			t.Fatalf("metadata %s: delivered with an invalid signature_version", md)
		}
		r := st.row("d")
		if r.Status != "dead" || !strings.Contains(r.LastError, "signature_version") {
			t.Fatalf("metadata %s: status %q last_error %q, want dead + signature_version reason", md, r.Status, r.LastError)
		}
		if logs := st.logsFor("d"); len(logs) != 1 || logs[0].Error != r.LastError {
			t.Fatalf("metadata %s: dead row not explained in the delivery log: %+v", md, logs)
		}
	}
}

// TestV2WithoutSecretFailsClosed (AC4): v2 with an empty secret is never
// sent unsigned (or v1-signed); the row is dead.
func TestV2WithoutSecretFailsClosed(t *testing.T) {
	srv, captured := captureServer(t)
	d, st := metadataDispatcher(srv, `{"signature_version":"v2"}`, "")
	deliverOnce(d, st, "d")
	if hs, _ := captured(); len(hs) != 0 {
		t.Fatalf("v2 without a secret was sent")
	}
	if r := st.row("d"); r.Status != "dead" || r.LastError != errV2WithoutSecret.Error() {
		t.Fatalf("status %q last_error %q, want dead / %q", r.Status, r.LastError, errV2WithoutSecret)
	}
}

// TestV2EntropyFailureRetries: a nonce read failure is transient — the
// row is scheduled for a retry, nothing is sent.
func TestV2EntropyFailureRetries(t *testing.T) {
	srv, captured := captureServer(t)
	d, st := metadataDispatcher(srv, `{"signature_version":"v2"}`, "s3cret")
	d.rand = errReader{err: io.ErrUnexpectedEOF}
	deliverOnce(d, st, "d")
	if hs, _ := captured(); len(hs) != 0 {
		t.Fatalf("delivery sent without a nonce")
	}
	r := st.row("d")
	if r.Status != "failed" || r.NextRetryAt == nil || !r.NextRetryAt.After(time.Now()) {
		t.Fatalf("row = %+v, want failed with a future retry", r)
	}
}
