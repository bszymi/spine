package workspace

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// PoolPolicy is the per-workspace connection-pool policy from
// ADR-012. Zero-valued fields fall back to the documented defaults
// (PoolPolicyDefault).
type PoolPolicy struct {
	// MinConns is the minimum number of connections the pool keeps
	// open. Default 2.
	MinConns int32

	// MaxConns is the upper bound on simultaneously-acquired
	// connections. Default 10. Per ADR-012, Spine and the platform
	// share a global RDS connection budget via PgBouncer; this
	// number is sized for the per-workspace PgBouncer pool, not the
	// raw RDS limit.
	MaxConns int32

	// AcquireTimeout caps how long Acquire will wait for a free
	// connection before failing. Default 5s.
	AcquireTimeout time.Duration

	// HealthCheckPeriod is how often pgxpool runs a health check on
	// idle connections. Default 30s.
	HealthCheckPeriod time.Duration

	// QueueSize is the bound on requests waiting for a connection
	// once the pool is at MaxConns. The 51st waiter (with the
	// default 50) fails fast with ErrPoolSaturated rather than
	// blocking. Default 50.
	QueueSize int
}

// PoolPolicyDefault returns the ADR-012 defaults.
func PoolPolicyDefault() PoolPolicy {
	return PoolPolicy{
		MinConns:          2,
		MaxConns:          10,
		AcquireTimeout:    5 * time.Second,
		HealthCheckPeriod: 30 * time.Second,
		QueueSize:         50,
	}
}

// withDefaults merges p with PoolPolicyDefault so callers can supply
// only the fields they want to override.
func (p PoolPolicy) withDefaults() PoolPolicy {
	d := PoolPolicyDefault()
	if p.MinConns > 0 {
		d.MinConns = p.MinConns
	}
	if p.MaxConns > 0 {
		d.MaxConns = p.MaxConns
	}
	if p.AcquireTimeout > 0 {
		d.AcquireTimeout = p.AcquireTimeout
	}
	if p.HealthCheckPeriod > 0 {
		d.HealthCheckPeriod = p.HealthCheckPeriod
	}
	if p.QueueSize > 0 {
		d.QueueSize = p.QueueSize
	}
	return d
}

// ErrPoolSaturated is returned by WorkspaceDBPool.Acquire when the
// per-workspace bounded queue is full. Callers should map this to
// HTTP 503 (Retry-After) so the API surface is honest about the
// transient overload.
var ErrPoolSaturated = errors.New("workspace pool saturated")

// BuildPgxpoolConfig parses databaseURL and applies the policy. The
// returned *pgxpool.Config is suitable for pgxpool.NewWithConfig.
//
// A parse error never carries the connection string (INIT-024 TASK-007,
// review round 1): pgconn's ParseConfigError quotes it, and redacts only a
// userinfo password — a password given as a query parameter
// (`?password=`, `sslpassword=`) is printed as is. Every caller logs or
// returns this error: the pool's build error reaches the shared-mode
// request path, MultiProjectionSync logs it at Error on every poll, and the
// workspace endpoints surface it. connStringFreeError keeps the reason and
// drops the string.
func BuildPgxpoolConfig(databaseURL string, policy PoolPolicy) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", connStringFreeError(err, databaseURL))
	}
	policy = policy.withDefaults()
	cfg.MinConns = policy.MinConns
	cfg.MaxConns = policy.MaxConns
	cfg.HealthCheckPeriod = policy.HealthCheckPeriod
	return cfg, nil
}

