package scheduler

// INIT-024 TASK-008: TickWait bounds a tick's stall, never its length.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/observe"
)

// idleStores is n workspaces with an isoStore each, in id order.
func idleStores(n int) (map[string]ServiceSet, []string, map[string]*isoStore) {
	sets := map[string]ServiceSet{}
	stores := map[string]*isoStore{}
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("ws-%02d", i)
		ids = append(ids, id)
		st := &isoStore{}
		stores[id] = st
		sets[id] = ServiceSet{Store: st}
	}
	return sets, ids, stores
}

// A tick of healthy but slow workspaces — longer in total than TickWait,
// but always making progress — scans every one of them: no workspace late
// in the list is left for a next tick that would cut it short again.
func TestMultiScheduler_SlowButProgressingTickScansEveryWorkspace(t *testing.T) {
	const n = 12
	sets, ids, stores := idleStores(n)
	for _, id := range ids {
		st := stores[id]
		release := make(chan struct{})
		st.blockScan = release
		go func() {
			// Each scan takes about 30ms once it has started.
			for st.scans.Load() == 0 {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(30 * time.Millisecond)
			close(release)
		}()
	}
	ms := NewMultiScheduler(&msPool{sets: sets}, msResolver{ids: ids}, MultiSchedulerConfig{
		MaxConcurrentWorkspaces: 2, TickWait: 80 * time.Millisecond,
	})
	start := time.Now()
	ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)
	took := time.Since(start)
	for _, id := range ids {
		if got := stores[id].scans.Load(); got != 1 {
			t.Fatalf("%s scanned %d times in the tick, want 1 (the tick took %s, TickWait 80ms)", id, got, took)
		}
	}
	if took < 80*time.Millisecond {
		t.Fatalf("the tick took %s; the test needs one longer than TickWait to mean anything", took)
	}
}

// As many hung workspaces as there are slots stall one tick at most: the
// tick stops waiting after TickWait without progress, and the next tick
// skips the hung ones (still running) and scans the rest with every slot.
func TestMultiScheduler_HungWorkspacesCannotUseUpTheSlots(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	sets := map[string]ServiceSet{}
	stores := map[string]*isoStore{}
	ids := []string{"a-hung-1", "a-hung-2", "b-ok-1", "b-ok-2", "b-ok-3"}
	for _, id := range ids {
		st := &isoStore{}
		if id[0] == 'a' {
			st.blockScan = release
		}
		stores[id] = st
		sets[id] = ServiceSet{Store: st}
	}
	ms := NewMultiScheduler(&msPool{sets: sets}, msResolver{ids: ids}, MultiSchedulerConfig{
		MaxConcurrentWorkspaces: 2, TickWait: 40 * time.Millisecond,
	})
	start := time.Now()
	ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a tick whose slots were all held by hung scans took %s, want about TickWait", took)
	}
	for _, id := range []string{"b-ok-1", "b-ok-2", "b-ok-3"} {
		if n := stores[id].scans.Load(); n != 0 {
			t.Fatalf("%s scanned in the stalled tick (%d); want it left to the next tick", id, n)
		}
	}
	ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)
	for _, id := range []string{"b-ok-1", "b-ok-2", "b-ok-3"} {
		if n := stores[id].scans.Load(); n != 1 {
			t.Fatalf("%s scanned %d times by the next tick, want 1", id, n)
		}
	}
	for _, id := range []string{"a-hung-1", "a-hung-2"} {
		if n := stores[id].scans.Load(); n != 1 {
			t.Fatalf("%s entered %d scans, want 1 (it is still running)", id, n)
		}
	}
	releaseOnce.Do(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ms.Stop()
	close(ms.exited) // Start was never called; mark it returned for Wait
	if err := ms.Wait(ctx); err != nil {
		t.Fatalf("the released scans did not drain: %v", err)
	}
	ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)
	for _, id := range ids {
		if n := stores[id].scans.Load(); n > 2 {
			t.Fatalf("%s scanned after Stop", id)
		}
	}
}

