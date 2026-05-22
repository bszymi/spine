package delivery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/adapters/store"
)

// fakeSubscriptionStore implements store.SubscriptionStore for the
// bootstrap unit tests. Only the three methods BootstrapInternalSubscription
// uses (List/Create/Update) carry behavior — Get/Delete/ListByEventType
// panic so a regression that starts calling them is loud, not silent.
type fakeSubscriptionStore struct {
	subs []store.EventSubscription

	listErr   error
	createErr error
	updateErr error

	listCalls   int
	createCalls int
	updateCalls int

	lastCreated *store.EventSubscription
	lastUpdated *store.EventSubscription
}

func (f *fakeSubscriptionStore) ListSubscriptions(_ context.Context, _ string) ([]store.EventSubscription, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.EventSubscription, len(f.subs))
	copy(out, f.subs)
	return out, nil
}

func (f *fakeSubscriptionStore) CreateSubscription(_ context.Context, sub *store.EventSubscription) error {
	f.createCalls++
	if f.createErr != nil {
		return f.createErr
	}
	cp := *sub
	f.lastCreated = &cp
	f.subs = append(f.subs, cp)
	return nil
}

func (f *fakeSubscriptionStore) UpdateSubscription(_ context.Context, sub *store.EventSubscription) error {
	f.updateCalls++
	if f.updateErr != nil {
		return f.updateErr
	}
	cp := *sub
	f.lastUpdated = &cp
	for i := range f.subs {
		if f.subs[i].SubscriptionID == sub.SubscriptionID {
			f.subs[i] = cp
			break
		}
	}
	return nil
}

func (f *fakeSubscriptionStore) GetSubscription(context.Context, string) (*store.EventSubscription, error) {
	panic("GetSubscription: not used by BootstrapInternalSubscription")
}

func (f *fakeSubscriptionStore) DeleteSubscription(context.Context, string) error {
	panic("DeleteSubscription: not used by BootstrapInternalSubscription")
}

func (f *fakeSubscriptionStore) ListActiveSubscriptionsByEventType(context.Context, string) ([]store.EventSubscription, error) {
	panic("ListActiveSubscriptionsByEventType: not used by BootstrapInternalSubscription")
}

// expectedEventTypes mirrors the canonical list bootstrap.go writes
// onto every internal subscription. Kept verbatim so a drift between
// production and the test catches accidental list churn at review time
// instead of in production rollouts.
func expectedEventTypes() []string {
	return []string{
		string(domain.EventStepAssigned),
		string(domain.EventStepCompleted),
		string(domain.EventStepFailed),
		string(domain.EventRunCompleted),
		string(domain.EventRunFailed),
		string(domain.EventRunCancelled),
		string(domain.EventRunPartiallyMerged),
	}
}

// canonicalConfig returns the BootstrapConfig used as the "matching"
// baseline for drift tests. Workspace-scoped so the WorkspaceID drift
// case has a non-empty starting point.
func canonicalConfig() BootstrapConfig {
	return BootstrapConfig{
		EventURL:    "https://internal.example.com/events",
		WorkspaceID: "ws-alpha",
		Token:       "secret-bearer-foo",
	}
}

// canonicalExisting returns an EventSubscription that matches the
// canonicalConfig on every field BootstrapInternalSubscription compares,
// so the second-pass drift cases can mutate exactly one field at a time.
func canonicalExisting() store.EventSubscription {
	cfg := canonicalConfig()
	return store.EventSubscription{
		SubscriptionID: "sub-existing-1",
		WorkspaceID:    cfg.WorkspaceID,
		Name:           InternalSubscriptionName,
		TargetType:     "webhook",
		TargetURL:      cfg.EventURL,
		EventTypes:     expectedEventTypes(),
		SigningSecret:  cfg.Token,
		Status:         "active",
		Metadata:       []byte(`{"source":"bootstrap"}`),
	}
}

