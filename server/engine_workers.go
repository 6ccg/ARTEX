package server

import (
	"context"
	"log"

	"github.com/Autumn-27/artex/db"
)

// RefreshTaskWorkers changes the plan for an already started task. Retirement
// never cancels execution: a loop observes its new slot between intents.
func (e *Engine) RefreshTaskWorkers(t *Task) {
	if t == nil {
		return
	}
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	e.runtimeMu.Lock()
	run := e.runtimes[t.ID]
	ready := run != nil && run.ctx != nil && run.started.Load() && !run.deleting.Load()
	e.runtimeMu.Unlock()
	if e.closing.Load() || !ready {
		return
	}
	e.refreshTaskWorkersLocked(t, run)
}

func (e *Engine) RefreshInheritedWorkers() {
	for _, t := range e.m.List() {
		if t.lifecycleSnapshot().WorkerCount == 0 {
			e.RefreshTaskWorkers(t)
		}
	}
}

// The caller holds the Engine admission gate until all WaitGroup additions end.
func (e *Engine) refreshTaskWorkersLocked(t *Task, run *taskRun) {
	run.workersMu.Lock()
	defer run.workersMu.Unlock()
	// Read current settings inside the lock so concurrent saves cannot install
	// an older plan after a newer save has already refreshed this task.
	slots, err := workerSlotsForTask(t, e.m.WorkerPools(), e.m.Workers())
	if err != nil {
		log.Printf("[engine] task %s worker pool invalid, using default workers: %v", t.ID, err)
		slots, _ = ExpandWorkerSlots(nil, workerCountForTask(t, e.m.Workers()))
	}
	e.syncWorkerSlotsLocked(t, run, slots)
}

func (e *Engine) syncWorkerSlotsLocked(t *Task, run *taskRun, slots []WorkerSlot) {
	run.workerPlan = make(map[string]WorkerSlot, len(slots))
	if run.workerLoops == nil {
		run.workerLoops = make(map[string]bool)
	}
	for _, slot := range slots {
		run.workerPlan[slot.Name] = slot
		if run.workerLoops[slot.Name] {
			continue
		}
		run.workerLoops[slot.Name] = true
		run.wg.Add(1)
		runTaskRoutine(run, func(ctx context.Context) { e.workerLoop(ctx, t, slot) })
	}
}

func (e *Engine) nextWorkerSlot(taskID string, previous WorkerSlot) (WorkerSlot, bool) {
	run := e.lookupTaskRun(taskID)
	if run == nil {
		return previous, true
	}
	run.workersMu.Lock()
	defer run.workersMu.Unlock()
	if run.workerPlan == nil { // A directly invoked loop has a fixed slot.
		return previous, true
	}
	slot, enabled := run.workerPlan[previous.Name]
	if !enabled {
		// Remove ownership under the plan lock, so a concurrent increase either
		// keeps this loop alive or starts exactly one replacement.
		delete(run.workerLoops, previous.Name)
		return WorkerSlot{}, false
	}
	run.slotsMu.Lock()
	if run.slotPlans == nil {
		run.slotPlans = make(map[string]int64)
	}
	if id, exists := run.slotPlans[slot.Name]; exists && id != slot.ProfileID {
		delete(run.slots, slot.Name)
	}
	// Keep retired slot metadata for manually resumed intents owned by them.
	run.slotPlans[slot.Name] = slot.ProfileID
	run.slotsMu.Unlock()
	return slot, true
}

// Serialize the last plan check and claim with resizing. A retiring slot cannot
// claim fresh work after the new plan has been installed.
func (e *Engine) claimWorkerIntent(t *Task, slot WorkerSlot) *db.Node {
	if run := e.lookupTaskRun(t.ID); run != nil {
		run.workersMu.Lock()
		defer run.workersMu.Unlock()
		if run.workerPlan != nil {
			if current, ok := run.workerPlan[slot.Name]; !ok || current != slot {
				return nil
			}
		}
	}
	return e.claimNext(t, slot.Name)
}