// A scan no tick waits for any more still ends at ScanDeadline: its
// context is cancelled, it gives back its pool reference (which an
// evicted workspace's next request would otherwise wait for), its
// workspace is scanned again by a later tick, and an Error names it.
func TestMultiScheduler_ScanDeadlineEndsAnAbandonedScan(t *testing.T) {
	logs := captureLogs(t)
	hung := &isoStore{blockUntilDone: true}
	ok := &isoStore{}
	pool := &refPool{sets: map[string]ServiceSet{"a-hung": {Store: hung}, "b-ok": {Store: ok}}}
	ms := NewMultiScheduler(pool, msResolver{ids: []string{"a-hung", "b-ok"}}, MultiSchedulerConfig{
		TickWait: 30 * time.Millisecond, ScanDeadline: 300 * time.Millisecond,
	})
	start := time.Now()
	ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Fatalf("the tick waited %s for the hung scan, want about TickWait", took)
	}
	if held := pool.held(); held["a-hung"] != 1 {
		t.Fatalf("references held right after the tick: %v, want a-hung's scan still holding one", held)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(pool.held()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("a-hung's scan still holds its pool reference 5s after its 300ms deadline: %v", pool.held())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if took := time.Since(start); took < 300*time.Millisecond {
		t.Fatalf("the scan ended after %s, before its deadline", took)
	}
	// The reference goes back inside the scan; its claim right after.
	for {
		ms.inflightMu.Lock()
		busy := len(ms.inflight)
		ms.inflightMu.Unlock()
		if busy == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a-hung's scan still claimed after it gave back its reference")
		}
		time.Sleep(time.Millisecond)
	}
	ms.forEachWorkspace(context.Background(), "timeout-scan", func(ctx context.Context, sched *Scheduler) {
		if observe.WorkspaceID(ctx) == "a-hung" {
			return // its next scan; do not block again
		}
		timeoutTick(ctx, sched)
	})
	if n := hung.scans.Load(); n != 1 {
		t.Fatalf("a-hung entered %d scans, want 1 (the second tick's share returned at once)", n)
	}
	pool.mu.Lock()
	gets := pool.gets["a-hung"]
	pool.mu.Unlock()
	if gets != 2 {
		t.Fatalf("a-hung asked for %d times over two ticks, want 2 (scanned again once its scan ended)", gets)
	}
	out := logs.String()
	if !strings.Contains(out, "workspace scan reached its deadline") || !strings.Contains(out, "workspace_id=a-hung") {
		t.Fatalf("no Error naming the cancelled scan:\n%s", out)
	}
}

// Regression (codex review round 5; red at b5448ea7): with one slot, a
// workspace first in the list whose scan outlasts TickWait, but ends
// before the next tick, stalled every tick's dispatch, and every tick
// started at the top of the list again, so the workspace after it was
// never scanned. A stalled tick now leaves a cursor and the next tick of
// the same kind starts with the workspaces it left undispatched.
func TestMultiScheduler_StalledDispatchResumesWhereItStopped(t *testing.T) {
	slow := &isoStore{scanDelay: 60 * time.Millisecond}
	healthy := &isoStore{}
	ms := NewMultiScheduler(&msPool{sets: map[string]ServiceSet{"a-slow": {Store: slow}, "b-healthy": {Store: healthy}}},
		msResolver{ids: []string{"a-slow", "b-healthy"}},
		MultiSchedulerConfig{MaxConcurrentWorkspaces: 1, TickWait: 20 * time.Millisecond})
	idle := func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			ms.inflightMu.Lock()
			busy := len(ms.inflight)
			ms.inflightMu.Unlock()
			if busy == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("a scan never ended")
			}
			time.Sleep(time.Millisecond)
		}
	}
	const ticks = 6
	for i := 0; i < ticks; i++ {
		ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)
		idle() // as at the next tick of a real interval: every scan has ended
	}
	if n := healthy.scans.Load(); n < ticks/2 {
		t.Fatalf("b-healthy scanned %d times in %d ticks behind a workspace that stalls each tick's dispatch; want at least every other tick", n, ticks)
	}
	if n := slow.scans.Load(); n < ticks/2 {
		t.Fatalf("a-slow scanned %d times in %d ticks; want at least every other tick", n, ticks)
	}
}
