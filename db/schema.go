package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strconv"
)

//go:embed upgrade_legacy.sql
var legacySchemaSQL string

const schemaRevision = 2
const schemaRevisionKey = "database_schema_revision"

// The caller holds schemaMigrationLockKey on conn. Each upgrade and its version
// commit together; an interrupted upgrade leaves no partially advanced version.
func initializeSchema(ctx context.Context, conn *sql.Conn) error {
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass('settings') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	version := 0
	if exists {
		var raw string
		err := conn.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, schemaRevisionKey).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			var parseErr error
			version, parseErr = strconv.Atoi(raw)
			if parseErr != nil || version < 0 {
				return fmt.Errorf("invalid database schema revision %q", raw)
			}
		}
	}
	if version > schemaRevision {
		return fmt.Errorf("database schema revision %d is newer than supported %d", version, schemaRevision)
	}
	if version == schemaRevision {
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if exists && version == 0 {
		if _, err := tx.ExecContext(ctx, legacySchemaSQL); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, llmRecordsSchema+llmRecordsMigrate+llmUsageSchema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_companies_nkey; DROP INDEX IF EXISTS idx_task_llm_profiles_order`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE tasks DROP COLUMN llm_profile_id`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER trg_assets_task_links ON assets; DROP FUNCTION sync_task_asset_links(); ALTER TABLE assets DROP COLUMN task_ids`); err != nil {
			return err
		}
	} else if !exists {
		if _, err := tx.ExecContext(ctx, `SET LOCAL check_function_bodies=false`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS pending_trigger_runs (id BIGSERIAL PRIMARY KEY, agent_key TEXT NOT NULL, title TEXT NOT NULL, message TEXT NOT NULL, task_id BIGINT NOT NULL DEFAULT 0, task_desc TEXT NOT NULL DEFAULT '', task_goal TEXT NOT NULL DEFAULT '', mergeable BOOLEAN NOT NULL DEFAULT false, conversation_id BIGINT, started BOOLEAN NOT NULL DEFAULT false)`); err != nil {
		return err
	}
	if err := seedInitialSchemaData(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, schemaRevisionKey, strconv.Itoa(schemaRevision)); err != nil {
		return err
	}
	return tx.Commit()
}

func seedInitialSchemaData(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mcp_servers(name,transport,url,env,enabled) VALUES('ScopeSentry','http',NULL,'{"X-API-Key":""}',false) ON CONFLICT(name) DO NOTHING`)
	return err
}

// Legacy-only LLM DDL; current tables are part of schema.sql.
const llmRecordsSchema = `
CREATE TABLE IF NOT EXISTS llm_records (
    id            BIGSERIAL PRIMARY KEY,
    ts            TIMESTAMPTZ DEFAULT now(),
    model         TEXT,
    profile_name  TEXT,
    session_id    TEXT,
    task_id       TEXT,
    worker        TEXT,
    latency_ms    INTEGER,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    cache_read    INTEGER,
    cache_write   INTEGER,
    status        TEXT,
    error         TEXT,
    request_body  TEXT,
    response_body TEXT,
    raw_request   TEXT,
    raw_response  TEXT
);
CREATE INDEX IF NOT EXISTS idx_llm_records_ts ON llm_records(ts);
CREATE INDEX IF NOT EXISTS idx_llm_records_session ON llm_records(session_id);
`

// llmRecordsMigrate adds new columns to existing tables.
const llmRecordsMigrate = `
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS task_id TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS worker TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS profile_name TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS raw_request TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS raw_response TEXT;
`

const llmUsageSchema = `
CREATE TABLE IF NOT EXISTS llm_usage (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    task_id        TEXT,
    exploration_id BIGINT,
    worker         TEXT,
    model          TEXT,
    profile_name   TEXT,
    latency_ms     INTEGER,
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_read     INTEGER NOT NULL DEFAULT 0,
    cache_write    INTEGER NOT NULL DEFAULT 0,
    status         TEXT
);
CREATE INDEX IF NOT EXISTS idx_llm_usage_task  ON llm_usage(task_id);
CREATE INDEX IF NOT EXISTS idx_llm_usage_model ON llm_usage(task_id, model);
CREATE INDEX IF NOT EXISTS idx_llm_usage_exp   ON llm_usage(exploration_id);
`