// WorkspaceDBPool wraps a *pgxpool.Pool with the bounded-queue
// saturation gate from ADR-012. Wrapping happens at the Acquire
// boundary; callers that go straight to pgxpool.Pool.Query bypass
// the gate (acceptable transitionally — TASK-007 layers eviction
// and invalidation on top of this primitive).
//
// WorkspaceDBPool is safe to call from multiple goroutines.
type WorkspaceDBPool struct {
	workspaceID string
	pool        *pgxpool.Pool
	policy      PoolPolicy

	// inFlight gates concurrent users. Capacity is MaxConns +
	// QueueSize: up to MaxConns may be holding a connection, and up
	// to QueueSize more may be waiting. Beyond that, Acquire fails
	// fast. The gate is a counting semaphore implemented via a
	// buffered channel.
	inFlight chan struct{}

	// closed flips to 1 on Close so Acquire becomes a hard error
	// instead of trying to use a torn-down pool.
	closed atomic.Bool
}

// NewWorkspaceDBPool builds the pool from databaseURL using the
// given policy. The pool is registered with the global pool
// registry so per-workspace metrics can be emitted by the
// /metrics endpoint.
//
// ctx bounds the startup only — the ping, and the first minimum
// connections pgx opens in the background (its health check tops them up
// later on its own context) — so a service-set build passes its init
// deadline (INIT-024 TASK-008): a database that accepts the connection and
// never answers then fails the build instead of holding it, and closing
// the pool after the failed ping does not wait for connections that never
// finish.
func NewWorkspaceDBPool(ctx context.Context, workspaceID, databaseURL string, policy PoolPolicy) (*WorkspaceDBPool, error) {
	if workspaceID == "" {
		return nil, errors.New("workspace pool: workspaceID is required")
	}
	policy = policy.withDefaults()
	cfg, err := BuildPgxpoolConfig(databaseURL, policy)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open workspace pool: %w", connStringFreeError(err, databaseURL))
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping workspace pool: %w", connStringFreeError(err, databaseURL))
	}

	wp := &WorkspaceDBPool{
		workspaceID: workspaceID,
		pool:        pool,
		policy:      policy,
		inFlight:    make(chan struct{}, int(policy.MaxConns)+policy.QueueSize),
	}
	registerPool(wp)
	observe.PoolMetrics(workspaceID).Open.Inc()
	return wp, nil
}

// Pool returns the underlying *pgxpool.Pool. Use this to satisfy
// existing APIs that expect *pgxpool.Pool (notably PostgresStore).
// Direct use bypasses the saturation gate; reach for Acquire when
// you want the gate.
func (w *WorkspaceDBPool) Pool() *pgxpool.Pool { return w.pool }

// WorkspaceID returns the ID this pool was constructed with.
func (w *WorkspaceDBPool) WorkspaceID() string { return w.workspaceID }

// Policy returns the (defaults-merged) policy.
func (w *WorkspaceDBPool) Policy() PoolPolicy { return w.policy }

// enterGate acquires a saturation slot. Returns a domain.SpineError
// (code service_unavailable) wrapping ErrPoolSaturated when the
// bound is hit so the gateway maps it to HTTP 503 while callers
// can still errors.Is(err, ErrPoolSaturated). Otherwise hands back
// a release closure the caller MUST call. release is safe to call
// once.
func (w *WorkspaceDBPool) enterGate() (func(), error) {
	if w.closed.Load() {
		return nil, errors.New("workspace pool closed")
	}
	select {
	case w.inFlight <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-w.inFlight }) }, nil
	default:
		observe.PoolMetrics(w.workspaceID).Saturation.Inc()
		return nil, domain.NewErrorWithCause(
			domain.ErrUnavailable,
			fmt.Sprintf("workspace pool saturated: workspace=%q max=%d queue=%d", w.workspaceID, w.policy.MaxConns, w.policy.QueueSize),
			ErrPoolSaturated,
		)
	}
}

