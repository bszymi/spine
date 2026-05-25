package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// EventLogNotifyChannel is the Postgres NOTIFY channel name on which
// out-of-process event_log writers (today: SMP's
// spinecore.EventEmitter, INIT-011/EPIC-003/TASK-006 sub-PR 10b)
// publish event IDs after their event_log insert commits. Spine's
// in-process writers (DeliverySubscriber.handleEvent) do NOT publish
// on this channel because they already feed the in-process
// EventBroadcaster directly; doing both would double-fan-out every
// in-process event to connected SSE clients.
const EventLogNotifyChannel = "spine_event_log"

// EventFetcher fetches a previously-written event_log row by its
// event_id and returns the reconstructed domain.Event. Constructed
// once per runSession by a factory bound to the listener's dedicated
// LISTEN connection — fetching via that same conn (rather than re-
// acquiring from the pool) keeps the bridge functional on pools as
// small as max_conns=1, where the dedicated LISTEN connection IS the
// whole pool.
type EventFetcher interface {
	GetEventByID(ctx context.Context, eventID string) (*domain.Event, error)
}

// EventFetcherFactory builds a per-session EventFetcher around the
// listener's dedicated *pgx.Conn. Default factory is DefaultConnFetcher,
// which issues `SELECT payload FROM runtime.event_log` on the bound
// connection. Tests inject a custom factory via WithFetcherFactory to
// keep handleNotification testable without a real DB.
type EventFetcherFactory func(*pgx.Conn) EventFetcher

// NotifyListener bridges out-of-process event_log writes into Spine's
// in-process EventBroadcaster. It opens a single dedicated pgx
// connection — outside the shared store pool — issuing LISTEN on the
// configured channel, decodes the event_id carried in each notification,
// fetches the corresponding runtime.event_log row on the same dedicated
// connection, and forwards the reconstructed domain.Event to the
// broadcaster.
//
// The listener takes a *pgxpool.Pool only to derive the connection
// config (ConnString, runtime params); it never holds a pool slot.
// This is the load-bearing fix for codex sub-PR 10a round 4 P2:
// holding even one pool slot for the lifetime of LISTEN would starve
// the entire deployment on a max_conns=1 pool, hanging HTTP handlers,
// the scheduler, and webhook dispatch the moment the bridge is
// enabled.
//
// Scope: SSE live-broadcast only. Webhook subscribers are already
// fanned out by the writer (SMP's spinecore.EventEmitter writes
// runtime.event_delivery_queue in the same tx as runtime.event_log);
// reconnect-replay clients fetch by Last-Event-ID directly from
// event_log. This listener exists solely so connected /events/stream
// clients see SMP-originated events without waiting for the reconnect
// interval (1–3s) to pass.
//
// Failure model: a dropped connection or transient fetch error logs
// and retries with exponential backoff. The listener never propagates
// an error to the caller because the loss of the live-broadcast bridge
// must NOT take down the gateway — the seam doc's accepted
// transitional behaviour (reconnect-replay) remains the fallback.
type NotifyListener struct {
	pool         *pgxpool.Pool
	broadcaster  *EventBroadcaster
	fetchFactory EventFetcherFactory
	channel      string
	clock        func() time.Time

	// attached, when non-nil, is signalled (close) the first time
	// runSession completes its LISTEN handshake. Test-only — production
	// callers don't set it, so the listener has no observable lifecycle
	// hook beyond Run's blocking semantics. Used by the integration
	// test to drop the flaky "issue a probe NOTIFY in a polling loop"
	// handshake in favour of a deterministic signal: LISTEN is
	// synchronous on the wire, so by the time this channel closes the
	// listener is provably ready to receive notifications. Resetting
	// the field across reconnects would make the signal unreliable;
	// we close it exactly once.
	attached     chan struct{}
	attachedOnce sync.Once
}

// NewNotifyListener constructs a listener that subscribes to the
// canonical EventLogNotifyChannel and fetches events on the same
// dedicated connection it issued LISTEN on. Tests can use a custom
// channel name via WithChannel and a custom fetcher factory via
// WithFetcherFactory.
func NewNotifyListener(pool *pgxpool.Pool, broadcaster *EventBroadcaster) *NotifyListener {
	return &NotifyListener{
		pool:         pool,
		broadcaster:  broadcaster,
		fetchFactory: DefaultConnFetcher,
		channel:      EventLogNotifyChannel,
		clock:        time.Now,
	}
}

// WithChannel overrides the LISTEN channel. Only the tests use this;
// production wiring relies on the canonical channel name so a single
// SMP-side `pg_notify(EventLogNotifyChannel, …)` reaches every Spine
// instance subscribed to the same runtime DB.
func (l *NotifyListener) WithChannel(channel string) *NotifyListener {
	l.channel = channel
	return l
}

