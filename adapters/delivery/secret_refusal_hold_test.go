package delivery

// INIT-023 wave 2 integration: TASK-009 makes a signing secret that does
// not open (key not in the ring, wrong associated data, a legacy value in
// strict, an unknown envelope version, a foreign nested envelope) fail
// GetSubscription; TASK-010 decides what the dispatcher does with a
// subscription it cannot read. The combined contract, documented in the
// integration guide (§2.1 key rotation, §6.1 retry handling): such a
// refusal is an operator-side fault, so the delivery is retried and held
// — never dead-lettered at once, never sent unsigned — and it is
// delivered as soon as the row opens again (key restored, secret
// rotated). Only the 24 h hold cap dead-letters it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
)

func TestSecretEnvelopeRefusalIsHeldNotDeadLettered(t *testing.T) {
	for name, sentinel := range map[string]error{
		"key not in ring":             spinecrypto.ErrUnknownKeyID,
		"wrong AAD / tampered":        spinecrypto.ErrEnvelopeAuth,
		"unknown version":             spinecrypto.ErrUnsupportedEnvelope,
		"malformed envelope":          spinecrypto.ErrMalformedEnvelope,
		"legacy in strict":            store.ErrLegacySecretRefused,
		"foreign nested value":        store.ErrNestedEnvelope,
		"no cipher for a bound store": store.ErrSecretCipherRequired,
	} {
		t.Run(name, func(t *testing.T) {
			// The shape PostgresStore.GetSubscription returns under a
			// binding (openSubscriptionSecret, then GetSubscription).
			refusal := fmt.Errorf("decrypt signing_secret: %w",
				fmt.Errorf("subscription %q (enc:v2): %w", "sub-x", sentinel))
			srv, hits := statusServer(t, http.StatusOK)
			st := newQueueStore()
			st.add("d1", "sub-x", st.now())
			res := &mapResolver{
				subs:  map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL, SigningSecret: "whsec_x", SignatureVersion: SignatureVersionV2}},
				errs:  map[string]error{"sub-x": refusal},
				stick: map[string]bool{"sub-x": true},
			}
			d := NewWebhookDispatcher(st, res, DispatcherConfig{})
			sharedClock(d, st)

			for i := 0; i < 2*MaxRetriesDomain+3; i++ {
				d.poll(context.Background())
				r := st.row("d1")
				if r.Status == "dead" {
					t.Fatalf("poll %d: a refused secret dead-lettered the delivery: %+v", i, r)
				}
				if r.Status != "failed" || r.NextRetryAt == nil || !r.NextRetryAt.After(st.now()) || r.LastError != resolveUnavailableReason {
					t.Fatalf("poll %d: %+v, want failed with a future retry and %q", i, r, resolveUnavailableReason)
				}
				st.advance(time.Minute + time.Second)
			}
			if hits.Load() != 0 {
				t.Fatal("the endpoint was reached while the secret did not open")
			}

			// The operator fixes the row (restores the key, or
			// rotate-secret): the held delivery goes out, signed.
			res.mu.Lock()
			delete(res.errs, "sub-x")
			res.mu.Unlock()
			d.poll(context.Background())
			if r := st.row("d1"); r.Status != "delivered" || hits.Load() != 1 {
				t.Fatalf("after the fix: %+v, %d hits, want delivered once", r, hits.Load())
			}
			if errors.Is(refusal, ErrInvalidSubscriptionMetadata) {
				t.Fatal("an envelope refusal must not read as invalid metadata (permanent)")
			}
		})
	}
}
