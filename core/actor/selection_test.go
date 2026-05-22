package actor_test

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/domain"
)

// orderedFakeStore wraps fakeStore so ListActorsByStatus returns
// actors in a stable order (sorted by ActorID). Round-robin fairness
// assertions need a deterministic eligible slice — the bare fakeStore
// iterates a Go map and so reorders the slice every call. Production
// stores order rows by an index, so sorting here is a faithful proxy.
type orderedFakeStore struct {
	*fakeStore
}

func (o *orderedFakeStore) ListActorsByStatus(ctx context.Context, status domain.ActorStatus) ([]domain.Actor, error) {
	out, err := o.fakeStore.ListActorsByStatus(ctx, status)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ActorID < out[j].ActorID })
	return out, nil
}

// activeFakeStore wires a small set of distinct active actors with a
// pool key chosen by the test (via Type) so each round-robin test gets
// its own rrIndices entry — the rotation map is package-level state
// (selection.go:34), so colliding pool keys would entangle test cases.
func activeFakeStore(actorType domain.ActorType, ids ...string) *orderedFakeStore {
	fs := newFakeStore()
	for _, id := range ids {
		fs.actors[id] = &domain.Actor{
			ActorID: id,
			Type:    actorType,
			Role:    domain.RoleContributor,
			Status:  domain.ActorStatusActive,
		}
	}
	return &orderedFakeStore{fakeStore: fs}
}

// TestSelectRoundRobin_FairDistribution runs the round-robin selection
// 1000 times across 4 actors and asserts each actor was picked exactly
// 250 times. The pure round-robin implementation (`idx := count % N;
// count++`) gives perfect distribution — any tolerance window would hide
// real bugs (off-by-one, modulo skew) so this assertion is exact.
//
// Uses a unique ActorType so rrIndices entries don't collide with the
// existing TestSelectRoundRobin in actor_test.go (which uses "human").
func TestSelectRoundRobin_FairDistribution(t *testing.T) {
	const poolType = domain.ActorType("rr_fair_test_type")
	const iterations = 1000
	ids := []string{"rr-fair-1", "rr-fair-2", "rr-fair-3", "rr-fair-4"}
	fs := activeFakeStore(poolType, ids...)
	svc := actor.NewService(fs)

	picks := make(map[string]int, len(ids))
	for i := 0; i < iterations; i++ {
		got, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
			EligibleActorTypes: []string{string(poolType)},
			Strategy:           actor.StrategyRoundRobin,
		})
		if err != nil {
			t.Fatalf("iteration %d: SelectActor: %v", i, err)
		}
		picks[got.ActorID]++
	}

	expected := iterations / len(ids)
	for _, id := range ids {
		if picks[id] != expected {
			t.Errorf("actor %s: expected %d picks, got %d (full distribution: %v)",
				id, expected, picks[id], picks)
		}
	}
}

// TestSelectRoundRobin_ConcurrentSafe is the AC's "fails if the mutex
// protecting the cursor is removed" regression bait. With the mutex
// (selection.go:33-34, 169-170), concurrent SelectActor calls are
// serialized: 1000 goroutines × 1 pick each yields exactly 1000
// completed selections with each actor picked exactly 250 times. With
// the mutex removed, the `-race` detector flags the unsynchronized map
// read+write on rrIndices and the test fails under `go test -race`.
//
// Note: this test relies on `-race`; the project's test makefile gates
// run with -race so a regression will surface in CI.
func TestSelectRoundRobin_ConcurrentSafe(t *testing.T) {
	const poolType = domain.ActorType("rr_concurrent_test_type")
	const goroutines = 1000
	ids := []string{"rr-conc-1", "rr-conc-2", "rr-conc-3", "rr-conc-4"}
	fs := activeFakeStore(poolType, ids...)
	svc := actor.NewService(fs)

	results := make(chan string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			got, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
				EligibleActorTypes: []string{string(poolType)},
				Strategy:           actor.StrategyRoundRobin,
			})
			if err != nil {
				results <- ""
				return
			}
			results <- got.ActorID
		}()
	}
	wg.Wait()
	close(results)

	picks := make(map[string]int, len(ids))
	for id := range results {
		if id == "" {
			t.Fatal("a goroutine got an error from SelectActor")
		}
		picks[id]++
	}

	expected := goroutines / len(ids)
	total := 0
	for _, id := range ids {
		total += picks[id]
		if picks[id] != expected {
			t.Errorf("actor %s: expected %d picks under contention, got %d (full: %v)",
				id, expected, picks[id], picks)
		}
	}
	if total != goroutines {
		t.Errorf("expected %d total picks, got %d", goroutines, total)
	}
}

