-- Current PostgreSQL schema for empty databases.

CREATE FUNCTION public.finding_case_changed() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE cid BIGINT; remaining INTEGER;
BEGIN
    IF TG_TABLE_NAME = 'findings' THEN
        IF OLD IS NOT DISTINCT FROM NEW THEN RETURN NEW; END IF;
        SELECT case_id INTO cid FROM finding_case_members WHERE finding_id=NEW.id;
    ELSE
        cid := OLD.case_id;
    END IF;
    IF cid IS NOT NULL THEN
        UPDATE finding_cases SET version=version+1 WHERE id=cid;
        IF TG_OP='DELETE' THEN
            SELECT count(*) INTO remaining FROM finding_case_members WHERE case_id=cid;
            INSERT INTO finding_case_events(case_id,action,actor,reason,finding_ids)
                VALUES(cid,'member_deleted','system','成员移出或删除',jsonb_build_array(OLD.finding_id));
            IF remaining < 2 THEN
                UPDATE finding_cases SET active=false WHERE id=cid;
                DELETE FROM finding_case_members WHERE case_id=cid;
            END IF;
        END IF;
    END IF;
    RETURN NULL;
END $$;

CREATE FUNCTION public.finding_case_suggestion_reanchor() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    sid BIGINT;
    new_left BIGINT;
    new_right BIGINT;
BEGIN
    FOR sid IN
        SELECT id FROM finding_case_suggestions
        WHERE left_id = OLD.id OR right_id = OLD.id
    LOOP
        SELECT min(m.finding_id) INTO new_left
        FROM finding_case_suggestion_members m
        JOIN findings f ON f.id = m.finding_id
        WHERE m.suggestion_id = sid AND m.finding_id <> OLD.id;
        SELECT min(m.finding_id) INTO new_right
        FROM finding_case_suggestion_members m
        JOIN findings f ON f.id = m.finding_id
        WHERE m.suggestion_id = sid AND m.finding_id <> OLD.id
          AND m.finding_id > new_left;
        IF new_left IS NOT NULL AND new_right IS NOT NULL THEN
            UPDATE finding_case_suggestions
            SET left_id = new_left, right_id = new_right
            WHERE id = sid;
        END IF;
    END LOOP;
    RETURN OLD;
END $$;

CREATE FUNCTION public.set_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN NEW.updated_at = now(); RETURN NEW; END;
$$;

CREATE FUNCTION public.stop_deleted_conversation_retest() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE finding_retests SET status='stopped', error='复测会话已删除', finished_at=now()
    WHERE conversation_id=OLD.id AND status IN ('pending','running');
    RETURN OLD;
END;
$$;


CREATE FUNCTION public.try_inet(value text) RETURNS inet
    LANGUAGE plpgsql IMMUTABLE STRICT
    AS $$
BEGIN
    RETURN value::inet;
EXCEPTION WHEN others THEN
    RETURN NULL;
END;
$$;



CREATE TABLE public.activity (
    id bigint NOT NULL,
    exploration_id bigint NOT NULL,
    node_id bigint,
    worker text,
    kind text,
    tool text,
    tool_use_id text,
    is_error boolean DEFAULT false NOT NULL,
    summary text,
    detail text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    input_tokens integer,
    output_tokens integer,
    cache_read_tokens integer,
    cache_write_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    main_seg integer
);

CREATE SEQUENCE public.activity_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.activity_id_seq OWNED BY public.activity.id;

CREATE TABLE public.agent_prompt_vars (
    id bigint NOT NULL,
    agent_id bigint NOT NULL,
    var_name text NOT NULL,
    description text,
    example text,
    source text NOT NULL,
    CONSTRAINT agent_prompt_vars_source_check CHECK ((source = ANY (ARRAY['exploration'::text, 'runtime'::text, 'distilled'::text])))
);

CREATE SEQUENCE public.agent_prompt_vars_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.agent_prompt_vars_id_seq OWNED BY public.agent_prompt_vars.id;

