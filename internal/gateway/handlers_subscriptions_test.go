package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/store"
)

// subFakeStore extends fakeStore with subscription / delivery / event-log
// methods used by the subscription, delivery, and pull-event handlers.
type subFakeStore struct {
	*fakeStore

	createSubErr      error
	updateSubErr      error
	deleteSubErr      error
	listSubsErr       error
	listDeliveriesErr error
	getDeliveryErr    error
	historyErr        error
	updateStatusErr   error
	statsErr          error
	listEventsErr     error

	subs       map[string]*store.EventSubscription
	deliveries map[string]*store.DeliveryEntry
	logs       []store.DeliveryLogEntry
	events     []store.EventLogEntry
	stats      *store.DeliveryStats
}

func newSubFakeStore() *subFakeStore {
	return &subFakeStore{
		fakeStore:  newFakeStore(),
		subs:       map[string]*store.EventSubscription{},
		deliveries: map[string]*store.DeliveryEntry{},
	}
}

func (f *subFakeStore) CreateSubscription(_ context.Context, sub *store.EventSubscription) error {
	if f.createSubErr != nil {
		return f.createSubErr
	}
	f.subs[sub.SubscriptionID] = sub
	return nil
}

func (f *subFakeStore) GetSubscription(_ context.Context, id string) (*store.EventSubscription, error) {
	sub, ok := f.subs[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "subscription not found")
	}
	return sub, nil
}

func (f *subFakeStore) UpdateSubscription(_ context.Context, sub *store.EventSubscription) error {
	if f.updateSubErr != nil {
		return f.updateSubErr
	}
	f.subs[sub.SubscriptionID] = sub
	return nil
}

func (f *subFakeStore) DeleteSubscription(_ context.Context, id string) error {
	if f.deleteSubErr != nil {
		return f.deleteSubErr
	}
	delete(f.subs, id)
	return nil
}

func (f *subFakeStore) ListSubscriptions(_ context.Context, _ string) ([]store.EventSubscription, error) {
	if f.listSubsErr != nil {
		return nil, f.listSubsErr
	}
	out := make([]store.EventSubscription, 0, len(f.subs))
	for _, s := range f.subs {
		out = append(out, *s)
	}
	return out, nil
}

func (f *subFakeStore) ListDeliveries(_ context.Context, _ string, _ string, _ int) ([]store.DeliveryEntry, error) {
	if f.listDeliveriesErr != nil {
		return nil, f.listDeliveriesErr
	}
	out := make([]store.DeliveryEntry, 0, len(f.deliveries))
	for _, d := range f.deliveries {
		out = append(out, *d)
	}
	return out, nil
}

func (f *subFakeStore) GetDelivery(_ context.Context, id string) (*store.DeliveryEntry, error) {
	if f.getDeliveryErr != nil {
		return nil, f.getDeliveryErr
	}
	d, ok := f.deliveries[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "delivery not found")
	}
	return d, nil
}

func (f *subFakeStore) ListDeliveryHistory(_ context.Context, _ store.DeliveryHistoryQuery) ([]store.DeliveryLogEntry, error) {
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	return f.logs, nil
}

func (f *subFakeStore) UpdateDeliveryStatus(_ context.Context, id, status, _ string, _ *time.Time) error {
	if f.updateStatusErr != nil {
		return f.updateStatusErr
	}
	if d, ok := f.deliveries[id]; ok {
		d.Status = status
	}
	return nil
}

func (f *subFakeStore) GetDeliveryStats(_ context.Context, _ string) (*store.DeliveryStats, error) {
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	if f.stats != nil {
		return f.stats, nil
	}
	return &store.DeliveryStats{}, nil
}

func (f *subFakeStore) ListEventsAfter(_ context.Context, _ string, _ []string, _ int) ([]store.EventLogEntry, error) {
	if f.listEventsErr != nil {
		return nil, f.listEventsErr
	}
	return f.events, nil
}

func setupSubServer(t *testing.T) (*httptest.Server, *subFakeStore, string) {
	t.Helper()
	return setupSubServerWithCfg(t, func(cfg *gateway.ServerConfig) {})
}

