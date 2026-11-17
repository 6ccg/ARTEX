package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestLegacySchemaUpgradePreservesRelationsAndRunsOnce(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	name := fmt.Sprintf("upgrade_fixture_%d", time.Now().UnixNano())
	if _, err := conn.ExecContext(ctx, `CREATE SCHEMA `+name+`; SET search_path TO `+name); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `SET search_path TO public; DROP SCHEMA `+name+` CASCADE`) //nolint:errcheck
	if _, err := conn.ExecContext(ctx, legacySchemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO llm_profiles(id,name,format,model,is_default) VALUES(1,'legacy','anthropic','fixture',true),(2,'chain','anthropic','fixture',false);
ALTER TABLE llm_profiles ADD COLUMN proxy text;
UPDATE llm_profiles SET proxy='http://proxy.example:8080' WHERE id=1;
INSERT INTO explorations(id,goal) VALUES(1,'goal'),(2,'goal');
INSERT INTO tasks(id,description,goal,exploration_id,llm_profile_id,active_llm_profile_id,status,completed_at)
VALUES(1,'chain','goal',1,1,2,'created',NULL),(2,'legacy','goal',2,1,NULL,'timeout','2026-11-15T00:00:00Z');
INSERT INTO task_llm_profiles(task_id,profile_id,position) VALUES(1,2,0);
INSERT INTO assets(id,type,domain,task_ids) VALUES(1,'root_domain','upgrade.example',ARRAY[1,2,9999]::bigint[]);
UPDATE task_asset_links SET source='manual',source_summary='preserve provenance' WHERE task_id=1;
INSERT INTO scheduler_state(key,value) VALUES('last_timeout_id','2');
`); err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var links, chain, columns int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM task_asset_links WHERE asset_id=1`).Scan(&links); err != nil || links != 2 {
		t.Fatalf("links=%d err=%v", links, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM task_llm_profiles WHERE (task_id=1 AND profile_id=2) OR (task_id=2 AND profile_id=1)`).Scan(&chain); err != nil || chain != 2 {
		t.Fatalf("chain=%d err=%v", chain, err)
	}
	var source, proxy, cursor string
	if err := conn.QueryRowContext(ctx, `SELECT source FROM task_asset_links WHERE task_id=1 AND asset_id=1`).Scan(&source); err != nil || source != "manual" {
		t.Fatalf("source=%s err=%v", source, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='llm_proxy'`).Scan(&proxy); err != nil || proxy != "http://proxy.example:8080" {
		t.Fatalf("proxy=%s err=%v", proxy, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT value FROM scheduler_state WHERE key='last_timeout_cursor'`).Scan(&cursor); err != nil || cursor == "" {
		t.Fatalf("cursor=%s err=%v", cursor, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=$1 AND ((table_name='assets' AND column_name='task_ids') OR (table_name='tasks' AND column_name='llm_profile_id'))`, name).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("legacy columns=%d err=%v", columns, err)
	}
	// A revision 1 database must only run the new migration, not legacy DDL.
	if _, err := conn.ExecContext(ctx, `DROP TABLE pending_trigger_runs; UPDATE settings SET value='1' WHERE key='database_schema_revision'`); err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(ctx, conn); err != nil {
		t.Fatalf("revision 1 upgrade: %v", err)
	}
	var pendingExists bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass('pending_trigger_runs') IS NOT NULL`).Scan(&pendingExists); err != nil || !pendingExists {
		t.Fatalf("pending table: %v %v", pendingExists, err)
	}
	// Revision 5 had no MCP server tables; upgrading must add them without legacy DDL.
	if _, err := conn.ExecContext(ctx, `DROP TABLE api_token_tasks,api_tokens,mcp_audit_log; UPDATE settings SET value='5' WHERE key='database_schema_revision'`); err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(ctx, conn); err != nil {
		t.Fatalf("revision 5 upgrade: %v", err)
	}
	for _, table := range []string{"api_tokens", "api_token_tasks", "mcp_audit_log"} {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("MCP table %s: exists=%v err=%v", table, exists, err)
		}
	}
	// Holding a read transaction would deadlock a second connection trying DDL.
	reader, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	if _, err := reader.Exec(`SELECT id FROM ` + name + `.assets LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `SET lock_timeout='200ms'`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `RESET lock_timeout`) //nolint:errcheck
	if err := initializeSchema(ctx, conn); err != nil {
		t.Fatalf("reopen reran DDL: %v", err)
	}
}
