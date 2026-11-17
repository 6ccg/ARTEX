package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestTriggerPendingRestoresMergedConversation(t *testing.T) {
	m := newTestManager(t)
	s := newIdleAPIServer(t, m)
	const key = "pending_restore_test"
	if _, err := m.pg.CreateAgent(key, key, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.pg.Exec(`DELETE FROM pending_trigger_runs WHERE agent_key=$1`, key); m.pg.DeleteAgent(key) })
	id, err := m.pg.SaveProfile(&db.LLMProfile{Name: key, Format: "anthropic", Model: "test", APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.pg.DeleteProfile(id) })
	if err := m.pg.SetAgentLLMProfile(key, &id); err != nil {
		t.Fatal(err)
	}
	s.triggerActive[key] = 1
	for _, msg := range []string{"one", "two"} {
		if !s.StartTriggeredRun(key, "test", msg, 12, true, "desc", "goal") {
			t.Fatal("enqueue failed")
		}
	}
	item := s.nextTriggerRun(key, triggerBehavior{mergeMode: "by_task"})
	if len(item.pendingIDs) != 2 {
		t.Fatalf("merged ids: %v", item.pendingIDs)
	}
	c, err := s.bindPendingConversation(&item, finalTriggerMessage(item))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.pg.Exec(`DELETE FROM conversations WHERE id=$1`, c.ID) })
	restarted, err := NewRuntime(context.Background(), m, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	restarted.triggerActive[key] = 1
	restarted.restorePendingTriggers()
	q := restarted.triggerQ[key]
	if len(q) != 1 || len(q[0].pendingIDs) != 2 || q[0].conversationID != c.ID || !q[0].started {
		t.Fatalf("restore: %+v", q)
	}
	busyKey := restarted.convBusyKey(c.ID)
	restarted.chatBusy[busyKey] = true
	if restarted.runTriggeredRun(&q[0]) || !restarted.chatBusy[busyKey] {
		t.Fatal("restored trigger must wait for the existing conversation turn")
	}
	delete(restarted.chatBusy, busyKey)
	if _, err := restarted.bindPendingConversation(&q[0], q[0].message); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := m.pg.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1 AND kind='user'`, c.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("initial activities: %d %v", count, err)
	}
	if err := m.pg.SetAgentLLMProfile(key, nil); err != nil {
		t.Fatal(err)
	}
	setRetestProvider(&Server{Runtime: restarted}, retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		panic("trigger regression: model panicked")
	}})
	if !restarted.runTriggeredRun(&q[0]) {
		t.Fatal("a panicking run must fail once instead of retrying forever")
	}
	if err := m.pg.QueryRow(`SELECT count(*) FROM pending_trigger_runs WHERE agent_key=$1`, key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("panicked run left pending work: %d %v", count, err)
	}
}

func TestSchedulerTargetsAdvanceIndependently(t *testing.T) {
	m := newTestManager(t)
	s := newIdleAPIServer(t, m)
	keys := []string{"target_ready_test", "target_wait_test"}
	for _, key := range keys {
		if _, err := m.pg.CreateAgent(key, key, ""); err != nil {
			t.Fatal(err)
		}
		key := key
		t.Cleanup(func() { m.pg.Exec(`DELETE FROM pending_trigger_runs WHERE agent_key=$1`, key); m.pg.DeleteAgent(key) })
	}
	pid, err := m.pg.SaveProfile(&db.LLMProfile{Name: keys[0], Format: "anthropic", Model: "test", APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.pg.DeleteProfile(pid) })
	if err := m.pg.SetAgentLLMProfile(keys[0], &pid); err != nil {
		t.Fatal(err)
	}
	old, _ := m.pg.GetSchedState(schedKeyLastTaskCreate)
	t.Cleanup(func() {
		m.pg.SetSchedState(schedKeyLastTaskCreate, old)
		for _, id := range []int64{971001, 971002} {
			m.pg.Exec(`DELETE FROM scheduler_state WHERE key=$1`, fmt.Sprintf("%s:trigger:%d", schedKeyLastTaskCreate, id))
		}
	})
	var last int64
	if err := m.pg.QueryRow(`SELECT COALESCE(MAX(id),0) FROM tasks`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSchedState(schedKeyLastTaskCreate, i64s(last)); err != nil {
		t.Fatal(err)
	}
	task, err := m.pg.CreateTask("independent triggers", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.pg.DeleteTask(task.ID) })
	s.triggerActive[keys[0]], s.triggerActive[keys[1]] = 1, 1
	triggers := []*db.AgentTrigger{{ID: 971001, AgentKey: keys[0], OnTaskCreate: true}, {ID: 971002, AgentKey: keys[1], OnTaskCreate: true}}
	sc := newScheduler(s.Runtime)
	sc.fireTaskCreates(triggers)
	sc.fireTaskCreates(triggers)
	if len(s.triggerQ[keys[0]]) != 1 || len(s.triggerQ[keys[1]]) != 0 {
		t.Fatalf("queues ready=%d waiting=%d", len(s.triggerQ[keys[0]]), len(s.triggerQ[keys[1]]))
	}
	if err := m.pg.SetAgentLLMProfile(keys[1], &pid); err != nil {
		t.Fatal(err)
	}
	sc.fireTaskCreates(triggers)
	if len(s.triggerQ[keys[0]]) != 1 || len(s.triggerQ[keys[1]]) != 1 {
		t.Fatalf("queues after recovery ready=%d waiting=%d", len(s.triggerQ[keys[0]]), len(s.triggerQ[keys[1]]))
	}
}

func TestTriggerReenabledStartsAtCurrentBoundary(t *testing.T) {
	m := newTestManager(t)
	const key = "trigger_boundary_test"
	if _, err := m.pg.CreateAgent(key, key, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.pg.DeleteAgent(key) })
	tr, err := m.pg.CreateTrigger(&db.AgentTrigger{AgentKey: key, Enabled: true, OnTaskCreate: true})
	if err != nil {
		t.Fatal(err)
	}
	stateKey := fmt.Sprintf("%s:trigger:%d", schedKeyLastTaskCreate, tr.ID)
	t.Cleanup(func() { m.pg.DeleteTrigger(tr.ID); m.pg.Exec(`DELETE FROM scheduler_state WHERE key=$1`, stateKey) })
	tr.Enabled = false
	if err := m.pg.UpdateTrigger(tr); err != nil {
		t.Fatal(err)
	}
	task, err := m.pg.CreateTask("disabled boundary", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.pg.DeleteTask(task.ID) })
	tr.Enabled = true
	if err := m.pg.UpdateTrigger(tr); err != nil {
		t.Fatal(err)
	}
	value, err := m.pg.GetSchedState(stateKey)
	if err != nil || value != i64s(task.ID) {
		t.Fatalf("reenabled boundary %q, want %d: %v", value, task.ID, err)
	}
}