// acquireWithTimeout combines the saturation gate with a
// pgxpool.Acquire bounded by the configured AcquireTimeout. The
// returned release closure releases the connection AND the gate
// slot exactly once.
func (w *WorkspaceDBPool) acquireWithTimeout(ctx context.Context) (*pgxpool.Conn, func(), error) {
	release, err := w.enterGate()
	if err != nil {
		return nil, func() {}, err
	}
	acquireCtx, cancel := context.WithTimeout(ctx, w.policy.AcquireTimeout)
	conn, err := w.pool.Acquire(acquireCtx)
	cancel()
	if err != nil {
		release()
		return nil, func() {}, err
	}
	var once sync.Once
	combined := func() {
		once.Do(func() {
			conn.Release()
			release()
		})
	}
	return conn, combined, nil
}

// Begin starts a transaction through the saturation gate. The gate
// slot and connection are held until the returned pgx.Tx is
// committed or rolled back.
func (w *WorkspaceDBPool) Begin(ctx context.Context) (pgx.Tx, error) {
	conn, release, err := w.acquireWithTimeout(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		release()
		return nil, err
	}
	return &gatedTx{Tx: tx, release: release}, nil
}

// Query runs a query through the saturation gate. The gate slot
// and connection are held until the returned pgx.Rows is closed.
func (w *WorkspaceDBPool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	conn, release, err := w.acquireWithTimeout(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		release()
		return nil, err
	}
	return &gatedRows{Rows: rows, release: release}, nil
}

// QueryRow runs a single-row query through the saturation gate. The
// returned pgx.Row releases the slot and connection when its Scan
// completes (or fails). If the gate refuses the slot, the error
// surfaces on Scan to match pgx's QueryRow contract.
func (w *WorkspaceDBPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	conn, release, err := w.acquireWithTimeout(ctx)
	if err != nil {
		return errRow{err: err}
	}
	row := conn.QueryRow(ctx, sql, args...)
	return &gatedRow{Row: row, release: release}
}

