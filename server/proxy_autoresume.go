package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
)

const (
	settingProxyReconnectMax      = "proxy_reconnect_max_attempts"
	settingProxyReconnectInterval = "proxy_reconnect_interval_seconds"
	defaultProxyReconnectMax      = 5
	defaultProxyReconnectInterval = 30
)

// isProxyFailure is used for LLM/planner errors, where the error has no trusted
// indication that it came from the global target proxy. Keep this deliberately
// narrow: generic EOF/reset/refused errors belong to the model_error retry path.
func isProxyFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, part := range []string{"proxyconnect", "proxy connection", "proxy error", "proxy handshake", "socks handshake", "failed to connect to proxy", "connect to proxy"} {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

// isGlobalProxyDialFailure is only used by the MITM upstream callback. That
// callback is on the actual configured global-proxy path; the saved proxy is
// still probed before any task is paused, so a target-side RequestError cannot
// pause tasks while the proxy itself remains healthy.
func isGlobalProxyDialFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, part := range []string{"proxyconnect", "proxy connection", "proxy error", "proxy handshake", "socks", "connect to proxy", "upstream proxy", "connection refused", "connection reset", "network is unreachable", "broken pipe", "unexpected eof", "eof"} {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

type proxyReconnectSupervisor struct {
	mu      sync.Mutex
	wake    chan struct{}
	started bool
}

type proxyReconnectGuard struct {
	generation   uint64
	attempt      int
	checkAttempt bool
}

func (s *Runtime) snapshotProxyReconnectGuards(checkAttempt bool) map[string]proxyReconnectGuard {
	return s.snapshotProxyReconnectGuardsWithExhausted(checkAttempt, false)
}

func (s *Runtime) snapshotProxyReconnectGuardsWithExhausted(checkAttempt, includeExhausted bool) map[string]proxyReconnectGuard {
	guards := make(map[string]proxyReconnectGuard)
	if s == nil || s.m == nil {
		return guards
	}
	for _, t := range s.m.List() {
		st := t.lifecycleSnapshot()
		if st.PauseReason != "paused_by_proxy" {
			continue
		}
		if st.ReconnectExhausted && !includeExhausted {
			continue
		}
		guards[t.ID] = proxyReconnectGuard{
			generation:   st.ReconnectGeneration,
			attempt:      st.ReconnectAttempt,
			checkAttempt: checkAttempt,
		}
	}
	return guards
}

func proxyReconnectGuardMatches(st taskLifecycleState, guard proxyReconnectGuard) bool {
	return st.PauseReason == "paused_by_proxy" && st.ReconnectGeneration == guard.generation &&
		(!guard.checkAttempt || st.ReconnectAttempt == guard.attempt)
}

func (s *Runtime) startProxySupervisor() {
	s.proxyReconnect.mu.Lock()
	if s.proxyReconnect.wake == nil {
		s.proxyReconnect.wake = make(chan struct{}, 1)
	}
	if !s.proxyReconnect.started {
		s.proxyReconnect.started = true
		s.backgroundWG.Go(func() { s.runProxyReconnect(s.ctx) })
	}
	s.proxyReconnect.mu.Unlock()
}

func (s *Runtime) signalProxySupervisor() {
	s.proxyReconnect.mu.Lock()
	if s.proxyReconnect.wake == nil {
		s.proxyReconnect.wake = make(chan struct{}, 1)
	}
	wake := s.proxyReconnect.wake
	s.proxyReconnect.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *Runtime) proxyReconnectConfig() (int, time.Duration) {
	max := defaultProxyReconnectMax
	interval := defaultProxyReconnectInterval
	if s == nil || s.m == nil || s.m.pg == nil {
		return max, time.Duration(interval) * time.Second
	}
	if v, ok, _ := s.m.pg.GetSetting(settingProxyReconnectMax); ok {
		if n, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &max); err != nil || n != 1 || max <= 0 {
			max = defaultProxyReconnectMax
		}
	}
	if v, ok, _ := s.m.pg.GetSetting(settingProxyReconnectInterval); ok {
		if n, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &interval); err != nil || n != 1 || interval <= 0 {
			interval = defaultProxyReconnectInterval
		}
	}
	return max, time.Duration(interval) * time.Second
}

