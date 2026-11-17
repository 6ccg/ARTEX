package agent

import (
	"context"
	"testing"
	"time"
)

func TestIntentBudgetSharedAndClamped(t *testing.T) {
	w := &Worker{runTimeout: 40 * time.Millisecond}
	ctx := w.WithIntentBudget(context.Background())
	deadline := workerPrimaryDeadline(ctx, w.runTimeout)
	first, limited := IntentBudgetRemaining(ctx)
	if !limited || first <= 0 || first > w.runTimeout {
		t.Fatalf("initial budget %v %v", first, limited)
	}
	time.Sleep(50 * time.Millisecond)
	if next := workerPrimaryDeadline(ctx, w.runTimeout); !next.Equal(deadline) {
		t.Fatal("retry extended the absolute intent deadline")
	}
	remaining, limited := IntentBudgetRemaining(ctx)
	if !limited || remaining != time.Nanosecond {
		t.Fatalf("expired budget %v %v", remaining, limited)
	}
	prep, cancel := workerPrepareContext(ctx)
	defer cancel()
	if prep.Err() == nil {
		t.Fatal("expired preparation must be cancelled")
	}
	if ctx.Err() != nil {
		t.Fatal("settlement parent must remain live")
	}
	dur, clamped := workerMaxDuration(TaskClock{DeadlineUnix: time.Now().Add(-time.Second).Unix()}, 0)
	if dur != time.Nanosecond || !clamped {
		t.Fatalf("task deadline: %v %v", dur, clamped)
	}
	unlimited, limited := IntentBudgetRemaining((&Worker{}).WithIntentBudget(context.Background()))
	if limited || unlimited != 0 {
		t.Fatalf("unlimited: %v %v", unlimited, limited)
	}
}