// setupSubServerWithCfg is setupSubServer plus an injection hook so
// tests can swap in a non-default webhook target validator or other
// server-config fields without copying the boilerplate.
func setupSubServerWithCfg(t *testing.T, mutate func(cfg *gateway.ServerConfig)) (*httptest.Server, *subFakeStore, string) {
	t.Helper()
	sfs := newSubFakeStore()
	sfs.fakeStore.actors["admin-1"] = &domain.Actor{
		ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin",
		Role: domain.RoleAdmin, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(sfs)
	plaintext, _, err := authSvc.CreateToken(context.Background(), "admin-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	cfg := gateway.ServerConfig{Store: sfs, Auth: authSvc}
	if mutate != nil {
		mutate(&cfg)
	}
	srv := gateway.NewServer(":0", cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, sfs, plaintext
}

func doJSON(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req, err := http.NewRequest(method, url, buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

// setupSubServerReader returns a server using a RoleReader actor, which lacks
// subscription.create / update / delete permissions. Handy for covering the
// authorize() failure branches without spinning up a second harness.
func setupSubServerReader(t *testing.T) (*httptest.Server, *subFakeStore, string) {
	t.Helper()
	sfs := newSubFakeStore()
	sfs.fakeStore.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(sfs)
	plaintext, _, err := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: sfs, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, sfs, plaintext
}

// TestSubscriptionHandlers_ReaderForbidden drives every admin-gated
// subscription endpoint with a reader-role token and asserts 403. This
// covers the short-circuit return in each handler's authorize() check.
func TestSubscriptionHandlers_ReaderForbidden(t *testing.T) {
	ts, _, token := setupSubServerReader(t)
	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/subscriptions", map[string]any{"name": "x", "target_url": "http://x"}},
		{http.MethodPatch, "/api/v1/subscriptions/sub-1", map[string]any{"name": "y"}},
		{http.MethodDelete, "/api/v1/subscriptions/sub-1", nil},
		{http.MethodPost, "/api/v1/subscriptions/sub-1/activate", nil},
		{http.MethodPost, "/api/v1/subscriptions/sub-1/pause", nil},
		{http.MethodPost, "/api/v1/subscriptions/sub-1/rotate-secret", nil},
		{http.MethodPost, "/api/v1/subscriptions/sub-1/test", nil},
		{http.MethodPost, "/api/v1/subscriptions/sub-1/deliveries/d-1/replay", nil},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp := doJSON(t, tc.method, ts.URL+tc.path, token, tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("want 403, got %d", resp.StatusCode)
			}
		})
	}
}

func TestSubscriptionCreate_HappyPath(t *testing.T) {
	ts, fs, token := setupSubServer(t)

	body := map[string]any{
		"name":        "ci-hooks",
		"target_url":  "https://example.test/hook",
		"event_types": []string{"run.completed"},
	}
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions", token, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["signing_secret"] == "" {
		t.Errorf("expected signing_secret in response")
	}
	if len(fs.subs) != 1 {
		t.Errorf("expected 1 subscription persisted, got %d", len(fs.subs))
	}
}

func TestSubscriptionCreate_MissingFields(t *testing.T) {
	ts, _, token := setupSubServer(t)

	cases := map[string]map[string]any{
		"missing name":       {"target_url": "https://example.test/hook"},
		"missing target_url": {"name": "x"},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions", token, body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("want 400, got %d", resp.StatusCode)
			}
		})
	}
}

