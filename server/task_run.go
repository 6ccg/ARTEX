package server

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/agent"
)

// taskRun owns all transient state for one task. The Engine admission gate
// excludes registration before shutdown/deletion waits on wg. Runtime creation
// and the loop context are protected by Engine.runtimeMu; execution contexts and
// worker slot metadata have task-local locks.
type taskRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	execMu     sync.Mutex
	execCtx    context.Context
	execCancel context.CancelCauseFunc

	started      atomic.Bool
	paused       atomic.Bool
	deleting     atomic.Bool
	settling     atomic.Bool
	stamped      atomic.Bool
	coordinator  atomic.Pointer[int]
	lastAct      atomic.Int64
	llmCalls     atomic.Int64
	dropped      atomic.Int64
	plannerRound atomic.Int64
	deadline     atomic.Int64
	inflight     atomic.Int64

	slotsMu   sync.Mutex
	slots     map[string]slotAvailability
	slotPlans map[string]int64

	// workerPlan is desired capacity; workerLoops also includes draining slots.
	workersMu   sync.Mutex
	workerPlan  map[string]WorkerSlot
	workerLoops map[string]bool
}

func (e *Engine) taskRun(taskID string) *taskRun {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	if e.runtimes == nil {
		e.runtimes = make(map[string]*taskRun)
	}
	run := e.runtimes[taskID]
	if run == nil {
		run = &taskRun{}
		e.runtimes[taskID] = run
	}
	return run
}

func (e *Engine) lookupTaskRun(taskID string) *taskRun {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	return e.runtimes[taskID]
}

func (r *taskRun) cancelExecution(cause error) {
	r.execMu.Lock()
	defer r.execMu.Unlock()
	if r.execCancel != nil {
		r.execCancel(cause)
	}
}

func (e *Engine) bumpDrop(taskID string) int64 {
	return e.taskRun(taskID).dropped.Add(1)
}

func (e *Engine) nextPlannerRound(taskID string) int {
	return int(e.taskRun(taskID).plannerRound.Add(1))
}

func (e *Engine) Pause(taskID string, cause error) {
	run := e.taskRun(taskID)
	run.execMu.Lock()
	defer run.execMu.Unlock()
	run.paused.Store(true)
	if run.execCancel != nil {
		run.execCancel(cause)
	}
}

func (e *Engine) BeginDelete(taskID string) bool {
	e.deleteMu.Lock()
	defer e.deleteMu.Unlock()
	if e.closing.Load() {
		return false
	}
	run := e.taskRun(taskID)
	if !run.deleting.CompareAndSwap(false, true) {
		return false
	}
	run.paused.Store(true)
	run.cancelExecution(agent.AbortTaskDeleted)
	return true
}

func (e *Engine) AbortDelete(taskID string, keepPaused bool) {
	e.deleteMu.Lock()
	run := e.lookupTaskRun(taskID)
	if run == nil || !run.deleting.Load() {
		e.deleteMu.Unlock()
		return
	}
	run.deleting.Store(false)
	if !keepPaused {
		run.paused.Store(false)
	}
	e.deleteMu.Unlock()
	if !keepPaused && e.m != nil {
		if task, ok := e.m.Task(taskID); ok {
			task.Notify()
		}
	}
}

func (e *Engine) IsDeleting(taskID string) bool {
	run := e.lookupTaskRun(taskID)
	return run != nil && run.deleting.Load()
}

// The caller holds the admission gate for reading until wg.Add completes.
func (e *Engine) registerTaskRoutines(parent context.Context, taskID string, count int) *taskRun {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	if e.runtimes == nil {
		e.runtimes = make(map[string]*taskRun)
	}
	run := e.runtimes[taskID]
	if run == nil {
		run = &taskRun{}
		e.runtimes[taskID] = run
	}
	if run.ctx == nil {
		run.ctx, run.cancel = context.WithCancel(parent)
	}
	run.wg.Add(count)
	return run
}

func runTaskRoutine(run *taskRun, fn func(context.Context)) {
	go func() {
		defer run.wg.Done()
		fn(run.ctx)
	}()
}

func (e *Engine) isClosing() bool { return e.closing.Load() }

