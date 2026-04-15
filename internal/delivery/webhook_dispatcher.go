package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// SubscriptionDetail contains the delivery target info for a subscription.
type SubscriptionDetail struct {
	SubscriptionID string
	TargetURL      string
	SigningSecret   string
}

// SubscriptionResolver looks up subscription details by ID.
type SubscriptionResolver interface {
	GetSubscription(ctx context.Context, subscriptionID string) (*SubscriptionDetail, error)
}

// WebhookDispatcher reads pending entries from the delivery queue and
// POSTs them to configured webhook URLs.
type WebhookDispatcher struct {
	store       store.Store
	resolver    SubscriptionResolver
	client      *http.Client
	concurrency int
	pollInterval time.Duration
	breaker     *CircuitBreaker
}

// DispatcherConfig configures the webhook dispatcher.
type DispatcherConfig struct {
	Concurrency  int           // Max concurrent deliveries (default 5)
	HTTPTimeout  time.Duration // Per-request timeout (default 10s)
	PollInterval time.Duration // How often to poll the queue (default 1s)
}

// NewWebhookDispatcher creates a dispatcher that delivers events to webhook URLs.
func NewWebhookDispatcher(st store.Store, resolver SubscriptionResolver, cfg DispatcherConfig) *WebhookDispatcher {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 5
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 1 * time.Second
	}

	return &WebhookDispatcher{
		store:        st,
		resolver:     resolver,
		client:       &http.Client{Timeout: cfg.HTTPTimeout},
		concurrency:  cfg.Concurrency,
		pollInterval: cfg.PollInterval,
		breaker:      NewCircuitBreaker(),
	}
}

// Run starts the dispatcher loop. It polls the delivery queue and dispatches
// webhooks until the context is cancelled.
func (d *WebhookDispatcher) Run(ctx context.Context) {
	log := observe.Logger(ctx)
	log.Info("webhook dispatcher started", "concurrency", d.concurrency)

	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("webhook dispatcher stopping")
			return
		case <-ticker.C:
			d.poll(ctx)
		}
	}
}

// poll claims pending deliveries and dispatches them concurrently.
func (d *WebhookDispatcher) poll(ctx context.Context) {
	log := observe.Logger(ctx)

	entries, err := d.store.ClaimDeliveries(ctx, d.concurrency)
	if err != nil {
		log.Error("failed to claim deliveries", "error", err)
		return
	}
	if len(entries) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, entry := range entries {
		wg.Add(1)
		go func(e store.DeliveryEntry) {
			defer wg.Done()
			d.deliver(ctx, e)
		}(entry)
	}
	wg.Wait()
}

// deliver sends a single webhook and records the result.
func (d *WebhookDispatcher) deliver(ctx context.Context, entry store.DeliveryEntry) {
	log := observe.Logger(ctx).With(
		"delivery_id", entry.DeliveryID,
		"subscription_id", entry.SubscriptionID,
		"event_type", entry.EventType,
	)

	// Circuit breaker check
	if !d.breaker.AllowDelivery(entry.SubscriptionID) {
		log.Debug("circuit open, skipping delivery")
		// Return to pending so it can be retried later
		_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "pending", "circuit open", nil)
		return
	}

	sub, err := d.resolver.GetSubscription(ctx, entry.SubscriptionID)
	if err != nil {
		log.Error("failed to resolve subscription", "error", err)
		_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "failed", err.Error(), nil)
		return
	}

	// Build request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.TargetURL, bytes.NewReader(entry.Payload))
	if err != nil {
		log.Error("failed to build request", "error", err)
		_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "failed", err.Error(), nil)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Spine-Event", entry.EventType)
	req.Header.Set("X-Spine-Delivery", entry.DeliveryID)

	// HMAC signing
	if sub.SigningSecret != "" {
		sig := computeHMAC(entry.Payload, sub.SigningSecret)
		req.Header.Set("X-Spine-Signature", "sha256="+sig)
	}

	// Execute
	start := time.Now()
	resp, err := d.client.Do(req)
	durationMs := int(time.Since(start).Milliseconds())

	// Generate log ID
	logID, _ := generateDeliveryID() // reuse ID generator, prefix doesn't matter for log

	if err != nil {
		log.Error("webhook delivery failed", "error", err, "duration_ms", durationMs)
		d.breaker.RecordFailure(entry.SubscriptionID)
		d.handleFailure(ctx, entry, logID, durationMs, 0, true, err.Error(), nil)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	statusCode := resp.StatusCode

	if statusCode >= 200 && statusCode < 300 {
		log.Info("webhook delivered", "status", statusCode, "duration_ms", durationMs)
		d.breaker.RecordSuccess(entry.SubscriptionID)
		_ = d.store.MarkDelivered(ctx, entry.DeliveryID)
	} else {
		errMsg := fmt.Sprintf("HTTP %d", statusCode)
		retryable := isRetryable(statusCode, false)
		if retryable {
			log.Warn("webhook delivery failed (retryable)", "status", statusCode, "duration_ms", durationMs)
		} else {
			log.Warn("webhook delivery rejected (permanent)", "status", statusCode, "duration_ms", durationMs)
		}
		d.breaker.RecordFailure(entry.SubscriptionID)
		d.handleFailure(ctx, entry, logID, durationMs, statusCode, retryable, errMsg, resp)
		return
	}

	_ = d.store.LogDeliveryAttempt(ctx, &store.DeliveryLogEntry{
		LogID:          logID,
		DeliveryID:     entry.DeliveryID,
		SubscriptionID: entry.SubscriptionID,
		EventID:        entry.EventID,
		StatusCode:     &statusCode,
		DurationMs:     &durationMs,
		CreatedAt:      time.Now().UTC(),
	})
}

// handleFailure processes a delivery failure, deciding whether to retry or dead-letter.
func (d *WebhookDispatcher) handleFailure(ctx context.Context, entry store.DeliveryEntry, logID string, durationMs int, statusCode int, retryable bool, errMsg string, resp *http.Response) {
	log := observe.Logger(ctx)
	maxRetries := maxRetriesFor(entry.EventType)
	newAttemptCount := entry.AttemptCount + 1

	var sc *int
	if statusCode > 0 {
		sc = &statusCode
	}

	_ = d.store.LogDeliveryAttempt(ctx, &store.DeliveryLogEntry{
		LogID:          logID,
		DeliveryID:     entry.DeliveryID,
		SubscriptionID: entry.SubscriptionID,
		EventID:        entry.EventID,
		StatusCode:     sc,
		DurationMs:     &durationMs,
		Error:          errMsg,
		CreatedAt:      time.Now().UTC(),
	})

	if !retryable || newAttemptCount >= maxRetries {
		status := "dead"
		if !retryable {
			status = "failed"
		}
		log.Warn("delivery exhausted", "delivery_id", entry.DeliveryID, "status", status, "attempts", newAttemptCount)
		_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, status, errMsg, nil)
		return
	}

	// Schedule retry with backoff
	delay := nextRetryDelay(newAttemptCount - 1)
	if statusCode == 429 {
		if ra := retryAfterFromHeader(resp); ra > 0 {
			delay = ra
		}
	}
	retryAt := time.Now().Add(delay)
	log.Info("scheduling retry", "delivery_id", entry.DeliveryID, "attempt", newAttemptCount, "retry_at", retryAt)
	_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "failed", errMsg, &retryAt)
}

// computeHMAC returns the hex-encoded HMAC-SHA256 of the payload.
func computeHMAC(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