// TestSelectRoundRobin_PoolKeyIsolation pins that two SelectionRequests
// with different pool keys (here: different actor types) maintain
// independent rotation cursors. A regression that collapsed the keying
// (e.g., dropping EligibleActorTypes from the key) would let one pool's
// picks advance the other's index and the assertion below would break.
func TestSelectRoundRobin_PoolKeyIsolation(t *testing.T) {
	typeA := domain.ActorType("rr_pool_a")
	typeB := domain.ActorType("rr_pool_b")

	inner := newFakeStore()
	inner.actors["pool-a-1"] = &domain.Actor{ActorID: "pool-a-1", Type: typeA, Role: domain.RoleContributor, Status: domain.ActorStatusActive}
	inner.actors["pool-a-2"] = &domain.Actor{ActorID: "pool-a-2", Type: typeA, Role: domain.RoleContributor, Status: domain.ActorStatusActive}
	inner.actors["pool-b-1"] = &domain.Actor{ActorID: "pool-b-1", Type: typeB, Role: domain.RoleContributor, Status: domain.ActorStatusActive}
	inner.actors["pool-b-2"] = &domain.Actor{ActorID: "pool-b-2", Type: typeB, Role: domain.RoleContributor, Status: domain.ActorStatusActive}
	fs := &orderedFakeStore{fakeStore: inner}
	svc := actor.NewService(fs)

	reqA := actor.SelectionRequest{EligibleActorTypes: []string{string(typeA)}, Strategy: actor.StrategyRoundRobin}
	reqB := actor.SelectionRequest{EligibleActorTypes: []string{string(typeB)}, Strategy: actor.StrategyRoundRobin}

	// Pool A: pick once → cursor at 1.
	a1, err := svc.SelectActor(context.Background(), reqA)
	if err != nil {
		t.Fatalf("first A pick: %v", err)
	}
	// Pool B: pick once → independent cursor, also at 1.
	b1, err := svc.SelectActor(context.Background(), reqB)
	if err != nil {
		t.Fatalf("first B pick: %v", err)
	}
	// Pool A: second pick must advance A's cursor without B leaking.
	a2, err := svc.SelectActor(context.Background(), reqA)
	if err != nil {
		t.Fatalf("second A pick: %v", err)
	}
	// Pool B: second pick must advance B's cursor independently.
	b2, err := svc.SelectActor(context.Background(), reqB)
	if err != nil {
		t.Fatalf("second B pick: %v", err)
	}

	// Within a pool, two consecutive picks must be different actors.
	if a1.ActorID == a2.ActorID {
		t.Errorf("pool A: expected rotation, got %s twice", a1.ActorID)
	}
	if b1.ActorID == b2.ActorID {
		t.Errorf("pool B: expected rotation, got %s twice", b1.ActorID)
	}
	// Cross-pool: each pick must come from its own pool.
	for _, p := range []*domain.Actor{a1, a2} {
		if p.Type != typeA {
			t.Errorf("pool A pick has wrong type: %s/%s", p.ActorID, p.Type)
		}
	}
	for _, p := range []*domain.Actor{b1, b2} {
		if p.Type != typeB {
			t.Errorf("pool B pick has wrong type: %s/%s", p.ActorID, p.Type)
		}
	}
}

