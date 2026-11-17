package db

import (
	"context"
	"testing"
	"time"
)

func regressionStore(t *testing.T) (*DB, *ExplorationStore) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	id, err := d.CreateExploration("performance regression", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM explorations WHERE id=$1`, id) })
	return d, d.Exploration(id)
}

func TestActivityCursorWaitsForEarlierTransaction(t *testing.T) {
	d, store := regressionStore(t)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(-$1::bigint)`, store.ID()); err != nil {
		t.Fatal(err)
	}
	done := make(chan int64, 1)
	failure := make(chan error, 1)
	go func() {
		id, err := store.AppendActivity(Activity{Worker: "system", Kind: "text"})
		if err != nil {
			failure <- err
			return
		}
		done <- id
	}()
	select {
	case <-done:
		t.Fatal("later ID committed ahead of earlier transaction")
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	// The waiting append must acquire the gate before allocating its sequence ID.
	var first int64
	if err := tx.QueryRow(`INSERT INTO activity(exploration_id,worker,kind) VALUES($1,'system','text') RETURNING id`, store.ID()).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-done:
		if second <= first {
			t.Fatal("cursor order reversed")
		}
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("append did not resume")
	}
	rows, _, err := store.ActivityList(nil, first, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("resume rows=%d err=%v", len(rows), err)
	}
}

func TestSystemActivityHistoryFiltersBeforePagination(t *testing.T) {
	_, store := regressionStore(t)
	for _, item := range []Activity{{Worker: "system", Kind: "text"}, {Worker: "planner", Kind: "text"}, {Worker: "work#1", Kind: "llm_failover"}} {
		if _, err := store.AppendActivity(item); err != nil {
			t.Fatal(err)
		}
	}
	rows, more, err := store.ActivityPage(ActivitySessionFilter{System: true}, 0, 1)
	if err != nil || len(rows) != 1 || !more || rows[0].Kind != "llm_failover" {
		t.Fatalf("first=%+v more=%v err=%v", rows, more, err)
	}
	rows, more, err = store.ActivityPage(ActivitySessionFilter{System: true}, rows[0].ID, 1)
	if err != nil || len(rows) != 1 || more || rows[0].Worker != "system" {
		t.Fatalf("older=%+v more=%v err=%v", rows, more, err)
	}
}

func TestToolResultsStayWithinCallScopeAndDoNotMultiply(t *testing.T) {
	d, store := regressionStore(t)
	other, err := d.CreateExploration("other", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, other)
	appendRow := func(s *ExplorationStore, kind, worker, detail string) {
		t.Helper()
		_, err := s.AppendActivity(Activity{Kind: kind, Worker: worker, Tool: "regression_tool", ToolUseID: "reused-id", Detail: detail, IsError: detail == "correct first result" || detail == "wrong worker" || detail == "late error"})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendRow(store, "tool_use", "work#1", "regression_tool first")
	appendRow(d.Exploration(other), "tool_result", "work#1", "wrong task")
	appendRow(store, "tool_result", "work#2", "wrong worker")
	appendRow(store, "tool_result", "work#1", "correct first result")
	appendRow(store, "tool_result", "work#1", "duplicate result")
	appendRow(store, "tool_use", "work#1", "regression_tool second")
	appendRow(store, "tool_use", "work#1", "regression_tool third")
	appendRow(store, "tool_result", "work#1", "correct third result")
	appendRow(store, "tool_result", "work#1", "late error")
	exp := store.ID()
	rows, total, err := d.ListCommands(&exp, "regression_tool", 0, 10)
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatalf("total=%d rows=%d err=%v", total, len(rows), err)
	}
	if rows[0].Output != "correct third result" || rows[1].Output != "" || rows[2].Output != "correct first result" {
		t.Fatalf("wrong pairing: %+v", rows)
	}
	stats, err := d.ToolStats(&exp, "regression_tool")
	if err != nil || len(stats) != 1 || stats[0].Total != 3 || stats[0].Errors != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	stats, err = d.ToolStats(&exp, "third")
	if err != nil || len(stats) != 1 || stats[0].Total != 1 || stats[0].Errors != 0 {
		t.Fatalf("filtered stats=%+v err=%v", stats, err)
	}
	stats, err = d.ToolStats(&exp, "no-matching-execution")
	if err != nil || len(stats) != 0 {
		t.Fatalf("stats with no matching calls=%+v err=%v", stats, err)
	}
}