CREATE TABLE public.agent_prompts (
    id bigint NOT NULL,
    agent_id bigint NOT NULL,
    version integer NOT NULL,
    template_text text NOT NULL,
    note text,
    updated_by text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.agent_prompts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.agent_prompts_id_seq OWNED BY public.agent_prompts.id;

CREATE TABLE public.agent_skill_visibility (
    agent_id bigint NOT NULL,
    skill_name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.agent_triggers (
    id bigint NOT NULL,
    agent_key text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    interval_sec integer DEFAULT 0 NOT NULL,
    on_finding boolean DEFAULT false NOT NULL,
    on_goal_met boolean DEFAULT false NOT NULL,
    on_task_timeout boolean DEFAULT false NOT NULL,
    on_tool_call boolean DEFAULT false NOT NULL,
    on_task_create boolean DEFAULT false NOT NULL,
    interval_message text DEFAULT ''::text NOT NULL,
    finding_message text DEFAULT ''::text NOT NULL,
    goal_message text DEFAULT ''::text NOT NULL,
    task_timeout_message text DEFAULT ''::text NOT NULL,
    tool_call_message text DEFAULT ''::text NOT NULL,
    task_create_message text DEFAULT ''::text NOT NULL,
    tool_names text DEFAULT ''::text NOT NULL,
    last_fire timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.agent_triggers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.agent_triggers_id_seq OWNED BY public.agent_triggers.id;

CREATE TABLE public.agent_visibility (
    agent_id bigint NOT NULL,
    resource_kind text NOT NULL,
    resource_id bigint NOT NULL,
    mcp_tool_name text DEFAULT ''::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_visibility_resource_kind_check CHECK ((resource_kind = 'mcp'::text))
);

CREATE TABLE public.agents (
    id bigint NOT NULL,
    key text NOT NULL,
    name text NOT NULL,
    description text,
    role text NOT NULL,
    builtin boolean DEFAULT true NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    llm_profile_id bigint,
    current_prompt_id bigint,
    max_turns integer DEFAULT 0 NOT NULL,
    run_seconds integer DEFAULT 1200 NOT NULL,
    web_search boolean DEFAULT false NOT NULL,
    interactive_shell boolean DEFAULT false NOT NULL,
    wrapup_prompt text DEFAULT ''::text NOT NULL,
    wrapup_max_turns integer DEFAULT 0 NOT NULL,
    task_timeout_wrapup_prompt text DEFAULT ''::text NOT NULL,
    task_timeout_wrapup_max_turns integer DEFAULT 0 NOT NULL,
    trigger_run_mode text DEFAULT 'serial'::text NOT NULL,
    trigger_merge_mode text DEFAULT 'all'::text NOT NULL,
    trigger_max_parallel integer DEFAULT 5 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agents_key_check CHECK ((key ~ '^[a-z][a-z0-9_]*$'::text)),
    CONSTRAINT agents_role_ck CHECK ((role = ANY (ARRAY['goals'::text, 'main'::text, 'planner'::text, 'worker'::text, 'assistant'::text]))),
    CONSTRAINT agents_trigger_merge_mode_check CHECK ((trigger_merge_mode = ANY (ARRAY['by_task'::text, 'all'::text, 'none'::text]))),
    CONSTRAINT agents_trigger_run_mode_check CHECK ((trigger_run_mode = ANY (ARRAY['serial'::text, 'parallel'::text])))
);

CREATE SEQUENCE public.agents_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.agents_id_seq OWNED BY public.agents.id;

CREATE TABLE public.asset_intercept_rules (
    id bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    kind text NOT NULL,
    pattern text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    builtin boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT asset_intercept_rules_kind_check CHECK ((kind = ANY (ARRAY['exact_domain'::text, 'exact_ip'::text, 'exact_url'::text, 'fuzzy_domain'::text, 'fuzzy_ip'::text, 'fuzzy_url'::text, 'cidr'::text])))
);

CREATE SEQUENCE public.asset_intercept_rules_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.asset_intercept_rules_id_seq OWNED BY public.asset_intercept_rules.id;

CREATE TABLE public.assets (
    id bigint NOT NULL,
    type text NOT NULL,
    company_id bigint,
    company_source text DEFAULT 'explicit'::text NOT NULL,
    domain text,
    root_domain text,
    ip text,
    c_segment cidr,
    port integer,
    icp text,
    bound_domains text[] DEFAULT '{}'::text[] NOT NULL,
    open_ports jsonb[] DEFAULT '{}'::jsonb[] NOT NULL,
    record_type text,
    record_value text[],
    bundle_id text,
    app_name text,
    category text,
    app_description text,
    app_icp text,
    url text,
    service_type text,
    service_name text,
    favicon_mmh3 text,
    status_code integer,
    content_length bigint,
    page_title text,
    technologies text[] DEFAULT '{}'::text[] NOT NULL,
    auth jsonb[] DEFAULT '{}'::jsonb[] NOT NULL,
    method text,
    params jsonb[] DEFAULT '{}'::jsonb[] NOT NULL,
    extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT assets_company_source_check CHECK ((company_source = ANY (ARRAY['explicit'::text, 'scope'::text]))),
    CONSTRAINT assets_port_check CHECK (((port >= 1) AND (port <= 65535))),
    CONSTRAINT assets_service_type_check CHECK ((service_type = ANY (ARRAY['http'::text, 'other'::text]))),
    CONSTRAINT assets_type_check CHECK ((type = ANY (ARRAY['root_domain'::text, 'ip'::text, 'subdomain'::text, 'app'::text, 'service'::text, 'endpoint'::text])))
);

CREATE SEQUENCE public.assets_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.assets_id_seq OWNED BY public.assets.id;

CREATE TABLE public.companies (
    id bigint NOT NULL,
    name text NOT NULL,
    nkey text NOT NULL,
    logo text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.companies_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.companies_id_seq OWNED BY public.companies.id;

CREATE TABLE public.company_scope (
    id bigint NOT NULL,
    company_id bigint NOT NULL,
    kind text NOT NULL,
    domain text,
    net cidr,
    value text,
    raw text NOT NULL,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_company_scope_payload CHECK ((((kind = 'domain'::text) AND (domain IS NOT NULL) AND (net IS NULL) AND (value IS NULL)) OR ((kind = ANY (ARRAY['ip'::text, 'cidr'::text])) AND (domain IS NULL) AND (net IS NOT NULL) AND (value IS NULL)) OR ((kind = ANY (ARRAY['icp'::text, 'keyword'::text])) AND (domain IS NULL) AND (net IS NULL) AND (value IS NOT NULL)))),
    CONSTRAINT company_scope_kind_check CHECK ((kind = ANY (ARRAY['domain'::text, 'ip'::text, 'cidr'::text, 'icp'::text, 'keyword'::text])))
);

CREATE SEQUENCE public.company_scope_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.company_scope_id_seq OWNED BY public.company_scope.id;

CREATE TABLE public.conversation_activities (
    id bigint NOT NULL,
    conversation_id bigint NOT NULL,
    worker text,
    kind text,
    tool text,
    tool_use_id text,
    is_error boolean DEFAULT false NOT NULL,
    summary text,
    detail text,
    input_tokens integer,
    output_tokens integer,
    cache_read_tokens integer,
    cache_write_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.conversation_activities_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.conversation_activities_id_seq OWNED BY public.conversation_activities.id;

CREATE TABLE public.conversations (
    id bigint NOT NULL,
    agent_key text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    llm_profile_id bigint,
    pinned_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.conversations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.conversations_id_seq OWNED BY public.conversations.id;

CREATE TABLE public.exploration_anchors (
    node_id bigint NOT NULL,
    asset_id bigint NOT NULL
);

CREATE TABLE public.exploration_edges (
    exploration_id bigint NOT NULL,
    src_id bigint NOT NULL,
    dst_id bigint NOT NULL,
    rel text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_edge_noself CHECK ((src_id <> dst_id)),
    CONSTRAINT ck_edge_rel CHECK ((rel = ANY (ARRAY['spawns'::text, 'derived_from'::text, 'yields'::text, 'proves'::text, 'covers'::text])))
);

CREATE TABLE public.exploration_nodes (
    id bigint NOT NULL,
    exploration_id bigint NOT NULL,
    kind text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    priority integer DEFAULT 0 NOT NULL,
    state text DEFAULT 'open'::text NOT NULL,
    origin text,
    owner text,
    blocked_reason text,
    delete_reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    content_version integer DEFAULT 0 NOT NULL,
    cold_since_round bigint,
    CONSTRAINT ck_node_kind CHECK ((kind = ANY (ARRAY['begin'::text, 'goal'::text, 'intent'::text, 'fact'::text, 'finding'::text, 'hint'::text, 'digest'::text]))),
    CONSTRAINT ck_node_state CHECK ((((kind = 'begin'::text) AND (state = 'open'::text)) OR ((kind = 'intent'::text) AND (state = ANY (ARRAY['open'::text, 'running'::text, 'paused'::text, 'done'::text, 'blocked'::text, 'exhausted'::text, 'stopped'::text, 'deleted'::text]))) OR ((kind = 'goal'::text) AND (state = ANY (ARRAY['open'::text, 'met'::text, 'abandoned'::text]))) OR ((kind = 'fact'::text) AND (state = ANY (ARRAY['confirmed'::text, 'dismissed'::text, 'origin'::text]))) OR ((kind = 'finding'::text) AND (state = ANY (ARRAY['confirmed'::text, 'dismissed'::text]))) OR ((kind = 'hint'::text) AND (state = ANY (ARRAY['active'::text, 'consumed'::text]))) OR ((kind = 'digest'::text) AND (state = ANY (ARRAY['active'::text, 'superseded'::text])))))
);

CREATE SEQUENCE public.exploration_nodes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.exploration_nodes_id_seq OWNED BY public.exploration_nodes.id;

CREATE TABLE public.explorations (
    id bigint NOT NULL,
    description text,
    goal text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    round_no bigint DEFAULT 0 NOT NULL,
    CONSTRAINT explorations_status_check CHECK ((status = ANY (ARRAY['open'::text, 'achieved'::text, 'failed'::text])))
);

CREATE SEQUENCE public.explorations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.explorations_id_seq OWNED BY public.explorations.id;

CREATE TABLE public.finding_case_blocks (
    left_id bigint NOT NULL,
    right_id bigint NOT NULL,
    reason text NOT NULL,
    CONSTRAINT finding_case_blocks_check CHECK ((left_id < right_id))
);

CREATE TABLE public.finding_case_events (
    id bigint NOT NULL,
    case_id bigint NOT NULL,
    action text NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL,
    finding_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.finding_case_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.finding_case_events_id_seq OWNED BY public.finding_case_events.id;

CREATE TABLE public.finding_case_members (
    finding_id bigint NOT NULL,
    case_id bigint NOT NULL,
    joined_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.finding_case_review_runs (
    conversation_id bigint NOT NULL,
    task_id bigint,
    state text DEFAULT 'queued'::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    finding_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    reviewed_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    conclusion jsonb DEFAULT '{}'::jsonb NOT NULL
);

CREATE TABLE public.finding_case_suggestion_members (
    suggestion_id bigint NOT NULL,
    finding_id bigint NOT NULL
);

CREATE TABLE public.finding_case_suggestions (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    left_id bigint NOT NULL,
    right_id bigint NOT NULL,
    title text NOT NULL,
    reason text NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT finding_case_suggestions_check CHECK ((left_id < right_id))
);

CREATE SEQUENCE public.finding_case_suggestions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.finding_case_suggestions_id_seq OWNED BY public.finding_case_suggestions.id;

CREATE TABLE public.finding_cases (
    id bigint NOT NULL,
    task_id bigint,
    origin_task_id bigint NOT NULL,
    title text NOT NULL,
    reason text NOT NULL,
    severity text DEFAULT ''::text NOT NULL,
    severity_reason text DEFAULT ''::text NOT NULL,
    report text DEFAULT ''::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    report_version bigint DEFAULT 0 NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.finding_cases_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.finding_cases_id_seq OWNED BY public.finding_cases.id;

CREATE TABLE public.finding_retests (
    id bigint NOT NULL,
    finding_id bigint NOT NULL,
    conversation_id bigint,
    status text DEFAULT 'pending'::text NOT NULL,
    verdict text DEFAULT ''::text NOT NULL,
    notes text DEFAULT ''::text NOT NULL,
    snapshot jsonb NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    evidence text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    CONSTRAINT finding_retests_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'running'::text, 'completed'::text, 'failed'::text, 'stopped'::text]))),
    CONSTRAINT finding_retests_verdict_check CHECK ((verdict = ANY (ARRAY[''::text, 'reproduced'::text, 'fixed'::text, 'inconclusive'::text])))
);

CREATE SEQUENCE public.finding_retests_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.finding_retests_id_seq OWNED BY public.finding_retests.id;

CREATE TABLE public.finding_traffic_bindings (
    id bigint NOT NULL,
    finding_id bigint NOT NULL,
    snapshot_id text NOT NULL,
    role text DEFAULT 'supporting'::text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    "position" integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.finding_traffic_bindings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.finding_traffic_bindings_id_seq OWNED BY public.finding_traffic_bindings.id;

CREATE TABLE public.findings (
    id bigint NOT NULL,
    task_id bigint,
    node_id bigint,
    vulnclass text DEFAULT ''::text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    severity text DEFAULT ''::text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    evidence text DEFAULT ''::text NOT NULL,
    worker text DEFAULT ''::text NOT NULL,
    asset_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    report text DEFAULT ''::text NOT NULL,
    report_conversation_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    evidence_version bigint DEFAULT 0 NOT NULL,
    report_evidence_version bigint DEFAULT 0 NOT NULL
);

CREATE SEQUENCE public.findings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.findings_id_seq OWNED BY public.findings.id;

CREATE TABLE public.intercept_pending (
    id bigint NOT NULL,
    rule_id bigint,
    conversation_id bigint,
    task_id text,
    agent_name text DEFAULT ''::text NOT NULL,
    tool_name text NOT NULL,
    tool_input jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    decided_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    audit jsonb,
    decision_source text DEFAULT ''::text NOT NULL,
    CONSTRAINT intercept_pending_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'allowed'::text, 'denied'::text, 'timeout'::text])))
);

CREATE SEQUENCE public.intercept_pending_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.intercept_pending_id_seq OWNED BY public.intercept_pending.id;

CREATE TABLE public.intercept_rules (
    id bigint NOT NULL,
    name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    priority integer DEFAULT 0 NOT NULL,
    match_target text NOT NULL,
    match_type text NOT NULL,
    pattern text NOT NULL,
    action text NOT NULL,
    message text DEFAULT ''::text NOT NULL,
    timeout_enabled boolean DEFAULT true NOT NULL,
    timeout_seconds integer DEFAULT 60 NOT NULL,
    timeout_action text DEFAULT 'deny'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT intercept_rules_action_check CHECK ((action = ANY (ARRAY['allow'::text, 'deny'::text, 'ask'::text]))),
    CONSTRAINT intercept_rules_match_target_check CHECK ((match_target = ANY (ARRAY['tool_name'::text, 'tool_input'::text]))),
    CONSTRAINT intercept_rules_match_type_check CHECK ((match_type = ANY (ARRAY['string'::text, 'regex'::text])))
);

CREATE SEQUENCE public.intercept_rules_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.intercept_rules_id_seq OWNED BY public.intercept_rules.id;

CREATE TABLE public.llm_profile_health (
    profile_id bigint NOT NULL,
    fails integer DEFAULT 0 NOT NULL,
    trips integer DEFAULT 0 NOT NULL,
    open_until timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    last_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.llm_profiles (
    id bigint NOT NULL,
    name text NOT NULL,
    format text NOT NULL,
    base_url text,
    model text NOT NULL,
    api_key text,
    api_key_hint text,
    rate_per_second double precision DEFAULT 0 NOT NULL,
    rate_per_minute double precision DEFAULT 0 NOT NULL,
    context_window_k integer DEFAULT 0 NOT NULL,
    reasoning_effort text DEFAULT ''::text NOT NULL,
    thinking_type text DEFAULT ''::text NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    priority integer DEFAULT 0 NOT NULL,
    pool_exclude boolean DEFAULT false NOT NULL,
    streaming boolean DEFAULT true NOT NULL,
    max_tokens integer DEFAULT 0 NOT NULL,
    max_tokens_field text DEFAULT ''::text NOT NULL,
    session_header_key text DEFAULT ''::text NOT NULL,
    retry_connect_attempts integer DEFAULT 0 NOT NULL,
    retry_connect_interval_ms integer DEFAULT 0 NOT NULL,
    retry_empty_attempts integer DEFAULT 0 NOT NULL,
    retry_empty_interval_ms integer DEFAULT 0 NOT NULL,
    retry_stream_attempts integer DEFAULT 0 NOT NULL,
    retry_stream_interval_ms integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT llm_profiles_format_check CHECK ((format = ANY (ARRAY['openai'::text, 'anthropic'::text, 'openai-responses'::text, 'jev'::text]))),
    CONSTRAINT llm_profiles_max_tokens_check CHECK ((max_tokens >= 0)),
    CONSTRAINT llm_profiles_max_tokens_field_check CHECK ((max_tokens_field = ANY (ARRAY[''::text, 'max_completion_tokens'::text]))),
    CONSTRAINT llm_profiles_retry_check CHECK (((retry_connect_attempts >= '-1'::integer) AND (retry_empty_attempts >= '-1'::integer) AND (retry_stream_attempts >= '-1'::integer) AND (retry_connect_interval_ms >= 0) AND (retry_empty_interval_ms >= 0) AND (retry_stream_interval_ms >= 0)))
);

CREATE SEQUENCE public.llm_profiles_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.llm_profiles_id_seq OWNED BY public.llm_profiles.id;

CREATE TABLE public.llm_records (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now(),
    model text,
    profile_name text,
    session_id text,
    task_id text,
    worker text,
    latency_ms integer,
    input_tokens integer,
    output_tokens integer,
    cache_read integer,
    cache_write integer,
    status text,
    error text,
    request_body text,
    response_body text,
    raw_request text,
    raw_response text
);

CREATE SEQUENCE public.llm_records_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.llm_records_id_seq OWNED BY public.llm_records.id;

CREATE TABLE public.llm_usage (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    task_id text,
    exploration_id bigint,
    worker text,
    model text,
    profile_name text,
    latency_ms integer,
    input_tokens integer DEFAULT 0 NOT NULL,
    output_tokens integer DEFAULT 0 NOT NULL,
    cache_read integer DEFAULT 0 NOT NULL,
    cache_write integer DEFAULT 0 NOT NULL,
    status text
);

CREATE SEQUENCE public.llm_usage_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.llm_usage_id_seq OWNED BY public.llm_usage.id;

CREATE TABLE public.main_sessions (
    exploration_id bigint NOT NULL,
    seq integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.mcp_servers (
    id bigint NOT NULL,
    name text NOT NULL,
    transport text NOT NULL,
    command text,
    args jsonb DEFAULT '[]'::jsonb NOT NULL,
    env jsonb DEFAULT '{}'::jsonb NOT NULL,
    url text,
    enabled boolean DEFAULT true NOT NULL,
    insecure boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT mcp_servers_transport_check CHECK ((transport = ANY (ARRAY['stdio'::text, 'http'::text, 'sse'::text])))
);

CREATE SEQUENCE public.mcp_servers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.mcp_servers_id_seq OWNED BY public.mcp_servers.id;

CREATE TABLE public.mcp_tools_cache (
    id bigint NOT NULL,
    server_id bigint NOT NULL,
    tool_name text NOT NULL,
    description text,
    schema jsonb,
    discovered_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.mcp_tools_cache_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.mcp_tools_cache_id_seq OWNED BY public.mcp_tools_cache.id;

CREATE TABLE public.notification_channels (
    id bigint NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    mode text DEFAULT 'realtime'::text NOT NULL,
    filter jsonb DEFAULT '{}'::jsonb NOT NULL,
    rate_per_min integer DEFAULT 20 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.notification_channels_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.notification_channels_id_seq OWNED BY public.notification_channels.id;

CREATE TABLE public.notification_deliveries (
    id bigint NOT NULL,
    event_id bigint NOT NULL,
    channel_id bigint NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    batch_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    sent_at timestamp with time zone
);

CREATE SEQUENCE public.notification_deliveries_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.notification_deliveries_id_seq OWNED BY public.notification_deliveries.id;

CREATE TABLE public.notification_events (
    id bigint NOT NULL,
    kind text NOT NULL,
    finding_id bigint NOT NULL,
    snapshot jsonb NOT NULL,
    fanned_out boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.notification_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.notification_events_id_seq OWNED BY public.notification_events.id;

CREATE TABLE public.scheduler_state (
    key text NOT NULL,
    value text DEFAULT ''::text NOT NULL
);

CREATE TABLE public.server_logs (
    id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    level text DEFAULT 'info'::text NOT NULL,
    tag text DEFAULT ''::text NOT NULL,
    text text DEFAULT ''::text NOT NULL
);

CREATE SEQUENCE public.server_logs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.server_logs_id_seq OWNED BY public.server_logs.id;

CREATE TABLE public.settings (
    key text NOT NULL,
    value text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.side_question_requests (
    id text NOT NULL,
    ordinal bigint NOT NULL,
    session_key text NOT NULL,
    generation bigint NOT NULL,
    client_id text NOT NULL,
    question text NOT NULL,
    answer text DEFAULT ''::text NOT NULL,
    status text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    model jsonb NOT NULL,
    snapshot_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    sequence bigint DEFAULT 0 NOT NULL,
    usage jsonb DEFAULT '{}'::jsonb NOT NULL,
    context_info jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT side_question_requests_status_check CHECK ((status = ANY (ARRAY['running'::text, 'completed'::text, 'failed'::text, 'cancelled'::text, 'interrupted'::text])))
);

CREATE SEQUENCE public.side_question_requests_ordinal_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.side_question_requests_ordinal_seq OWNED BY public.side_question_requests.ordinal;

CREATE TABLE public.side_question_sessions (
    session_key text NOT NULL,
    conversation_id bigint,
    task_id bigint,
    exploration_id bigint,
    intent_id bigint,
    approval_id bigint,
    run_id bigint NOT NULL,
    version bigint NOT NULL,
    snapshot jsonb NOT NULL,
    generation bigint DEFAULT 0 NOT NULL,
    memory jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT side_question_sessions_check CHECK (((approval_id IS NOT NULL) OR ((conversation_id IS NOT NULL) AND (task_id IS NULL) AND (exploration_id IS NULL) AND (intent_id IS NULL)) OR ((conversation_id IS NULL) AND (task_id IS NOT NULL) AND (exploration_id IS NOT NULL))))
);

CREATE TABLE public.skill_usage (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    skill text NOT NULL,
    agent_key text,
    task_id bigint,
    exploration_id bigint,
    intent_id bigint,
    session_id text,
    args_len integer DEFAULT 0 NOT NULL,
    found boolean DEFAULT true NOT NULL
);

CREATE SEQUENCE public.skill_usage_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.skill_usage_id_seq OWNED BY public.skill_usage.id;

CREATE TABLE public.task_archives (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    state text DEFAULT 'archive_queued'::text NOT NULL,
    phase text DEFAULT 'queued'::text NOT NULL,
    progress integer DEFAULT 0 NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    warnings jsonb DEFAULT '[]'::jsonb NOT NULL,
    format_version integer DEFAULT 2 NOT NULL,
    archive_path text DEFAULT ''::text NOT NULL,
    sha256 text DEFAULT ''::text NOT NULL,
    original_size bigint DEFAULT 0 NOT NULL,
    compressed_size bigint DEFAULT 0 NOT NULL,
    task_name text DEFAULT ''::text NOT NULL,
    task_description text DEFAULT ''::text NOT NULL,
    task_goal text DEFAULT ''::text NOT NULL,
    original_status text DEFAULT ''::text NOT NULL,
    category_id_snapshot bigint,
    category_name_snapshot text DEFAULT ''::text NOT NULL,
    source_task_ids bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    remaining_timeout_seconds bigint DEFAULT 0 NOT NULL,
    data_counts jsonb DEFAULT '{}'::jsonb NOT NULL,
    aggregate_stats jsonb DEFAULT '{}'::jsonb NOT NULL,
    archived_at timestamp with time zone,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT task_archives_progress_check CHECK (((progress >= 0) AND (progress <= 100))),
    CONSTRAINT task_archives_state_check CHECK ((state = ANY (ARRAY['archive_queued'::text, 'archiving'::text, 'archive_failed'::text, 'ready'::text, 'restore_queued'::text, 'restoring'::text, 'restore_failed'::text, 'delete_queued'::text, 'deleting'::text, 'delete_failed'::text])))
);

CREATE SEQUENCE public.task_archives_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.task_archives_id_seq OWNED BY public.task_archives.id;

CREATE TABLE public.task_asset_links (
    task_id bigint NOT NULL,
    asset_id bigint NOT NULL,
    source text DEFAULT 'system'::text NOT NULL,
    source_summary text DEFAULT ''::text NOT NULL,
    source_node_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.task_categories (
    id bigint NOT NULL,
    name text NOT NULL,
    nkey text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.task_categories_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.task_categories_id_seq OWNED BY public.task_categories.id;

CREATE TABLE public.task_constraints (
    id bigint NOT NULL,
    exploration_id bigint NOT NULL,
    kind text NOT NULL,
    text text NOT NULL,
    origin text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT task_constraints_kind_check CHECK ((kind = ANY (ARRAY['allow'::text, 'deny'::text])))
);

CREATE SEQUENCE public.task_constraints_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.task_constraints_id_seq OWNED BY public.task_constraints.id;

CREATE TABLE public.task_intelligence (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    node_id bigint,
    kind text NOT NULL,
    title text NOT NULL,
    content text NOT NULL,
    evidence text DEFAULT ''::text NOT NULL,
    confidence text NOT NULL,
    status text NOT NULL,
    asset_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    worker text DEFAULT ''::text NOT NULL,
    fingerprint text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    credential_analysis jsonb,
    CONSTRAINT task_intelligence_confidence_check CHECK ((confidence = ANY (ARRAY['observed'::text, 'inferred'::text]))),
    CONSTRAINT task_intelligence_kind_check CHECK ((kind = ANY (ARRAY['credential'::text, 'lead'::text, 'sensitive_data'::text])))
);

CREATE SEQUENCE public.task_intelligence_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.task_intelligence_id_seq OWNED BY public.task_intelligence.id;

CREATE TABLE public.task_intercept_rules (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    action text DEFAULT 'block'::text NOT NULL,
    kind text NOT NULL,
    pattern text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT task_intercept_rules_action_check CHECK ((action = ANY (ARRAY['block'::text, 'allow'::text]))),
    CONSTRAINT task_intercept_rules_kind_check CHECK ((kind = ANY (ARRAY['exact_domain'::text, 'exact_ip'::text, 'exact_url'::text, 'fuzzy_domain'::text, 'fuzzy_ip'::text, 'fuzzy_url'::text, 'cidr'::text])))
);

CREATE SEQUENCE public.task_intercept_rules_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.task_intercept_rules_id_seq OWNED BY public.task_intercept_rules.id;

CREATE TABLE public.task_llm_profiles (
    task_id bigint NOT NULL,
    profile_id bigint NOT NULL,
    "position" integer NOT NULL,
    status text DEFAULT 'ready'::text NOT NULL,
    last_error text,
    exhausted_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT task_llm_profiles_position_check CHECK (("position" >= 0)),
    CONSTRAINT task_llm_profiles_status_check CHECK ((status = ANY (ARRAY['ready'::text, 'quota_exhausted'::text])))
);

CREATE TABLE public.task_relations (
    task_id bigint NOT NULL,
    source_task_id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_task_relation_not_self CHECK ((task_id <> source_task_id))
);

CREATE TABLE public.task_schedule_history (
    id bigint NOT NULL,
    schedule_id bigint NOT NULL,
    task_id bigint,
    action text NOT NULL,
    success boolean NOT NULL,
    message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.task_schedule_history_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.task_schedule_history_id_seq OWNED BY public.task_schedule_history.id;

CREATE TABLE public.task_schedule_tasks (
    schedule_id bigint NOT NULL,
    task_id bigint NOT NULL,
    paused_by_schedule boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.task_schedules (
    id bigint NOT NULL,
    name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    timezone_mode text DEFAULT 'beijing'::text NOT NULL,
    schedule_type text DEFAULT 'weekly'::text NOT NULL,
    run_date date,
    end_date date,
    weekdays text DEFAULT ''::text NOT NULL,
    start_time time without time zone NOT NULL,
    end_time time without time zone,
    start_immediately boolean DEFAULT false NOT NULL,
    status text DEFAULT 'waiting'::text NOT NULL,
    last_window_key text DEFAULT ''::text NOT NULL,
    last_transition_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    remark text DEFAULT ''::text NOT NULL,
    CONSTRAINT task_schedules_check CHECK ((((schedule_type = 'once'::text) AND (run_date IS NOT NULL)) OR ((schedule_type = 'weekly'::text) AND (run_date IS NULL)))),
    CONSTRAINT task_schedules_check1 CHECK (((schedule_type = 'once'::text) OR (weekdays <> ''::text))),
    CONSTRAINT task_schedules_schedule_type_check CHECK ((schedule_type = ANY (ARRAY['once'::text, 'weekly'::text]))),
    CONSTRAINT task_schedules_status_check CHECK ((status = ANY (ARRAY['waiting'::text, 'running'::text, 'outside'::text, 'completed'::text, 'error'::text, 'paused'::text]))),
    CONSTRAINT task_schedules_timezone_mode_check CHECK ((timezone_mode = ANY (ARRAY['beijing'::text, 'system'::text])))
);

CREATE SEQUENCE public.task_schedules_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.task_schedules_id_seq OWNED BY public.task_schedules.id;

CREATE TABLE public.task_scope (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    kind text NOT NULL,
    company_id bigint,
    domain text,
    net cidr,
    value text,
    source text DEFAULT 'auto'::text NOT NULL,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT task_scope_kind_check CHECK ((kind = ANY (ARRAY['company'::text, 'root_domain'::text, 'subdomain'::text, 'ip'::text, 'cidr'::text, 'icp'::text, 'keyword'::text]))),
    CONSTRAINT task_scope_source_check CHECK ((source = ANY (ARRAY['auto'::text, 'agent'::text, 'manual'::text])))
);

CREATE SEQUENCE public.task_scope_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.task_scope_id_seq OWNED BY public.task_scope.id;

CREATE TABLE public.task_templates (
    id bigint NOT NULL,
    name text NOT NULL,
    nkey text NOT NULL,
    description text NOT NULL,
    goal text NOT NULL,
    remark text DEFAULT ''::text NOT NULL,
    category_id bigint,
    intercept_rules jsonb DEFAULT '[]'::jsonb NOT NULL,
    worker_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.task_templates_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.task_templates_id_seq OWNED BY public.task_templates.id;

CREATE TABLE public.tasks (
    id bigint NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    category_id bigint,
    description text NOT NULL,
    goal text NOT NULL,
    exploration_id bigint NOT NULL,
    status text DEFAULT 'created'::text NOT NULL,
    paused boolean DEFAULT false NOT NULL,
    pause_reason text DEFAULT ''::text NOT NULL,
    queued boolean DEFAULT false NOT NULL,
    queued_at timestamp with time zone,
    queue_mode text DEFAULT ''::text NOT NULL,
    active_llm_profile_id bigint,
    llm_chain_revision bigint DEFAULT 0 NOT NULL,
    company_id bigint,
    parent_ref text,
    timeout_seconds integer DEFAULT 0 NOT NULL,
    no_result_seconds integer DEFAULT 0 NOT NULL,
    no_result_min_findings integer DEFAULT 1 NOT NULL,
    no_result_min_severity text DEFAULT ''::text NOT NULL,
    no_result_base_seconds bigint DEFAULT 0 NOT NULL,
    no_result_checked_at timestamp with time zone,
    stop_reason text DEFAULT ''::text NOT NULL,
    plan_heartbeat_seconds integer DEFAULT 300 NOT NULL,
    coverage_enabled boolean DEFAULT true NOT NULL,
    worker_count integer DEFAULT 0 NOT NULL,
    pinned_at timestamp with time zone,
    first_run_at timestamp with time zone,
    deadline_at timestamp with time zone,
    active_run_seconds bigint DEFAULT 0 NOT NULL,
    run_segment_started_at timestamp with time zone,
    archived_at timestamp with time zone,
    deleted_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    share_global_assets boolean DEFAULT false NOT NULL,
    remark text DEFAULT ''::text NOT NULL,
    schedule_paused boolean DEFAULT false NOT NULL,
    schedule_manual_until timestamp with time zone,
    CONSTRAINT tasks_status_check CHECK ((status = ANY (ARRAY['created'::text, 'running'::text, 'paused'::text, 'done'::text, 'failed'::text, 'timeout'::text])))
);

CREATE SEQUENCE public.tasks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.tasks_id_seq OWNED BY public.tasks.id;

CREATE TABLE public.tool_usage (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tool_key text NOT NULL,
    agent_key text,
    task_id bigint,
    exploration_id bigint,
    intent_id bigint,
    session_id text
);

CREATE SEQUENCE public.tool_usage_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.tool_usage_id_seq OWNED BY public.tool_usage.id;

CREATE TABLE public.tools (
    key text NOT NULL,
    system boolean DEFAULT true NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    schema jsonb DEFAULT '{}'::jsonb NOT NULL,
    agents jsonb DEFAULT '[]'::jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    kind text DEFAULT 'builtin'::text NOT NULL,
    exec jsonb DEFAULT '{}'::jsonb NOT NULL,
    deferred boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.traffic_evidence_snapshots (
    id text NOT NULL,
    source_traffic_id text NOT NULL,
    captured_at bigint NOT NULL,
    url text NOT NULL,
    method text NOT NULL,
    status integer NOT NULL,
    content_type text DEFAULT ''::text NOT NULL,
    req_head text NOT NULL,
    resp_head text NOT NULL,
    req_hash text NOT NULL,
    resp_hash text NOT NULL,
    req_len bigint NOT NULL,
    resp_len bigint NOT NULL,
    unreferenced_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.activity ALTER COLUMN id SET DEFAULT nextval('public.activity_id_seq'::regclass);

ALTER TABLE ONLY public.agent_prompt_vars ALTER COLUMN id SET DEFAULT nextval('public.agent_prompt_vars_id_seq'::regclass);

ALTER TABLE ONLY public.agent_prompts ALTER COLUMN id SET DEFAULT nextval('public.agent_prompts_id_seq'::regclass);

ALTER TABLE ONLY public.agent_triggers ALTER COLUMN id SET DEFAULT nextval('public.agent_triggers_id_seq'::regclass);

ALTER TABLE ONLY public.agents ALTER COLUMN id SET DEFAULT nextval('public.agents_id_seq'::regclass);

ALTER TABLE ONLY public.asset_intercept_rules ALTER COLUMN id SET DEFAULT nextval('public.asset_intercept_rules_id_seq'::regclass);

ALTER TABLE ONLY public.assets ALTER COLUMN id SET DEFAULT nextval('public.assets_id_seq'::regclass);

ALTER TABLE ONLY public.companies ALTER COLUMN id SET DEFAULT nextval('public.companies_id_seq'::regclass);

ALTER TABLE ONLY public.company_scope ALTER COLUMN id SET DEFAULT nextval('public.company_scope_id_seq'::regclass);

ALTER TABLE ONLY public.conversation_activities ALTER COLUMN id SET DEFAULT nextval('public.conversation_activities_id_seq'::regclass);

ALTER TABLE ONLY public.conversations ALTER COLUMN id SET DEFAULT nextval('public.conversations_id_seq'::regclass);

ALTER TABLE ONLY public.exploration_nodes ALTER COLUMN id SET DEFAULT nextval('public.exploration_nodes_id_seq'::regclass);

ALTER TABLE ONLY public.explorations ALTER COLUMN id SET DEFAULT nextval('public.explorations_id_seq'::regclass);

ALTER TABLE ONLY public.finding_case_events ALTER COLUMN id SET DEFAULT nextval('public.finding_case_events_id_seq'::regclass);

ALTER TABLE ONLY public.finding_case_suggestions ALTER COLUMN id SET DEFAULT nextval('public.finding_case_suggestions_id_seq'::regclass);

ALTER TABLE ONLY public.finding_cases ALTER COLUMN id SET DEFAULT nextval('public.finding_cases_id_seq'::regclass);

ALTER TABLE ONLY public.finding_retests ALTER COLUMN id SET DEFAULT nextval('public.finding_retests_id_seq'::regclass);

ALTER TABLE ONLY public.finding_traffic_bindings ALTER COLUMN id SET DEFAULT nextval('public.finding_traffic_bindings_id_seq'::regclass);

ALTER TABLE ONLY public.findings ALTER COLUMN id SET DEFAULT nextval('public.findings_id_seq'::regclass);

ALTER TABLE ONLY public.intercept_pending ALTER COLUMN id SET DEFAULT nextval('public.intercept_pending_id_seq'::regclass);

ALTER TABLE ONLY public.intercept_rules ALTER COLUMN id SET DEFAULT nextval('public.intercept_rules_id_seq'::regclass);

ALTER TABLE ONLY public.llm_profiles ALTER COLUMN id SET DEFAULT nextval('public.llm_profiles_id_seq'::regclass);

ALTER TABLE ONLY public.llm_records ALTER COLUMN id SET DEFAULT nextval('public.llm_records_id_seq'::regclass);

ALTER TABLE ONLY public.llm_usage ALTER COLUMN id SET DEFAULT nextval('public.llm_usage_id_seq'::regclass);

ALTER TABLE ONLY public.mcp_servers ALTER COLUMN id SET DEFAULT nextval('public.mcp_servers_id_seq'::regclass);

ALTER TABLE ONLY public.mcp_tools_cache ALTER COLUMN id SET DEFAULT nextval('public.mcp_tools_cache_id_seq'::regclass);

ALTER TABLE ONLY public.notification_channels ALTER COLUMN id SET DEFAULT nextval('public.notification_channels_id_seq'::regclass);

ALTER TABLE ONLY public.notification_deliveries ALTER COLUMN id SET DEFAULT nextval('public.notification_deliveries_id_seq'::regclass);

ALTER TABLE ONLY public.notification_events ALTER COLUMN id SET DEFAULT nextval('public.notification_events_id_seq'::regclass);

ALTER TABLE ONLY public.server_logs ALTER COLUMN id SET DEFAULT nextval('public.server_logs_id_seq'::regclass);

ALTER TABLE ONLY public.side_question_requests ALTER COLUMN ordinal SET DEFAULT nextval('public.side_question_requests_ordinal_seq'::regclass);

ALTER TABLE ONLY public.skill_usage ALTER COLUMN id SET DEFAULT nextval('public.skill_usage_id_seq'::regclass);

ALTER TABLE ONLY public.task_archives ALTER COLUMN id SET DEFAULT nextval('public.task_archives_id_seq'::regclass);

ALTER TABLE ONLY public.task_categories ALTER COLUMN id SET DEFAULT nextval('public.task_categories_id_seq'::regclass);

ALTER TABLE ONLY public.task_constraints ALTER COLUMN id SET DEFAULT nextval('public.task_constraints_id_seq'::regclass);

ALTER TABLE ONLY public.task_intelligence ALTER COLUMN id SET DEFAULT nextval('public.task_intelligence_id_seq'::regclass);

ALTER TABLE ONLY public.task_intercept_rules ALTER COLUMN id SET DEFAULT nextval('public.task_intercept_rules_id_seq'::regclass);

ALTER TABLE ONLY public.task_schedule_history ALTER COLUMN id SET DEFAULT nextval('public.task_schedule_history_id_seq'::regclass);

ALTER TABLE ONLY public.task_schedules ALTER COLUMN id SET DEFAULT nextval('public.task_schedules_id_seq'::regclass);

ALTER TABLE ONLY public.task_scope ALTER COLUMN id SET DEFAULT nextval('public.task_scope_id_seq'::regclass);

ALTER TABLE ONLY public.task_templates ALTER COLUMN id SET DEFAULT nextval('public.task_templates_id_seq'::regclass);

ALTER TABLE ONLY public.tasks ALTER COLUMN id SET DEFAULT nextval('public.tasks_id_seq'::regclass);

ALTER TABLE ONLY public.tool_usage ALTER COLUMN id SET DEFAULT nextval('public.tool_usage_id_seq'::regclass);

ALTER TABLE ONLY public.activity
    ADD CONSTRAINT activity_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_prompt_vars
    ADD CONSTRAINT agent_prompt_vars_agent_id_var_name_key UNIQUE (agent_id, var_name);

ALTER TABLE ONLY public.agent_prompt_vars
    ADD CONSTRAINT agent_prompt_vars_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_prompts
    ADD CONSTRAINT agent_prompts_agent_id_version_key UNIQUE (agent_id, version);

ALTER TABLE ONLY public.agent_prompts
    ADD CONSTRAINT agent_prompts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_skill_visibility
    ADD CONSTRAINT agent_skill_visibility_pkey PRIMARY KEY (agent_id, skill_name);

ALTER TABLE ONLY public.agent_triggers
    ADD CONSTRAINT agent_triggers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_visibility
    ADD CONSTRAINT agent_visibility_pkey PRIMARY KEY (agent_id, resource_kind, resource_id, mcp_tool_name);

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_key_key UNIQUE (key);

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.asset_intercept_rules
    ADD CONSTRAINT asset_intercept_rules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.assets
    ADD CONSTRAINT assets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.companies
    ADD CONSTRAINT companies_nkey_key UNIQUE (nkey);

ALTER TABLE ONLY public.companies
    ADD CONSTRAINT companies_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.company_scope
    ADD CONSTRAINT company_scope_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.conversation_activities
    ADD CONSTRAINT conversation_activities_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.conversations
    ADD CONSTRAINT conversations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.exploration_anchors
    ADD CONSTRAINT exploration_anchors_pkey PRIMARY KEY (node_id, asset_id);

ALTER TABLE ONLY public.exploration_edges
    ADD CONSTRAINT exploration_edges_pkey PRIMARY KEY (exploration_id, src_id, rel, dst_id);

ALTER TABLE ONLY public.exploration_nodes
    ADD CONSTRAINT exploration_nodes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.explorations
    ADD CONSTRAINT explorations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.finding_case_blocks
    ADD CONSTRAINT finding_case_blocks_pkey PRIMARY KEY (left_id, right_id);

ALTER TABLE ONLY public.finding_case_events
    ADD CONSTRAINT finding_case_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.finding_case_members
    ADD CONSTRAINT finding_case_members_pkey PRIMARY KEY (finding_id);

ALTER TABLE ONLY public.finding_case_review_runs
    ADD CONSTRAINT finding_case_review_runs_pkey PRIMARY KEY (conversation_id);

ALTER TABLE ONLY public.finding_case_suggestion_members
    ADD CONSTRAINT finding_case_suggestion_members_pkey PRIMARY KEY (suggestion_id, finding_id);

ALTER TABLE ONLY public.finding_case_suggestions
    ADD CONSTRAINT finding_case_suggestions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.finding_cases
    ADD CONSTRAINT finding_cases_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.finding_retests
    ADD CONSTRAINT finding_retests_conversation_id_key UNIQUE (conversation_id);

ALTER TABLE ONLY public.finding_retests
    ADD CONSTRAINT finding_retests_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.finding_traffic_bindings
    ADD CONSTRAINT finding_traffic_bindings_finding_id_snapshot_id_key UNIQUE (finding_id, snapshot_id);

ALTER TABLE ONLY public.finding_traffic_bindings
    ADD CONSTRAINT finding_traffic_bindings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.findings
    ADD CONSTRAINT findings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.intercept_pending
    ADD CONSTRAINT intercept_pending_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.intercept_rules
    ADD CONSTRAINT intercept_rules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.llm_profile_health
    ADD CONSTRAINT llm_profile_health_pkey PRIMARY KEY (profile_id);

ALTER TABLE ONLY public.llm_profiles
    ADD CONSTRAINT llm_profiles_name_key UNIQUE (name);

ALTER TABLE ONLY public.llm_profiles
    ADD CONSTRAINT llm_profiles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.llm_records
    ADD CONSTRAINT llm_records_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.llm_usage
    ADD CONSTRAINT llm_usage_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.main_sessions
    ADD CONSTRAINT main_sessions_pkey PRIMARY KEY (exploration_id, seq);

ALTER TABLE ONLY public.mcp_servers
    ADD CONSTRAINT mcp_servers_name_key UNIQUE (name);

ALTER TABLE ONLY public.mcp_servers
    ADD CONSTRAINT mcp_servers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.mcp_tools_cache
    ADD CONSTRAINT mcp_tools_cache_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.mcp_tools_cache
    ADD CONSTRAINT mcp_tools_cache_server_id_tool_name_key UNIQUE (server_id, tool_name);

ALTER TABLE ONLY public.notification_channels
    ADD CONSTRAINT notification_channels_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.notification_events
    ADD CONSTRAINT notification_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.scheduler_state
    ADD CONSTRAINT scheduler_state_pkey PRIMARY KEY (key);

ALTER TABLE ONLY public.server_logs
    ADD CONSTRAINT server_logs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_pkey PRIMARY KEY (key);

ALTER TABLE ONLY public.side_question_requests
    ADD CONSTRAINT side_question_requests_ordinal_key UNIQUE (ordinal);

ALTER TABLE ONLY public.side_question_requests
    ADD CONSTRAINT side_question_requests_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.side_question_requests
    ADD CONSTRAINT side_question_requests_session_key_generation_client_id_key UNIQUE (session_key, generation, client_id);

ALTER TABLE ONLY public.side_question_sessions
    ADD CONSTRAINT side_question_sessions_pkey PRIMARY KEY (session_key);

ALTER TABLE ONLY public.skill_usage
    ADD CONSTRAINT skill_usage_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_archives
    ADD CONSTRAINT task_archives_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_archives
    ADD CONSTRAINT task_archives_task_id_key UNIQUE (task_id);

ALTER TABLE ONLY public.task_asset_links
    ADD CONSTRAINT task_asset_links_pkey PRIMARY KEY (task_id, asset_id);

ALTER TABLE ONLY public.task_categories
    ADD CONSTRAINT task_categories_nkey_key UNIQUE (nkey);

ALTER TABLE ONLY public.task_categories
    ADD CONSTRAINT task_categories_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_constraints
    ADD CONSTRAINT task_constraints_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_intelligence
    ADD CONSTRAINT task_intelligence_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_intelligence
    ADD CONSTRAINT task_intelligence_task_id_kind_fingerprint_key UNIQUE (task_id, kind, fingerprint);

ALTER TABLE ONLY public.task_intercept_rules
    ADD CONSTRAINT task_intercept_rules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_llm_profiles
    ADD CONSTRAINT task_llm_profiles_pkey PRIMARY KEY (task_id, profile_id);

ALTER TABLE ONLY public.task_llm_profiles
    ADD CONSTRAINT task_llm_profiles_task_id_position_key UNIQUE (task_id, "position");

ALTER TABLE ONLY public.task_relations
    ADD CONSTRAINT task_relations_pkey PRIMARY KEY (task_id, source_task_id);

ALTER TABLE ONLY public.task_schedule_history
    ADD CONSTRAINT task_schedule_history_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_schedule_tasks
    ADD CONSTRAINT task_schedule_tasks_pkey PRIMARY KEY (schedule_id, task_id);

ALTER TABLE ONLY public.task_schedules
    ADD CONSTRAINT task_schedules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_scope
    ADD CONSTRAINT task_scope_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_templates
    ADD CONSTRAINT task_templates_nkey_key UNIQUE (nkey);

ALTER TABLE ONLY public.task_templates
    ADD CONSTRAINT task_templates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT tasks_exploration_id_key UNIQUE (exploration_id);

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT tasks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.tool_usage
    ADD CONSTRAINT tool_usage_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.tools
    ADD CONSTRAINT tools_pkey PRIMARY KEY (key);

ALTER TABLE ONLY public.traffic_evidence_snapshots
    ADD CONSTRAINT traffic_evidence_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.company_scope
    ADD CONSTRAINT uq_sv2_domain UNIQUE (company_id, domain);

ALTER TABLE ONLY public.company_scope
    ADD CONSTRAINT uq_sv2_net UNIQUE (company_id, net);

CREATE INDEX idx_act_latest ON public.activity USING btree (exploration_id, created_at DESC);

CREATE INDEX idx_act_main_seg ON public.activity USING btree (exploration_id, main_seg, id) WHERE (worker = 'mainagent'::text);

CREATE INDEX idx_act_node ON public.activity USING btree (exploration_id, node_id, id);

CREATE INDEX idx_act_result_usage ON public.activity USING btree (exploration_id) INCLUDE (input_tokens, output_tokens, cache_read_tokens, cache_write_tokens) WHERE (kind = 'result'::text);

CREATE INDEX idx_act_since ON public.activity USING btree (exploration_id, id);

CREATE INDEX idx_act_tool_call ON public.activity USING btree (exploration_id, tool_use_id, id) WHERE (kind = ANY (ARRAY['tool_use'::text, 'tool_result'::text]));

CREATE INDEX idx_act_worker ON public.activity USING btree (exploration_id, worker, id);

CREATE INDEX idx_agent_triggers_agent ON public.agent_triggers USING btree (agent_key);

CREATE INDEX idx_agents_llm_profile ON public.agents USING btree (llm_profile_id) WHERE (llm_profile_id IS NOT NULL);

CREATE INDEX idx_anchor_asset ON public.exploration_anchors USING btree (asset_id);

CREATE INDEX idx_askv_skill ON public.agent_skill_visibility USING btree (skill_name);

CREATE INDEX idx_asset_intercept_enabled ON public.asset_intercept_rules USING btree (enabled);

CREATE INDEX idx_av2_bound_domains ON public.assets USING gin (bound_domains) WHERE (type = 'ip'::text);

CREATE INDEX idx_av2_c_segment ON public.assets USING gist (c_segment inet_ops) WHERE (c_segment IS NOT NULL);

CREATE INDEX idx_av2_company ON public.assets USING btree (company_id) WHERE (company_id IS NOT NULL);

CREATE INDEX idx_av2_company_type ON public.assets USING btree (company_id, type) WHERE (company_id IS NOT NULL);

CREATE INDEX idx_av2_domain ON public.assets USING btree (domain) WHERE (domain IS NOT NULL);

CREATE INDEX idx_av2_ip ON public.assets USING btree (ip) WHERE (ip IS NOT NULL);

CREATE INDEX idx_av2_last_seen ON public.assets USING btree (last_seen DESC);

CREATE INDEX idx_av2_open_ports ON public.assets USING gin (open_ports) WHERE (type = 'ip'::text);

CREATE INDEX idx_av2_root_domain ON public.assets USING btree (root_domain) WHERE (root_domain IS NOT NULL);


CREATE INDEX idx_av2_technologies ON public.assets USING gin (technologies) WHERE (type = 'service'::text);

CREATE INDEX idx_av2_type_seen ON public.assets USING btree (type, last_seen DESC);


CREATE INDEX idx_conv_act ON public.conversation_activities USING btree (conversation_id, id);

CREATE INDEX idx_conv_act_tool_call ON public.conversation_activities USING btree (conversation_id, tool_use_id, id) WHERE (kind = ANY (ARRAY['tool_use'::text, 'tool_result'::text]));

CREATE INDEX idx_conversations_llm_profile ON public.conversations USING btree (llm_profile_id) WHERE (llm_profile_id IS NOT NULL);

CREATE INDEX idx_conversations_pinned ON public.conversations USING btree (pinned_at DESC) WHERE (pinned_at IS NOT NULL);

CREATE INDEX idx_expedges_dst ON public.exploration_edges USING btree (dst_id, rel);

CREATE INDEX idx_expedges_src ON public.exploration_edges USING btree (src_id, rel);

CREATE INDEX idx_expnodes_frontier ON public.exploration_nodes USING btree (exploration_id, priority DESC) WHERE ((kind = 'intent'::text) AND (state = 'open'::text));

CREATE INDEX idx_expnodes_part ON public.exploration_nodes USING btree (exploration_id, kind);

CREATE INDEX idx_finding_case_members_case ON public.finding_case_members USING btree (case_id);

CREATE INDEX idx_finding_case_reviews_active_task ON public.finding_case_review_runs USING btree (task_id) WHERE (state = ANY (ARRAY['queued'::text, 'running'::text]));

CREATE INDEX idx_finding_case_suggestion_members_finding ON public.finding_case_suggestion_members USING btree (finding_id);

CREATE UNIQUE INDEX idx_finding_retests_active ON public.finding_retests USING btree (finding_id) WHERE (status = ANY (ARRAY['pending'::text, 'running'::text]));

CREATE INDEX idx_finding_retests_history ON public.finding_retests USING btree (finding_id, id DESC);

CREATE INDEX idx_finding_traffic_order ON public.finding_traffic_bindings USING btree (finding_id, "position", id);

CREATE INDEX idx_finding_traffic_snapshot ON public.finding_traffic_bindings USING btree (snapshot_id);

CREATE INDEX idx_findings_asset_ids ON public.findings USING gin (asset_ids jsonb_path_ops);

CREATE INDEX idx_findings_status ON public.findings USING btree (status, created_at DESC);

CREATE INDEX idx_findings_task ON public.findings USING btree (task_id, created_at DESC);

CREATE INDEX idx_findings_time ON public.findings USING btree (created_at DESC);

CREATE INDEX idx_intercept_pending_created ON public.intercept_pending USING btree (created_at DESC);

CREATE INDEX idx_intercept_pending_status ON public.intercept_pending USING btree (status, created_at DESC);

CREATE INDEX idx_intercept_pending_task ON public.intercept_pending USING btree (task_id, created_at DESC);

CREATE INDEX idx_llm_records_session ON public.llm_records USING btree (session_id);

CREATE INDEX idx_llm_records_ts ON public.llm_records USING btree (ts);

CREATE INDEX idx_llm_usage_exp ON public.llm_usage USING btree (exploration_id);

CREATE INDEX idx_llm_usage_model ON public.llm_usage USING btree (task_id, model);

CREATE INDEX idx_llm_usage_task ON public.llm_usage USING btree (task_id);

CREATE INDEX idx_notification_deliveries_batch ON public.notification_deliveries USING btree (batch_id) WHERE (batch_id IS NOT NULL);

CREATE INDEX idx_notification_deliveries_channel ON public.notification_deliveries USING btree (channel_id, id DESC);

CREATE INDEX idx_notification_deliveries_due ON public.notification_deliveries USING btree (next_attempt_at) WHERE (state = 'pending'::text);

CREATE INDEX idx_notification_deliveries_history ON public.notification_deliveries USING btree (id DESC);

CREATE INDEX idx_notification_events_pending ON public.notification_events USING btree (id) WHERE (NOT fanned_out);

CREATE INDEX idx_schedule_history ON public.task_schedule_history USING btree (schedule_id, id DESC);

CREATE INDEX idx_server_logs_id ON public.server_logs USING btree (id DESC);

CREATE UNIQUE INDEX idx_side_request_running ON public.side_question_requests USING btree (session_key) WHERE (status = 'running'::text);

CREATE INDEX idx_side_requests_history ON public.side_question_requests USING btree (session_key, ordinal DESC);

CREATE INDEX idx_side_sessions_approval ON public.side_question_sessions USING btree (approval_id);

CREATE INDEX idx_side_sessions_conv ON public.side_question_sessions USING btree (conversation_id);

CREATE INDEX idx_side_sessions_exp ON public.side_question_sessions USING btree (exploration_id);

CREATE INDEX idx_side_sessions_intent ON public.side_question_sessions USING btree (intent_id);

CREATE INDEX idx_side_sessions_task ON public.side_question_sessions USING btree (task_id);

CREATE INDEX idx_skill_usage_skill ON public.skill_usage USING btree (skill, ts DESC);

CREATE INDEX idx_skill_usage_task ON public.skill_usage USING btree (task_id);

CREATE INDEX idx_sv2_company ON public.company_scope USING btree (company_id);

CREATE INDEX idx_sv2_domain ON public.company_scope USING btree (domain) WHERE (kind = 'domain'::text);

CREATE INDEX idx_sv2_icp ON public.company_scope USING btree (value) WHERE (kind = 'icp'::text);

CREATE INDEX idx_sv2_net ON public.company_scope USING gist (net inet_ops) WHERE (kind = ANY (ARRAY['ip'::text, 'cidr'::text]));

CREATE INDEX idx_task_archives_archived ON public.task_archives USING btree (archived_at DESC, id DESC);

CREATE INDEX idx_task_archives_sources ON public.task_archives USING gin (source_task_ids);

CREATE INDEX idx_task_archives_state ON public.task_archives USING btree (state, requested_at, id);

CREATE INDEX idx_task_asset_links_asset ON public.task_asset_links USING btree (asset_id, task_id);

CREATE INDEX idx_task_asset_links_node ON public.task_asset_links USING btree (source_node_id) WHERE (source_node_id IS NOT NULL);

CREATE INDEX idx_task_categories_name ON public.task_categories USING btree (name, id);

CREATE INDEX idx_task_constraints_exp ON public.task_constraints USING btree (exploration_id);

CREATE INDEX idx_task_intelligence_assets ON public.task_intelligence USING gin (asset_ids);

CREATE INDEX idx_task_intelligence_task ON public.task_intelligence USING btree (task_id, id DESC);

CREATE INDEX idx_task_intercept_task ON public.task_intercept_rules USING btree (task_id);


CREATE INDEX idx_task_llm_profiles_profile ON public.task_llm_profiles USING btree (profile_id, task_id);

CREATE INDEX idx_task_relations_source ON public.task_relations USING btree (source_task_id);

CREATE INDEX idx_task_schedule_tasks_task ON public.task_schedule_tasks USING btree (task_id);

CREATE INDEX idx_task_schedules_enabled ON public.task_schedules USING btree (enabled, id);

CREATE INDEX idx_task_templates_updated ON public.task_templates USING btree (updated_at DESC, id DESC);

CREATE INDEX idx_tasks_active_llm_profile ON public.tasks USING btree (active_llm_profile_id) WHERE (active_llm_profile_id IS NOT NULL);

CREATE INDEX idx_tasks_alive ON public.tasks USING btree (created_at DESC) WHERE (deleted_at IS NULL);

CREATE INDEX idx_tasks_archived ON public.tasks USING btree (archived_at DESC) WHERE (archived_at IS NOT NULL);

CREATE INDEX idx_tasks_category ON public.tasks USING btree (category_id, created_at DESC) WHERE ((deleted_at IS NULL) AND (category_id IS NOT NULL));


CREATE INDEX idx_tasks_pinned ON public.tasks USING btree (pinned_at DESC) WHERE ((deleted_at IS NULL) AND (pinned_at IS NOT NULL));

CREATE INDEX idx_tasks_status ON public.tasks USING btree (status) WHERE (deleted_at IS NULL);

CREATE INDEX idx_tool_usage_task ON public.tool_usage USING btree (task_id);

CREATE INDEX idx_tool_usage_tool ON public.tool_usage USING btree (tool_key, ts DESC);

CREATE INDEX idx_ts_company ON public.task_scope USING btree (company_id) WHERE (kind = 'company'::text);

CREATE INDEX idx_ts_domain ON public.task_scope USING btree (domain) WHERE (kind = ANY (ARRAY['root_domain'::text, 'subdomain'::text]));

CREATE INDEX idx_ts_net ON public.task_scope USING gist (net inet_ops) WHERE (kind = ANY (ARRAY['ip'::text, 'cidr'::text]));

CREATE INDEX idx_vis_resource ON public.agent_visibility USING btree (resource_kind, resource_id);

CREATE UNIQUE INDEX uq_av2_app_bundle ON public.assets USING btree (bundle_id) WHERE ((type = 'app'::text) AND (bundle_id IS NOT NULL));

CREATE UNIQUE INDEX uq_av2_app_name ON public.assets USING btree (app_name) WHERE ((type = 'app'::text) AND (bundle_id IS NULL));

CREATE UNIQUE INDEX uq_av2_endpoint ON public.assets USING btree (url, method) WHERE (type = 'endpoint'::text);

CREATE UNIQUE INDEX uq_av2_ip ON public.assets USING btree (ip) WHERE (type = 'ip'::text);

CREATE UNIQUE INDEX uq_av2_root_domain ON public.assets USING btree (domain) WHERE (type = 'root_domain'::text);

CREATE UNIQUE INDEX uq_av2_service_http ON public.assets USING btree (url) WHERE ((type = 'service'::text) AND (service_type = 'http'::text));

CREATE UNIQUE INDEX uq_av2_service_other ON public.assets USING btree (COALESCE(domain, ''::text), COALESCE(ip, ''::text), port, service_name) WHERE ((type = 'service'::text) AND (service_type = 'other'::text));

CREATE UNIQUE INDEX uq_av2_subdomain ON public.assets USING btree (domain, COALESCE(record_type, ''::text)) WHERE (type = 'subdomain'::text);

CREATE UNIQUE INDEX uq_llm_one_default ON public.llm_profiles USING btree (is_default) WHERE is_default;

CREATE UNIQUE INDEX uq_sv2_value ON public.company_scope USING btree (company_id, kind, value) WHERE (kind = ANY (ARRAY['icp'::text, 'keyword'::text]));

CREATE UNIQUE INDEX uq_task_scope_v2 ON public.task_scope USING btree (task_id, kind, COALESCE(domain, ''::text), COALESCE((net)::text, ''::text), COALESCE(company_id, (0)::bigint), COALESCE(value, ''::text));

CREATE TRIGGER finding_case_content_changed AFTER UPDATE ON public.findings FOR EACH ROW EXECUTE FUNCTION public.finding_case_changed();

CREATE TRIGGER finding_case_member_deleted AFTER DELETE ON public.finding_case_members FOR EACH ROW EXECUTE FUNCTION public.finding_case_changed();

CREATE TRIGGER finding_case_suggestion_reanchor BEFORE DELETE ON public.findings FOR EACH ROW EXECUTE FUNCTION public.finding_case_suggestion_reanchor();

CREATE TRIGGER trg_agent_triggers_upd BEFORE UPDATE ON public.agent_triggers FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_agents_upd BEFORE UPDATE ON public.agents FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_asset_intercept_rules_upd BEFORE UPDATE ON public.asset_intercept_rules FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();


CREATE TRIGGER trg_av2_upd BEFORE UPDATE ON public.assets FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_companies_upd BEFORE UPDATE ON public.companies FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_conversation_retest_delete BEFORE DELETE ON public.conversations FOR EACH ROW EXECUTE FUNCTION public.stop_deleted_conversation_retest();

CREATE TRIGGER trg_conversations_upd BEFORE UPDATE ON public.conversations FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_exp_upd BEFORE UPDATE ON public.explorations FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_expnodes_upd BEFORE UPDATE ON public.exploration_nodes FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_intercept_rules_upd BEFORE UPDATE ON public.intercept_rules FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_llm_upd BEFORE UPDATE ON public.llm_profiles FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_mcp_upd BEFORE UPDATE ON public.mcp_servers FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_notification_channels_upd BEFORE UPDATE ON public.notification_channels FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_task_archives_upd BEFORE UPDATE ON public.task_archives FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_task_asset_links_upd BEFORE UPDATE ON public.task_asset_links FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_task_categories_upd BEFORE UPDATE ON public.task_categories FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_task_intercept_rules_upd BEFORE UPDATE ON public.task_intercept_rules FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_task_llm_profiles_upd BEFORE UPDATE ON public.task_llm_profiles FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_task_schedules_upd BEFORE UPDATE ON public.task_schedules FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_task_templates_upd BEFORE UPDATE ON public.task_templates FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_tasks_upd BEFORE UPDATE ON public.tasks FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

CREATE TRIGGER trg_tools_upd BEFORE UPDATE ON public.tools FOR EACH ROW EXECUTE FUNCTION public.set_updated_at();

ALTER TABLE ONLY public.activity
    ADD CONSTRAINT activity_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES public.explorations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.activity
    ADD CONSTRAINT activity_node_id_fkey FOREIGN KEY (node_id) REFERENCES public.exploration_nodes(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_prompt_vars
    ADD CONSTRAINT agent_prompt_vars_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_prompts
    ADD CONSTRAINT agent_prompts_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_skill_visibility
    ADD CONSTRAINT agent_skill_visibility_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_visibility
    ADD CONSTRAINT agent_visibility_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT agents_llm_profile_id_fkey FOREIGN KEY (llm_profile_id) REFERENCES public.llm_profiles(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.assets
    ADD CONSTRAINT assets_company_id_fkey FOREIGN KEY (company_id) REFERENCES public.companies(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.company_scope
    ADD CONSTRAINT company_scope_company_id_fkey FOREIGN KEY (company_id) REFERENCES public.companies(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.conversation_activities
    ADD CONSTRAINT conversation_activities_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.conversations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.conversations
    ADD CONSTRAINT conversations_llm_profile_id_fkey FOREIGN KEY (llm_profile_id) REFERENCES public.llm_profiles(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.exploration_anchors
    ADD CONSTRAINT exploration_anchors_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.assets(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.exploration_anchors
    ADD CONSTRAINT exploration_anchors_node_id_fkey FOREIGN KEY (node_id) REFERENCES public.exploration_nodes(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.exploration_edges
    ADD CONSTRAINT exploration_edges_dst_id_fkey FOREIGN KEY (dst_id) REFERENCES public.exploration_nodes(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.exploration_edges
    ADD CONSTRAINT exploration_edges_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES public.explorations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.exploration_edges
    ADD CONSTRAINT exploration_edges_src_id_fkey FOREIGN KEY (src_id) REFERENCES public.exploration_nodes(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.exploration_nodes
    ADD CONSTRAINT exploration_nodes_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES public.explorations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_case_blocks
    ADD CONSTRAINT finding_case_blocks_left_id_fkey FOREIGN KEY (left_id) REFERENCES public.findings(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_case_blocks
    ADD CONSTRAINT finding_case_blocks_right_id_fkey FOREIGN KEY (right_id) REFERENCES public.findings(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_case_events
    ADD CONSTRAINT finding_case_events_case_id_fkey FOREIGN KEY (case_id) REFERENCES public.finding_cases(id);

ALTER TABLE ONLY public.finding_case_members
    ADD CONSTRAINT finding_case_members_case_id_fkey FOREIGN KEY (case_id) REFERENCES public.finding_cases(id);

ALTER TABLE ONLY public.finding_case_members
    ADD CONSTRAINT finding_case_members_finding_id_fkey FOREIGN KEY (finding_id) REFERENCES public.findings(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_case_review_runs
    ADD CONSTRAINT finding_case_review_runs_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.conversations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_case_review_runs
    ADD CONSTRAINT finding_case_review_runs_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.finding_case_suggestion_members
    ADD CONSTRAINT finding_case_suggestion_members_finding_id_fkey FOREIGN KEY (finding_id) REFERENCES public.findings(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_case_suggestion_members
    ADD CONSTRAINT finding_case_suggestion_members_suggestion_id_fkey FOREIGN KEY (suggestion_id) REFERENCES public.finding_case_suggestions(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_case_suggestions
    ADD CONSTRAINT finding_case_suggestions_left_id_fkey FOREIGN KEY (left_id) REFERENCES public.findings(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_case_suggestions
    ADD CONSTRAINT finding_case_suggestions_right_id_fkey FOREIGN KEY (right_id) REFERENCES public.findings(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_case_suggestions
    ADD CONSTRAINT finding_case_suggestions_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_cases
    ADD CONSTRAINT finding_cases_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.finding_retests
    ADD CONSTRAINT finding_retests_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.conversations(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.finding_retests
    ADD CONSTRAINT finding_retests_finding_id_fkey FOREIGN KEY (finding_id) REFERENCES public.findings(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_traffic_bindings
    ADD CONSTRAINT finding_traffic_bindings_finding_id_fkey FOREIGN KEY (finding_id) REFERENCES public.findings(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.finding_traffic_bindings
    ADD CONSTRAINT finding_traffic_bindings_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES public.traffic_evidence_snapshots(id);

ALTER TABLE ONLY public.findings
    ADD CONSTRAINT findings_node_id_fkey FOREIGN KEY (node_id) REFERENCES public.exploration_nodes(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.findings
    ADD CONSTRAINT findings_report_conversation_id_fkey FOREIGN KEY (report_conversation_id) REFERENCES public.conversations(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.findings
    ADD CONSTRAINT findings_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT fk_agents_curprompt FOREIGN KEY (current_prompt_id) REFERENCES public.agent_prompts(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.intercept_pending
    ADD CONSTRAINT intercept_pending_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.conversations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.intercept_pending
    ADD CONSTRAINT intercept_pending_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES public.intercept_rules(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.llm_profile_health
    ADD CONSTRAINT llm_profile_health_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES public.llm_profiles(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.main_sessions
    ADD CONSTRAINT main_sessions_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES public.explorations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.mcp_tools_cache
    ADD CONSTRAINT mcp_tools_cache_server_id_fkey FOREIGN KEY (server_id) REFERENCES public.mcp_servers(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.notification_channels(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_event_id_fkey FOREIGN KEY (event_id) REFERENCES public.notification_events(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.side_question_requests
    ADD CONSTRAINT side_question_requests_session_key_fkey FOREIGN KEY (session_key) REFERENCES public.side_question_sessions(session_key) ON DELETE CASCADE;

ALTER TABLE ONLY public.side_question_sessions
    ADD CONSTRAINT side_question_sessions_approval_id_fkey FOREIGN KEY (approval_id) REFERENCES public.intercept_pending(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.side_question_sessions
    ADD CONSTRAINT side_question_sessions_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.conversations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.side_question_sessions
    ADD CONSTRAINT side_question_sessions_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES public.explorations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.side_question_sessions
    ADD CONSTRAINT side_question_sessions_intent_id_fkey FOREIGN KEY (intent_id) REFERENCES public.exploration_nodes(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.side_question_sessions
    ADD CONSTRAINT side_question_sessions_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_archives
    ADD CONSTRAINT task_archives_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_asset_links
    ADD CONSTRAINT task_asset_links_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.assets(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_asset_links
    ADD CONSTRAINT task_asset_links_source_node_id_fkey FOREIGN KEY (source_node_id) REFERENCES public.exploration_nodes(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.task_asset_links
    ADD CONSTRAINT task_asset_links_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_constraints
    ADD CONSTRAINT task_constraints_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES public.explorations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_intelligence
    ADD CONSTRAINT task_intelligence_node_id_fkey FOREIGN KEY (node_id) REFERENCES public.exploration_nodes(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.task_intelligence
    ADD CONSTRAINT task_intelligence_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_intercept_rules
    ADD CONSTRAINT task_intercept_rules_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_llm_profiles
    ADD CONSTRAINT task_llm_profiles_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES public.llm_profiles(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_llm_profiles
    ADD CONSTRAINT task_llm_profiles_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_relations
    ADD CONSTRAINT task_relations_source_task_id_fkey FOREIGN KEY (source_task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_relations
    ADD CONSTRAINT task_relations_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_schedule_history
    ADD CONSTRAINT task_schedule_history_schedule_id_fkey FOREIGN KEY (schedule_id) REFERENCES public.task_schedules(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_schedule_tasks
    ADD CONSTRAINT task_schedule_tasks_schedule_id_fkey FOREIGN KEY (schedule_id) REFERENCES public.task_schedules(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_schedule_tasks
    ADD CONSTRAINT task_schedule_tasks_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_scope
    ADD CONSTRAINT task_scope_company_id_fkey FOREIGN KEY (company_id) REFERENCES public.companies(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_scope
    ADD CONSTRAINT task_scope_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_templates
    ADD CONSTRAINT task_templates_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.task_categories(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT tasks_active_llm_profile_id_fkey FOREIGN KEY (active_llm_profile_id) REFERENCES public.llm_profiles(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT tasks_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.task_categories(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT tasks_company_id_fkey FOREIGN KEY (company_id) REFERENCES public.companies(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT tasks_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES public.explorations(id) ON DELETE RESTRICT;

-- =====================================================================
-- MCP server (ARTEX 作为 MCP 服务端，供外部智能体编排任务)
--   api_tokens       访问令牌：长期、可吊销、可限权，只存 SHA-256，明文仅创建时返回一次
--   api_token_tasks  令牌建过的任务，用于并发/每小时限额
--   mcp_audit_log    每次 tools/call 的审计记录
-- =====================================================================
CREATE TABLE IF NOT EXISTS api_tokens (
    id                 BIGSERIAL PRIMARY KEY,
    name               TEXT NOT NULL,
    -- SHA-256(明文) 的十六进制。令牌本身是 32 字节随机数，不需要 bcrypt 这类慢哈希。
    token_hash         TEXT NOT NULL UNIQUE,
    -- 明文前缀，仅用于在列表里辨认是哪一个令牌。
    token_prefix       TEXT NOT NULL,
    -- 权限范围：read / task:create / task:control，由 server 侧白名单校验。
    scopes             JSONB NOT NULL DEFAULT '[]',
    -- 限定企业：非空时只能在这些企业下建任务、也只能看到/控制这些企业下的任务。
    company_ids        JSONB NOT NULL DEFAULT '[]',
    -- 限额，0 = 不限。max_active_tasks 统计本令牌建的、尚未进入终态的任务(含暂停/排队)。
    max_active_tasks   INTEGER NOT NULL DEFAULT 0,
    max_tasks_per_hour INTEGER NOT NULL DEFAULT 0,
    expires_at         TIMESTAMPTZ,
    last_used_at       TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- task_id 不加外键：任务删除后这行仍要计入「每小时建任务数」，否则建了就删可绕过限额。
CREATE TABLE IF NOT EXISTS api_token_tasks (
    token_id   BIGINT NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
    task_id    BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (token_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_api_token_tasks_recent ON api_token_tasks(token_id, created_at DESC);

-- token_id 不加外键：令牌删除后审计仍要留存，token_name 冗余保存当时的名称。
CREATE TABLE IF NOT EXISTS mcp_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    token_id    BIGINT NOT NULL,
    token_name  TEXT NOT NULL DEFAULT '',
    tool        TEXT NOT NULL,
    args        TEXT NOT NULL DEFAULT '',
    ok          BOOLEAN NOT NULL,
    result      TEXT NOT NULL DEFAULT '',
    remote_addr TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