func (s *Runtime) runProxyProbe(ctx context.Context, proxyRaw, caPEMPath string, probes []string, timeout time.Duration) egressProbeResult {
	return s.runProxyProbeFamily(ctx, proxyRaw, caPEMPath, probes, timeout, 0)
}

func (s *Runtime) runProxyProbeFamily(ctx context.Context, proxyRaw, caPEMPath string, probes []string, timeout time.Duration, family int) egressProbeResult {
	if s != nil && s.proxyProbe != nil {
		return s.proxyProbe(ctx, proxyRaw, caPEMPath, probes, timeout)
	}
	return probeEgressFamily(ctx, proxyRaw, caPEMPath, probes, timeout, family)
}

func (s *Runtime) handleProxyFailure(taskID string, cause error) {
	if strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	if s.handleProxyPoolFailure(cause) {
		return
	}
	if probe := s.runProxyProbe(s.ctx, s.m.GlobalProxy(), "", s.m.egressProbes(), 10*time.Second); probe.OK {
		return
	}
	if s.stopping() {
		return
	}
	s.pauseProxyTask(taskID, cause)
}

func (s *Runtime) pauseProxyTask(taskID string, cause error) {
	if strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	max, interval := s.proxyReconnectConfig()
	now := time.Now().Add(interval).Unix()
	lastError := truncateProxyError(cause)
	transitioned, err := s.m.ApplyProxyPauseWithCallback(taskID, func() {
		if s.engine != nil {
			s.engine.Pause(taskID, agent.AbortPausedByProxy)
		}
		if t, ok := s.m.Task(taskID); ok {
			t.updateLifecycle(func(st *taskLifecycleState) {
				// A successful admission clears the prior round's counters. This
				// transition starts a fresh round, so make that reset atomic with
				// the persisted proxy pause and install a new generation.
				st.ReconnectGeneration++
				st.ReconnectAttempt = 0
				st.ReconnectMax = max
				st.ReconnectNextAt = now
				st.ReconnectExhausted = false
				st.ReconnectLastError = lastError
			})
		}
	})
	if err != nil {
		log.Printf("[proxy] pause task %s: %v", taskID, err)
		return
	}
	if !transitioned {
		// A no-op means the task was already paused (possibly by a user,
		// schedule, or an exhausted proxy attempt). Preserve its reason and
		// reconnect state exactly as-is.
		return
	}
	s.cancelTaskChat(taskID, agent.AbortChatPausedWithTask)
	s.signalProxySupervisor()
}

func (s *Runtime) handleProxyTrafficFailure(cause error) {
	if s.handleProxyPoolFailure(cause) {
		return
	}
	if !isGlobalProxyDialFailure(cause) || strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	// Confirm the saved global proxy itself before pausing. MITM RequestError can
	// also represent a target-side failure, and those must not pause a task.
	if probe := probeEgress(serverContext(s), s.m.GlobalProxy(), "", s.m.egressProbes(), 10*time.Second); probe.OK {
		return
	}
	if s.stopping() {
		return
	}
	for _, t := range s.m.List() {
		st := t.lifecycleSnapshot()
		if isTerminalStatus(st.Status) || st.Paused || s.engine.IsDeleting(t.ID) {
			continue
		}
		s.pauseProxyTask(t.ID, cause)
	}
}

func (s *Runtime) handleProxyToolFailure(ctx context.Context, cause error) {
	if s.handleProxyPoolFailure(cause) {
		return
	}
	if !isGlobalProxyDialFailure(cause) || strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	if probe := probeEgress(ctx, s.m.GlobalProxy(), "", s.m.egressProbes(), 10*time.Second); probe.OK {
		return
	}
	if ctx.Err() != nil || s.stopping() {
		return
	}
	if info := agent.RunInfoFrom(ctx); info.TaskID > 0 {
		s.pauseProxyTask(fmt.Sprint(info.TaskID), cause)
	}
}

func truncateProxyError(err error) string {
	if err == nil {
		return ""
	}
	s := maskSecrets(err.Error())
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

func (s *Runtime) runProxyReconnect(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.proxyReconnectTick()
		case <-s.proxyReconnect.wake:
			s.proxyReconnectTick()
		}
	}
}