func TestBootstrapInternalSubscription_FirstCallCreatesRow(t *testing.T) {
	ctx := context.Background()
	st := &fakeSubscriptionStore{}
	cfg := canonicalConfig()

	if err := BootstrapInternalSubscription(ctx, st, cfg); err != nil {
		t.Fatalf("BootstrapInternalSubscription: %v", err)
	}

	if st.listCalls != 1 {
		t.Fatalf("ListSubscriptions calls = %d, want 1", st.listCalls)
	}
	if st.createCalls != 1 {
		t.Fatalf("CreateSubscription calls = %d, want 1", st.createCalls)
	}
	if st.updateCalls != 0 {
		t.Fatalf("UpdateSubscription calls = %d, want 0 on first call", st.updateCalls)
	}
	if st.lastCreated == nil {
		t.Fatal("lastCreated is nil; expected CreateSubscription to receive a row")
	}
	created := st.lastCreated
	if created.Name != InternalSubscriptionName {
		t.Errorf("Name = %q, want %q", created.Name, InternalSubscriptionName)
	}
	if created.TargetURL != cfg.EventURL {
		t.Errorf("TargetURL = %q, want %q", created.TargetURL, cfg.EventURL)
	}
	if created.SigningSecret != cfg.Token {
		t.Errorf("SigningSecret = %q, want %q", created.SigningSecret, cfg.Token)
	}
	if created.WorkspaceID != cfg.WorkspaceID {
		t.Errorf("WorkspaceID = %q, want %q", created.WorkspaceID, cfg.WorkspaceID)
	}
	if created.Status != "active" {
		t.Errorf("Status = %q, want %q", created.Status, "active")
	}
	if created.TargetType != "webhook" {
		t.Errorf("TargetType = %q, want %q", created.TargetType, "webhook")
	}
	if created.CreatedBy != "system" {
		t.Errorf("CreatedBy = %q, want %q", created.CreatedBy, "system")
	}
	if !reflect.DeepEqual(created.EventTypes, expectedEventTypes()) {
		t.Errorf("EventTypes = %v, want %v", created.EventTypes, expectedEventTypes())
	}
	if created.SubscriptionID == "" {
		t.Error("SubscriptionID is empty; expected generateDeliveryID() output")
	}
	if string(created.Metadata) != `{"source":"bootstrap"}` {
		t.Errorf("Metadata = %s, want bootstrap source marker", string(created.Metadata))
	}
}

func TestBootstrapInternalSubscription_SecondCallIsNoOp(t *testing.T) {
	ctx := context.Background()
	st := &fakeSubscriptionStore{}
	cfg := canonicalConfig()

	if err := BootstrapInternalSubscription(ctx, st, cfg); err != nil {
		t.Fatalf("first BootstrapInternalSubscription: %v", err)
	}
	if err := BootstrapInternalSubscription(ctx, st, cfg); err != nil {
		t.Fatalf("second BootstrapInternalSubscription: %v", err)
	}

	if st.createCalls != 1 {
		t.Fatalf("CreateSubscription calls = %d, want 1 (second call must be no-op)", st.createCalls)
	}
	if st.updateCalls != 0 {
		t.Fatalf("UpdateSubscription calls = %d, want 0 (matching state must not Update)", st.updateCalls)
	}
	if got := len(st.subs); got != 1 {
		t.Fatalf("len(subs) = %d, want 1 (no duplicate row)", got)
	}
	if st.subs[0].Name != InternalSubscriptionName {
		t.Errorf("subs[0].Name = %q, want %q", st.subs[0].Name, InternalSubscriptionName)
	}
}