// Exec runs a one-shot exec through the saturation gate. The slot
// is released as soon as Exec returns.
func (w *WorkspaceDBPool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	conn, release, err := w.acquireWithTimeout(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer release()
	return conn.Exec(ctx, sql, args...)
}

// Ping pings the pool through the saturation gate.
func (w *WorkspaceDBPool) Ping(ctx context.Context) error {
	conn, release, err := w.acquireWithTimeout(ctx)
	if err != nil {
		return err
	}
	defer release()
	return conn.Ping(ctx)
}

// gatedTx wraps pgx.Tx so Commit and Rollback release the gate slot
// and underlying connection. All other delegated methods run on the
// same transaction (same connection / same gate slot).
type gatedTx struct {
	pgx.Tx
	release func()
}

func (t *gatedTx) Commit(ctx context.Context) error {
	defer t.release()
	return t.Tx.Commit(ctx)
}

func (t *gatedTx) Rollback(ctx context.Context) error {
	defer t.release()
	return t.Tx.Rollback(ctx)
}

// gatedRows wraps pgx.Rows so Close releases the gate slot.
type gatedRows struct {
	pgx.Rows
	release func()
}

func (r *gatedRows) Close() {
	r.Rows.Close()
	r.release()
}

// gatedRow wraps pgx.Row so Scan releases the gate slot.
type gatedRow struct {
	pgx.Row
	release func()
	once    sync.Once
}

func (r *gatedRow) Scan(dest ...any) error {
	defer r.once.Do(r.release)
	return r.Row.Scan(dest...)
}

// errRow is returned by QueryRow when the gate refuses a slot. The
// error surfaces on Scan so callers see the saturation error
// through pgxpool.Pool's existing single-Scan call shape.
type errRow struct{ err error }

func (r errRow) Scan(_ ...any) error { return r.err }

// Acquire takes a connection through the saturation gate. The
// returned release function MUST be called even on error paths:
// it returns the gate slot. The pgxpool.Conn is non-nil only on
// success.
//
// Behaviour:
//   - Gate slot acquired immediately if room: fast path.
//   - No room (>= MaxConns + QueueSize concurrent users): returns
//     ErrPoolSaturated. The saturation counter is incremented.
//   - Slot acquired but connection wait exceeds AcquireTimeout:
//     returns context.DeadlineExceeded.
func (w *WorkspaceDBPool) Acquire(ctx context.Context) (*pgxpool.Conn, func(), error) {
	release, err := w.enterGate()
	if err != nil {
		return nil, func() {}, err
	}

	acquireCtx, cancel := context.WithTimeout(ctx, w.policy.AcquireTimeout)
	conn, err := w.pool.Acquire(acquireCtx)
	cancel()
	if err != nil {
		release()
		return nil, func() {}, err
	}

	// Caller must release the gate AND the conn. Compose into a
	// single release closure so a single defer covers both, and so
	// double-release is a no-op.
	var once sync.Once
	combined := func() {
		once.Do(func() {
			conn.Release()
			release()
		})
	}
	return conn, combined, nil
}

// Stat returns the current pool stats. Used by metric export.
func (w *WorkspaceDBPool) Stat() *pgxpool.Stat { return w.pool.Stat() }

// Close marks the pool closed and tears it down. Subsequent
// Acquire calls return an error rather than a closed-pool panic.
// The reason label is recorded on the close counter.
func (w *WorkspaceDBPool) Close(reason string) {
	if !w.closed.CompareAndSwap(false, true) {
		return
	}
	if w.pool != nil {
		w.pool.Close()
	}
	unregisterPool(w)
	observe.PoolMetrics(w.workspaceID).RecordClose(reason)
}

// poolRegistry tracks live WorkspaceDBPools so metric export can
// iterate them. The export reads pgxpool.Stat() on each, so live
// pool size / in-use / idle counts always reflect reality.
var poolRegistry = struct {
	mu    sync.RWMutex
	pools map[string]*WorkspaceDBPool
}{
	pools: make(map[string]*WorkspaceDBPool),
}

func registerPool(p *WorkspaceDBPool) {
	poolRegistry.mu.Lock()
	defer poolRegistry.mu.Unlock()
	if existing, ok := poolRegistry.pools[p.workspaceID]; ok && existing != p {
		// Old pool exists for this workspace ID — close it before
		// replacing. This handles re-build after invalidation
		// (TASK-007).
		existing.closed.Store(true)
		existing.pool.Close()
	}
	poolRegistry.pools[p.workspaceID] = p
}

func unregisterPool(p *WorkspaceDBPool) {
	poolRegistry.mu.Lock()
	defer poolRegistry.mu.Unlock()
	if existing, ok := poolRegistry.pools[p.workspaceID]; ok && existing == p {
		delete(poolRegistry.pools, p.workspaceID)
	}
}

// snapshotPoolGauges returns a snapshot of registered pools with
// their current stats. Used by metric export via the
// observe.SetPoolGaugeProvider hook.
func snapshotPoolGauges() []observe.PoolGaugeSnapshot {
	poolRegistry.mu.RLock()
	defer poolRegistry.mu.RUnlock()
	out := make([]observe.PoolGaugeSnapshot, 0, len(poolRegistry.pools))
	for id, p := range poolRegistry.pools {
		stat := p.pool.Stat()
		out = append(out, observe.PoolGaugeSnapshot{
			WorkspaceID:   id,
			TotalConns:    stat.TotalConns(),
			AcquiredConns: stat.AcquiredConns(),
			IdleConns:     stat.IdleConns(),
			MaxConns:      stat.MaxConns(),
		})
	}
	return out
}

func init() {
	observe.SetPoolGaugeProvider(snapshotPoolGauges)
}

// ErrInvalidDatabaseURL marks a workspace connection string pgx could not
// parse. The wrapping error carries pgx's reason, never the string itself.
var ErrInvalidDatabaseURL = errors.New("invalid database connection string")

// connStringFreeError returns err with nothing of databaseURL's
// credentials in its text (INIT-024 TASK-007, review round 1).
//
//   - A *pgconn.ParseConfigError ("cannot parse `<string>`: <reason>") is
//     replaced by ErrInvalidDatabaseURL with the reason only.
//   - Any error's text (the reason included) then has every credential the
//     string carries — userinfo password, password= and sslpassword= in
//     the query or in key=value form — and the string itself replaced by
//     "xxxxx". Connect and ping errors (pgconn.ConnectError names host,
//     user and database) normally carry none and keep their wrapping.
//   - The reason's quoted fragments of the string are replaced too
//     (maskQuotedFragments, INIT-024 TASK-013).
//
// A replaced error does not wrap the original: its Error() is the leak.
func connStringFreeError(err error, databaseURL string) error {
	if err == nil {
		return nil
	}
	secrets := connStringSecrets(databaseURL)
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		reason := ""
		msg := parseErr.Error()
		if i := strings.LastIndex(msg, "`: "); i >= 0 {
			reason = msg[i+len("`: "):]
		}
		reason = maskSecrets(maskQuotedFragments(reason, databaseURL), databaseURL, secrets)
		if reason == "" {
			return ErrInvalidDatabaseURL
		}
		return fmt.Errorf("%w: %s", ErrInvalidDatabaseURL, reason)
	}
	msg := err.Error()
	if masked := maskSecrets(msg, databaseURL, secrets); masked != msg {
		return errors.New(masked)
	}
	return err
}