// TestSubscriptionCreate_SSRFRejections covers each acceptance criterion
// URL from TASK-026. With a validator configured (no allowlist), the
// create handler must refuse every SSRF-flavoured URL with 400
// invalid_params before touching the store.
func TestSubscriptionCreate_SSRFRejections(t *testing.T) {
	ts, fs, token := setupSubServerWithCfg(t, func(cfg *gateway.ServerConfig) {
		cfg.WebhookTargets = delivery.NewTargetValidator(nil)
	})

	cases := map[string]string{
		"AWS IMDS (link-local)":        "http://169.254.169.254/latest/meta-data/",
		"AWS IMDS over https literal":  "https://169.254.169.254/",
		"loopback IPv4":                "http://127.0.0.1/hook",
		"loopback hostname":            "http://localhost/hook",
		"https loopback IPv4 literal":  "https://127.0.0.1/hook",
		"https RFC1918 literal":        "https://10.0.0.5/hook",
		"file scheme":                  "file:///etc/passwd",
		"userinfo in https":            "https://user:pass@example.com/hook",
		"empty":                        "",
		"no scheme":                    "example.com/hook",
		"gopher scheme":                "gopher://example.com/",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions", token, map[string]any{
				"name":       "ssrf-test",
				"target_url": target,
			})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("target %q: want 400, got %d", target, resp.StatusCode)
			}
			var body struct {
				Errors []struct {
					Code string `json:"code"`
				} `json:"errors"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(body.Errors) == 0 || body.Errors[0].Code != string(domain.ErrInvalidParams) {
				t.Errorf("target %q: expected invalid_params code, got %+v", target, body.Errors)
			}
			if len(fs.subs) != 0 {
				t.Errorf("target %q: validator must reject before persistence; got %d rows", target, len(fs.subs))
			}
		})
	}
}

// TestSubscriptionUpdate_SSRFRejection mirrors the create test — an
// update that swaps in an unsafe URL must be refused even though the
// existing row is otherwise safe.
func TestSubscriptionUpdate_SSRFRejection(t *testing.T) {
	ts, fs, token := setupSubServerWithCfg(t, func(cfg *gateway.ServerConfig) {
		cfg.WebhookTargets = delivery.NewTargetValidator(nil)
	})
	fs.subs["sub-1"] = &store.EventSubscription{
		SubscriptionID: "sub-1",
		Name:           "ci",
		TargetURL:      "https://public.example.com/hook",
		Status:         "active",
	}

	unsafe := "http://169.254.169.254/"
	resp := doJSON(t, http.MethodPatch, ts.URL+"/api/v1/subscriptions/sub-1", token, map[string]any{
		"target_url": unsafe,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	if fs.subs["sub-1"].TargetURL != "https://public.example.com/hook" {
		t.Errorf("persisted target_url must be unchanged after a rejected update, got %q", fs.subs["sub-1"].TargetURL)
	}
}

// TestSubscriptionTest_RejectsPersistedUnsafeURL proves that a
// subscription row created before this task landed (with a
// dangerous target_url) cannot be probed via the test endpoint —
// the dispatcher and test-endpoint re-validate on every use.
func TestSubscriptionTest_RejectsPersistedUnsafeURL(t *testing.T) {
	ts, fs, token := setupSubServerWithCfg(t, func(cfg *gateway.ServerConfig) {
		cfg.WebhookTargets = delivery.NewTargetValidator(nil)
	})
	fs.subs["legacy-sub"] = &store.EventSubscription{
		SubscriptionID: "legacy-sub",
		Name:           "legacy",
		TargetURL:      "http://169.254.169.254/latest/meta-data/",
		Status:         "active",
	}

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/legacy-sub/test", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for persisted unsafe URL, got %d", resp.StatusCode)
	}
}

func TestSubscriptionCreate_Allowlist_AcceptsLocalhost(t *testing.T) {
	ts, fs, token := setupSubServerWithCfg(t, func(cfg *gateway.ServerConfig) {
		cfg.WebhookTargets = delivery.NewTargetValidator([]string{"localhost"})
	})

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions", token, map[string]any{
		"name":       "dev-hook",
		"target_url": "http://localhost:8080/hook",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("allowlisted localhost http should succeed, got %d", resp.StatusCode)
	}
	if len(fs.subs) != 1 {
		t.Errorf("expected 1 persisted subscription, got %d", len(fs.subs))
	}
}

func TestSubscriptionCreate_StoreError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.createSubErr = domain.NewError(domain.ErrInternal, "boom")

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions", token, map[string]any{
		"name": "x", "target_url": "https://example.test/hook",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestSubscriptionList(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", Name: "a", Status: "active"}
	fs.subs["sub-2"] = &store.EventSubscription{SubscriptionID: "sub-2", Name: "b", Status: "paused"}

	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if len(got.Items) != 2 {
		t.Errorf("want 2 items, got %d", len(got.Items))
	}
}

func TestSubscriptionGet(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", Name: "a", Status: "active"}

	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/sub-1", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	resp404 := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/missing", token, nil)
	defer resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp404.StatusCode)
	}
}

func TestSubscriptionUpdate(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", Name: "old", TargetURL: "http://old", Status: "active"}

	newName := "new-name"
	newURL := "https://new.test"
	body := map[string]any{
		"name":        newName,
		"target_url":  newURL,
		"event_types": []string{"x"},
		"metadata":    []byte(`{"k":"v"}`),
	}
	resp := doJSON(t, http.MethodPatch, ts.URL+"/api/v1/subscriptions/sub-1", token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if fs.subs["sub-1"].Name != newName || fs.subs["sub-1"].TargetURL != newURL {
		t.Errorf("update not persisted: %+v", fs.subs["sub-1"])
	}
}

func TestSubscriptionDelete(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1"}

	resp := doJSON(t, http.MethodDelete, ts.URL+"/api/v1/subscriptions/sub-1", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if _, ok := fs.subs["sub-1"]; ok {
		t.Errorf("subscription not deleted")
	}
}

func TestSubscriptionActivatePause(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", Status: "paused"}

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/activate", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate: want 200, got %d", resp.StatusCode)
	}
	if fs.subs["sub-1"].Status != "active" {
		t.Errorf("activate: status = %q, want active", fs.subs["sub-1"].Status)
	}

	resp2 := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/pause", token, nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("pause: want 200, got %d", resp2.StatusCode)
	}
	if fs.subs["sub-1"].Status != "paused" {
		t.Errorf("pause: status = %q, want paused", fs.subs["sub-1"].Status)
	}
}

func TestSubscriptionRotateSecret(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", SigningSecret: "whsec_old"}

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/rotate-secret", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if fs.subs["sub-1"].SigningSecret == "whsec_old" {
		t.Errorf("secret was not rotated")
	}
}

func TestSubscriptionTest_Success(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", TargetURL: backend.URL}

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/test", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["success"] != true {
		t.Errorf("want success=true, got %v", got)
	}
}

func TestSubscriptionTest_DispatchesPingPayload(t *testing.T) {
	type received struct {
		contentType string
		eventHeader string
		body        map[string]any
	}
	capture := make(chan received, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		capture <- received{
			contentType: r.Header.Get("Content-Type"),
			eventHeader: r.Header.Get("X-Spine-Event"),
			body:        decoded,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", TargetURL: backend.URL}

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/test", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	select {
	case got := <-capture:
		if got.contentType != "application/json" {
			t.Errorf("Content-Type: want application/json, got %q", got.contentType)
		}
		if got.eventHeader != "ping" {
			t.Errorf("X-Spine-Event: want ping, got %q", got.eventHeader)
		}
		if got.body["type"] != "ping" {
			t.Errorf("body.type: want ping, got %v (body=%v)", got.body["type"], got.body)
		}
		if _, ok := got.body["timestamp"].(string); !ok {
			t.Errorf("body.timestamp: want string, got %v", got.body["timestamp"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not receive ping request")
	}
}

func TestSubscriptionTest_BadURL(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", TargetURL: "://bad"}

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/test", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 (handler reports error in body), got %d", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["success"] != false {
		t.Errorf("want success=false, got %v", got)
	}
}

func TestDeliveryList(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.deliveries["d-1"] = &store.DeliveryEntry{DeliveryID: "d-1", SubscriptionID: "sub-1", Status: "delivered"}

	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/sub-1/deliveries?limit=10&status=delivered", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestDeliveryGet(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.deliveries["d-1"] = &store.DeliveryEntry{DeliveryID: "d-1", SubscriptionID: "sub-1", Status: "delivered"}
	fs.logs = []store.DeliveryLogEntry{
		{LogID: "l-1", DeliveryID: "d-1", SubscriptionID: "sub-1"},
		{LogID: "l-2", DeliveryID: "other"}, // filtered out
	}

	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/sub-1/deliveries/d-1", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got struct {
		Attempts []store.DeliveryLogEntry `json:"attempts"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if len(got.Attempts) != 1 {
		t.Errorf("want 1 attempt (filtered), got %d", len(got.Attempts))
	}
}

