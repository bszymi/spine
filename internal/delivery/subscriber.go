package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// Subscription describes an active event delivery target.
type Subscription struct {
	SubscriptionID string
	EventTypes     []string // empty = all events
}

// SubscriptionLister returns active subscriptions that match an event type.
type SubscriptionLister interface {
	ListActiveSubscriptions(ctx context.Context, eventType domain.EventType) ([]Subscription, error)
}

// DeliverySubscriber subscribes to all event types on the EventRouter and
// persists matching events to the delivery queue for async dispatch.
type DeliverySubscriber struct {
	store         store.Store
	subscriptions SubscriptionLister
	Broadcaster   *EventBroadcaster
}

// NewDeliverySubscriber creates a subscriber that bridges the internal
// EventRouter to the persistent delivery queue.
func NewDeliverySubscriber(st store.Store, subs SubscriptionLister) *DeliverySubscriber {
	return &DeliverySubscriber{
		store:         st,
		subscriptions: subs,
		Broadcaster:   NewEventBroadcaster(),
	}
}

// Subscribe registers handlers for all known event types on the router.
// Each received event is fanned out to matching subscriptions and enqueued
// for delivery. Errors are logged but never propagated — the subscriber
// must not block the internal event pipeline.
func (s *DeliverySubscriber) Subscribe(ctx context.Context, router event.EventRouter) error {
	allTypes := allEventTypes()
	for _, et := range allTypes {
		if err := router.Subscribe(ctx, et, s.handleEvent); err != nil {
			return fmt.Errorf("subscribe to %s: %w", et, err)
		}
	}
	observe.Logger(ctx).Info("delivery subscriber started", "event_types", len(allTypes))
	return nil
}

// handleEvent is the EventHandler called for every event. It looks up
// matching subscriptions and enqueues one delivery entry per subscription.
func (s *DeliverySubscriber) handleEvent(ctx context.Context, evt domain.Event) error {
	log := observe.Logger(ctx)

	// Broadcast to SSE listeners (non-blocking).
	s.Broadcaster.Broadcast(evt)

	payload, err := json.Marshal(evt)
	if err != nil {
		log.Error("failed to marshal event for delivery", "event_id", evt.EventID, "error", err)
		return nil
	}

	// Always write to the event log (for pull API and SSE replay),
	// regardless of whether any subscriptions match.
	if err := s.store.WriteEventLog(ctx, &store.EventLogEntry{
		EventID:   evt.EventID,
		EventType: string(evt.Type),
		Payload:   payload,
		CreatedAt: evt.Timestamp,
	}); err != nil {
		log.Error("failed to write event log", "event_id", evt.EventID, "error", err)
	}

	subs, err := s.subscriptions.ListActiveSubscriptions(ctx, evt.Type)
	if err != nil {
		log.Error("failed to list subscriptions", "event_type", evt.Type, "error", err)
		return nil // don't block the event pipeline
	}

	if len(subs) == 0 {
		return nil
	}

	for _, sub := range subs {
		deliveryID, err := generateDeliveryID()
		if err != nil {
			log.Error("failed to generate delivery ID", "error", err)
			continue
		}

		entry := &store.DeliveryEntry{
			DeliveryID:     deliveryID,
			SubscriptionID: sub.SubscriptionID,
			EventID:        evt.EventID,
			EventType:      string(evt.Type),
			Payload:        payload,
			Status:         "pending",
			AttemptCount:   0,
			CreatedAt:      time.Now().UTC(),
		}

		if err := s.store.EnqueueDelivery(ctx, entry); err != nil {
			log.Error("failed to enqueue delivery",
				"event_id", evt.EventID,
				"subscription_id", sub.SubscriptionID,
				"error", err,
			)
			continue // idempotent — duplicates silently ignored
		}
	}

	return nil
}

func generateDeliveryID() (string, error) {
	id, err := observe.GenerateTraceID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("dlv-%s", id[:12]), nil
}

// allEventTypes returns all known domain and operational event types.
func allEventTypes() []domain.EventType {
	return []domain.EventType{
		// Domain events
		domain.EventArtifactCreated,
		domain.EventArtifactUpdated,
		domain.EventRunStarted,
		domain.EventRunCompleted,
		domain.EventRunFailed,
		domain.EventRunCancelled,
		domain.EventRunPaused,
		domain.EventRunResumed,
		domain.EventRunPartiallyMerged,
		domain.EventStepAssigned,
		domain.EventStepStarted,
		domain.EventStepCompleted,
		domain.EventStepFailed,
		domain.EventStepTimeout,
		domain.EventRetryAttempted,
		domain.EventRunTimeout,
		// Operational events
		domain.EventDivergenceStarted,
		domain.EventConvergenceCompleted,
		domain.EventEngineRecovered,
		domain.EventProjectionSynced,
		domain.EventThreadCreated,
		domain.EventCommentAdded,
		domain.EventThreadResolved,
		domain.EventValidationPassed,
		domain.EventValidationFailed,
		domain.EventAssignmentFailed,
		domain.EventTaskUnblocked,
		domain.EventTaskReleased,
		domain.EventBranchProtectionOverride,
		domain.EventRunBranchCleanupFailed,
	}
}