func TestBootstrapInternalSubscription_DriftTriggersUpdate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mutate   func(*store.EventSubscription)
		assertOn func(t *testing.T, updated *store.EventSubscription, cfg BootstrapConfig)
	}{
		{
			name:   "url-drift",
			mutate: func(s *store.EventSubscription) { s.TargetURL = "https://stale.example.com/old" },
			assertOn: func(t *testing.T, updated *store.EventSubscription, cfg BootstrapConfig) {
				if updated.TargetURL != cfg.EventURL {
					t.Errorf("updated.TargetURL = %q, want %q", updated.TargetURL, cfg.EventURL)
				}
			},
		},
		{
			name:   "token-drift",
			mutate: func(s *store.EventSubscription) { s.SigningSecret = "stale-secret" },
			assertOn: func(t *testing.T, updated *store.EventSubscription, cfg BootstrapConfig) {
				if updated.SigningSecret != cfg.Token {
					t.Errorf("updated.SigningSecret = %q, want %q", updated.SigningSecret, cfg.Token)
				}
			},
		},
		{
			name:   "workspace-drift",
			mutate: func(s *store.EventSubscription) { s.WorkspaceID = "ws-other" },
			assertOn: func(t *testing.T, updated *store.EventSubscription, cfg BootstrapConfig) {
				if updated.WorkspaceID != cfg.WorkspaceID {
					t.Errorf("updated.WorkspaceID = %q, want %q", updated.WorkspaceID, cfg.WorkspaceID)
				}
			},
		},
		{
			name:   "status-drift",
			mutate: func(s *store.EventSubscription) { s.Status = "paused" },
			assertOn: func(t *testing.T, updated *store.EventSubscription, _ BootstrapConfig) {
				if updated.Status != "active" {
					t.Errorf("updated.Status = %q, want active", updated.Status)
				}
			},
		},
		{
			name: "event-types-drift-shorter",
			mutate: func(s *store.EventSubscription) {
				s.EventTypes = []string{string(domain.EventStepAssigned)}
			},
			assertOn: func(t *testing.T, updated *store.EventSubscription, _ BootstrapConfig) {
				if !reflect.DeepEqual(updated.EventTypes, expectedEventTypes()) {
					t.Errorf("updated.EventTypes = %v, want %v", updated.EventTypes, expectedEventTypes())
				}
			},
		},
		{
			name: "event-types-drift-different-order",
			mutate: func(s *store.EventSubscription) {
				// Reverse the canonical list: stringSlicesEqual is
				// order-sensitive by design (so a release reorder is
				// also caught), so a reversed list must trigger Update.
				orig := expectedEventTypes()
				rev := make([]string, len(orig))
				for i, v := range orig {
					rev[len(orig)-1-i] = v
				}
				s.EventTypes = rev
			},
			assertOn: func(t *testing.T, updated *store.EventSubscription, _ BootstrapConfig) {
				if !reflect.DeepEqual(updated.EventTypes, expectedEventTypes()) {
					t.Errorf("updated.EventTypes = %v, want canonical order", updated.EventTypes)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := canonicalConfig()
			existing := canonicalExisting()
			tc.mutate(&existing)

			st := &fakeSubscriptionStore{subs: []store.EventSubscription{existing}}

			if err := BootstrapInternalSubscription(context.Background(), st, cfg); err != nil {
				t.Fatalf("BootstrapInternalSubscription: %v", err)
			}

			if st.updateCalls != 1 {
				t.Fatalf("UpdateSubscription calls = %d, want 1 (drift must update)", st.updateCalls)
			}
			if st.createCalls != 0 {
				t.Fatalf("CreateSubscription calls = %d, want 0 (drift must not insert duplicate)", st.createCalls)
			}
			if st.lastUpdated == nil {
				t.Fatal("lastUpdated is nil; expected UpdateSubscription to receive a row")
			}
			if st.lastUpdated.SubscriptionID != existing.SubscriptionID {
				t.Errorf("lastUpdated.SubscriptionID = %q, want %q (must update existing row, not insert)",
					st.lastUpdated.SubscriptionID, existing.SubscriptionID)
			}
			tc.assertOn(t, st.lastUpdated, cfg)
		})
	}
}

func TestBootstrapInternalSubscription_ListErrorSurfaced(t *testing.T) {
	wantErr := errors.New("list-failed")
	st := &fakeSubscriptionStore{listErr: wantErr}

	err := BootstrapInternalSubscription(context.Background(), st, canonicalConfig())
	if !errors.Is(err, wantErr) {
		t.Fatalf("BootstrapInternalSubscription error = %v, want %v", err, wantErr)
	}
	if st.createCalls != 0 || st.updateCalls != 0 {
		t.Errorf("create=%d update=%d, want 0/0 when list fails", st.createCalls, st.updateCalls)
	}
}

func TestBootstrapInternalSubscription_CreateErrorSurfaced(t *testing.T) {
	wantErr := errors.New("create-failed")
	st := &fakeSubscriptionStore{createErr: wantErr}

	err := BootstrapInternalSubscription(context.Background(), st, canonicalConfig())
	if !errors.Is(err, wantErr) {
		t.Fatalf("BootstrapInternalSubscription error = %v, want %v", err, wantErr)
	}
	if st.createCalls != 1 {
		t.Errorf("createCalls = %d, want 1 (Create was attempted before failing)", st.createCalls)
	}
}

func TestBootstrapInternalSubscription_UpdateErrorSurfaced(t *testing.T) {
	wantErr := errors.New("update-failed")
	existing := canonicalExisting()
	existing.TargetURL = "https://stale.example.com/old"

	st := &fakeSubscriptionStore{
		subs:      []store.EventSubscription{existing},
		updateErr: wantErr,
	}

	err := BootstrapInternalSubscription(context.Background(), st, canonicalConfig())
	if !errors.Is(err, wantErr) {
		t.Fatalf("BootstrapInternalSubscription error = %v, want %v", err, wantErr)
	}
	if st.updateCalls != 1 {
		t.Errorf("updateCalls = %d, want 1 (Update was attempted before failing)", st.updateCalls)
	}
}

func TestStringSlicesEqual(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both-empty", []string{}, []string{}, true},
		{"both-nil", nil, nil, true},
		{"empty-vs-nil", []string{}, nil, true},
		{"identical", []string{"a", "b"}, []string{"a", "b"}, true},
		{"length-mismatch", []string{"a"}, []string{"a", "b"}, false},
		{"order-sensitive", []string{"a", "b"}, []string{"b", "a"}, false},
		{"single-mismatch", []string{"a", "b", "c"}, []string{"a", "x", "c"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stringSlicesEqual(tc.a, tc.b); got != tc.want {
				t.Fatalf("stringSlicesEqual(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
