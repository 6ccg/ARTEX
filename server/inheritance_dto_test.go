package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestInheritedDTOProvenance(t *testing.T) {
	createdAt := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	node := &db.Node{
		ID:           41,
		Kind:         db.KindFinding,
		Payload:      json.RawMessage(`{"vulnclass":"xss","severity":"high","summary":"source finding"}`),
		State:        "confirmed",
		CreatedAt:    createdAt,
		SourceTaskID: 7,
		Inherited:    true,
	}

	nodeDTO := taskNodeDTO(node)
	if !nodeDTO.Inherited || nodeDTO.SourceTaskID != "7" {
		t.Fatalf("node provenance lost: %+v", nodeDTO)
	}
	finding := findingDTOsForOwner("7", "source", []*db.Node{node}, nil, nil)
	if len(finding) != 1 || !finding[0].Inherited || finding[0].SourceTaskID != "7" || finding[0].TaskID != "7" {
		t.Fatalf("finding provenance lost: %+v", finding)
	}

	intentID := int64(44)
	activity := activityDTO(db.Activity{
		ID:           45,
		NodeID:       &intentID,
		CreatedAt:    createdAt,
		SourceTaskID: 7,
		Inherited:    true,
	})
	if !activity.Inherited || activity.SourceTaskID != "7" || activity.IntentID != "44" {
		t.Fatalf("activity provenance lost: %+v", activity)
	}
	assetRef := coverageAssetRefDTO(db.AssetRef{
		ID: 46, Kind: db.KindFact, State: "confirmed", Summary: "source fact", SourceTaskID: 7, Inherited: true,
	})
	if !assetRef.Inherited || assetRef.SourceTaskID != "7" {
		t.Fatalf("asset ref provenance lost: %+v", assetRef)
	}
}

func TestInheritedIntentResultOnlyAllowsHistory(t *testing.T) {
	for _, state := range []string{"done", "blocked", "exhausted", "stopped"} {
		if !inheritedIntentResult(state) {
			t.Errorf("terminal state %q should be inherited history", state)
		}
	}
	for _, state := range []string{"open", "running", ""} {
		if inheritedIntentResult(state) {
			t.Errorf("live state %q must not enter inherited history", state)
		}
	}
}

func TestInheritedGraphPagesFilterLiveIntentsAndEdges(t *testing.T) {
	m := newTestManager(t)
	source, err := m.CreateTask("graph source", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.CreateTaskWithOptions("graph child", "", db.TaskCreateOptions{SourceTaskIDs: []int64{mustTaskID(t, source.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(source.ID, DeleteTaskOptions{})
	defer m.DeleteTask(child.ID, DeleteTaskOptions{})
	nodes := []struct{ kind, state string }{{db.KindFact, "confirmed"}, {db.KindIntent, "running"}, {db.KindIntent, "done"}, {db.KindFinding, "confirmed"}}
	ids := []int64{}
	for _, node := range nodes {
		id, err := source.Store.AddNode(node.kind, map[string]any{}, 0, node.state, "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, edge := range []db.Edge{{From: ids[0], Rel: db.RelDerivedFrom, To: ids[1]}, {From: ids[0], Rel: db.RelDerivedFrom, To: ids[2]}, {From: ids[2], Rel: db.RelYields, To: ids[3]}} {
		if err := source.Store.Link(edge.From, edge.Rel, edge.To); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Runtime: &Runtime{m: m}}
	seen := map[string]TaskNodeDTO{}
	edges := map[EdgeDTO]bool{}
	cursor := int64(0)
	for {
		w := httptest.NewRecorder()
		s.explorationGraph(w, httptest.NewRequest("GET", fmt.Sprintf("/api/exploration/graph?task=%s&after=%d&limit=1", child.ID, cursor), nil))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var page struct {
			Nodes  []TaskNodeDTO
			Edges  []EdgeDTO
			Cursor int64 `json:"next_cursor"`
			More   bool  `json:"has_more"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, node := range page.Nodes {
			seen[node.ID] = node
		}
		for _, edge := range page.Edges {
			edges[edge] = true
		}
		if !page.More {
			break
		}
		if page.Cursor <= cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = page.Cursor
	}
	hidden := fmt.Sprint(ids[1])
	if _, ok := seen[hidden]; ok {
		t.Fatal("live source intent leaked")
	}
	for _, id := range []int64{ids[0], ids[2], ids[3]} {
		node, ok := seen[fmt.Sprint(id)]
		if !ok || !node.Inherited || node.SourceTaskID != source.ID {
			t.Fatalf("missing source node/provenance: %+v", node)
		}
	}
	if len(edges) != 2 {
		t.Fatalf("lost visible cross-page edge: %+v", edges)
	}
	for edge := range edges {
		if edge.Src == hidden || edge.Dst == hidden {
			t.Fatalf("hidden source edge leaked: %+v", edge)
		}
	}
}

func TestFindingProvenanceInTask(t *testing.T) {
	task := &Task{ID: "10", SourceTaskIDs: []int64{7, 8}}

	local := int64(10)
	if source, inherited, allowed := findingProvenanceInTask(task, &local); !allowed || inherited || source != "" {
		t.Fatalf("local finding: source=%q inherited=%v allowed=%v", source, inherited, allowed)
	}
	sourceID := int64(7)
	if source, inherited, allowed := findingProvenanceInTask(task, &sourceID); !allowed || !inherited || source != "7" {
		t.Fatalf("direct source finding: source=%q inherited=%v allowed=%v", source, inherited, allowed)
	}
	unrelated := int64(6)
	if _, _, allowed := findingProvenanceInTask(task, &unrelated); allowed {
		t.Fatal("unrelated finding must not be readable through task context")
	}
	if _, _, allowed := findingProvenanceInTask(task, nil); allowed {
		t.Fatal("detached finding must not be readable through task context")
	}
}