func (s *Runtime) proxyReconnectTick() {
	if s.stopping() {
		return
	}
	if strings.TrimSpace(s.m.GlobalProxy()) == "" {
		// Clearing the runtime route is a successful direct transition. Reuse the
		// generation gate so only eligible, non-exhausted proxy pauses resume.
		s.proxyReconnectSuccess()
		return
	}
	if s.m.proxyPool != nil && len(s.m.proxyPoolEntries()) > 0 {
		// A pool tick may confirm the active route or switch to another available
		// member. If it cannot confirm either, retain the original #106 per-task
		// attempt/backoff/exhaustion loop (especially for a one-member legacy
		// pool).
		outcome := s.proxyPoolTick()
		if outcome.confirmed || outcome.switched {
			return
		}
	}
	now := time.Now().Unix()
	max, interval := s.proxyReconnectConfig()
	for _, t := range s.m.List() {
		if s.stopping() {
			return
		}
		st := t.lifecycleSnapshot()
		if isTerminalStatus(st.Status) {
			t.updateLifecycle(func(v *taskLifecycleState) {
				if v.PauseReason == "paused_by_proxy" {
					v.PauseReason = ""
				}
				v.ReconnectAttempt, v.ReconnectMax, v.ReconnectNextAt = 0, 0, 0
				v.ReconnectExhausted, v.ReconnectLastError = false, ""
			})
			continue
		}
		if st.PauseReason != "paused_by_proxy" || st.ReconnectExhausted || (st.ReconnectNextAt > 0 && st.ReconnectNextAt > now) {
			continue
		}
		attempt := st.ReconnectAttempt + 1
		round := st.ReconnectGeneration
		claimed := false
		t.updateLifecycle(func(v *taskLifecycleState) {
			if v.PauseReason != "paused_by_proxy" || v.ReconnectGeneration != round || v.ReconnectAttempt != st.ReconnectAttempt {
				return
			}
			v.ReconnectAttempt = attempt
			v.ReconnectMax = max
			v.ReconnectNextAt = 0
			claimed = true
		})
		if !claimed {
			continue
		}
		res := s.runProxyProbe(serverContext(s), s.m.GlobalProxy(), "", s.m.egressProbes(), 10*time.Second)
		if s.stopping() {
			return
		}
		if res.OK {
			s.proxyReconnectSuccessWithGuards(map[string]proxyReconnectGuard{
				t.ID: {generation: round, attempt: attempt, checkAttempt: true},
			}, nil)
			return
		}
		err := res.Error
		exhausted := attempt >= max
		next := time.Now().Add(interval).Unix()
		t.updateLifecycle(func(v *taskLifecycleState) {
			if v.PauseReason != "paused_by_proxy" || v.ReconnectGeneration != round || v.ReconnectAttempt != attempt {
				return
			}
			v.ReconnectNextAt = next
			v.ReconnectExhausted = exhausted
			v.ReconnectLastError = err
		})
		log.Printf("[proxy] reconnect attempt %d/%d failed: %s", attempt, max, err)
	}
}

func (s *Runtime) proxyReconnectSuccess() {
	s.proxyReconnectSuccessWithGuards(s.snapshotProxyReconnectGuards(false), nil)
}

func (s *Runtime) proxyReconnectSuccessFor(taskID string, generation uint64, attempt int) {
	s.proxyReconnectSuccessWithGuards(map[string]proxyReconnectGuard{
		taskID: {generation: generation, attempt: attempt, checkAttempt: true},
	}, nil)
}

// proxyReconnectSuccessWithHook is kept small and private so tests can
// deterministically place a competing pause immediately after admission has
// released taskControlMu. Production callers use proxyReconnectSuccess without a
// hook.
func (s *Runtime) proxyReconnectSuccessWithHook(afterAdmission func()) {
	s.proxyReconnectSuccessWithGuards(s.snapshotProxyReconnectGuards(false), afterAdmission)
}

func (s *Runtime) proxyReconnectSuccessWithGuard(expectedID string, expectedGeneration uint64, expectedAttempt int, afterAdmission func()) {
	guards := s.snapshotProxyReconnectGuards(false)
	if expectedID != "" {
		guards = map[string]proxyReconnectGuard{
			expectedID: {generation: expectedGeneration, attempt: expectedAttempt, checkAttempt: true},
		}
	}
	s.proxyReconnectSuccessWithGuards(guards, afterAdmission)
}

