package delivery

// INIT-023 TASK-010 review round 2, finding 1: a permanent verdict on a
// subscription (deleted, invalid metadata, refused or unusable target
// URL, unusable TLS or signing configuration) dead-letters the
// subscription's whole due queue in one statement. Before, only the
// claimed row went dead and the rest stayed due, so `concurrency` or more
// such subscriptions with old backlogs took every claim slot until their
// backlogs drained one row per poll each.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// permanentKind sets up subscription sub so that every delivery of it
// meets one permanent verdict. good is a healthy endpoint the other
// subscriptions use; the dispatcher config gets a validator allowlisting
// its host, so the refused-URL kind is refused by the real validator.
type permanentKind struct {
	name  string
	setup func(res *mapResolver, sub string)
	// noTargets builds the dispatcher without a validator (the request-
	// build error is reachable only past a permissive validator).
	noTargets bool
}

func permanentKinds() []permanentKind {
	sticky := func(res *mapResolver, sub string, err error) {
		res.mu.Lock()
		defer res.mu.Unlock()
		res.errs[sub] = err
		res.stick[sub] = true
	}
	detail := func(res *mapResolver, sub string, d SubscriptionDetail) {
		res.mu.Lock()
		defer res.mu.Unlock()
		d.SubscriptionID = sub
		res.subs[sub] = &d
	}
	return []permanentKind{
		{name: "deleted", setup: func(res *mapResolver, sub string) {
			sticky(res, sub, domain.NewError(domain.ErrNotFound, "subscription not found"))
		}},
		{name: "invalid metadata", setup: func(res *mapResolver, sub string) {
			_, err := parseSubscriptionMetadata([]byte(`{"signature_version":"v3"}`))
			sticky(res, sub, err)
		}},
		{name: "refused URL", setup: func(res *mapResolver, sub string) {
			detail(res, sub, SubscriptionDetail{TargetURL: "http://not-allowlisted.example/hook/s3cr3t"})
		}},
		{name: "request build error", noTargets: true, setup: func(res *mapResolver, sub string) {
			detail(res, sub, SubscriptionDetail{TargetURL: "http://[::1/hook"})
		}},
		{name: "TLS config error", setup: func(res *mapResolver, sub string) {
			detail(res, sub, SubscriptionDetail{TargetURL: "http://127.0.0.1:1/hook", TLS: &SubscriptionTLSConfig{CABundlePEM: "not a pem bundle"}})
		}},
		{name: "v2 without a secret", setup: func(res *mapResolver, sub string) {
			detail(res, sub, SubscriptionDetail{TargetURL: "http://127.0.0.1:1/hook", SignatureVersion: SignatureVersionV2})
		}},
		{name: "unknown signature version", setup: func(res *mapResolver, sub string) {
			detail(res, sub, SubscriptionDetail{TargetURL: "http://127.0.0.1:1/hook", SigningSecret: "s", SignatureVersion: "v3"})
		}},
	}
}

func permanentDispatcher(st *queueStore, res *mapResolver, kind permanentKind, concurrency int) *WebhookDispatcher {
	cfg := DispatcherConfig{Concurrency: concurrency}
	if !kind.noTargets {
		cfg.Targets = NewTargetValidator([]string{"127.0.0.1"})
	}
	d := NewWebhookDispatcher(st, res, cfg)
	sharedClock(d, st)
	return d
}

// TestPermanentVerdictDeadLettersWholeDueQueue: for every permanent
// verdict, one claimed row dead-letters the subscription's whole due queue
// in one statement — every due row dead with the claimed row's reason and
// one delivery-log row each, attempt_count untouched — while a row of the
// subscription scheduled beyond the next poll, and every other
// subscription's row, are left alone. Nothing reaches an endpoint.
func TestPermanentVerdictDeadLettersWholeDueQueue(t *testing.T) {
	for _, kind := range permanentKinds() {
		t.Run(kind.name, func(t *testing.T) {
			good, goodHits := statusServer(t, http.StatusOK)
			st := newQueueStore()
			base := st.now().Add(-time.Hour)
			for i := 0; i < 30; i++ {
				st.add(fmt.Sprintf("d%02d", i), "sub-x", base.Add(time.Duration(i)*time.Second))
			}
			st.rows["d29"].Status, st.rows["d29"].AttemptCount = "failed", 2 // a due retry
			st.add("later", "sub-x", base)
			later := st.now().Add(time.Hour)
			st.rows["later"].Status, st.rows["later"].AttemptCount, st.rows["later"].NextRetryAt, st.rows["later"].LastError = "failed", 1, &later, "HTTP 503"
			st.add("other", "sub-good", base.Add(time.Minute))
			res := &mapResolver{
				subs:  map[string]*SubscriptionDetail{"sub-good": {SubscriptionID: "sub-good", TargetURL: good.URL}},
				errs:  map[string]error{},
				stick: map[string]bool{},
			}
			kind.setup(res, "sub-x")
			d := permanentDispatcher(st, res, kind, 1)
			d.poll(context.Background())

			if goodHits.Load() != 0 {
				t.Fatalf("the healthy row was claimed ahead of the older sub-x rows (concurrency 1)")
			}
			claimed := st.row("d00")
			reason := claimed.LastError
			if claimed.Status != "dead" || claimed.AttemptCount != 1 || reason == "" {
				t.Fatalf("claimed row = %+v, want dead/attempt 1 with a reason", claimed)
			}
			if len(st.logsFor("d00")) != 1 {
				t.Fatalf("claimed row: %d log rows, want 1", len(st.logsFor("d00")))
			}
			for i := 1; i < 30; i++ {
				id := fmt.Sprintf("d%02d", i)
				r := st.row(id)
				wantAttempts := 0
				if id == "d29" {
					wantAttempts = 2
				}
				if r.Status != "dead" || r.LastError != reason || r.AttemptCount != wantAttempts || r.NextRetryAt != nil {
					t.Fatalf("%s = %+v, want dead/%q/attempts %d", id, r, reason, wantAttempts)
				}
				if logs := st.logsFor(id); len(logs) != 1 || logs[0].Error != reason || logs[0].StatusCode != nil {
					t.Fatalf("%s: log rows %+v, want one explaining the dead-letter", id, logs)
				}
			}
			if r := st.row("later"); r.Status != "failed" || r.NextRetryAt == nil || !r.NextRetryAt.Equal(later) || r.LastError != "HTTP 503" {
				t.Fatalf("a row scheduled beyond the next poll was touched: %+v", r)
			}
			if r := st.row("other"); r.Status != "pending" || r.NextRetryAt != nil || r.LastError != "" {
				t.Fatalf("another subscription's row was touched: %+v", r)
			}
			if n := res.calls.Load(); n != 1 {
				t.Fatalf("resolver called %d times, want 1 (one verdict for the whole queue)", n)
			}
		})
	}
}

