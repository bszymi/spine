package gateway_test

// INIT-023 TASK-009 (codex review): rotate-secret must work on a row
// whose stored signing secret the store refuses to open, because it is
// the documented repair for exactly those rows.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
)

// replacerFakeStore refuses to open sub-1's stored secret, like a bound
// PostgresStore with a transplanted row, but can replace it.
type replacerFakeStore struct {
	*subFakeStore
	replaced map[string]string
}

func (f *replacerFakeStore) GetSubscription(_ context.Context, id string) (*store.EventSubscription, error) {
	return nil, domain.NewError(domain.ErrInternal, "decrypt signing_secret: refused")
}

func (f *replacerFakeStore) ReplaceSubscriptionSecret(_ context.Context, id, secret string) error {
	if _, ok := f.subs[id]; !ok {
		return domain.NewError(domain.ErrNotFound, "subscription not found")
	}
	f.replaced[id] = secret
	return nil
}

func TestSubscriptionRotateSecret_ReplacesRefusedSecretWithoutReadingIt(t *testing.T) {
	var rfs *replacerFakeStore
	ts, _, token := setupSubServerWithCfg(t, func(cfg *gateway.ServerConfig) {
		sfs := cfg.Store.(*subFakeStore)
		sfs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1"}
		rfs = &replacerFakeStore{subFakeStore: sfs, replaced: map[string]string{}}
		cfg.Store = rfs
	})

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/rotate-secret", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["subscription_id"] != "sub-1" || !strings.HasPrefix(body["signing_secret"], "whsec_") || rfs.replaced["sub-1"] != body["signing_secret"] {
		t.Fatalf("rotate response %v, stored %q", body, rfs.replaced["sub-1"])
	}

	missing := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/missing/rotate-secret", token, nil)
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing subscription: want 404, got %d", missing.StatusCode)
	}
}
