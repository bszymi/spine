package main

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/domain"
)

// noopSubscriptionLister returns no subscriptions. Placeholder until
// EPIC-002 adds the subscription store.
type noopSubscriptionLister struct{}

func (n *noopSubscriptionLister) ListActiveSubscriptions(_ context.Context, _ domain.EventType) ([]delivery.Subscription, error) {
	return nil, nil
}

// noopSubscriptionResolver always returns an error. No deliveries will
// reach it while noopSubscriptionLister returns empty.
type noopSubscriptionResolver struct{}

func (n *noopSubscriptionResolver) GetSubscription(_ context.Context, id string) (*delivery.SubscriptionDetail, error) {
	return nil, fmt.Errorf("subscription %s: no subscription store configured", id)
}