// WithAttachedSignal returns a channel that is closed the first time
// runSession completes its LISTEN handshake. Test-only — production
// callers don't use this. The signal fires exactly once across the
// listener's lifetime; reconnects after the first attach do not
// re-fire because tests only need to know "ready to receive at least
// one notification."
func (l *NotifyListener) WithAttachedSignal() <-chan struct{} {
	if l.attached == nil {
		l.attached = make(chan struct{})
	}
	return l.attached
}

// WithFetcherFactory overrides the per-session fetcher factory.
// Production wiring uses DefaultConnFetcher, which fetches via the
// same conn the listener already holds for LISTEN — required for
// correctness on pools with max_conns=1. Tests inject a static
// in-memory fetcher to exercise handleNotification without a DB.
func (l *NotifyListener) WithFetcherFactory(f EventFetcherFactory) *NotifyListener {
	l.fetchFactory = f
	return l
}

// Run blocks until ctx is cancelled, consuming notifications and
// broadcasting reconstructed events. Returns ctx.Err() at shutdown
// (always context.Canceled or context.DeadlineExceeded — not a
// wrapped variant) so callers can use plain equality to recognise
// clean shutdowns. All in-flight errors (acquire / listen / wait /
// fetch) are logged and the loop retries — this is the live-broadcast
// bridge, not a transactional guarantee, and the reconnect-replay
// path covers anything lost to a temporary outage.
func (l *NotifyListener) Run(ctx context.Context) error {
	log := observe.Logger(ctx)
	log.Info("event notify listener starting", "channel", l.channel)

	const (
		backoffMin = 250 * time.Millisecond
		backoffMax = 30 * time.Second
	)
	backoff := backoffMin

	for {
		if err := ctx.Err(); err != nil {
			log.Info("event notify listener stopping", "channel", l.channel, "reason", err)
			return err
		}

		attached, err := l.runSession(ctx)
		// runSession wraps every error (`listen "%s": %w`, `wait for
		// notification: %w`, etc.), so a shutdown-during-WaitForNotification
		// returns a wrapped context.Canceled. Honor the contract by
		// returning ctx.Err() verbatim whenever the context says we're
		// done, regardless of how the wrapped error spells it.
		if ctxErr := ctx.Err(); ctxErr != nil {
			log.Info("event notify listener stopping", "channel", l.channel, "reason", ctxErr)
			return ctxErr
		}
		// A successful LISTEN attach during this session is conclusive
		// evidence the connection was healthy at least once: reset the
		// backoff so a fresh outage starts from backoffMin rather than
		// inheriting a stale 30s delay from a previous incident. Without
		// this reset, a deployment that flaps once and then recovers
		// would stay on the maximum backoff forever, defeating the
		// live-bridge purpose of the listener.
		if attached {
			backoff = backoffMin
		}
		if err != nil {
			log.Warn("event notify listener session failed; backing off",
				"channel", l.channel, "error", err, "backoff", backoff)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		// Exponential backoff with a hard cap. Reset is driven by
		// "attached at least once" above, not by per-notification
		// success — a session that attaches and immediately drops still
		// proves the conn handshake works.
		if backoff < backoffMax {
			backoff *= 2
			if backoff > backoffMax {
				backoff = backoffMax
			}
		}
	}
}

// runSession opens a single dedicated connection (outside the shared
// pool), issues LISTEN, builds a conn-bound EventFetcher, and loops
// on WaitForNotification until the connection or context fails.
// Returns (attached, err) where attached reports whether the LISTEN
// handshake succeeded at least once during this session. The Run loop
// uses that signal to reset the exponential backoff so a session that
// proved the conn healthy starts the next reconnect from backoffMin.
//
// The fetcher binds to the dedicated conn so we never re-enter any
// pool from the LISTEN handler — that re-entry was the round-2
// deadlock; opening outside the pool entirely was the round-4 fix to
// the broader starvation problem (LISTEN tied up the pool's only slot
// for the lifetime of the listener, blocking every other PostgresStore
// caller).
func (l *NotifyListener) runSession(ctx context.Context) (bool, error) {
	// pgxpool.Pool.Config().ConnConfig is a deep copy of the underlying
	// *pgx.ConnConfig, so mutating it (none of the code below does) would
	// not affect the pool. pgx.ConnectConfig opens a fresh connection
	// using the same DSN/runtime params the pool was built from — not
	// from the pool itself. This is the canonical pattern for LISTEN
	// against a pgxpool-backed deployment.
	conn, err := pgx.ConnectConfig(ctx, l.pool.Config().ConnConfig)
	if err != nil {
		return false, fmt.Errorf("connect dedicated listener: %w", err)
	}
	defer func() {
		// Use a fresh context for Close — the outer ctx may already be
		// canceled when we get here, which would cause the close to
		// short-circuit and leave the server-side conn lingering until
		// the next backend timeout. A 5s budget is more than enough for
		// a clean TLS teardown on any healthy network.
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	// LISTEN is per-session; SQL identifiers can't be parameterised so
	// the channel name is interpolated. The channel is hard-coded in
	// the package (EventLogNotifyChannel) or set via the test-only
	// WithChannel; no user input ever lands here. Even so, the quote
	// pattern below mirrors pgx.Identifier{l.channel}.Sanitize() (we
	// avoid the import only because the identifier is a fixed literal
	// in production).
	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", quoteIdent(l.channel))); err != nil {
		return false, fmt.Errorf("listen %q: %w", l.channel, err)
	}

	log := observe.Logger(ctx)
	log.Info("event notify listener attached", "channel", l.channel)
	if l.attached != nil {
		l.attachedOnce.Do(func() { close(l.attached) })
	}

	fetcher := l.fetchFactory(conn)

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return true, fmt.Errorf("wait for notification: %w", err)
		}
		l.handleNotification(ctx, fetcher, notification)
	}
}

