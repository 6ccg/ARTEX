package llmpool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

func TestCancelledCompletionKeepsFailureState(t *testing.T) {
	reg := NewRegistry(nil, nil)
	reg.Trip(1, "transient failure", false)
	before := reg.Get(1)
	p := New([]*Member{member(1, "cancelled", 1, &fakeProv{errs: []error{context.Canceled}})}, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, _ = p.Complete(ctx, llm.CompletionRequest{})
	if after := reg.Get(1); after != before {
		t.Fatalf("failure state changed on cancel: before=%+v after=%+v", before, after)
	}
}

func TestResetCannotOvertakePendingPersistence(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var saved atomic.Bool
	reg := NewRegistry(func(int64, State) { close(entered); <-release; saved.Store(true) }, func(int64) { saved.Store(false) })
	tripDone, resetDone := make(chan struct{}), make(chan struct{})
	go func() { reg.Trip(1, "failure", true); close(tripDone) }()
	<-entered
	go func() { reg.Reset(1); close(resetDone) }()
	select {
	case <-resetDone:
		close(release)
		t.Fatal("reset overtook persistence")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-tripDone
	<-resetDone
	if saved.Load() || reg.IsOpen(1) {
		t.Fatal("old failure survived reset")
	}
}

// A configured soft-trip threshold replaces the default: two transient failures
// are enough, and the fixed cooldown replaces the 1/5/30min ladder.
func TestSetPolicyOverridesThresholdAndCooldown(t *testing.T) {
	reg := NewRegistry(nil, nil)
	reg.SetPolicy(2, 90*time.Second)
	if reg.Trip(1, "429", false) {
		t.Fatal("tripped on the first transient failure, want the second")
	}
	if !reg.Trip(1, "429", false) {
		t.Fatal("should trip on transient failure #2")
	}
	st := reg.Get(1)
	if d := time.Until(st.OpenUntil); d < 80*time.Second || d > 90*time.Second {
		t.Fatalf("cooldown=%v, want ~90s", d)
	}
	// Every later trip keeps the same fixed window instead of climbing the ladder.
	reg.Trip(1, "429", true)
	st = reg.Get(1)
	if d := time.Until(st.OpenUntil); d < 80*time.Second || d > 90*time.Second {
		t.Fatalf("second cooldown=%v, want ~90s (fixed)", d)
	}
}

// A negative threshold turns off soft tripping entirely: transient failures never
// open the breaker, deterministic ones still do immediately.
func TestSetPolicyDisablesSoftTrip(t *testing.T) {
	reg := NewRegistry(nil, nil)
	reg.SetPolicy(-1, 0)
	for i := range 10 {
		if reg.Trip(1, "429", false) {
			t.Fatalf("transient failure #%d tripped the breaker, want never", i+1)
		}
	}
	if reg.IsOpen(1) {
		t.Fatal("breaker should stay closed for transient failures")
	}
	if !reg.Trip(1, "no credit", true) {
		t.Fatal("a hard failure must still trip immediately")
	}
	if d := time.Until(reg.Get(1).OpenUntil); d < 50*time.Second || d > 60*time.Second {
		t.Fatalf("cooldown=%v, want the default first rung (~1min)", d)
	}
}

// The zero policy is the historical behaviour: 3 transient failures, ladder cooldown.
func TestZeroPolicyKeepsDefaults(t *testing.T) {
	reg := NewRegistry(nil, nil)
	reg.SetPolicy(0, 0)
	for i := 1; i < softTripAfter; i++ {
		if reg.Trip(1, "429", false) {
			t.Fatalf("tripped after %d transient failures, want %d", i, softTripAfter)
		}
	}
	if !reg.Trip(1, "429", false) {
		t.Fatalf("should trip on failure #%d", softTripAfter)
	}
	if d := time.Until(reg.Get(1).OpenUntil); d < 50*time.Second || d > 60*time.Second {
		t.Fatalf("cooldown=%v, want the first ladder rung (~1min)", d)
	}
}