func (e *Engine) close(cancelRoot context.CancelFunc) {
	e.deleteMu.Lock()
	e.closing.Store(true)
	e.runtimeMu.Lock()
	runs := make([]*taskRun, 0, len(e.runtimes))
	for _, run := range e.runtimes {
		run.cancelExecution(agent.AbortShutdown)
		if run.cancel != nil {
			run.cancel()
		}
		runs = append(runs, run)
	}
	e.runtimeMu.Unlock()
	e.deleteMu.Unlock()
	if cancelRoot != nil {
		cancelRoot()
	}
	for _, run := range runs {
		run.wg.Wait()
	}
}

// StopTask waits for loops AND admitted task operations, then drops one owner
// instead of independently clearing a collection of task-indexed maps.
func (e *Engine) StopTask(taskID string) {
	e.deleteMu.Lock()
	run := e.lookupTaskRun(taskID)
	if run == nil {
		e.deleteMu.Unlock()
		return
	}
	run.deleting.Store(true)
	run.paused.Store(true)
	run.cancelExecution(agent.AbortTaskDeleted)
	e.runtimeMu.Lock()
	if run.cancel != nil {
		run.cancel()
	}
	e.runtimeMu.Unlock()
	e.deleteMu.Unlock()
	run.wg.Wait()
	e.deleteMu.Lock()
	e.runtimeMu.Lock()
	if e.runtimes[taskID] == run {
		delete(e.runtimes, taskID)
	}
	e.runtimeMu.Unlock()
	e.deleteMu.Unlock()
}

func (e *Engine) cancelExec(taskID string, cause error) {
	if run := e.lookupTaskRun(taskID); run != nil {
		run.cancelExecution(cause)
	}
}

func (e *Engine) Resume(task *Task) {
	if task == nil {
		return
	}
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.closing.Load() || e.IsDeleting(task.ID) {
		return
	}
	run := e.taskRun(task.ID)
	run.execMu.Lock()
	run.paused.Store(false)
	run.execMu.Unlock()
	task.Notify()
}

func (e *Engine) execContextFor(parent context.Context, taskID string) context.Context {
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.closing.Load() {
		ctx, cancel := context.WithCancelCause(parent)
		cancel(agent.AbortShutdown)
		return ctx
	}
	run := e.taskRun(taskID)
	run.execMu.Lock()
	defer run.execMu.Unlock()
	if run.paused.Load() {
		ctx, cancel := context.WithCancelCause(parent)
		cancel(agent.AbortPausedRaceGuard)
		return ctx
	}
	if run.execCtx == nil || run.execCtx.Err() != nil {
		run.execCtx, run.execCancel = context.WithCancelCause(parent)
	}
	return run.execCtx
}

func (e *Engine) IsPaused(taskID string) bool {
	run := e.lookupTaskRun(taskID)
	return run != nil && run.paused.Load()
}

func (e *Engine) Started(taskID string) bool {
	run := e.lookupTaskRun(taskID)
	return run != nil && run.started.Load()
}

func (e *Engine) LastActivity(taskID string) int64 {
	if run := e.lookupTaskRun(taskID); run != nil {
		return run.lastAct.Load()
	}
	return 0
}

func (e *Engine) BeginLLMCall(taskID string) { e.taskRun(taskID).llmCalls.Add(1) }

func (e *Engine) EndLLMCall(taskID string) {
	if run := e.lookupTaskRun(taskID); run != nil {
		for {
			count := run.llmCalls.Load()
			if count <= 0 || run.llmCalls.CompareAndSwap(count, count-1) {
				return
			}
		}
	}
}

func (e *Engine) ActiveLLMCalls(taskID string) int64 {
	if run := e.lookupTaskRun(taskID); run != nil {
		return run.llmCalls.Load()
	}
	return 0
}

func (e *Engine) touch(taskID string) { e.taskRun(taskID).lastAct.Store(time.Now().Unix()) }

func NewEngine(m *Manager) *Engine {
	return &Engine{m: m, debounce: 800 * time.Millisecond, bc: NewBroadcaster(),
		work: map[int64]*workExecution{}, steerBox: map[int64][]string{},
		runtimes: map[string]*taskRun{}}
}

func (e *Engine) setSlotPlan(taskID string, plan map[string]int64) {
	run := e.taskRun(taskID)
	run.slotsMu.Lock()
	run.slotPlans = plan
	run.slotsMu.Unlock()
}

func (e *Engine) slotProfile(taskID, slot string) (int64, bool) {
	run := e.lookupTaskRun(taskID)
	if run == nil {
		return 0, false
	}
	run.slotsMu.Lock()
	defer run.slotsMu.Unlock()
	id, ok := run.slotPlans[slot]
	return id, ok
}
