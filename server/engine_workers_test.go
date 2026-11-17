package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestIdleWorkerDoesNotResolveModels(t *testing.T) {
	for _, state := range []string{"paused", "done"} {
		t.Run(state, func(t *testing.T) {
			m := newTestManager(t)
			task, err := m.CreateTask("idle worker", "goal", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.DeleteTask(task.ID, DeleteTaskOptions{})
			e := NewEngine(m)
			if state == "paused" {
				e.Pause(task.ID, agent.AbortPausedByUser)
			} else if err := m.SetTaskStatus(task.ID, state); err != nil {
				t.Fatal(err)
			}
			var resolved atomic.Int32
			e.SetWorkerResolver(func(*Task, WorkerSlot) (*agent.Worker, error) {
				resolved.Add(1)
				return nil, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			e.workerLoop(ctx, task, WorkerSlot{Name: "work#1", ProfileID: 1})
			if resolved.Load() != 0 {
				t.Fatalf("idle worker resolved models %d times", resolved.Load())
			}
		})
	}
}

func waitWorkerCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for worker adjustment")
}

func TestWorkerRebindRejectsPreviouslyResolvedModelBeforeClaim(t *testing.T) {
	e := NewEngine(nil)
	old := WorkerSlot{Name: "work#1", ProfileID: 7}
	newSlot := WorkerSlot{Name: "work#1", ProfileID: 12}
	run := e.taskRun("1")
	run.workerPlan = map[string]WorkerSlot{newSlot.Name: newSlot}
	run.workerLoops = map[string]bool{newSlot.Name: true}
	// No Store is needed: the stale model must be rejected before querying it.
	if intent := e.claimWorkerIntent(&Task{ID: "1"}, old); intent != nil {
		t.Fatal("old model was allowed to claim work after reconfiguration")
	}
	if slot, enabled := e.nextWorkerSlot("1", old); !enabled || slot != newSlot {
		t.Fatalf("worker did not adopt the next model: %+v enabled=%v", slot, enabled)
	}
	run.workerPlan = map[string]WorkerSlot{}
	if intent := e.claimWorkerIntent(&Task{ID: "1"}, newSlot); intent != nil {
		t.Fatal("retired worker claimed work")
	}
	if _, enabled := e.nextWorkerSlot("1", newSlot); enabled || run.workerLoops[newSlot.Name] {
		t.Fatal("retired loop retained slot ownership")
	}
}

func TestLiveWorkerResizeDrainsAndDoesNotClaimAfterRetirement(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	task, err := m.CreateTaskWithOptions("live concurrency", "goal", db.TaskCreateOptions{WorkerCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	var intents []int64
	for range 3 {
		id, err := task.Store.AddIntent(map[string]any{"text": "test"}, 0, nil, "test")
		if err != nil {
			t.Fatal(err)
		}
		intents = append(intents, id)
	}
	type call struct {
		ctx     context.Context
		release chan struct{}
	}
	calls := make(chan call, 10)
	worker := agent.NewWorker(nil, retestProvider{complete: func(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		c := call{ctx: ctx, release: make(chan struct{})}
		calls <- c
		select {
		case <-c.release:
		case <-ctx.Done():
			return llm.Message{}, "", llm.Usage{}, ctx.Err()
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("finished")}}, "end_turn", llm.Usage{}, nil
	}}, "test", m.dir, nil, 10000, 1)
	worker.SetNonStreaming(func() bool { return true })
	e := NewEngine(m)
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return nil, worker })
	ctx, cancel := context.WithCancel(context.Background())
	defer e.close(cancel)
	e.Run(ctx, task)
	nextCall := func() call {
		t.Helper()
		select {
		case c := <-calls:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not start")
			return call{}
		}
	}
	resize := func(count int) {
		t.Helper()
		if _, err := m.UpdateTaskMetadata(task.ID, db.TaskPatch{WorkerCount: &count}); err != nil {
			t.Fatal(err)
		}
		e.RefreshTaskWorkers(task)
	}
	first := nextCall()
	resize(2)
	second := nextCall()
	resize(1)
	if first.ctx.Err() != nil || second.ctx.Err() != nil {
		t.Fatal("resizing cancelled in-flight work")
	}
	close(second.release)
	run := e.lookupTaskRun(task.ID)
	waitWorkerCondition(t, func() bool {
		run.workersMu.Lock()
		defer run.workersMu.Unlock()
		return len(run.workerLoops) == 1
	})
	last, err := task.Store.GetNode(intents[2])
	if err != nil || last.State != "open" {
		t.Fatalf("retiring worker claimed another intent: node=%+v err=%v", last, err)
	}
	// Grow again after retirement: the replacement must use the same slot only
	// once, while the original worker remains busy.
	resize(2)
	third := nextCall()
	close(third.release)
	close(first.release)
	waitWorkerCondition(t, func() bool { return e.inflightCount(task.ID) == 0 })
}

func TestTaskWorkerCountPatchAndGlobalRefresh(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	if err := m.SetWorkerPools(nil, 3); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("concurrency patch", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	e := NewEngine(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer e.close(cancel)
	e.Run(ctx, task)
	s := &Server{Runtime: &Runtime{m: m, engine: e}}
	patch := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		r.SetPathValue("id", task.ID)
		w := httptest.NewRecorder()
		s.updateTaskMetadata(w, r)
		return w
	}
	putSettings := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		s.putSettings(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("settings update failed: %d %s", w.Code, w.Body)
		}
	}
	planCount := func() int {
		run := e.lookupTaskRun(task.ID)
		run.workersMu.Lock()
		defer run.workersMu.Unlock()
		return len(run.workerPlan)
	}
	for _, body := range []string{`{"worker_count":-1}`, `{"worker_count":51}`, `{"worker_count":1.5}`} {
		if w := patch(body); w.Code != 400 {
			t.Fatalf("invalid count accepted: %s: %d %s", body, w.Code, w.Body)
		}
	}
	w := patch(`{"worker_count":2}`)
	if w.Code != 200 || planCount() != 2 {
		t.Fatalf("live patch failed: %d %s, slots=%d", w.Code, w.Body, planCount())
	}
	var dto TaskDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil || dto.WorkerCount != 2 {
		t.Fatalf("patch response: %+v err=%v", dto, err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	saved, err := m.pg.GetTask(id)
	if err != nil || saved.WorkerCount != 2 {
		t.Fatalf("override not persisted: %+v err=%v", saved, err)
	}
	putSettings(`{"workers":4}`)
	if planCount() != 2 {
		t.Fatal("global refresh changed an explicit task override")
	}
	if w := patch(`{"worker_count":0}`); w.Code != 200 || planCount() != 4 {
		t.Fatalf("reset to global failed: %d %s, slots=%d", w.Code, w.Body, planCount())
	}
	profileID, err := m.pg.SaveProfile(&db.LLMProfile{Name: "live-pool", Format: "openai", Model: "test", APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteProfile(profileID)
	putSettings(`{"worker_pools":[{"profile_id":"` + strconv.FormatInt(profileID, 10) + `","workers":2}]}`)
	run := e.lookupTaskRun(task.ID)
	run.workersMu.Lock()
	if len(run.workerPlan) != 2 || run.workerPlan["work#1"].ProfileID != profileID {
		t.Errorf("live model pool not applied: %+v", run.workerPlan)
	}
	run.workersMu.Unlock()
	putSettings(`{"worker_pools":[],"workers":3}`)
	if planCount() != 3 {
		t.Fatal("clearing model pools did not restore default workers")
	}
}
