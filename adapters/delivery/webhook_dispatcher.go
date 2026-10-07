package delivery

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
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
	// maxCircuitHold bounds how long an open circuit may keep deferring
	// a delivery, measured from the row's created_at.
	maxCircuitHold time.Duration

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

	// MaxCircuitHold bounds how long a delivery may be held without a
	// terminal outcome because its subscription cannot be delivered to —
	// an open circuit, or a subscription that cannot be read (database,
	// decrypt) — measured from the row's created_at. A held row older
	// than this is dead-lettered (and stays replayable) instead of being
	// held again. Zero selects DefaultMaxCircuitHold.
	MaxCircuitHold time.Duration
}

// DefaultMaxCircuitHold is the MaxCircuitHold used when the config
// leaves it zero (INIT-023 TASK-010 owner question 1, default pending
// the owner: 24 hours).
const DefaultMaxCircuitHold = 24 * time.Hour

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
	if cfg.MaxCircuitHold <= 0 {
		cfg.MaxCircuitHold = DefaultMaxCircuitHold
	}

	return &WebhookDispatcher{
		store:          st,
		resolver:       resolver,
		client:         cfg.Targets.HTTPClient(cfg.HTTPTimeout),
		httpTimeout:    cfg.HTTPTimeout,
		concurrency:    cfg.Concurrency,
		pollInterval:   cfg.PollInterval,
		breaker:        NewCircuitBreaker(),
		maxCircuitHold: cfg.MaxCircuitHold,
		tlsClients:     make(map[string]*tlsClientEntry),
		targets:        cfg.Targets,
		now:            time.Now,
		rand:           rand.Reader,
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
//
// Every claimed row leaves deliver in one of three states (INIT-023
// TASK-010 invariant I3, audit P3-5): 'delivered'; 'dead' (terminal,
// reaped by retention, replayable); or 'failed'/'pending' with
// next_retry_at strictly in the future. Before this, four exits wrote a
// row back as immediately claimable (non-retryable 4xx, circuit open,
// resolver error, rejected URL / request build error); ClaimDeliveries
// re-claimed them on every poll, ahead of newer rows, so a handful of
// such rows hot-looped and starved every other subscription. (The one
// gap: a status write that is itself lost leaves the row 'delivering';
// recovering that needs a claimed_at column, a schema change.)
func (d *WebhookDispatcher) deliver(ctx context.Context, entry store.DeliveryEntry) {
	log := observe.Logger(ctx).With(
		"delivery_id", entry.DeliveryID,
		"subscription_id", entry.SubscriptionID,
		"event_type", entry.EventType,
	)

	// Circuit breaker check. The permit must be resolved on every exit
	// path: the HTTP outcomes call Success/Failure, and the deferred
	// Release resolves a half-open probe that exits early (resolver,
	// URL, request-build or signing failure) so it cannot wedge the
	// subscription's circuit half-open (P3-12).
	permit, ok := d.breaker.Acquire(ctx, entry.SubscriptionID)
	if !ok {
		d.holdForCircuit(ctx, log, entry)
		return
	}
	defer permit.Release(ctx)

	sub, err := d.resolver.GetSubscription(ctx, entry.SubscriptionID)
	if err != nil {
		var se *domain.SpineError
		switch {
		case errors.As(err, &se) && se.Code == domain.ErrNotFound:
			// The subscription was deleted. Queue rows have no foreign
			// key to event_subscriptions and DeleteSubscription leaves
			// them behind, so without this the orphans would be
			// re-claimed forever. No retry can bring the row back.
			log.Warn("subscription no longer exists; dead-lettering delivery")
			d.deadLetter(ctx, entry, "subscription not found")
		case errors.Is(err, ErrInvalidSubscriptionMetadata):
			// Permanent configuration error (unknown signature_version,
			// malformed metadata): no retry can fix it. The parse reason
			// names keys and values the operator wrote, never
			// infrastructure detail.
			log.Warn("subscription metadata invalid; dead-lettering delivery", "error", err.Error())
			d.deadLetter(ctx, entry, err.Error())
		default:
			// Transient (DB, decrypt, pool): retried with backoff and
			// never dead-lettered for it before MaxCircuitHold (see
			// retryUnresolved). The error columns get a fixed reason —
			// pgconn connect errors name the database host:port, user and
			// database, and last_error is shown to subscription readers
			// (and to SMP workspace members in the run page) — so the
			// full error goes to the operator log only.
			log.Error("failed to resolve subscription", "error", err.Error())
			d.retryUnresolved(ctx, log, entry)
		}
		return
	}

	// Re-validate the persisted URL before every delivery so
	// subscriptions predating the SSRF hardening are rejected here,
	// not at the socket. Failures are permanent: dead-letter.
	//
	// From here on every error text that reaches a log line or an error
	// column goes through RedactError: the target URL's path and query
	// may carry the receiver's credential (P3-26).
	if err := d.targets.ValidateURL(sub.TargetURL); err != nil {
		msg := RedactError(err, sub.TargetURL)
		log.Warn("webhook target_url rejected by validator", "error", msg)
		d.deadLetter(ctx, entry, msg)
		return
	}

	// Build request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.TargetURL, bytes.NewReader(entry.Payload))
	if err != nil {
		msg := RedactError(err, sub.TargetURL)
		log.Error("failed to build request", "error", msg)
		d.deadLetter(ctx, entry, msg)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Spine-Event", entry.EventType)
	req.Header.Set("X-Spine-Delivery", entry.DeliveryID)

	// Execute. Per-subscription TLS (pinned SPKI / custom CA) — if
	// configured — uses a dedicated client cached by subscription ID.
	client, err := d.clientFor(sub)
	if err != nil {
		msg := RedactError(err, sub.TargetURL)
		log.Error("subscription TLS config invalid", "error", msg)
		permit.Failure(ctx)
		tlsLogID, _ := generateDeliveryID()
		d.handleFailure(ctx, entry, tlsLogID, 0, 0, false, msg, nil)
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
		// client.Do errors are *url.Error, whose text is the full
		// request (or redirect) URL. Never store or log err.Error().
		msg := RedactError(err, sub.TargetURL)
		log.Error("webhook delivery failed", "error", msg, "duration_ms", durationMs)
		permit.Failure(ctx)
		d.handleFailure(ctx, entry, logID, durationMs, 0, true, msg, nil)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	statusCode := resp.StatusCode

	if statusCode >= 200 && statusCode < 300 {
		log.Info("webhook delivered", "status", statusCode, "duration_ms", durationMs)
		permit.Success(ctx)
		_ = d.store.MarkDelivered(ctx, entry.DeliveryID)
	} else {
		errMsg := fmt.Sprintf("HTTP %d", statusCode)
		retryable := isRetryable(statusCode, false)
		if retryable {
			log.Warn("webhook delivery failed (retryable)", "status", statusCode, "duration_ms", durationMs)
		} else {
			log.Warn("webhook delivery rejected (permanent)", "status", statusCode, "duration_ms", durationMs)
		}
		permit.Failure(ctx)
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

// resolveUnavailableReason is the fixed last_error / delivery-log text for
// a transient subscription-resolver failure (see deliver).
const resolveUnavailableReason = "resolve subscription: temporarily unavailable"

// circuitOpenReason is the last_error of a delivery the circuit holds.
const circuitOpenReason = "circuit open"

// resolveRetryInterval is how often a delivery whose subscription cannot
// be read is retried once its attempt budget is spent (see
// retryUnresolved).
const resolveRetryInterval = time.Minute

// holdCap is the effective MaxCircuitHold.
func (d *WebhookDispatcher) holdCap() time.Duration {
	if d.maxCircuitHold <= 0 {
		return DefaultMaxCircuitHold
	}
	return d.maxCircuitHold
}

// heldTooLong reports whether entry has been queued for longer than
// maxHold. A row with no created_at is never treated as aged out.
func (d *WebhookDispatcher) heldTooLong(entry store.DeliveryEntry, maxHold time.Duration) bool {
	return !entry.CreatedAt.IsZero() && d.clock().Sub(entry.CreatedAt) > maxHold
}

// heldTooLongReason is the dead-letter reason of a row held past the cap:
// "circuit open: held longer than 24h0m0s".
func heldTooLongReason(reason string, maxHold time.Duration) string {
	return reason + ": held longer than " + maxHold.String()
}

// holdForCircuit handles a delivery the circuit breaker refused (open,
// or half-open with the probe in flight). Nothing was sent, so no
// attempt is consumed: the row is deferred to the circuit's next probe
// time (or one poll interval while a probe is in flight) through
// DeferDelivery. Before this the row went back to 'pending' with no
// next_retry_at and a bumped attempt_count, so ClaimDeliveries re-claimed
// it on every poll (a hot loop starving other subscriptions) and each
// hold burned one of its retries.
//
// The rest of the subscription's due backlog is held to the same instant
// in one statement (holdBacklog), so a large held backlog is not
// claimable between probes and cannot take claim slots from other
// subscriptions, however many subscriptions are held.
//
// A row held longer than MaxCircuitHold (measured from created_at — the
// queue has no "held since" column) is dead-lettered instead, so a
// permanently failing endpoint cannot keep rows non-terminal forever.
// It stays replayable. A row with no created_at is never treated as
// aged out.
func (d *WebhookDispatcher) holdForCircuit(ctx context.Context, log *slog.Logger, entry store.DeliveryEntry) {
	maxHold := d.holdCap()
	until := d.breaker.NextAttemptAt(entry.SubscriptionID, d.pollInterval)
	if d.heldTooLong(entry, maxHold) {
		log.Warn("circuit held delivery past MaxCircuitHold; dead-lettering", "max_circuit_hold", maxHold.String())
		d.deadLetter(ctx, entry, heldTooLongReason(circuitOpenReason, maxHold))
	} else {
		log.Debug("circuit open, deferring delivery", "until", until)
		if err := d.store.DeferDelivery(ctx, entry.DeliveryID, circuitOpenReason, until); err != nil {
			log.Error("failed to defer circuit-held delivery", "error", err)
		}
	}
	d.holdBacklog(ctx, log, entry.SubscriptionID, circuitOpenReason, until)
}

// retryUnresolved handles a transient failure to read the delivery's
// subscription (database, pool, decrypt).
//
// The claimed row is retried like an endpoint failure — an attempt is
// recorded and the backoff schedule applies (1, 2, 4, 8 s), so once the
// budget is spent the row is 'failed' with attempt_count at the budget,
// which is what an embedder's stuck-delivery view (SMP's run banner)
// shows — but it is never dead-lettered for this before MaxCircuitHold:
// past the budget it is retried every resolveRetryInterval and is
// delivered as soon as the subscription can be read again. A resolver
// fault (a missing or rotated decryption key on one replica, a database
// blip) is an operator-side condition, not the endpoint's verdict; before
// INIT-023 TASK-010 such rows were retried on every poll until the fault
// cleared, and dead-lettering them after ~15 s would have turned a
// fixable misconfiguration into lost deliveries that each need a manual
// replay. Rows older than MaxCircuitHold are dead-lettered, as for an
// open circuit.
//
// The subscription's other due rows are held to the same retry instant
// without consuming attempts (holdBacklog), so an unreadable
// subscription takes about one claim slot per retry, however large its
// backlog.
func (d *WebhookDispatcher) retryUnresolved(ctx context.Context, log *slog.Logger, entry store.DeliveryEntry) {
	maxHold := d.holdCap()
	if d.heldTooLong(entry, maxHold) {
		log.Warn("subscription unreadable past MaxCircuitHold; dead-lettering", "max_circuit_hold", maxHold.String())
		d.deadLetter(ctx, entry, heldTooLongReason(resolveUnavailableReason, maxHold))
		d.holdBacklog(ctx, log, entry.SubscriptionID, resolveUnavailableReason, d.clock().Add(resolveRetryInterval))
		return
	}
	delay := nextRetryDelay(entry.AttemptCount)
	if entry.AttemptCount+1 >= maxRetriesFor(entry.EventType) {
		delay = resolveRetryInterval
	}
	retryAt := d.clock().Add(delay)
	logID, _ := generateDeliveryID()
	zero := 0
	_ = d.store.LogDeliveryAttempt(ctx, &store.DeliveryLogEntry{
		LogID:          logID,
		DeliveryID:     entry.DeliveryID,
		SubscriptionID: entry.SubscriptionID,
		EventID:        entry.EventID,
		DurationMs:     &zero,
		Error:          resolveUnavailableReason,
		CreatedAt:      d.clock().UTC(),
	})
	log.Info("subscription unreadable; scheduling retry", "attempt", entry.AttemptCount+1, "retry_at", retryAt)
	_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "failed", resolveUnavailableReason, &retryAt)
	d.holdBacklog(ctx, log, entry.SubscriptionID, resolveUnavailableReason, retryAt)
}

// holdBacklog takes the subscription's other due deliveries off the
// claimable set until `until`, in one attempt-free statement
// (DeliveryStore.HoldDueDeliveries); those older than MaxCircuitHold are
// dead-lettered there and get their delivery-log row here.
//
// Why: the fair claim gives each subscription's oldest due row a slot
// ahead of any newer row. If only the rows the dispatcher was handed were
// deferred, a held subscription would keep an old due row on every poll,
// and once `concurrency` subscriptions were held with a backlog of more
// than about a minute of polls each, they would take every claim slot
// and starve every other subscription (INIT-023 TASK-010 review round 1;
// I7). After a hold such a subscription has nothing claimable until
// `until`, so it costs about two slots per probe cycle (the probe, and
// the first hold after it) however many rows it holds.
//
// Embedders: with several dispatchers on one queue (SMP replicas), a
// replica whose circuit is open holds the subscription's due rows for
// every replica until its probe time. A replica-local fault therefore
// delays the subscription's due backlog by up to the recovery timeout
// per probe cycle; rows that arrive after the hold are claimable at once.
func (d *WebhookDispatcher) holdBacklog(ctx context.Context, log *slog.Logger, subscriptionID, reason string, until time.Time) {
	maxHold := d.holdCap()
	expiredReason := heldTooLongReason(reason, maxHold)
	expired, err := d.store.HoldDueDeliveries(ctx, store.DeliveryHold{
		SubscriptionID: subscriptionID,
		Until:          until,
		Reason:         reason,
		ExpireBefore:   d.clock().Add(-maxHold),
		ExpiredReason:  expiredReason,
	})
	if err != nil {
		log.Error("failed to hold the subscription's due deliveries", "error", err)
		return
	}
	for i := range expired {
		d.logDeadLetter(ctx, expired[i], expiredReason)
	}
	if len(expired) > 0 {
		log.Warn("held deliveries past MaxCircuitHold dead-lettered", "count", len(expired), "max_circuit_hold", maxHold.String())
	}
}

// deadLetter is the one terminal write for failures that never produced
// an HTTP outcome (configuration errors, a deleted subscription, a
// rejected target, an over-long hold). It writes an event_delivery_log
// row carrying the reason, so the delivery-history API always explains a
// dead row, then moves the row to 'dead' — the existing terminal status
// (no schema change), which retention reaps and the replay endpoint can
// re-queue. reason must already be safe to show to a subscription reader
// (no URL path/query, no resolved address, no infrastructure error text).
func (d *WebhookDispatcher) deadLetter(ctx context.Context, entry store.DeliveryEntry, reason string) {
	d.logDeadLetter(ctx, entry, reason)
	_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "dead", reason, nil)
}

// logDeadLetter writes the delivery-log row that explains a dead-lettered
// delivery: no status code, the reason as the error.
func (d *WebhookDispatcher) logDeadLetter(ctx context.Context, entry store.DeliveryEntry, reason string) {
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

	// Non-retryable or out of budget: 'dead'. A non-retryable outcome
	// used to be written as 'failed' with no next_retry_at, which
	// ClaimDeliveries re-claims immediately — every permanent 4xx
	// hot-looped forever (P3-5). 'dead' is terminal, reaped by
	// retention, and replayable.
	if !retryable || newAttemptCount >= maxRetries {
		log.Warn("delivery exhausted", "delivery_id", entry.DeliveryID, "status", "dead", "retryable", retryable, "attempts", newAttemptCount)
		_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "dead", errMsg, nil)
		return
	}

	// Schedule retry with backoff
	delay := nextRetryDelay(newAttemptCount - 1)
	if statusCode == 429 {
		if ra := retryAfterFromHeader(resp); ra > 0 {
			delay = ra
		}
	}
	retryAt := d.clock().Add(delay)
	log.Info("scheduling retry", "delivery_id", entry.DeliveryID, "attempt", newAttemptCount, "retry_at", retryAt)
	_ = d.store.UpdateDeliveryStatus(ctx, entry.DeliveryID, "failed", errMsg, &retryAt)
}

// computeHMAC returns the hex-encoded HMAC-SHA256 of the payload — the
// v1 signature without its "sha256=" prefix. Kept as a thin wrapper over
// SignV1 so there is one MAC implementation.
func computeHMAC(payload []byte, secret string) string {
	return strings.TrimPrefix(SignV1(secret, payload), "sha256=")
}
