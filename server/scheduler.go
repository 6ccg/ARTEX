package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Scheduler drives P3 triggers: on each tick it fires due interval triggers and
// scans for new findings / newly-met goals (any task) to fire event triggers.
// Each fire enqueues a NEW conversation on the agent's per-agent trigger queue
// (StartTriggeredRun): fires for the same agent run one at a time in FIFO order,
// distinct agents still run concurrently. State is persisted (per-trigger last_fire
// + finding watermark + fired-goal set) so a restart resumes without double-firing.
// Triggers only attach to CUSTOM agents.
type Scheduler struct {
	s         *Runtime
	pg        *db.DB
	tick      time.Duration
	startedAt time.Time
}

const (
	schedKeyLastFinding    = "last_finding_id"     // watermark: max finding node id fired for
	schedKeyFiredGoals     = "fired_goals"         // JSON array of goal node ids already fired
	schedKeyLastTimeout    = "last_timeout_cursor" // completion time + task ID
	schedKeyLastToolCall   = "last_toolcall_id"    // watermark: max activity id fired for tool call
	schedKeyLastTaskCreate = "last_taskcreate_id"  // watermark: max task id fired for task create
)

func newScheduler(s *Runtime) *Scheduler {
	return &Scheduler{s: s, pg: s.m.pg, tick: 5 * time.Second, startedAt: time.Now()}
}

// Run loops until ctx is done, ticking the scheduler. Started once from server New.
func (sc *Scheduler) Run(ctx context.Context) {
	if sc.pg == nil {
		return
	}
	for {
		if err := sc.init(); err == nil {
			break
		} else {
			log.Printf("[scheduler] 初始化事件进度失败: %v", err)
		}
		if sleepCtx(ctx, sc.tick) {
			return
		}
	}
	t := time.NewTicker(sc.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sc.s.reconcileConcurrency() // 并发上限:有空位就把排队任务补位启动
			sc.step()
		}
	}
}

// init seeds the watermarks on first run so pre-existing findings/goals don't all
// fire at once — only events created AFTER the scheduler first starts count.
func (sc *Scheduler) init() error { return sc.syncWatermarks(true) }

// With no enabled triggers, consume progress without loading historical report
// bodies or tool output. Initialization uses the same projections for missing keys.
func (sc *Scheduler) syncWatermarks(onlyMissing bool) error {
	for _, entry := range []struct{ key, query string }{
		{schedKeyLastFinding, `SELECT COALESCE(MAX(n.id),0) FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE n.kind='finding' AND t.deleted_at IS NULL`},
		{schedKeyLastToolCall, `SELECT COALESCE(MAX(a.id),0) FROM activity a JOIN tasks t ON t.exploration_id=a.exploration_id WHERE a.kind='tool_result' AND t.deleted_at IS NULL`},
		{schedKeyLastTaskCreate, `SELECT COALESCE(MAX(id),0) FROM tasks WHERE deleted_at IS NULL`},
	} {
		raw, err := sc.pg.GetSchedState(entry.key)
		if err != nil {
			return err
		}
		if onlyMissing && raw != "" {
			continue
		}
		var last, next int64
		if raw != "" {
			last, err = strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return err
			}
		}
		if err := sc.pg.QueryRow(entry.query).Scan(&next); err != nil {
			return err
		}
		if raw == "" || next > last {
			if err := sc.pg.SetSchedState(entry.key, strconv.FormatInt(next, 10)); err != nil {
				return err
			}
		}
	}
	raw, err := sc.pg.GetSchedState(schedKeyFiredGoals)
	if err != nil {
		return err
	}
	if !onlyMissing || raw == "" {
		ids := []int64{}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &ids); err != nil {
				return err
			}
		}
		seen := make(map[int64]bool, len(ids))
		for _, id := range ids {
			seen[id] = true
		}
		rows, err := sc.pg.Query(`SELECT n.id FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE n.kind='goal' AND n.state='met' AND t.deleted_at IS NULL`)
		if err != nil {
			return err
		}
		changed := raw == ""
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if !seen[id] {
				seen[id], changed = true, true
				ids = append(ids, id)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if changed {
			data, err := json.Marshal(ids)
			if err != nil {
				return err
			}
			if err := sc.pg.SetSchedState(schedKeyFiredGoals, string(data)); err != nil {
				return err
			}
		}
	}
	raw, err = sc.pg.GetSchedState(schedKeyLastTimeout)
	if err != nil {
		return err
	}
	if !onlyMissing || raw == "" {
		var previous db.TaskTimeoutCursor
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &previous); err != nil {
				return err
			}
		}
		cursor, err := sc.pg.LatestTaskTimeoutCursor()
		if err != nil {
			return err
		}
		if raw == "" || cursor.CompletedAt.After(previous.CompletedAt) ||
			(cursor.CompletedAt.Equal(previous.CompletedAt) && cursor.TaskID > previous.TaskID) {
			data, err := json.Marshal(cursor)
			if err != nil {
				return err
			}
			return sc.pg.SetSchedState(schedKeyLastTimeout, string(data))
		}
	}
	return nil
}

