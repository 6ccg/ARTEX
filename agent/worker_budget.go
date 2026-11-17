package agent

import (
	"context"
	"time"
)

type workerBudgetKey struct{}

// workerPrimaryDeadline returns the earliest absolute intent or task deadline.
// A standalone Execute without an intent budget starts its own budget here.
func workerPrimaryDeadline(ctx context.Context, own time.Duration) time.Time {
	deadline, shared := ctx.Value(workerBudgetKey{}).(time.Time)
	if !shared && own > 0 {
		deadline = time.Now().Add(own)
	}
	if tc := taskClockFrom(ctx); tc.DeadlineUnix > 0 {
		taskDeadline := time.Unix(tc.DeadlineUnix, 0)
		if deadline.IsZero() || taskDeadline.Before(deadline) {
			deadline = taskDeadline
		}
	}
	return deadline
}

// CanResumeIntent reports whether a retry can continue the recorded conversation.
func (w *Worker) CanResumeIntent() bool { return w.tx != nil }

// WithIntentBudget starts one wall-clock budget shared by all attempts of an intent.
// The deadline is metadata, not a parent cancellation: settlement needs a live ctx.
func (w *Worker) WithIntentBudget(ctx context.Context) context.Context {
	var deadline time.Time
	if w.runTimeout > 0 {
		deadline = time.Now().Add(w.runTimeout)
	}
	return context.WithValue(ctx, workerBudgetKey{}, deadline)
}

// IntentBudgetRemaining returns zero for unlimited and a positive minimum after expiry,
// because the harness interprets zero as unlimited.
func IntentBudgetRemaining(ctx context.Context) (time.Duration, bool) {
	deadline, ok := ctx.Value(workerBudgetKey{}).(time.Time)
	if !ok || deadline.IsZero() {
		return 0, false
	}
	left := time.Until(deadline)
	if left <= 0 {
		return time.Nanosecond, true
	}
	return left, true
}

func workerMaxDuration(tc TaskClock, own time.Duration) (time.Duration, bool) {
	if tc.DeadlineUnix <= 0 {
		return own, false
	}
	left := time.Until(time.Unix(tc.DeadlineUnix, 0))
	if left <= 0 {
		left = time.Nanosecond
	}
	if own <= 0 || left <= own {
		return left, true
	}
	return own, false
}

// IntentWaitRemaining also clamps retry backoff to the task deadline.
func IntentWaitRemaining(ctx context.Context) (time.Duration, bool) {
	own, limited := IntentBudgetRemaining(ctx)
	duration, taskLimited := workerMaxDuration(taskClockFrom(ctx), own)
	return duration, limited || taskLimited
}

func workerPrepareContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := workerPrimaryDeadline(ctx, 0)
	if !deadline.IsZero() {
		return context.WithDeadline(ctx, deadline)
	}
	return context.WithCancel(ctx)
}
