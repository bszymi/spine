package delivery

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// InternalSubscriptionName is the well-known name for the SMP bootstrap subscription.
const InternalSubscriptionName = "smp-internal"

// BootstrapConfig holds the environment-derived config for internal subscriptions.
type BootstrapConfig struct {
	EventURL    string // SMP_EVENT_URL
	WorkspaceID string // SMP_WORKSPACE_ID (optional)
	Token       string // SMP_INTERNAL_TOKEN (used as signing secret)
}

// BootstrapInternalSubscription creates or updates the internal SMP subscription.
// Idempotent: safe to call on every startup.
func BootstrapInternalSubscription(ctx context.Context, st store.Store, cfg BootstrapConfig) error {
	log := observe.Logger(ctx)

	// Check if subscription already exists by listing and matching name.
	subs, err := st.ListSubscriptions(ctx, "")
	if err != nil {
		return err
	}

	var existing *store.EventSubscription
	for i := range subs {
		if subs[i].Name == InternalSubscriptionName {
			existing = &subs[i]
			break
		}
	}

	eventTypes := []string{
		string(domain.EventStepAssigned),
		string(domain.EventStepCompleted),
		string(domain.EventStepFailed),
		string(domain.EventRunCompleted),
		string(domain.EventRunFailed),
		string(domain.EventRunCancelled),
		string(domain.EventRunPartiallyMerged),
	}

	if existing != nil {
		// Update existing subscription if config changed.
		// EventTypes is included in the change check so that a new
		// release adding to eventTypes (e.g. run_partially_merged
		// for EPIC-005 TASK-003) actually propagates to upgraded
		// deployments — without this comparison the bootstrap would
		// return early and the new event would never be delivered to
		// the internal subscriber.
		changed := existing.TargetURL != cfg.EventURL ||
			existing.SigningSecret != cfg.Token ||
			existing.WorkspaceID != cfg.WorkspaceID ||
			existing.Status != "active" ||
			!stringSlicesEqual(existing.EventTypes, eventTypes)

		if !changed {
			log.Info("internal subscription already configured", "subscription_id", existing.SubscriptionID)
			return nil
		}

		existing.TargetURL = cfg.EventURL
		existing.SigningSecret = cfg.Token
		existing.WorkspaceID = cfg.WorkspaceID
		existing.EventTypes = eventTypes
		existing.Status = "active"
		if err := st.UpdateSubscription(ctx, existing); err != nil {
			return err
		}
		log.Info("internal subscription updated", "subscription_id", existing.SubscriptionID)
		return nil
	}

	// Create new internal subscription.
	subID, err := generateDeliveryID()
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	sub := &store.EventSubscription{
		SubscriptionID: subID,
		WorkspaceID:    cfg.WorkspaceID,
		Name:           InternalSubscriptionName,
		TargetType:     "webhook",
		TargetURL:      cfg.EventURL,
		EventTypes:     eventTypes,
		SigningSecret:  cfg.Token,
		Status:         "active",
		Metadata:       []byte(`{"source":"bootstrap"}`),
		CreatedBy:      "system",
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := st.CreateSubscription(ctx, sub); err != nil {
		return err
	}

	log.Info("internal subscription created", "subscription_id", sub.SubscriptionID, "target_url", cfg.EventURL)
	return nil
}

// stringSlicesEqual reports whether two []string have the same
// contents in the same order. Used by BootstrapInternalSubscription
// to detect when the EventTypes list has drifted between releases —
// otherwise an existing subscription with the old list would never
// pick up new event types added to eventTypes (e.g. when a new run
// lifecycle event is registered).
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