func (sc *Scheduler) step() {
	sc.fireTaskSchedules()
	triggers, err := sc.pg.ListEnabledTriggers()
	if err != nil {
		return
	}
	if len(triggers) == 0 {
		if err := sc.syncWatermarks(false); err != nil {
			log.Printf("[scheduler] 更新停用期间事件进度失败: %v", err)
		}
		return
	}
	sc.fireIntervals(triggers)
	sc.fireFindings(triggers)
	sc.fireGoals(triggers)
	sc.fireTaskTimeouts(triggers)
	sc.fireToolCalls(triggers)
	sc.fireTaskCreates(triggers)
}

// fireIntervals fires triggers whose interval has elapsed since last_fire.
func (sc *Scheduler) fireIntervals(triggers []*db.AgentTrigger) {
	now := time.Now()
	for _, tr := range triggers {
		if tr.IntervalSec <= 0 {
			continue
		}
		due := tr.LastFire == nil || now.Sub(*tr.LastFire) >= time.Duration(tr.IntervalSec)*time.Second
		if !due {
			continue
		}
		ctx := "\n\n【本次为定时触发】" + now.Format(" 2006-01-02 15:04:05 MST")
		if sc.s.StartTriggeredRun(tr.AgentKey, fmt.Sprintf("定时触发 · %s", now.Format("15:04")), tr.IntervalMessage+ctx, 0, false, "", "") {
			_ = sc.pg.TouchTriggerFire(tr.ID)
		}
	}
}

// fireFindings fires on_finding triggers for findings above the persisted
// watermark (monotonic node id → no double-fire across restarts).
func (sc *Scheduler) fireFindings(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnFinding {
			want = append(want, tr)
		}
	}
	last, _ := strconv.ParseInt(sc.mustState(schedKeyLastFinding), 10, 64)
	if !sc.triggersReady(want) {
		return
	}
	events, err := sc.pg.NewFindingsSince(last)
	if err != nil || len(events) == 0 {
		return
	}
	// Advance the watermark whether or not any on_finding trigger is active: a
	// finding fires only for triggers live at the moment it appears. Otherwise a
	// trigger enabled later would replay the entire historical backlog at once.
	maxID := last
	for _, e := range events {
		if e.NodeID > maxID {
			maxID = e.NodeID
		}
		if len(want) == 0 {
			continue
		}
		msgCtx := fmt.Sprintf("\n\n【本次由任务发现 finding 触发】\n发现: [%s/%s] %s",
			e.VulnClass, e.Severity, e.Summary)
		for _, tr := range want {
			if !sc.s.StartTriggeredRun(tr.AgentKey, fmt.Sprintf("finding 触发 · task#%d", e.TaskID), tr.FindingMessage+msgCtx, e.TaskID, true, e.TaskDesc, e.TaskGoal) {
				return
			}
		}
	}
	_ = sc.pg.SetSchedState(schedKeyLastFinding, strconv.FormatInt(maxID, 10))
}