// TestDeadLetteredBacklogsDoNotStarveOtherSubscriptions (AC9a, review
// round 2): k subscriptions each have 300 old due rows and a permanent
// verdict (deleted — say an admin removed five subscriptions whose
// circuits had held five minutes of events —, invalid metadata, refused
// URL); one healthy row arrives per simulated second for 600 polls.
// Every bad subscription costs one claim slot, once, so a healthy row
// waits at most ceil(k / concurrency) polls, and every bad row ends dead
// with one delivery-log row. The control turns the bulk statement into a
// no-op (per-row dead-lettering, the round-1 shape) and shows the scenario
// starving: at 5x300 the first healthy delivery came at poll 300.
func TestDeadLetteredBacklogsDoNotStarveOtherSubscriptions(t *testing.T) {
	const concurrency, ticks, rows = 5, 600, 300
	run := func(t *testing.T, kind permanentKind, subs int, noBulk bool) (delivered int64, maxLag int) {
		good, goodHits := statusServer(t, http.StatusOK)
		st := newQueueStore()
		st.noBulkHold = noBulk
		base := st.now().Add(-2 * time.Hour)
		res := &mapResolver{
			subs:  map[string]*SubscriptionDetail{"sub-good": {SubscriptionID: "sub-good", TargetURL: good.URL}},
			errs:  map[string]error{},
			stick: map[string]bool{},
		}
		for j := 0; j < subs; j++ {
			sub := fmt.Sprintf("sub-bad-%02d", j)
			kind.setup(res, sub)
			for i := 0; i < rows; i++ {
				st.add(fmt.Sprintf("bad-%02d-%05d", j, i), sub, base.Add(time.Duration(i*subs+j)*time.Millisecond))
			}
		}
		d := permanentDispatcher(st, res, kind, concurrency)
		enqueuedAt := map[string]int{}
		for tick := 0; tick < ticks; tick++ {
			id := fmt.Sprintf("good-%04d", tick)
			st.add(id, "sub-good", st.now())
			enqueuedAt[id] = tick
			d.poll(context.Background())
			for gid, at := range enqueuedAt {
				if st.row(gid).Status == "delivered" {
					delete(enqueuedAt, gid)
				}
				if tick-at > maxLag {
					maxLag = tick - at
				}
			}
			st.advance(time.Second)
		}
		if !noBulk {
			for j := 0; j < subs; j++ {
				for i := 0; i < rows; i++ {
					id := fmt.Sprintf("bad-%02d-%05d", j, i)
					if r := st.row(id); r.Status != "dead" || r.LastError == "" || len(st.logsFor(id)) != 1 {
						t.Fatalf("%s = %+v with %d log rows, want dead with one", id, r, len(st.logsFor(id)))
					}
				}
			}
		}
		return goodHits.Load(), maxLag
	}
	kinds := permanentKinds()[:3] // deleted, invalid metadata, refused URL
	for _, kind := range kinds {
		for _, subs := range []int{4, 5, 6, 10} {
			t.Run(fmt.Sprintf("%s/%dx%d", kind.name, subs, rows), func(t *testing.T) {
				bound := (subs + concurrency - 1) / concurrency
				delivered, maxLag := run(t, kind, subs, false)
				t.Logf("%d %s subscriptions x %d rows: %d/%d healthy rows delivered, max wait %d polls (bound %d)", subs, kind.name, rows, delivered, ticks, maxLag, bound)
				if delivered < int64(ticks-bound) {
					t.Fatalf("only %d/%d healthy rows delivered", delivered, ticks)
				}
				if maxLag > bound {
					t.Fatalf("a healthy row waited %d polls (want <= %d)", maxLag, bound)
				}
			})
		}
	}
	t.Run("control: per-row dead-lettering starves at 5x300", func(t *testing.T) {
		delivered, maxLag := run(t, kinds[0], 5, true)
		t.Logf("per-row dead-lettering, 5x300: %d/%d healthy rows delivered, max wait %d polls", delivered, ticks, maxLag)
		if maxLag < rows/2 {
			t.Fatalf("max wait %d polls: the scenario no longer exercises the review-round-2 starvation", maxLag)
		}
	})
}
