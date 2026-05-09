package delivery

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/store"
)

// fakeDeliveryStore implements store.DeliveryStore for the retention
// unit tests. Only DeleteExpiredDeliveries carries behavior — every
// other DeliveryStore method panics so a regression that starts calling
// them is loud, not silent.
type fakeDeliveryStore struct {
	mu sync.Mutex

	deleteCalls    int
	lastBefore     time.Time
	deleteReturned int64
	deleteErr      error
}

func (f *fakeDeliveryStore) DeleteExpiredDeliveries(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	f.lastBefore = before
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	return f.deleteReturned, nil
}

func (f *fakeDeliveryStore) snapshot() (calls int, before time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleteCalls, f.lastBefore
}

// Unused DeliveryStore methods — runRetentionPass only calls
// DeleteExpiredDeliveries.
func (f *fakeDeliveryStore) EnqueueDelivery(context.Context, *store.DeliveryEntry) error {
	panic("EnqueueDelivery: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) ClaimDeliveries(context.Context, int) ([]store.DeliveryEntry, error) {
	panic("ClaimDeliveries: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) UpdateDeliveryStatus(context.Context, string, string, string, *time.Time) error {
	panic("UpdateDeliveryStatus: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) MarkDelivered(context.Context, string) error {
	panic("MarkDelivered: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) LogDeliveryAttempt(context.Context, *store.DeliveryLogEntry) error {
	panic("LogDeliveryAttempt: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) ListDeliveryHistory(context.Context, store.DeliveryHistoryQuery) ([]store.DeliveryLogEntry, error) {
	panic("ListDeliveryHistory: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) GetDelivery(context.Context, string) (*store.DeliveryEntry, error) {
	panic("GetDelivery: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) ListDeliveries(context.Context, string, string, int) ([]store.DeliveryEntry, error) {
	panic("ListDeliveries: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) GetDeliveryStats(context.Context, string) (*store.DeliveryStats, error) {
	panic("GetDeliveryStats: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) WriteEventLog(context.Context, *store.EventLogEntry) error {
	panic("WriteEventLog: not used by runRetentionPass")
}

func (f *fakeDeliveryStore) ListEventsAfter(context.Context, string, []string, int) ([]store.EventLogEntry, error) {
	panic("ListEventsAfter: not used by runRetentionPass")
}

// captureRetentionLog redirects slog.Default to a synchronized buffer
// for the duration of t and restores the previous default on cleanup.
// Synchronized because StartRetentionCleanup logs from a goroutine in
// some tests; a vanilla bytes.Buffer would race.
func captureRetentionLog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitForLogContains polls logBuf until it contains needle or the
// 2-second deadline elapses. On timeout it cancels the supplied
// goroutine and fails the test, which prevents a hung StartRetentionCleanup
// from blocking the suite.
func waitForLogContains(t *testing.T, logBuf *syncBuf, needle string, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logBuf.String(), needle) {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("log never contained %q within 2s; got:\n%s", needle, logBuf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunRetentionPass_DeletesUsingPastCutoff(t *testing.T) {
	t.Parallel()

	const retention = 24 * time.Hour
	st := &fakeDeliveryStore{deleteReturned: 5}
	log := slog.New(slog.NewTextHandler(&syncBuf{}, nil))

	start := time.Now()
	runRetentionPass(context.Background(), st, retention, log)
	end := time.Now()

	calls, before := st.snapshot()
	if calls != 1 {
		t.Fatalf("DeleteExpiredDeliveries calls = %d, want 1", calls)
	}

	expectedMin := start.Add(-retention)
	expectedMax := end.Add(-retention)
	if before.Before(expectedMin) || before.After(expectedMax) {
		t.Fatalf("before = %v, want in [%v, %v] (i.e. now - %v)", before, expectedMin, expectedMax, retention)
	}
}

// TestRunRetentionPass_DirectionFlipBait pins the sign of the cutoff
// calculation. A regression that wrote `time.Now().Add(retention)`
// instead of `Add(-retention)` would push `before` into the future,
// causing DeleteExpiredDeliveries to wipe every live row — the exact
// failure mode TASK-019's "regression bait against direction flip" AC
// guards against.
func TestRunRetentionPass_DirectionFlipBait(t *testing.T) {
	t.Parallel()

	const retention = 7 * 24 * time.Hour
	st := &fakeDeliveryStore{}
	log := slog.New(slog.NewTextHandler(&syncBuf{}, nil))

	callTime := time.Now()
	runRetentionPass(context.Background(), st, retention, log)

	_, before := st.snapshot()
	if !before.Before(callTime) {
		t.Fatalf("before = %v, want strictly before now (%v); a positive Add(retention) would fail this", before, callTime)
	}
	gap := callTime.Sub(before)
	// A 1s slack handles the gap between callTime and the actual
	// time.Now() inside runRetentionPass on slow CI runners.
	if gap < retention-time.Second || gap > retention+time.Second {
		t.Fatalf("gap = %v, want ~%v (now - retention)", gap, retention)
	}
}

func TestRunRetentionPass_StoreErrorLogged(t *testing.T) {
	logBuf := captureRetentionLog(t)

	wantErr := errors.New("delete-failed")
	st := &fakeDeliveryStore{deleteErr: wantErr}

	runRetentionPass(context.Background(), st, time.Hour, slog.Default())

	logged := logBuf.String()
	if !strings.Contains(logged, "retention cleanup failed") {
		t.Errorf("log missing failure marker; got:\n%s", logged)
	}
	if !strings.Contains(logged, "level=ERROR") {
		t.Errorf("log not at ERROR level; got:\n%s", logged)
	}
	if !strings.Contains(logged, "delete-failed") {
		t.Errorf("log missing wrapped error string; got:\n%s", logged)
	}
	if strings.Contains(logged, "retention cleanup complete") {
		t.Errorf("log unexpectedly emitted success line on error path; got:\n%s", logged)
	}
}

func TestRunRetentionPass_ZeroDeletedSuppressesCompletionLog(t *testing.T) {
	logBuf := captureRetentionLog(t)

	st := &fakeDeliveryStore{deleteReturned: 0}

	runRetentionPass(context.Background(), st, time.Hour, slog.Default())

	logged := logBuf.String()
	// The function intentionally suppresses the completion line when
	// deleted == 0 to keep the steady-state log volume low. A regression
	// that always logs would flood ops dashboards.
	if strings.Contains(logged, "retention cleanup complete") {
		t.Errorf("expected no completion log when 0 rows deleted; got:\n%s", logged)
	}
	if strings.Contains(logged, "retention cleanup failed") {
		t.Errorf("expected no failure log on success path; got:\n%s", logged)
	}
}

func TestRunRetentionPass_PositiveDeletedLogsCompletion(t *testing.T) {
	logBuf := captureRetentionLog(t)

	st := &fakeDeliveryStore{deleteReturned: 42}

	runRetentionPass(context.Background(), st, time.Hour, slog.Default())

	logged := logBuf.String()
	if !strings.Contains(logged, "retention cleanup complete") {
		t.Errorf("log missing completion marker; got:\n%s", logged)
	}
	if !strings.Contains(logged, "deleted=42") {
		t.Errorf("log missing deleted=42 attr; got:\n%s", logged)
	}
}

func TestStartRetentionCleanup_DefaultFallback(t *testing.T) {
	logBuf := captureRetentionLog(t)

	ctx, cancel := context.WithCancel(context.Background())
	st := &fakeDeliveryStore{}

	done := make(chan struct{})
	go func() {
		StartRetentionCleanup(ctx, st, 0) // 0 must fall back to defaultRetention (7d)
		close(done)
	}()

	// The startup line is the only log the test needs — no tick fires.
	waitForLogContains(t, logBuf, "delivery retention cleanup started", cancel, done)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartRetentionCleanup did not return within 2s of ctx cancel")
	}

	logged := logBuf.String()
	// 7 days serializes as "168h0m0s" via time.Duration.String().
	const wantRetention = "retention=168h0m0s"
	if !strings.Contains(logged, wantRetention) {
		t.Errorf("expected %q in startup log (default 7d fallback); got:\n%s", wantRetention, logged)
	}

	calls, _ := st.snapshot()
	if calls != 0 {
		t.Errorf("DeleteExpiredDeliveries calls = %d, want 0 (no tick should fire in <2s)", calls)
	}
}

func TestStartRetentionCleanup_NegativeFallback(t *testing.T) {
	logBuf := captureRetentionLog(t)

	ctx, cancel := context.WithCancel(context.Background())
	st := &fakeDeliveryStore{}

	done := make(chan struct{})
	go func() {
		StartRetentionCleanup(ctx, st, -5*time.Hour) // negative also falls back
		close(done)
	}()

	waitForLogContains(t, logBuf, "delivery retention cleanup started", cancel, done)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartRetentionCleanup did not return within 2s of ctx cancel")
	}

	if !strings.Contains(logBuf.String(), "retention=168h0m0s") {
		t.Errorf("expected default 7d fallback for negative retention; got:\n%s", logBuf.String())
	}
}

func TestStartRetentionCleanup_ExplicitRetentionPreserved(t *testing.T) {
	logBuf := captureRetentionLog(t)

	ctx, cancel := context.WithCancel(context.Background())
	st := &fakeDeliveryStore{}

	const explicit = 3 * time.Hour

	done := make(chan struct{})
	go func() {
		StartRetentionCleanup(ctx, st, explicit)
		close(done)
	}()

	waitForLogContains(t, logBuf, "delivery retention cleanup started", cancel, done)

	cancel()
	<-done

	// 3h serializes as "3h0m0s" — the test pins the exact format so a
	// regression that swaps in defaultRetention even when retention>0
	// would fail visibly.
	if !strings.Contains(logBuf.String(), "retention=3h0m0s") {
		t.Errorf("expected explicit 3h retention in startup log; got:\n%s", logBuf.String())
	}
}

func TestStartRetentionCleanup_ContextCancelReturns(t *testing.T) {
	_ = captureRetentionLog(t) // silence retention startup log

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before goroutine starts

	st := &fakeDeliveryStore{}

	done := make(chan struct{})
	go func() {
		StartRetentionCleanup(ctx, st, time.Hour)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartRetentionCleanup did not return on a pre-cancelled context within 2s")
	}

	calls, _ := st.snapshot()
	if calls != 0 {
		t.Errorf("DeleteExpiredDeliveries calls = %d on pre-cancelled ctx, want 0", calls)
	}
}

func TestDefaultRetentionIsSevenDays(t *testing.T) {
	t.Parallel()
	if defaultRetention != 7*24*time.Hour {
		t.Fatalf("defaultRetention = %v, want %v", defaultRetention, 7*24*time.Hour)
	}
}