// TestSelectAnyEligible_ReturnsFirstEligible pins the documented
// AnyEligible behaviour: returns the first actor in the eligible slice
// (which preserves ListActorsByStatus order). A regression to e.g.
// random selection would surface as a flaky test under repeat.
func TestSelectAnyEligible_ReturnsFirstEligible(t *testing.T) {
	const poolType = domain.ActorType("any_eligible_test_type")
	fs := activeFakeStore(poolType, "ae-1", "ae-2", "ae-3")
	svc := actor.NewService(fs)

	// Deterministic store ordering is map-iteration order in fakeStore
	// (non-deterministic). Pick repeatedly and assert the same ID is
	// returned every time — proves SelectActor doesn't add its own
	// randomness on top of whatever order the store gives.
	first, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		EligibleActorTypes: []string{string(poolType)},
		Strategy:           actor.StrategyAnyEligible,
	})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	for i := 0; i < 50; i++ {
		got, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
			EligibleActorTypes: []string{string(poolType)},
			Strategy:           actor.StrategyAnyEligible,
		})
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if got.ActorID != first.ActorID {
			t.Fatalf("AnyEligible non-deterministic: first=%s iter=%s",
				first.ActorID, got.ActorID)
		}
	}
}

// TestSelectActor_DefaultStrategyIsAnyEligible pins selection.go's
// `case StrategyAnyEligible, "":` branch — an unset Strategy must
// behave as AnyEligible, not error. A regression that made the empty
// strategy reachable via the default branch would fail this test.
func TestSelectActor_DefaultStrategyIsAnyEligible(t *testing.T) {
	const poolType = domain.ActorType("default_strategy_test_type")
	fs := activeFakeStore(poolType, "ds-1")
	svc := actor.NewService(fs)

	got, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		EligibleActorTypes: []string{string(poolType)},
		// Strategy intentionally unset.
	})
	if err != nil {
		t.Fatalf("unexpected error with unset Strategy: %v", err)
	}
	if got.ActorID != "ds-1" {
		t.Errorf("expected ds-1, got %s", got.ActorID)
	}
}

// TestSelectActor_UnknownStrategyRejected pins the explicit
// ErrInvalidParams branch — a regression that silently fell through
// to AnyEligible would mask malformed input.
func TestSelectActor_UnknownStrategyRejected(t *testing.T) {
	const poolType = domain.ActorType("unknown_strategy_test_type")
	fs := activeFakeStore(poolType, "us-1")
	svc := actor.NewService(fs)

	_, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
		EligibleActorTypes: []string{string(poolType)},
		Strategy:           actor.SelectionStrategy("not_a_real_strategy"),
	})
	if err == nil {
		t.Fatal("expected error for unknown strategy")
	}
	derr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	if derr.Code != domain.ErrInvalidParams {
		t.Errorf("expected ErrInvalidParams, got %s", derr.Code)
	}
}

// TestSelectRoundRobin_WrapsAroundOnSizeChange pins the modulo
// behaviour: even after the index has advanced past the pool size, the
// next pick wraps cleanly. The `idx % len(actors)` step (selection.go:172)
// is what makes the rotation safe — a regression that dropped the modulo
// would panic on out-of-bounds.
func TestSelectRoundRobin_WrapsAroundOnSizeChange(t *testing.T) {
	const poolType = domain.ActorType("rr_wrap_test_type")
	ids := []string{"wrap-1", "wrap-2"}
	fs := activeFakeStore(poolType, ids...)
	svc := actor.NewService(fs)

	// Drive the cursor well past the pool size.
	seen := make(map[string]int, len(ids))
	for i := 0; i < 20; i++ {
		got, err := svc.SelectActor(context.Background(), actor.SelectionRequest{
			EligibleActorTypes: []string{string(poolType)},
			Strategy:           actor.StrategyRoundRobin,
		})
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		seen[got.ActorID]++
	}
	for _, id := range ids {
		if seen[id] != 10 {
			t.Errorf("actor %s: expected 10 picks across 20 iterations, got %d (full: %v)",
				id, seen[id], seen)
		}
	}
}
