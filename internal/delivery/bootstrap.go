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
	}

	if existing != nil {
		// Update existing subscription if config changed.
		changed := existing.TargetURL != cfg.EventURL ||
			existing.SigningSecret != cfg.Token ||
			existing.WorkspaceID != cfg.WorkspaceID ||
			existing.Status != "active"

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
