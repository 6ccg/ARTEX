package server

import (
	"runtime"
	"sync"
	"testing"
)

func TestEngineLLMCallHandoffPreservesConcurrentBegin(t *testing.T) {
	previous := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(previous)
	e := NewEngine(nil)
	const taskID = "handoff"
	e.BeginLLMCall(taskID)
	for range 10000 {
		start := make(chan struct{})
		var done sync.WaitGroup
		done.Add(2)
		go func() { defer done.Done(); <-start; e.EndLLMCall(taskID) }()
		go func() { defer done.Done(); <-start; e.BeginLLMCall(taskID) }()
		close(start)
		done.Wait()
		if got := e.ActiveLLMCalls(taskID); got != 1 {
			t.Fatalf("active calls after one end and one begin = %d, want 1", got)
		}
	}
}

func TestEngineActiveLLMCallsConcurrent(t *testing.T) {
	e := NewEngine(nil)
	const taskID = "42"
	const calls = 64

	var started sync.WaitGroup
	var release sync.WaitGroup
	var finished sync.WaitGroup
	started.Add(calls)
	finished.Add(calls)
	release.Add(1)
	for range calls {
		go func() {
			defer finished.Done()
			e.BeginLLMCall(taskID)
			started.Done()
			release.Wait()
			e.EndLLMCall(taskID)
		}()
	}
	started.Wait()
	if got := e.ActiveLLMCalls(taskID); got != calls {
		t.Fatalf("active calls = %d, want %d", got, calls)
	}
	release.Done()
	finished.Wait()
	if got := e.ActiveLLMCalls(taskID); got != 0 {
		t.Fatalf("active calls after completion = %d, want 0", got)
	}

	// A defensive extra end must never expose a negative UI count.
	e.EndLLMCall(taskID)
	if got := e.ActiveLLMCalls(taskID); got != 0 {
		t.Fatalf("active calls after extra end = %d, want 0", got)
	}
}