// handleNotification dispatches a single NOTIFY payload. The payload
// format is documented at the package boundary (sub-PR 10b doc): a
// JSON object {"event_id":"<id>"} OR a bare event_id string. We
// support both shapes because the SMP-side emitter ships a JSON
// envelope for forward-extensibility while operators can fire
// `NOTIFY spine_event_log, 'evt-…'` from psql as a manual diagnostic.
//
// fetcher is per-session and bound to the same conn the listener
// issued LISTEN on (see runSession). Passing it in rather than reading
// from l.* makes the unit test trivially injectable and reinforces
// the conn-binding invariant — a future refactor that adds a
// background fetch goroutine should plumb fetcher through to its
// goroutine, not capture it implicitly.
func (l *NotifyListener) handleNotification(ctx context.Context, fetcher EventFetcher, n *pgconnNotification) {
	log := observe.Logger(ctx)
	eventID := parseNotifyPayload(n.Payload)
	if eventID == "" {
		log.Warn("event notify listener: empty event_id in payload", "raw", n.Payload)
		return
	}

	evt, err := fetcher.GetEventByID(ctx, eventID)
	if err != nil {
		log.Warn("event notify listener: fetch event failed",
			"event_id", eventID, "error", err)
		return
	}
	if evt == nil {
		// Race: NOTIFY arrived before the row was visible to a fresh
		// transaction. Skip and rely on the reconnect-replay path to
		// deliver this one. Logged at debug because it is expected
		// under heavy concurrency and not actionable.
		log.Debug("event notify listener: event_id not found in event_log",
			"event_id", eventID)
		return
	}

	l.broadcaster.Broadcast(*evt)
}

// DefaultConnFetcher builds the production EventFetcher: a thin wrapper
// around the dedicated LISTEN connection that issues `SELECT payload
// FROM runtime.event_log WHERE event_id = $1` and unmarshals the
// stored event. Exported so tests that want the real query against a
// real DB can use it without re-implementing the SQL.
func DefaultConnFetcher(conn *pgx.Conn) EventFetcher {
	return &connEventFetcher{conn: conn}
}

type connEventFetcher struct {
	conn *pgx.Conn
}

func (f *connEventFetcher) GetEventByID(ctx context.Context, eventID string) (*domain.Event, error) {
	var payload []byte
	err := f.conn.QueryRow(ctx,
		`SELECT payload FROM runtime.event_log WHERE event_id = $1`,
		eventID,
	).Scan(&payload)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query event_log: %w", err)
	}
	var evt domain.Event
	if err := json.Unmarshal(payload, &evt); err != nil {
		return nil, fmt.Errorf("decode event payload for %q: %w", eventID, err)
	}
	return &evt, nil
}

// pgconnNotification is the shape WaitForNotification returns. Aliased
// to a package-local type so the test in this package can construct
// notifications without importing pgconn (which would couple the test
// to the pgx version pin).
type pgconnNotification = pgconn.Notification

// parseNotifyPayload accepts either a JSON envelope `{"event_id":"…"}`
// or a bare event_id string. Returns the empty string when neither
// shape yields a value so the caller can log + drop.
func parseNotifyPayload(raw string) string {
	if raw == "" {
		return ""
	}
	if raw[0] == '{' {
		var env struct {
			EventID string `json:"event_id"`
		}
		if err := json.Unmarshal([]byte(raw), &env); err == nil {
			return env.EventID
		}
		return ""
	}
	return raw
}

// quoteIdent double-quotes a SQL identifier, escaping embedded quotes
// per the SQL standard. Used here for the LISTEN channel name; pgx's
// Identifier.Sanitize is functionally equivalent and we duplicate the
// minimal logic to avoid importing the helper for a single literal.
func quoteIdent(name string) string {
	out := make([]byte, 0, len(name)+2)
	out = append(out, '"')
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '"' {
			out = append(out, '"', '"')
			continue
		}
		out = append(out, c)
	}
	out = append(out, '"')
	return string(out)
}