// maskSecrets replaces the connection string and each credential in msg.
// Longer credentials go first, so a short one (a piece of a password)
// cannot break up a longer one and leave its remainder in the text.
func maskSecrets(msg, databaseURL string, secrets []string) string {
	if databaseURL != "" {
		msg = strings.ReplaceAll(msg, databaseURL, "xxxxx")
	}
	secrets = append([]string(nil), secrets...)
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, s := range secrets {
		msg = strings.ReplaceAll(msg, s, "xxxxx")
	}
	return msg
}

// quotedFragment matches a double-quoted segment of a pgx error's reason,
// %q-escaped or not.
var quotedFragment = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// maskQuotedFragments replaces each quoted segment of a parse error's
// reason that quotes text of databaseURL (INIT-024 TASK-013).
//
// pgx v5.11.0's parse errors quote the part of a malformed string they
// stopped at: a keyword read up to a space (`missing "=" after "…"`), a
// query pair without '=', a URI component that does not decode. In a
// malformed string that part can be a piece of a password — the text
// after an unquoted space or an unencoded '&', or the tail an unencoded
// '@' moved into the host — which connStringSecrets, reading the string
// as well-formed, does not list. A segment that is a parameter name
// (isParamName: "password" in `extra key/value separator "=" in URI query
// parameter: "password"`) is kept, as is one shorter than 3 bytes: neither
// is a value, and the name says what to fix.
func maskQuotedFragments(reason, databaseURL string) string {
	return quotedFragment.ReplaceAllStringFunc(reason, func(q string) string {
		texts := []string{q[1 : len(q)-1]}
		if u, err := strconv.Unquote(q); err == nil && u != texts[0] {
			texts = append(texts, u)
		}
		for _, s := range texts {
			if len(s) >= 3 && strings.Contains(databaseURL, s) && !isParamName(s, databaseURL) {
				return `"xxxxx"`
			}
		}
		return q
	})
}

// isParamName reports whether every occurrence of s in databaseURL is a
// parameter name: at the start of the string or after '?', '&' or
// whitespace, and followed by '='.
func isParamName(s, databaseURL string) bool {
	if s == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(databaseURL[i:], s)
		if j < 0 {
			return true
		}
		start, end := i+j, i+j+len(s)
		if start > 0 && !strings.ContainsRune("?& \t\n\r\v\f", rune(databaseURL[start-1])) {
			return false
		}
		if end >= len(databaseURL) || databaseURL[end] != '=' {
			return false
		}
		i = end
	}
}

