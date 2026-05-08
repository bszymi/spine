package delivery

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// StoreSubscriptionLister implements SubscriptionLister backed by the database.
type StoreSubscriptionLister struct {
	store store.SubscriptionStore
}

// NewStoreSubscriptionLister creates a subscription lister backed by
// the store. Takes the narrow store.SubscriptionStore role interface
// (INIT-022 EPIC-001 TASK-010).
func NewStoreSubscriptionLister(st store.SubscriptionStore) *StoreSubscriptionLister {
	return &StoreSubscriptionLister{store: st}
}

func (s *StoreSubscriptionLister) ListActiveSubscriptions(ctx context.Context, eventType domain.EventType) ([]Subscription, error) {
	subs, err := s.store.ListActiveSubscriptionsByEventType(ctx, string(eventType))
	if err != nil {
		return nil, err
	}
	result := make([]Subscription, len(subs))
	for i, sub := range subs {
		result[i] = Subscription{
			SubscriptionID: sub.SubscriptionID,
			EventTypes:     sub.EventTypes,
		}
	}
	return result, nil
}

// StoreSubscriptionResolver implements SubscriptionResolver backed by the database.
type StoreSubscriptionResolver struct {
	store store.SubscriptionStore
}

// NewStoreSubscriptionResolver creates a subscription resolver backed
// by the store. Takes the narrow store.SubscriptionStore role
// interface (INIT-022 EPIC-001 TASK-010).
func NewStoreSubscriptionResolver(st store.SubscriptionStore) *StoreSubscriptionResolver {
	return &StoreSubscriptionResolver{store: st}
}

func (s *StoreSubscriptionResolver) GetSubscription(ctx context.Context, subscriptionID string) (*SubscriptionDetail, error) {
	sub, err := s.store.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := parseSubscriptionTLS(sub.Metadata)
	if err != nil {
		return nil, err
	}
	return &SubscriptionDetail{
		SubscriptionID: sub.SubscriptionID,
		TargetURL:      sub.TargetURL,
		SigningSecret:  sub.SigningSecret,
		TLS:            tlsCfg,
	}, nil
}