// fireGoals fires on_goal_met triggers for met goals not yet in the fired set.
func (sc *Scheduler) fireGoals(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnGoalMet {
			want = append(want, tr)
		}
	}
	if !sc.triggersReady(want) {
		return
	}
	events, err := sc.pg.MetGoals()
	if err != nil || len(events) == 0 {
		return
	}
	// Mark goals as consumed whether or not a trigger is active, so enabling an
	// on_goal_met trigger later doesn't replay every already-met goal.
	fired := sc.firedGoalSet()
	changed := false
	for _, e := range events {
		if fired[e.NodeID] {
			continue
		}
		fired[e.NodeID] = true
		changed = true
		if len(want) == 0 {
			continue
		}
		msgCtx := fmt.Sprintf("\n\n【本次由任务完成目标触发】\n达成目标: %s", e.Summary)
		for _, tr := range want {
			if !sc.s.StartTriggeredRun(tr.AgentKey, fmt.Sprintf("目标触发 · task#%d", e.TaskID), tr.GoalMessage+msgCtx, e.TaskID, true, e.TaskDesc, e.TaskGoal) {
				return
			}
		}
	}
	if changed {
		sc.saveFiredGoalSet(fired)
	}
}

// fireTaskTimeouts fires on_task_timeout triggers for tasks that newly reached
// status='timeout' after the persisted completion cursor.
func (sc *Scheduler) fireTaskTimeouts(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnTaskTimeout {
			want = append(want, tr)
		}
	}
	raw, err := sc.pg.GetSchedState(schedKeyLastTimeout)
	if err != nil {
		return
	}
	var cursor db.TaskTimeoutCursor
	if !sc.triggersReady(want) {
		return
	}
	if err := json.Unmarshal([]byte(raw), &cursor); err != nil {
		return
	}
	events, err := sc.pg.TimedOutTasksSince(cursor)
	if err != nil || len(events) == 0 {
		return
	}
	// Advance the watermark even with no active trigger — see fireFindings.
	for _, e := range events {
		if len(want) == 0 {
			continue
		}
		msgCtx := "\n\n【本次由任务超时触发】"
		for _, tr := range want {
			if !sc.s.StartTriggeredRun(tr.AgentKey, fmt.Sprintf("超时触发 · task#%d", e.TaskID), tr.TaskTimeoutMessage+msgCtx, e.TaskID, true, e.TaskDesc, e.TaskGoal) {
				return
			}
		}
	}
	last := events[len(events)-1]
	data, err := json.Marshal(db.TaskTimeoutCursor{CompletedAt: last.CompletedAt, TaskID: last.TaskID})
	if err == nil {
		_ = sc.pg.SetSchedState(schedKeyLastTimeout, string(data))
	}
}

// fireTaskCreates fires on_task_create triggers for tasks newly created above the
// persisted watermark (task id → no double-fire across restarts).
func (sc *Scheduler) fireTaskCreates(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnTaskCreate {
			want = append(want, tr)
		}
	}
	last, _ := strconv.ParseInt(sc.mustState(schedKeyLastTaskCreate), 10, 64)
	if !sc.triggersReady(want) {
		return
	}
	events, err := sc.pg.NewTasksSince(last)
	if err != nil || len(events) == 0 {
		return
	}
	// Advance the watermark even with no active trigger — see fireFindings.
	maxID := last
	for _, e := range events {
		if e.NodeID > maxID {
			maxID = e.NodeID
		}
		if len(want) == 0 {
			continue
		}
		msgCtx := "\n\n【本次由任务创建触发】"
		for _, tr := range want {
			if !sc.s.StartTriggeredRun(tr.AgentKey, fmt.Sprintf("任务创建触发 · task#%d", e.TaskID), tr.TaskCreateMessage+msgCtx, e.TaskID, true, e.TaskDesc, e.TaskGoal) {
				return
			}
		}
	}
	_ = sc.pg.SetSchedState(schedKeyLastTaskCreate, strconv.FormatInt(maxID, 10))
}