func TestGraphPagesCoverAllVisibleNodesAndCrossPageEdges(t *testing.T) {
	_, store := regressionStore(t)
	ids := []int64{}
	for range 3 {
		id, err := store.AddIntent(map[string]any{}, 1, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := store.db.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'derived_from',$3)`, store.ID(), ids[0], ids[2]); err != nil {
		t.Fatal(err)
	}
	page, more, err := store.GraphNodePage(0, 2, false)
	if err != nil || len(page) != 2 || !more {
		t.Fatalf("page=%v more=%v err=%v", page, more, err)
	}
	edges, err := store.GraphPageEdges([]int64{ids[0]}, false)
	if err != nil || len(edges) != 1 || edges[0].To != ids[2] {
		t.Fatalf("cross-page edge=%+v err=%v", edges, err)
	}
	page, more, err = store.GraphNodePage(page[1].ID, 2, false)
	if err != nil || len(page) != 1 || more || page[0].ID != ids[2] {
		t.Fatalf("last=%+v err=%v", page, err)
	}
	hidden, _, err := store.GraphNodePage(0, 2, true)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("inherited frontier leaked: %+v %v", hidden, err)
	}
	index, err := store.GraphIndex(context.Background(), nil, 2)
	if err != nil || len(index) != 2 || index[0].Through != ids[1] || index[1].After != ids[2]-1 {
		t.Fatalf("wrong graph index: %+v, %v", index, err)
	}
	if _, err := store.db.Exec(`UPDATE exploration_nodes SET state='done' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GraphIndex(context.Background(), nil, 2)
	if err != nil || len(updated) != 2 || updated[0].Revision == index[0].Revision || updated[1] != index[1] {
		t.Fatalf("only the changed node's range should be invalidated: %+v, %v", updated, err)
	}
	if _, err := store.db.Exec(`DELETE FROM exploration_edges WHERE exploration_id=$1`, store.ID()); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := store.GraphIndex(context.Background(), nil, 2)
	if err != nil || len(afterDelete) != 2 || afterDelete[0].Revision == updated[0].Revision || afterDelete[1].Revision == updated[1].Revision {
		t.Fatalf("both ranges containing the deleted edge should be invalidated: %+v, %v", afterDelete, err)
	}
}

func TestSideSnapshotMetadataDoesNotReadRequest(t *testing.T) {
	d, snapshot := sideFixture(t)
	meta, err := d.SideSnapshotMeta(context.Background(), snapshot.Parent.Key())
	if err != nil || meta == nil || meta.Model != snapshot.Model || !meta.CapturedAt.Equal(snapshot.CapturedAt) || len(meta.Request.Messages) != 0 {
		t.Fatalf("metadata=%+v err=%v", meta, err)
	}
	full, err := d.SideSnapshot(context.Background(), snapshot.Parent.Key())
	if err != nil || len(full.Request.Messages) != 1 {
		t.Fatalf("full snapshot changed: %v", err)
	}
}

func TestConversationPageFiltersCountsAndLiteralSearch(t *testing.T) {
	d, _ := regressionStore(t)
	manual, err := d.CreateConversation("mainagent", "audit literal 100%", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteConversation(manual.ID)
	automation, err := d.CreateAutomationConversation("mainagent", "audit second", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteConversation(automation.ID)
	filter := ConversationFilter{Query: "audit ", Scope: "all"}
	rows, meta, err := d.ListConversationsPage(filter, 1, 0)
	if err != nil || len(rows) != 1 || meta.Total != 2 || meta.Groups["mainagent"].Total != 1 || meta.Groups["task:0"].Total != 1 {
		t.Fatalf("page=%+v meta=%+v err=%v", rows, meta, err)
	}
	filter.Query = "100%"
	rows, meta, err = d.ListConversationsPage(filter, 10, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != manual.ID {
		t.Fatalf("literal search=%+v %v", rows, err)
	}
	filter.Query = "audit "
	filter.Scope = "manual"
	filter.RunningIDs = []int64{automation.ID}
	rows, meta, err = d.ListConversationsPage(filter, 10, 0)
	if err != nil || len(rows) != 2 || meta.Groups["task:0"].Running != 1 {
		t.Fatalf("running automation=%+v meta=%+v %v", rows, meta, err)
	}
	ids, err := d.ConversationIDs(filter)
	if err != nil || len(ids) != 2 {
		t.Fatalf("selection IDs=%v err=%v", ids, err)
	}
	filter.SkipStats = true
	_, meta, err = d.ListConversationsPage(filter, 1, 1)
	if err != nil || meta.Total != 0 || len(meta.Groups) != 0 {
		t.Fatalf("skipped stats=%+v err=%v", meta, err)
	}
}
