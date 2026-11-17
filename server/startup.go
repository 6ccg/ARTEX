package server

import (
	"errors"
	"log"

	"github.com/Autumn-27/artex/agent"
)

// Start starts background producers once. Holding the lifecycle gate until all
// WaitGroup registrations finish prevents Close from racing Add with Wait.
func (s *Runtime) Start() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closing {
		return errors.New("runtime is closed")
	}
	if s.started {
		return nil
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.started = true
	s.startSideQuestions()
	if s.m.pg != nil {
		s.backgroundWG.Go(func() { s.evidenceStore().RunGC(s.ctx) })
		s.backgroundWG.Go(func() { newNotifier(s).Run(s.ctx) })
		s.backgroundWG.Go(s.discoverEmptyMCPsOnStartup)
		s.stopLogWriter = logSink.attachDB(s.m.pg)
	}
	s.restoreTaskRuntimes()
	s.startProxySupervisor()
	s.proxyPoolWG.Add(1)
	go func() { defer s.proxyPoolWG.Done(); s.runProxyPoolSupervisor(s.ctx) }()
	s.backgroundWG.Go(s.reconcileConcurrency)
	s.startTaskArchiveWorker()
	if s.m.pg != nil {
		s.backgroundWG.Go(s.runCredentialProcessor)
	}
	return nil
}

// Restored deadline and worker loops must inherit the same context as new tasks,
// including the side-question checkpoint publisher installed during startup.
func (s *Runtime) restoreTaskRuntimes() {
	m := s.m
	// reload tasks persisted on disk so the task list survives a restart, and
	// restore persisted paused state (so a task paused before restart stays paused).
	for _, t := range m.LoadExisting() {
		lifecycle := t.lifecycleSnapshot()
		// clear stale 'running' intents from a prior crash/restart (no live worker
		// owns them) so they re-claim instead of spinning forever in the UI.
		if n, _ := t.Store.ResetRunningIntents(); n > 0 {
			log.Printf("[engine] task %s 重置 %d 个残留 running 意图为 open", t.ID, n)
		}
		if lifecycle.Paused {
			s.engine.Pause(t.ID, agent.AbortPausedOnReload)
		}
		// 任务级超时:为每个未终态、带 timeout 的任务起 deadline 协调器,独立于 planner/worker
		// loop——非活跃任务重启后也能在到点后被收尾(deadline 已过则立即走收尾时序)。
		if !isTerminalStatus(lifecycle.Status) {
			s.engine.startDeadlineCoordinator(s.ctx, t)
		}
	}
	s.backgroundWG.Go(func() { newScheduler(s).Run(s.ctx) })
	// Restore every task that had already been admitted before shutdown. Starting
	// only the active UI task left other non-queued tasks counted as concurrency
	// occupants without live loops, which could permanently block the persistent
	// FIFO. Paused loops remain idle; queued tasks are admitted below as slots allow.
	for _, t := range m.List() {
		lifecycle := t.lifecycleSnapshot()
		if !lifecycle.Queued && !isTerminalStatus(lifecycle.Status) {
			s.engine.Run(s.ctx, t)
		}
	}
}