// UserinfoPasswordPieces returns the password of a postgres:// or
// postgresql:// connection string's userinfo and every piece of it that
// pgx v5.11.0 can print on its own (INIT-024 TASK-013): nil for any other
// string. Callers mask each one.
//
// pgx v5.11.0 ends the userinfo at the first '@', as libpq does; net/url
// (and pgx v5.9.2) at the last. An unencoded '@' in a password therefore
// splits it, and its tail becomes the host part: pgx percent-decodes it,
// splits it into a host list at ',', and reads ports after ':', IPv6
// brackets and a query after '?'. Connect errors name every host. So the
// password (raw and decoded) is cut at each of those delimiters, and each
// piece is returned with the whole.
func UserinfoPasswordPieces(databaseURL string) []string {
	pw, ok := urlUserinfoPassword(databaseURL)
	if !ok {
		return nil
	}
	forms := []string{pw}
	if dec, err := url.PathUnescape(pw); err == nil && dec != pw {
		forms = append(forms, dec)
	}
	out := append([]string(nil), forms...)
	for _, f := range forms {
		for _, piece := range strings.FieldsFunc(f, isURIDelimiter) {
			if piece != f {
				out = append(out, piece)
			}
		}
	}
	return out
}

// isURIDelimiter reports whether r separates the parts of a connection
// URI that pgx v5.11.0 reads and may print on their own.
func isURIDelimiter(r rune) bool {
	return strings.ContainsRune("@,:[]/?&=#", r)
}

// urlUserinfoPassword returns the password of a postgres:// or
// postgresql:// connection string's userinfo, read as widely as either
// URI parser Spine has used reads it: from the first ':' up to the last
// '@' before the first '/'.
func urlUserinfoPassword(databaseURL string) (string, bool) {
	rest, ok := strings.CutPrefix(databaseURL, "postgres://")
	if !ok {
		if rest, ok = strings.CutPrefix(databaseURL, "postgresql://"); !ok {
			return "", false
		}
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	at := strings.LastIndexByte(rest, '@')
	if at < 0 {
		return "", false
	}
	_, pw, ok := strings.Cut(rest[:at], ":")
	return pw, ok
}

// connStringCredential matches a password or sslpassword setting in a
// URL query or a key=value connection string, and a URL userinfo
// password; it backs up url.Parse for strings it cannot parse.
var connStringCredential = regexp.MustCompile(`(?:^|[?&\s])(?:ssl)?password\s*=\s*('[^']*'|[^&\s]+)|://[^/@:]*:([^@/]*)@`)

// connStringSecrets lists the credential values databaseURL carries, in
// raw and URL-decoded form. Values shorter than 3 bytes are left out:
// replacing them would mangle the message without protecting anything.
func connStringSecrets(databaseURL string) []string {
	var out []string
	add := func(v string) {
		v = strings.Trim(v, "'")
		if len(v) < 3 {
			return
		}
		out = append(out, v)
		// pgx v5.9.2 decoded a query value as net/url does (`+` is a
		// space); v5.11.0 decodes every component as libpq does (`+` is
		// literal). List both decodings.
		if dec, err := url.QueryUnescape(v); err == nil && dec != v && len(dec) >= 3 {
			out = append(out, dec)
		}
		if dec, err := url.PathUnescape(v); err == nil && dec != v && len(dec) >= 3 {
			out = append(out, dec)
		}
	}
	if u, err := url.Parse(databaseURL); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		if pw, ok := u.User.Password(); ok {
			add(pw)
		}
		for _, k := range []string{"password", "sslpassword"} {
			for _, v := range u.Query()[k] {
				add(v)
			}
		}
	}
	for _, m := range connStringCredential.FindAllStringSubmatch(databaseURL, -1) {
		add(m[1])
		add(m[2])
	}
	for _, piece := range UserinfoPasswordPieces(databaseURL) {
		add(piece)
	}
	return out
}