func TestDeliveryReplay(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.deliveries["d-failed"] = &store.DeliveryEntry{DeliveryID: "d-failed", Status: "failed"}
	fs.deliveries["d-delivered"] = &store.DeliveryEntry{DeliveryID: "d-delivered", Status: "delivered"}

	// Happy path: failed → pending
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/deliveries/d-failed/replay", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if fs.deliveries["d-failed"].Status != "pending" {
		t.Errorf("status not updated to pending: %q", fs.deliveries["d-failed"].Status)
	}

	// Delivered deliveries cannot be replayed.
	resp2 := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/deliveries/d-delivered/replay", token, nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400 for delivered status, got %d", resp2.StatusCode)
	}
}

func TestSubscriptionStats(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.stats = &store.DeliveryStats{TotalDeliveries: 10, Delivered: 8, Failed: 2, SuccessRate: 0.8}

	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/sub-1/stats", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestSubscriptionList_StoreError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.listSubsErr = domain.NewError(domain.ErrInternal, "boom")
	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestSubscriptionUpdate_BadJSON(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1"}

	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/v1/subscriptions/sub-1",
		bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

func TestSubscriptionUpdate_StoreError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1"}
	fs.updateSubErr = domain.NewError(domain.ErrInternal, "boom")

	name := "x"
	resp := doJSON(t, http.MethodPatch, ts.URL+"/api/v1/subscriptions/sub-1", token,
		map[string]any{"name": name})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestSubscriptionDelete_StoreError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.deleteSubErr = domain.NewError(domain.ErrInternal, "boom")
	resp := doJSON(t, http.MethodDelete, ts.URL+"/api/v1/subscriptions/sub-1", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestSubscriptionActivate_NotFound(t *testing.T) {
	ts, _, token := setupSubServer(t)
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/missing/activate", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

func TestSubscriptionActivate_UpdateStoreError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", Status: "paused"}
	fs.updateSubErr = domain.NewError(domain.ErrInternal, "boom")
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/activate", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestSubscriptionRotateSecret_NotFound(t *testing.T) {
	ts, _, token := setupSubServer(t)
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/missing/rotate-secret", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

func TestSubscriptionRotateSecret_StoreError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1"}
	fs.updateSubErr = domain.NewError(domain.ErrInternal, "boom")
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/rotate-secret", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestSubscriptionTest_NotFound(t *testing.T) {
	ts, _, token := setupSubServer(t)
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/missing/test", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

func TestDeliveryList_StoreError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.listDeliveriesErr = domain.NewError(domain.ErrInternal, "boom")
	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/sub-1/deliveries", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestDeliveryGet_NotFound(t *testing.T) {
	ts, _, token := setupSubServer(t)
	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/sub-1/deliveries/missing", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

func TestDeliveryGet_HistoryError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.deliveries["d-1"] = &store.DeliveryEntry{DeliveryID: "d-1"}
	fs.historyErr = domain.NewError(domain.ErrInternal, "boom")
	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/sub-1/deliveries/d-1", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestDeliveryReplay_NotFound(t *testing.T) {
	ts, _, token := setupSubServer(t)
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/deliveries/missing/replay", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

func TestDeliveryReplay_UpdateError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.deliveries["d-1"] = &store.DeliveryEntry{DeliveryID: "d-1", Status: "failed"}
	fs.updateStatusErr = domain.NewError(domain.ErrInternal, "boom")
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/deliveries/d-1/replay", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestSubscriptionStats_StoreError(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.statsErr = domain.NewError(domain.ErrInternal, "boom")
	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/sub-1/stats", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp.StatusCode)
	}
}

func TestEventList(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.events = []store.EventLogEntry{
		{EventID: "evt-1", EventType: "run.completed"},
		{EventID: "evt-2", EventType: "run.completed"},
	}

	// Basic list
	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/events?limit=10", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	// With type filter and cursor
	resp2 := doJSON(t, http.MethodGet, ts.URL+"/api/v1/events?types=run.completed,run.failed&after=evt-0&limit=5000", token, nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("with filter: want 200, got %d", resp2.StatusCode)
	}

	// Store error path.
	fs.listEventsErr = domain.NewError(domain.ErrInternal, "kaboom")
	resp3 := doJSON(t, http.MethodGet, ts.URL+"/api/v1/events", token, nil)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", resp3.StatusCode)
	}
}
