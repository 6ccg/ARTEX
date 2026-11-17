package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestPlannerDebounceStopsOnCancellation(t *testing.T) {
	e := NewEngine(nil)
	e.debounce = 2 * time.Second
	task := &Task{ID: "cancel-debounce", notify: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.plannerLoop(ctx, task) }()
	task.notify <- struct{}{}
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelled planner waited for the debounce timer")
	}
}

func TestEngineDoesNotRecreateExecutionContextAfterClose(t *testing.T) {
	e := NewEngine(nil)
	old := e.execContextFor(context.Background(), "shutdown")
	e.close(nil)
	for _, ctx := range []context.Context{old, e.execContextFor(context.Background(), "shutdown"), e.execContextFor(context.Background(), "late")} {
		if !errors.Is(context.Cause(ctx), agent.AbortShutdown) {
			t.Fatalf("execution context after shutdown: %v", context.Cause(ctx))
		}
	}
}

func TestStopTaskDrainsOperationsBeforeDroppingRun(t *testing.T) {
	e := NewEngine(nil)
	const id = "drain-owned-operation"
	ctx := e.execContextFor(context.Background(), id)
	if !e.beginTaskOperation(id) {
		t.Fatal("initial operation rejected")
	}
	done := make(chan struct{})
	go func() { e.StopTask(id); close(done) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("task execution was not cancelled")
	}
	select {
	case <-done:
		t.Fatal("task owner was removed before its operation finished")
	default:
	}
	if e.beginTaskOperation(id) {
		e.decInflight(id)
		t.Fatal("stopping task admitted another operation")
	}
	e.decInflight(id)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StopTask did not finish after its last operation")
	}
	if e.lookupTaskRun(id) != nil {
		t.Fatal("task owner retained after draining")
	}
}

func TestServerCloseCancelsAndDrainsAdmittedRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{Runtime: &Runtime{ctx: ctx, cancel: cancel}}
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	h := s.withRequestLifetime(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
		<-release
	}))
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	<-started
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("request was not cancelled by shutdown")
	}
	select {
	case <-done:
		t.Fatal("Close returned while the admitted request was still writing")
	default:
	}
	late := httptest.NewRecorder()
	h.ServeHTTP(late, httptest.NewRequest("GET", "/", nil))
	if late.Code != http.StatusServiceUnavailable || s.beginExternalWork() {
		t.Fatal("shutdown still admitted new work")
	}
	release <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not drain the request")
	}
}

func TestServerCloseCancelsProxyReconnectProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	task := &Task{ID: "1", PauseReason: "paused_by_proxy", Paused: true}
	m := &Manager{globalProxy: "http://proxy.invalid", tasks: map[string]*Task{"1": task}}
	s := &Server{Runtime: &Runtime{m: m, ctx: ctx, cancel: cancel}}
	entered := make(chan struct{})
	s.proxyProbe = func(ctx context.Context, _, _ string, _ []string, _ time.Duration) egressProbeResult {
		close(entered)
		<-ctx.Done()
		return egressProbeResult{Error: ctx.Err().Error()}
	}
	s.backgroundWG.Go(s.proxyReconnectTick)
	<-entered
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the reconnect probe")
	}
	if task.lifecycleSnapshot().ReconnectLastError != "" {
		t.Fatal("shutdown cancellation was recorded as a proxy failure")
	}
}

func TestEngineCloseWaitsForAdmittedOperation(t *testing.T) {
	e := NewEngine(nil)
	if !e.beginTaskOperation("shutdown") {
		t.Fatal("initial operation rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.close(cancel) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the server context")
	}
	if e.beginTaskOperation("late") {
		t.Fatal("shutdown admitted a new operation")
	}
	select {
	case <-done:
		t.Fatal("shutdown returned while an operation was still active")
	default:
	}
	e.decInflight("shutdown")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not drain the operation")
	}
}

func TestServerCloseStopsTaskRuntimesBeforeStores(t *testing.T) {
	if _, _, err := db.DSN(); err != nil {
		t.Skip(err)
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := newStartedServer(t, context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	defer s.Close()
	done := make(chan struct{})
	rt := s.engine.registerTaskRoutines(s.ctx, "shutdown-test", 1)
	runTaskRoutine(rt, func(ctx context.Context) { <-ctx.Done(); close(done) })
	s.Close()
	if err := m.pg.Ping(); err != nil {
		t.Fatalf("server closed a store owned by the application: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("Server returned before the runtime exited")
	}
	if s.ctx.Err() == nil {
		t.Fatal("Server did not cancel its runtime")
	}
	s.Close()
}