func (s *Runtime) proxyReconnectSuccessWithGuards(guards map[string]proxyReconnectGuard, afterAdmission func()) {
	if s.m.TransparentProxy() && strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	max, interval := s.proxyReconnectConfig()
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	for _, t := range s.m.List() {
		guard, ok := guards[t.ID]
		if !ok {
			continue
		}
		if !proxyReconnectGuardMatches(t.lifecycleSnapshot(), guard) {
			continue
		}
		if s.m.pg != nil {
			inside, scheduleErr := s.proxyResumeInsideSchedule(t.ID, time.Now())
			if scheduleErr != nil {
				log.Printf("[proxy] schedule gate for task %s: %v", t.ID, scheduleErr)
				continue
			}
			if !inside {
				if err := s.m.ConvertProxyPauseToSchedule(t.ID); err != nil {
					log.Printf("[proxy] schedule pause task %s: %v", t.ID, err)
				}
				continue
			}
		}
		var err error
		if s.engine != nil {
			_, err = s.admitTaskWhenScheduledWithGenerationChecked(t, "resume", true, false, "paused_by_proxy", true, guard.generation, true)
			if afterAdmission != nil {
				afterAdmission()
			}
		} else {
			err = s.m.ClearPauseReason(t.ID)
		}
		if err != nil {
			// Admission re-checks the persisted reason under taskControlMu. A user pause,
			// terminal transition, delete, or archive therefore cannot be revived;
			// only keep probing if the proxy pause is still eligible.
			st := t.lifecycleSnapshot()
			if proxyReconnectGuardMatches(st, guard) && !isTerminalStatus(st.Status) {
				t.updateLifecycle(func(v *taskLifecycleState) {
					v.ReconnectMax = max
					v.ReconnectNextAt = time.Now().Add(interval).Unix()
				})
			}
			log.Printf("[proxy] resume task %s: %v", t.ID, err)
			continue
		}
		// The non-queued path's startAdmittedTask already resumes the Engine
		// while taskControlMu is held. Do not Resume again after releasing the lock:
		// a concurrent user/orchestrator pause could have installed a newer
		// barrier in the meantime.
		t.updateLifecycle(func(v *taskLifecycleState) {
			if !proxyReconnectGuardMatches(*v, guard) {
				return
			}
			v.ReconnectAttempt = 0
			v.ReconnectMax = 0
			v.ReconnectNextAt = 0
			v.ReconnectExhausted = false
			v.ReconnectLastError = ""
		})
	}
}

func (s *Server) testTaskProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.m.Task(id)
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	proxy := strings.TrimSpace(s.m.GlobalProxy())
	guards := s.snapshotProxyReconnectGuardsWithExhausted(false, true)
	if proxy == "" {
		if s.m.TransparentProxy() {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "未配置执行环境代理，Docker 出网已阻断"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "direct": true})
		s.proxyReconnectSuccessWithGuards(guards, nil)
		return
	}
	res := s.runProxyProbe(r.Context(), proxy, "", s.m.egressProbes(), 10*time.Second)
	if res.OK {
		s.proxyReconnectSuccessWithGuards(guards, nil)
	}
	if !res.OK && res.Error == "" {
		res.Error = "代理探测失败"
	}
	writeJSON(w, 200, map[string]any{"ok": res.OK, "error": res.Error, "task_id": t.ID})
}

func (s *Runtime) setProxyReconnectSettings(max, interval *int) error {
	if max != nil {
		if *max < 1 || *max > 1000 {
			return errors.New("proxy reconnect max must be 1..1000")
		}
		if err := s.m.pg.SetSetting(settingProxyReconnectMax, fmt.Sprint(*max)); err != nil {
			return err
		}
	}
	if interval != nil {
		if *interval < 1 || *interval > 86400 {
			return errors.New("proxy reconnect interval must be 1..86400 seconds")
		}
		if err := s.m.pg.SetSetting(settingProxyReconnectInterval, fmt.Sprint(*interval)); err != nil {
			return err
		}
	}
	return nil
}
