package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

//go:embed upgrade_legacy.sql
var legacySchemaSQL string

const schemaRevision = 7
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
	if version < 3 {
		if err := migrateAssetClassification(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE conversations ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'manual';
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS source_task_id bigint;
UPDATE conversations c SET source='automation', source_task_id=COALESCE(
 substring(c.title FROM 'task#([0-9]{1,18})')::bigint,
 substring(c.title FROM '任务 #([0-9]{1,18})')::bigint,
 (SELECT substring(COALESCE(a.detail,a.summary) FROM '【任务 #([0-9]{1,18})')::bigint FROM conversation_activities a WHERE a.conversation_id=c.id AND a.kind='user' ORDER BY a.id LIMIT 1))
WHERE c.title ~ '^(工具触发|发现触发|目标触发|超时触发|任务创建触发|周期触发|定时触发|合并触发|漏洞报告|漏洞整理|复测|人工生成步骤与报告|资产与网络梳理|凭据整理)';
UPDATE conversations c SET title=left(subject.name,180)
FROM (SELECT DISTINCT ON (conversation_id) conversation_id,substring(COALESCE(detail,summary) FROM '"name"[[:space:]]*:[[:space:]]*"([^"]+)"') AS name FROM conversation_activities WHERE kind='user' ORDER BY conversation_id,id) subject
WHERE subject.conversation_id=c.id AND c.source='automation' AND c.title LIKE '工具触发 · report_finding%' AND subject.name IS NOT NULL`); err != nil {
			return err
		}
	}
	if err := seedInitialSchemaData(ctx, tx); err != nil {
		return err
	}
	if version < 4 {
		if err := migrateConversationTitles(ctx, tx); err != nil {
			return err
		}
	}
	if version < 5 {
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_act_system ON activity(exploration_id,id) WHERE worker='system' OR kind IN ('llm_switch','llm_failover');
CREATE INDEX IF NOT EXISTS idx_conv_result ON conversation_activities(conversation_id,id DESC) WHERE kind='result'`); err != nil {
			return err
		}
	}
	if version < 6 {
		if _, err := tx.ExecContext(ctx, mcpServerSchema); err != nil {
			return err
		}
	}
	if version < 7 {
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_act_failed_result ON activity(exploration_id,id)
WHERE kind='tool_result' AND is_error`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, schemaRevisionKey, strconv.Itoa(schemaRevision)); err != nil {
		return err
	}
	return tx.Commit()
}

// Existing local databases predate the upstream MCP server tables.
const mcpServerSchema = `
CREATE TABLE IF NOT EXISTS api_tokens (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    scopes JSONB NOT NULL DEFAULT '[]',
    company_ids JSONB NOT NULL DEFAULT '[]',
    max_active_tasks INTEGER NOT NULL DEFAULT 0,
    max_tasks_per_hour INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS api_token_tasks (
    token_id BIGINT NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
    task_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (token_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_api_token_tasks_recent ON api_token_tasks(token_id, created_at DESC);
CREATE TABLE IF NOT EXISTS mcp_audit_log (
    id BIGSERIAL PRIMARY KEY,
    token_id BIGINT NOT NULL,
    token_name TEXT NOT NULL DEFAULT '',
    tool TEXT NOT NULL,
    args TEXT NOT NULL DEFAULT '',
    ok BOOLEAN NOT NULL,
    result TEXT NOT NULL DEFAULT '',
    remote_addr TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// Recover a complete JSON string, including escaped quotes, from the trigger
// context. Only replace generated titles; user-renamed conversations stay intact.
func migrateConversationTitles(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.title,COALESCE(a.detail,a.summary,'') FROM conversations c
JOIN LATERAL (SELECT detail,summary FROM conversation_activities WHERE conversation_id=c.id AND kind='user' ORDER BY id LIMIT 1) a ON true
WHERE c.source='automation' AND c.agent_key='reporter'`)
	if err != nil {
		return err
	}
	type titleUpdate struct {
		id    int64
		title string
	}
	var updates []titleUpdate
	full := regexp.MustCompile(`"name"\s*:\s*("(?:[^"\\]|\\.)*")`)
	old := regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)
	for rows.Next() {
		var id int64
		var title, body string
		if err := rows.Scan(&id, &title, &body); err != nil {
			rows.Close()
			return err
		}
		m := full.FindStringSubmatch(body)
		if len(m) != 2 {
			continue
		}
		var name string
		if json.Unmarshal([]byte(m[1]), &name) != nil || strings.TrimSpace(name) == "" {
			continue
		}
		runes := []rune(name)
		if len(runes) > 180 {
			name = string(runes[:180])
		}
		previous := old.FindStringSubmatch(body)
		generated := strings.HasPrefix(title, "工具触发 · report_finding")
		if len(previous) == 2 {
			runes := []rune(previous[1])
			if len(runes) > 180 {
				runes = runes[:180]
			}
			generated = generated || title == string(runes)
		}
		if generated && title != name {
			updates = append(updates, titleUpdate{id, name})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET title=$2 WHERE id=$1`, u.id, u.title); err != nil {
			return err
		}
	}
	return nil
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
