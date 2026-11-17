package db

import (
	"sync"
	"testing"
)

func TestRuntimeStatsCountsStatesWithoutLoadingPayloads(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("runtime counters", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	store := d.Exploration(expID)
	empty, err := store.RuntimeStats()
	if err != nil || len(empty.Nodes) != 0 || empty.Goals.Total != 0 || empty.RunningIntents != 0 {
		t.Fatalf("empty stats=%+v err=%v", empty, err)
	}
	_, err = d.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,state,payload) VALUES
($1,'goal','met','{}'),($1,'goal','abandoned','{}'),($1,'intent','open','{}'),
($1,'intent','running','{}'),($1,'fact','confirmed','{}')`, expID)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := store.RuntimeStats()
	if err != nil || stats.Nodes[KindGoal] != 2 || stats.Nodes[KindIntent] != 2 || stats.Nodes[KindFact] != 1 ||
		stats.Goals.Total != 2 || stats.Goals.Met != 1 || stats.RunningIntents != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestClaimNextIntentPreservesOrderSkipsLocksAndHasOneWinner(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("claim next", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	store := d.Exploration(expID)
	add := func(priority int) int64 {
		t.Helper()
		id, err := store.AddIntent(map[string]any{}, priority, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	low, first, second := add(1), add(9), add(9)
	got, err := store.ClaimNextIntent("first")
	if err != nil || got == nil || got.ID != first || got.Owner != "first" || got.State != "running" {
		t.Fatalf("priority/FIFO claim=%+v err=%v", got, err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT id FROM exploration_nodes WHERE id=$1 FOR UPDATE`, second); err != nil {
		t.Fatal(err)
	}
	got, err = store.ClaimNextIntent("skip locked")
	if err != nil || got == nil || got.ID != low {
		t.Fatalf("locked claim=%+v err=%v", got, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	claimed := make(chan *Node, 4)
	for range cap(claimed) {
		wg.Go(func() {
			node, err := store.ClaimNextIntent("contender")
			if err != nil {
				t.Error(err)
				return
			}
			if node != nil {
				claimed <- node
			}
		})
	}
	wg.Wait()
	close(claimed)
	if len(claimed) != 1 {
		t.Fatalf("claim winners=%d, want one", len(claimed))
	}
	for node := range claimed {
		if node.ID != second {
			t.Fatalf("claimed unexpected intent %d", node.ID)
		}
	}
	got, err = store.ClaimNextIntent("empty")
	if err != nil || got != nil {
		t.Fatalf("empty claim=%+v err=%v", got, err)
	}
}