// fireToolCalls fires on_tool_call triggers for tool calls (tool_result rows) above
// the persisted watermark whose tool name is in the trigger's selected set. The fire
// message carries the task id/desc/goal + tool name + (truncated) input & output.
func (sc *Scheduler) fireToolCalls(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnToolCall && len(tr.ToolNames) > 0 {
			want = append(want, tr)
		}
	}
	last, _ := strconv.ParseInt(sc.mustState(schedKeyLastToolCall), 10, 64)
	events, err := sc.pg.NewToolCallsSince(last)
	if err != nil || len(events) == 0 {
		return
	}
	// Only matching, actionable events require a ready model. In particular a
	// failed report_finding must be consumed without trying to run the reporter.
	var needed []*db.AgentTrigger
	for _, tr := range want {
		for _, e := range events {
			if !(e.ToolIsErr && e.Tool == "report_finding") && containsFold(tr.ToolNames, e.Tool) {
				needed = append(needed, tr)
				break
			}
		}
	}
	if !sc.triggersReady(needed) {
		return
	}
	// Advance the watermark even with no active trigger — see fireFindings.
	maxID := last
	for _, e := range events {
		if e.NodeID > maxID {
			maxID = e.NodeID
		}
		if len(want) == 0 {
			continue
		}
		// A failed finding write has no committed finding to report. Keep other
		// tool-error triggers available for user-defined automation.
		if e.ToolIsErr && e.Tool == "report_finding" {
			continue
		}
		errTag := ""
		if e.ToolIsErr {
			errTag = "[error] "
		}
		msgCtx := fmt.Sprintf("\n\n【本次由工具调用触发】\n工具: %s\n入参: %s\n返回: %s%s",
			e.Tool, trunc(e.ToolInput, 1500), errTag, trunc(e.ToolOutput, 1500))
		for _, tr := range want {
			if !containsFold(tr.ToolNames, e.Tool) {
				continue
			}
			if !sc.s.StartTriggeredRun(tr.AgentKey, fmt.Sprintf("工具触发 · %s · task#%d", e.Tool, e.TaskID), tr.ToolCallMessage+msgCtx, e.TaskID, true, e.TaskDesc, e.TaskGoal) {
				return
			}
		}
	}
	_ = sc.pg.SetSchedState(schedKeyLastToolCall, strconv.FormatInt(maxID, 10))
}

func (sc *Scheduler) triggersReady(triggers []*db.AgentTrigger) bool {
	for _, tr := range triggers {
		if sc.s == nil || serverContext(sc.s).Err() != nil || sc.s.resolveChatAgent(&db.Conversation{AgentKey: tr.AgentKey}) == nil {
			return false
		}
	}
	return true
}

// containsFold reports whether name is in set (case-insensitive).
func containsFold(set []string, name string) bool {
	for _, s := range set {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// trunc caps s to max runes, appending an ellipsis + original length when cut.
func trunc(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("…(已截断,共 %d 字)", len(r))
}

func (sc *Scheduler) mustState(key string) string {
	v, _ := sc.pg.GetSchedState(key)
	return v
}

func (sc *Scheduler) firedGoalSet() map[int64]bool {
	out := map[int64]bool{}
	raw := sc.mustState(schedKeyFiredGoals)
	if raw == "" {
		return out
	}
	var ids []int64
	if json.Unmarshal([]byte(raw), &ids) == nil {
		for _, id := range ids {
			out[id] = true
		}
	}
	return out
}

func (sc *Scheduler) saveFiredGoalSet(set map[int64]bool) {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	b, _ := json.Marshal(ids)
	if err := sc.pg.SetSchedState(schedKeyFiredGoals, string(b)); err != nil {
		log.Printf("[scheduler] save fired goals failed: %v", err)
	}
}
