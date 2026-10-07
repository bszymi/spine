package delivery

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/observe"
)

// SubscriptionDetail contains the delivery target info for a subscription.
type SubscriptionDetail struct {
	SubscriptionID string
	TargetURL      string
	SigningSecret  string
	// SignatureVersion selects the signature format: "" or "v1" (the
	// body-only HMAC, default) or "v2" (timestamp + nonce + body, see
	// signature.go). StoreSubscriptionResolver fills it from
	// metadata.signature_version. Any other value is refused at send
	// time and the delivery is dead-lettered (fail closed).
	SignatureVersion string
	// TLS is optional per-subscription TLS hardening. When non-nil,
	// the dispatcher builds a dedicated http.Client honoring the
	// SPKI pin and/or custom CA bundle.
	TLS *SubscriptionTLSConfig
}

// SubscriptionResolver looks up subscription details by ID.
type SubscriptionResolver interface {
	GetSubscription(ctx context.Context, subscriptionID string) (*SubscriptionDetail, error)
}

// WebhookDispatcher reads pending entries from the delivery queue and
// POSTs them to configured webhook URLs.
type WebhookDispatcher struct {
	store        store.DeliveryStore
	resolver     SubscriptionResolver
	client       *http.Client
	httpTimeout  time.Duration
	concurrency  int
	pollInterval time.Duration
	breaker      *CircuitBreaker

	// tlsClients caches per-subscription http.Clients keyed by
	// subscription ID so we don't rebuild an x509 pool / Transport
	// for every delivery. Cache entries are invalidated when a
	// subscription's TLS config changes (new config signature).
	tlsMu      sync.Mutex
	tlsClients map[string]*tlsClientEntry

	// targets, if non-nil, gates every outbound delivery URL and
	// enforces the safe dialer on both the default and per-TLS
	// clients. Kept as a field (rather than only on cfg) so
	// deliver() can re-validate the persisted target_url before
	// issuing the request.
	targets *TargetValidator

	// now and rand are the clock and entropy source for signing and
	// scheduling. Production uses time.Now and crypto/rand.Reader; they
	// are fields so tests can pin the v2 timestamp / nonce and drive a
	// simulated clock.
	now  func() time.Time
	rand io.Reader
}

type tlsClientEntry struct {
	signature string
	client    *http.Client
}

// DispatcherConfig configures the webhook dispatcher.
type DispatcherConfig struct {
	Concurrency  int           // Max concurrent deliveries (default 5)
	HTTPTimeout  time.Duration // Per-request timeout (default 10s)
	PollInterval time.Duration // How often to poll the queue (default 1s)

	// Targets, if non-nil, rejects deliveries whose persisted
	// target_url fails URL validation and routes every outbound HTTP
	// request through the safe dialer so a rebound DNS name can't
	// reach a private address at connect time. Production callers
	// must set this; tests may leave it nil for a permissive client.
	Targets *TargetValidator
}

// NewWebhookDispatcher creates a dispatcher that delivers events to
// webhook URLs. Takes a store.DeliveryStore — the narrow role interface
// covering the delivery queue and event log — rather than the full
// store.Store union (INIT-022 EPIC-001 TASK-010).
func NewWebhookDispatcher(st store.DeliveryStore, resolver SubscriptionResolver, cfg DispatcherConfig) *WebhookDispatcher {
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
		client:       cfg.Targets.HTTPClient(cfg.HTTPTimeout),
		httpTimeout:  cfg.HTTPTimeout,
		concurrency:  cfg.Concurrency,
		pollInterval: cfg.PollInterval,
		breaker:      NewCircuitBreaker(),
		tlsClients:   make(map[string]*tlsClientEntry),
		targets:      cfg.Targets,
		now:          time.Now,
		rand:         rand.Reader,
	}
}

// clock returns the dispatcher's current time (time.Now unless a test
// replaced it).
func (d *WebhookDispatcher) clock() time.Time {
	if d.now == nil {
		return time.Now()
	}
	return d.now()
}

