package db

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

func TestTimeoutCursorMigration(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	previous, err := d.GetSchedState("last_timeout_cursor")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if previous == "" {
			d.Exec(`DELETE FROM scheduler_state WHERE key='last_timeout_cursor'`)
		} else {
			d.SetSchedState("last_timeout_cursor", previous)
		}
	}()
	task, err := d.CreateTask("timeout cursor migration", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	completed := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	if _, err := d.Exec(`UPDATE tasks SET status='timeout',completed_at=$1 WHERE id=$2`, completed, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM scheduler_state WHERE key='last_timeout_cursor'`); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSchedState("last_timeout_id", strconv.FormatInt(task.ID, 10)); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM scheduler_state WHERE key='last_timeout_id'`)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE assets ADD COLUMN task_ids bigint[] NOT NULL DEFAULT '{}'; ALTER TABLE tasks ADD COLUMN llm_profile_id bigint`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(legacySchemaSQL); err != nil {
		t.Fatal(err)
	}
	var raw string
	err = tx.QueryRow(`SELECT value FROM scheduler_state WHERE key='last_timeout_cursor'`).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	var cursor TaskTimeoutCursor
	if err := json.Unmarshal([]byte(raw), &cursor); err != nil || cursor.TaskID != task.ID || !cursor.CompletedAt.Equal(completed) {
		t.Fatalf("migrated cursor=%+v err=%v", cursor, err)
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM scheduler_state WHERE key='last_timeout_id'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy cursor still present: count=%d err=%v", count, err)
	}
}

func TestTimeoutEventsFollowCompletionOrder(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	older, err := d.CreateTask("older timeout", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(older.ID)
	newer, err := d.CreateTask("newer timeout", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(newer.ID)
	base := time.Date(2050, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := d.Exec(`UPDATE tasks SET status='timeout',completed_at=$1 WHERE id=$2`, base, newer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET status='timeout',completed_at=$1 WHERE id=$2`, base.Add(time.Second), older.ID); err != nil {
		t.Fatal(err)
	}
	cursor := TaskTimeoutCursor{CompletedAt: base, TaskID: newer.ID}
	events, err := d.TimedOutTasksSince(cursor)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		found = found || event.TaskID == older.ID
	}
	if !found {
		t.Fatal("older task completing after the cursor was never reported")
	}
	cursor = TaskTimeoutCursor{CompletedAt: base.Add(time.Second), TaskID: older.ID}
	if _, err := d.Exec(`UPDATE tasks SET status='running',completed_at=NULL WHERE id=$1`, older.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET status='timeout',completed_at=$1 WHERE id=$2`, base.Add(2*time.Second), older.ID); err != nil {
		t.Fatal(err)
	}
	events, err = d.TimedOutTasksSince(cursor)
	if err != nil || len(events) != 1 || events[0].TaskID != older.ID {
		t.Fatalf("revived task timeout was not reported: events=%v err=%v", events, err)
	}
}
