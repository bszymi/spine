package delivery

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// StoreSubscriptionLister implements SubscriptionLister backed by the database.
type StoreSubscriptionLister struct {
	store store.Store
}

// NewStoreSubscriptionLister creates a subscription lister backed by the store.
func NewStoreSubscriptionLister(st store.Store) *StoreSubscriptionLister {
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
	store store.Store
}

// NewStoreSubscriptionResolver creates a subscription resolver backed by the store.
func NewStoreSubscriptionResolver(st store.Store) *StoreSubscriptionResolver {
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
