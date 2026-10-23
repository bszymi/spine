package scheduler

// INIT-024 TASK-008: TickWait bounds a tick's stall, never its length.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
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
