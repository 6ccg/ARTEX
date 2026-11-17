package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestPlannerProgressHook(t *testing.T) {
	open, active := true, false
	h := plannerProgressHooks(func() (bool, error) { return open, nil }, func() (bool, error) { return active, nil }, nil)
	for i := 0; i < 2; i++ {
		_, blocking, _ := h.Stop(context.Background(), nil)
		if len(blocking) != 1 {
			t.Fatalf("attempt %d: %v", i, blocking)
		}
	}
	if _, blocking, _ := h.Stop(context.Background(), nil); len(blocking) != 0 {
		t.Fatal("retry limit exceeded")
	}
	active = true
	h = plannerProgressHooks(func() (bool, error) { return open, nil }, func() (bool, error) { return active, nil }, nil)
	if _, blocking, _ := h.Stop(context.Background(), nil); len(blocking) != 0 {
		t.Fatal("active work should not be interrupted")
	}
}

func TestGoalEvidence(t *testing.T) {
	for _, tc := range []struct {
		kind, payload string
		valid         bool
	}{
		{db.KindFact, `{"evidence":""}`, false},
		{db.KindFact, `{"confidence":"inferred","evidence":"guess"}`, false},
		{db.KindFact, `{"confidence":"observed","evidence":"response"}`, true},
		{db.KindFinding, `{"evidence":{"poc":"response"}}`, true},
		{db.KindFinding, `{"evidence":{"by":"worker","poc":" "}}`, false},
	} {
		n := &db.Node{Kind: tc.kind, Payload: json.RawMessage(tc.payload)}
		if got := validateGoalEvidence(n) == nil; got != tc.valid {
			t.Errorf("%s: valid=%v", tc.payload, got)
		}
	}
	n := &db.Node{Kind: db.KindFinding, Payload: json.RawMessage(`{"evidence":{"poc":""}}`), TrafficCount: 1}
	if err := validateGoalEvidence(n); err != nil {
		t.Fatalf("bound traffic is evidence: %v", err)
	}
}

func TestWorkerOverviewKeepsDependenciesAndOtherRoute(t *testing.T) {
	d := testDB(t)
	task, err := d.CreateTask("overview", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	store := d.Exploration(task.ExplorationID)
	fact, err := store.AddNode(db.KindFact, map[string]any{"summary": "dependency clue", "evidence": "observed"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := store.AddIntent(map[string]any{"summary": "assigned route"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Link(fact, db.RelDerivedFrom, intentID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeState(intentID, "running"); err != nil {
		t.Fatal(err)
	}
	other, err := store.AddIntent(map[string]any{"summary": "parallel route"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(nil, store, "worker")
	ts.SetTaskID(task.ID)
	data := ts.workerOverviewData(intent)
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "dependency clue") || !strings.Contains(string(raw), "parallel route") || !strings.Contains(string(raw), fmt.Sprint(other)) {
		t.Fatalf("lost dependency or concurrent route: %s", raw)
	}
}