// clientFor returns an http.Client tailored to the subscription's TLS
// config, falling back to the shared default when no TLS hardening is
// configured. An error here aborts the delivery — we fail closed
// rather than silently downgrading to default TLS.
func (d *WebhookDispatcher) clientFor(sub *SubscriptionDetail) (*http.Client, error) {
	if sub.TLS == nil {
		return d.client, nil
	}
	sig := sub.TLS.PinnedSPKISHA256 + "|" + sub.TLS.CABundlePEM
	d.tlsMu.Lock()
	defer d.tlsMu.Unlock()
	if entry, ok := d.tlsClients[sub.SubscriptionID]; ok && entry.signature == sig {
		return entry.client, nil
	}
	client, err := buildTLSClient(sub.TLS, d.httpTimeout, d.targets)
	if err != nil {
		return nil, err
	}
	d.tlsClients[sub.SubscriptionID] = &tlsClientEntry{signature: sig, client: client}
	return client, nil
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
	if !d.breaker.AllowDelivery(ctx, entry.SubscriptionID) {
		log.Debug("circuit open, skipping delivery")
		// Return to pending so it can be retried later
		_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "pending", "circuit open", nil)
		return
	}

	sub, err := d.resolver.GetSubscription(ctx, entry.SubscriptionID)
	if err != nil {
		if errors.Is(err, ErrInvalidSubscriptionMetadata) {
			// Permanent configuration error (unknown signature_version,
			// malformed metadata): no retry can fix it, so the row is
			// dead-lettered instead of being re-claimed forever. The
			// parse reason names keys and values the operator wrote,
			// never infrastructure detail.
			log.Warn("subscription metadata invalid; dead-lettering delivery", "error", err.Error())
			d.deadLetter(ctx, entry, err.Error())
			return
		}
		log.Error("failed to resolve subscription", "error", err)
		_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "failed", err.Error(), nil)
		return
	}

	// Re-validate the persisted URL before every delivery so
	// subscriptions predating the SSRF hardening are rejected here,
	// not at the socket. Failures are permanent (not retryable).
	if err := d.targets.ValidateURL(sub.TargetURL); err != nil {
		log.Warn("webhook target_url rejected by validator", "error", err)
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

	// Execute. Per-subscription TLS (pinned SPKI / custom CA) — if
	// configured — uses a dedicated client cached by subscription ID.
	client, err := d.clientFor(sub)
	if err != nil {
		log.Error("subscription TLS config invalid", "error", err)
		d.breaker.RecordFailure(ctx, entry.SubscriptionID)
		tlsLogID, _ := generateDeliveryID()
		d.handleFailure(ctx, entry, tlsLogID, 0, 0, false, err.Error(), nil)
		return
	}

	// Sign last, immediately before the send, so a v2 timestamp is send
	// time and every attempt (retry, operator replay, SMP redrive)
	// carries a fresh nonce. Nothing about the signature is persisted.
	// The body signed is entry.Payload, the exact bytes the request
	// carries.
	if err := signRequest(req.Header, sub.SignatureVersion, sub.SigningSecret, entry.Payload, d.clock(), d.rand); err != nil {
		if errors.Is(err, errV2WithoutSecret) || errors.Is(err, ErrInvalidSubscriptionMetadata) {
			// Permanent: never send unsigned, never fall back to v1.
			log.Warn("delivery cannot be signed as configured; dead-lettering", "error", err.Error())
			d.deadLetter(ctx, entry, err.Error())
			return
		}
		// Entropy read failure: transient, ride the backoff.
		log.Error("failed to sign delivery", "error", err.Error())
		signLogID, _ := generateDeliveryID()
		d.handleFailure(ctx, entry, signLogID, 0, 0, true, "sign delivery: "+err.Error(), nil)
		return
	}

	start := time.Now()
	resp, err := client.Do(req)
	durationMs := int(time.Since(start).Milliseconds())

	// Generate log ID
	logID, _ := generateDeliveryID() // reuse ID generator, prefix doesn't matter for log

	if err != nil {
		log.Error("webhook delivery failed", "error", err, "duration_ms", durationMs)
		d.breaker.RecordFailure(ctx, entry.SubscriptionID)
		d.handleFailure(ctx, entry, logID, durationMs, 0, true, err.Error(), nil)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	statusCode := resp.StatusCode

	if statusCode >= 200 && statusCode < 300 {
		log.Info("webhook delivered", "status", statusCode, "duration_ms", durationMs)
		d.breaker.RecordSuccess(ctx, entry.SubscriptionID)
		_ = d.store.MarkDelivered(ctx, entry.DeliveryID)
	} else {
		errMsg := fmt.Sprintf("HTTP %d", statusCode)
		retryable := isRetryable(statusCode, false)
		if retryable {
			log.Warn("webhook delivery failed (retryable)", "status", statusCode, "duration_ms", durationMs)
		} else {
			log.Warn("webhook delivery rejected (permanent)", "status", statusCode, "duration_ms", durationMs)
		}
		d.breaker.RecordFailure(ctx, entry.SubscriptionID)
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

// deadLetter is the one terminal write for failures that never produced
// an HTTP outcome (configuration errors, a deleted subscription, a
// rejected target, an over-long circuit hold). It writes an
// event_delivery_log row carrying the reason, so the delivery-history
// API always explains a dead row, then moves the row to 'dead' — the
// existing terminal status (no schema change), which retention reaps and
// the replay endpoint can re-queue. reason must already be safe to show
// to a subscription reader (no URL path/query, no resolved address, no
// infrastructure error text).
func (d *WebhookDispatcher) deadLetter(ctx context.Context, entry store.DeliveryEntry, reason string) {
	logID, _ := generateDeliveryID()
	zero := 0
	_ = d.store.LogDeliveryAttempt(ctx, &store.DeliveryLogEntry{
		LogID:          logID,
		DeliveryID:     entry.DeliveryID,
		SubscriptionID: entry.SubscriptionID,
		EventID:        entry.EventID,
		DurationMs:     &zero,
		Error:          reason,
		CreatedAt:      d.clock().UTC(),
	})
	_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "dead", reason, nil)
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

// computeHMAC returns the hex-encoded HMAC-SHA256 of the payload — the
// v1 signature without its "sha256=" prefix. Kept as a thin wrapper over
// SignV1 so there is one MAC implementation.
func computeHMAC(payload []byte, secret string) string {
	return strings.TrimPrefix(SignV1(secret, payload), "sha256=")
}
