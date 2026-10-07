package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// InternalSubscriptionName is the well-known name for the SMP bootstrap subscription.
const InternalSubscriptionName = "smp-internal"

// BootstrapConfig holds the environment-derived config for internal subscriptions.
type BootstrapConfig struct {
	EventURL    string // SMP_EVENT_URL
	WorkspaceID string // SMP_WORKSPACE_ID (optional)
	Token       string // SMP_INTERNAL_TOKEN (used as signing secret)

	// SignatureVersion opts the internal subscription into a webhook
	// signature version (INIT-023 TASK-010): "v1" or "v2". Empty — the
	// zero value every existing caller passes — leaves the stored
	// metadata exactly as it is (a fresh row gets today's
	// {"source":"bootstrap"}), so behaviour is unchanged until the
	// embedder opts in. Any other value is an error.
	SignatureVersion string
}

// bootstrapMetadata is the metadata of a freshly created internal
// subscription. Field order is the JSON key order.
type bootstrapMetadata struct {
	Source           string `json:"source"`
	SignatureVersion string `json:"signature_version,omitempty"`
}

// freshBootstrapMetadata renders {"source":"bootstrap"} plus, when
// version is set, "signature_version".
func freshBootstrapMetadata(version string) []byte {
	b, err := json.Marshal(bootstrapMetadata{Source: "bootstrap", SignatureVersion: version})
	if err != nil {
		// Two plain strings cannot fail to marshal.
		panic("marshal bootstrap metadata: " + err.Error())
	}
	return b
}

// BootstrapInternalSubscription creates or updates the internal SMP
// subscription. Idempotent: safe to call on every startup. Takes the
// narrow store.SubscriptionStore role interface (INIT-022 EPIC-001
// TASK-010).
func BootstrapInternalSubscription(ctx context.Context, st store.SubscriptionStore, cfg BootstrapConfig) error {
	log := observe.Logger(ctx)

	if cfg.SignatureVersion != "" && cfg.SignatureVersion != SignatureVersionV1 && cfg.SignatureVersion != SignatureVersionV2 {
		return fmt.Errorf("bootstrap internal subscription: signature version must be %q or %q, got %q",
			SignatureVersionV1, SignatureVersionV2, cfg.SignatureVersion)
	}

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

		// Signature-version opt-in (INIT-023 TASK-010). Only when the
		// caller names a version: an empty cfg.SignatureVersion never
		// reads or rewrites the metadata. A row whose metadata does not
		// parse counts as drifted, so the rewrite below heals it.
		rewriteMetadata := false
		if cfg.SignatureVersion != "" {
			if cur, err := SignatureVersionOf(existing.Metadata); err != nil || cur != cfg.SignatureVersion {
				rewriteMetadata = true
				changed = true
			}
		}

		if !changed {
			log.Info("internal subscription already configured", "subscription_id", existing.SubscriptionID)
			return nil
		}

		existing.TargetURL = cfg.EventURL
		existing.SigningSecret = cfg.Token
		existing.WorkspaceID = cfg.WorkspaceID
		existing.EventTypes = eventTypes
		existing.Status = "active"
		if rewriteMetadata {
			md, err := WithSignatureVersion(existing.Metadata, cfg.SignatureVersion)
			if err != nil {
				// The stored metadata is not a JSON object, so nothing in
				// it is readable (the dispatcher dead-letters every
				// delivery on it). Replace it with the canonical bootstrap
				// metadata rather than returning the error: SMP's
				// pre-dispatch ensure blocks a workspace's step dispatch on
				// any bootstrap error, and this row would never self-heal.
				log.Warn("internal subscription metadata unparseable; replacing it",
					"subscription_id", existing.SubscriptionID, "error", err.Error())
				md = freshBootstrapMetadata(cfg.SignatureVersion)
			}
			existing.Metadata = md
		}
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
		Metadata:       freshBootstrapMetadata(cfg.SignatureVersion),
		CreatedBy:      "system",
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := st.CreateSubscription(ctx, sub); err != nil {
		return err
	}

	log.Info("internal subscription created", "subscription_id", sub.SubscriptionID, "target_url", RedactURL(cfg.EventURL))
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
