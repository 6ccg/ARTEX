package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpool"
	"github.com/Autumn-27/norma/llm"
)

// Runtime owns application state and background producers. It has no HTTP handler dependency.
type Runtime struct {
	host *agent.Host

	scheduleMu sync.Mutex
	m          *Manager
	engine     *Engine
	ctx        context.Context
	dataDir    string

	skillDir string // root directory for skill subdirectories

	basicAuth basicAuthGate

	cfgMu     sync.Mutex
	chatAgent *agent.ChatAgent // conversational runner for the chat page; nil w/o LLM
	// llmProv is the fully-decorated global provider (recorder + failover chain)
	// installed by applyLLM. Task routers use it after an explicit chain is cleared.
	llmProv   llm.Provider
	llmDirect llm.Provider // concrete global provider, before any failover pool
	llmCfg    agent.Config // current LLM config (key not exposed)
	llmOn     bool
	llmProf   string // active LLM profile name (for llmrec tagging)

	// chatBusy guards the per-task main-agent run: the chat handler launches the
	// agent on the server's background ctx (not the request ctx) and returns
	// immediately, so a page reload / proxy timeout can't abort a live run. This
	// map serializes turns per task — one main-agent run at a time per task, so
	// concurrent messages don't corrupt the shared exp<id>-main transcript.
	chatMu   sync.Mutex
	chatBusy map[string]bool
	// chatCancel holds the cancel func for each in-flight conversation run (keyed by
	// convBusyKey), so a manual stop can abort JUST that session's agent run. Set/
	// cleared alongside chatBusy under chatMu. Aborting a run does NOT touch the P3
	// trigger queue — the drain goroutine simply proceeds to the next queued fire.
	chatCancel map[string]context.CancelCauseFunc

	// triggerQ buffers P3 trigger fires PER AGENT. A per-agent "pump" launches runs up
	// to a concurrency limit derived from the agent's策略: serial → limit 1 (+ optional
	// merge); parallel → limit = trigger_max_parallel (0=∞), no merge. triggerActive
	// counts in-flight runs per agent (replaces a boolean drain flag); a run's
	// completion decrements it and re-pumps to fill the freed slot. triggerCfg caches
	// the agent's last-read策略 so the pump never queries the DB while holding queueMu.
	// Distinct agents always run concurrently. Queue is in-memory (matches chatBusy); a
	// restart drops pending fires — the scheduler re-fires from watermarks next tick.
	queueMu       sync.Mutex
	triggerQ      map[string][]triggeredRun
	triggerActive map[string]int
	triggerCfg    map[string]triggerBehavior

	// profChatAgents caches a per-profile ChatAgent (chat page), keyed by profile id.
	// Built lazily on first use; invalidated when any profile is saved/activated/deleted
	// so edits take effect.
	profMu         sync.Mutex
	profChatAgents map[int64]*agent.ChatAgent // per-profile ChatAgent cache (chat page)

	// provByProfile caches ONE provider per LLM profile id so every agent bound or
	// pinned to the same profile shares a single provider instance — hence one rate
	// limiter. applyLLM also resolves the persisted global-active profile through this
	// cache, so global fallback and task chains do not accidentally double the
	// configured request rate. Cleared on profile edits, then repopulated by active reapply.
	provCacheMu   sync.Mutex
	provByProfile map[int64]*provEntry
	provCacheGen  uint64
	// Quota histories survive provider/agent cache invalidation. Key 0 belongs
	// to the non-persisted global config; persisted profiles use their DB IDs.
	requestLimiters map[int64]*agent.RequestLimiter

	// llmHealth is the process-wide circuit-breaker state for LLM failover (轮询).
	// It deliberately lives OUTSIDE the provider caches: rebuilding the chain
	// (saving an unrelated profile, flipping a setting) must not erase what we
	// learned about which backends are out of credit / rate-limited.
	llmHealth *llmpool.Registry

	// Explicit task chains use one task-scoped dynamic provider shared by goals,
	// planner, workers, and the main agent. Bundles are stable; their runtime reads
	// the persisted current profile at every new LLM call.
	taskAgentMu sync.Mutex
	taskAgents  map[string]*taskAgentBundle

	// Cold task archives run through one persistent FIFO worker. The buffered wake
	// channel coalesces enqueue bursts; the database remains the source of truth.
	archiveWake       chan struct{}
	archiveWG         sync.WaitGroup
	backgroundWG      sync.WaitGroup
	lifecycleMu       sync.Mutex
	closing           bool
	started           bool // protected by lifecycleMu
	cancel            context.CancelFunc
	closeOnce         sync.Once
	stopLogWriter     func()
	credentialWake    chan struct{}
	proxyReconnect    proxyReconnectSupervisor
	proxyPoolWake     chan struct{}
	proxyPoolWG       sync.WaitGroup
	proxyPoolProbeSem chan struct{}
	proxyPoolTickMu   sync.Mutex
	// proxyProbe is injectable for deterministic reconnect race tests. Production
	// servers leave it nil and use probeEgress directly.
	proxyProbe func(context.Context, string, string, []string, time.Duration) egressProbeResult
	side       *sideQuestionState
}

// provEntry is a cached provider + its config for one LLM profile id.
type provEntry struct {
	prov llm.Provider
	cfg  agent.Config
	// jev is retained in the cache entry so a cached chat provider can be
	// returned without consulting the DB on every hot-path resolution.
	jev bool
}

// triggeredRun is one queued P3 trigger fire awaiting its turn for an agent.
// taskID + mergeable let the drainer coalesce several event triggers (finding/goal)
// from the SAME task into one conversation before it starts (interval fires don't merge).
type triggeredRun struct {
	conversationID int64 // pre-created manual finding review; never coalesced

	agentKey  string
	title     string
	message   string // 事件正文(触发语 + 工具/入参/返回等);不含任务描述/目标头
	taskID    int64  // source task for finding/goal triggers; 0 for interval/none
	taskDesc  string // 任务描述(任务级,同任务相同);合并时只渲染一次
	taskGoal  string // 任务目标(任务级,同任务相同);合并时只渲染一次
	mergeable bool   // true for finding/goal event triggers (merge by taskID)
}

// NewRuntime prepares instance dependencies; Start activates background work.
func NewRuntime(ctx context.Context, m *Manager, skillDir string, dataDir string) (*Runtime, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &Runtime{host: &agent.Host{}, m: m, engine: NewEngine(m), ctx: ctx, skillDir: skillDir, dataDir: dataDir, chatBusy: map[string]bool{},
		chatCancel: map[string]context.CancelCauseFunc{}, triggerQ: map[string][]triggeredRun{},
		triggerActive: map[string]int{}, triggerCfg: map[string]triggerBehavior{}, proxyPoolWake: make(chan struct{}, 1), proxyPoolProbeSem: make(chan struct{}, proxyPoolMaxProbeConcurrency),
		profChatAgents: map[int64]*agent.ChatAgent{},
		provByProfile:  map[int64]*provEntry{}, llmHealth: newLLMHealthRegistry(m.pg),
		taskAgents: map[string]*taskAgentBundle{}, archiveWake: make(chan struct{}, 1), credentialWake: make(chan struct{}, 1)}
	s.cancel = cancel

	s.initSideQuestions()
	if m.pg != nil {
		if err := s.basicAuth.load(m.pg); err != nil {
			cancel()
			return nil, fmt.Errorf("load HTTP Basic Auth: %w", err)
		}
	}
	// 熔断阈值/冷却是失败路径上的热参数，启动时把全局重试策略推给 Registry 一次；
	// 之后每次保存策略再推一次（saveLLMRetryPolicy）。
	s.applyRetryPolicy()
	// Every task uses a stable task router. An empty explicit chain is resolved by
	// that router through Agent bindings and then the global provider, so adding a
	// first chain to a running task takes effect on its very next LLM call.
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		if !s.taskRuntimeAvailable(t, "planner", "worker") {
			return nil, nil
		}
		b := s.agentsForTask(t)
		return b.pl, b.wk
	})
	s.engine.SetWorkerResolver(func(t *Task, slot WorkerSlot) (*agent.Worker, error) {
		return s.workerForTaskSlot(t, slot)
	})
	// Global readiness (Ready()/llm_configured): a global active LLM provider is
	// installed. Task-level runnability is separate (ReadyFor → the resolver above).
	s.engine.SetReadiness(func() bool {
		s.cfgMu.Lock()
		defer s.cfgMu.Unlock()
		return s.llmOn
	})
	s.engine.SetProxyFailureHandler(s.handleProxyFailure)
	if m.traffic != nil {
		m.traffic.SetUpstreamFailureHandler(func(err error) {
			if !s.beginExternalWork() {
				return
			}
			defer s.backgroundWG.Done()
			s.handleProxyTrafficFailure(err)
		})
	}
	// Wire DB-stored prompt templates into the agents (新版方案 §3.3 / §5a). With no
	// override row, agents keep their built-in defaults — behavior is unchanged.
	if m.pg != nil {
		s.host.OpsecEnabled = func() bool { return m.pg.GetBool(settingOpsecEnabled, false) }
		s.host.PromptOverride = func(key string) (string, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil {
				return "", false
			}
			t, err := m.pg.CurrentPrompt(a.ID)
			if err != nil || t == "" {
				return "", false
			}
			if key == "reporter" && m.pg.GetBool(settingBrowserScreenshot, false) {
				t += `

【报告步骤截图已开启】
				报告撰写时，针对每个复现步骤中真实出现且仍可访问的 HTTP 页面，使用 browser MCP 的 browser_navigate 和 browser_take_screenshot 截取页面证据。只导航到漏洞证据中出现的目标 URL，不要自行扩大测试范围。将截图保存到当前工作目录下的 findings/screenshots/ 目录，文件名使用 finding-<finding_id>-step-<n>.png；确认文件存在后，在对应步骤中**统一使用工作区相对路径**加入 Markdown 图片：![步骤 <n> 截图](findings/screenshots/finding-<finding_id>-step-<n>.png)。禁止使用绝对路径、workspace://、file://，也不要混用相对路径和绝对路径；报告中所有截图都必须使用这一种格式。截图失败时如实写明，不要用占位图或把截图描述冒充图片。`
			}
			return t, true
		}
		// Wire DB-stored wrap-up (settlement) prompts. Empty column → built-in default.
		s.host.WrapupOverride = func(key string) (string, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil || a.WrapupPrompt == "" {
				return "", false
			}
			return a.WrapupPrompt, true
		}
		// Wire DB-stored wrap-up turn budgets. 0 / missing → built-in per-agent default.
		s.host.WrapupMaxTurnsOverride = func(key string) (int, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil || a.WrapupMaxTurns <= 0 {
				return 0, false
			}
			return a.WrapupMaxTurns, true
		}
		// Wire DB-stored TASK-TIMEOUT wrap-up prompt/turns (worker/planner). Empty/0 → default.
		s.host.WrapupTaskTimeoutOverride = func(key string) (string, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil || a.TaskTimeoutWrapupPrompt == "" {
				return "", false
			}
			return a.TaskTimeoutWrapupPrompt, true
		}
		s.host.WrapupTaskTimeoutTurnsOverride = func(key string) (int, bool) {
			a, err := m.pg.GetAgentByKey(key)
			if err != nil || a == nil || a.TaskTimeoutWrapupMaxTurns <= 0 {
				return 0, false
			}
			return a.TaskTimeoutWrapupMaxTurns, true
		}
		wireAgentAugment(s.host, m.pg, s.skillDir, s.hostTools) // 可见 skills/MCP + 流量/编排 host 工具装配进 agent 工具集
		domainReg := buildDomainReg(s.host, m.Assets())
		wireTools(s.host, m.pg, domainReg) // 内置工具表：按 agent 过滤 + 覆盖描述/schema + 注入默认值
		seedPrompts(m.pg)                  // 内置 agent 默认提示词正文播种进 agent_prompts(仅空时)
		s.seedOrchestrationTools()         // P2 跨任务编排工具 seed 进 tools 表(可按 agent 绑定)
		if err := s.seedFindingRetester(); err != nil {
			log.Printf("[retester] seed: %v", err)
		}
		s.seedPythonInterpreter() // 自定义脚本工具:开机检测 python 解释器入库(仅空时)

	}
	// precedence: persisted DB config > env.
	if cfg, ok := s.loadLLMConfig(); ok {
		if err := s.applyLLM(cfg); err != nil {
			log.Printf("[engine] saved LLM config init failed — engine idle: %v", err)
		} else {
			log.Printf("[engine] LLM configured from DB: %s / %s", cfg.Provider(), cfg.Model)
		}
	} else if cfg, ok := agent.FromEnv(); ok {
		if err := s.applyLLM(cfg); err != nil {
			log.Printf("[engine] env provider init failed — engine idle: %v", err)
		} else {
			log.Printf("[engine] LLM configured from env: %s / %s", cfg.Provider(), cfg.Model)
		}
	} else {
		log.Printf("[engine] no LLM provider configured — engine idle until set via /api/llm or env")
	}
	if m.pg != nil {
		s.host.CredentialRecorded = func() {
			select {
			case s.credentialWake <- struct{}{}:
			default:
			}
		}
		s.seedCredentialProcessor()
	}
	s.wireInterceptReviewer() // LLM 兜底审批:未命中拦截规则的命令交给模型判定
	return s, nil
}

func (s *Runtime) resolvedTaskStatus(t *Task) string {
	if t == nil {
		return "created"
	}
	lifecycle := t.lifecycleSnapshot()
	return s.resolvedTaskStatusFromSnapshot(t, lifecycle)
}

func (s *Runtime) resolvedTaskStatusFromSnapshot(t *Task, lifecycle taskLifecycleState) string {
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Queued:
		return "queued"
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case t.llmStateSnapshot().FailoverState == "chain_exhausted" && s.engine.Started(t.ID):
		// LLM readiness is an execution dependency, not lifecycle state. Keeping
		// an exhausted task running lets the user edit/reset its chain directly.
		return "running"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	default:
		return "created"
	}
}

func (s *Runtime) validateTaskProfileIDs(ids []int64) error {
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return fmt.Errorf("LLM 配置 id 无效或重复")
		}
		seen[id] = true
		if p, err := s.m.pg.ProfileByID(id); err == nil && p != nil && p.Format == "jev" {
			return db.ErrJevProfileJudgeOnly
		}
		if _, ok := s.loadProfileConfig(id); !ok {
			return fmt.Errorf("LLM 配置 #%d 不存在或未设置 API Key", id)
		}
	}
	return nil
}

// llmHost returns the host of the configured LLM endpoint (to keep it out of scope).
func (s *Runtime) llmHost() string {
	s.cfgMu.Lock()
	base := s.llmCfg.BaseURL
	s.cfgMu.Unlock()
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err == nil {
		return strings.ToLower(u.Hostname())
	}
	return ""
}

func (s *Runtime) seed(t *Task, text string) {
	scheme, host, port, ok := parseTarget(text)
	if !ok {
		log.Printf("[seed] task %s: 未能从 %q 解析出目标 host/IP，不创建站点（请手动配置 scope）", t.ID, text)
		return
	}
	// P0-1 guard: never treat the configured LLM gateway as a target.
	if gw := s.llmHost(); gw != "" && host == gw {
		log.Printf("[seed] task %s: 目标 %q 是 LLM 网关，拒绝作为渗透目标", t.ID, host)
		return
	}

	u := scheme + "://" + host
	if !(scheme == "https" && port == 443) && !(scheme == "http" && port == 80) {
		u += ":" + strconv.Itoa(port)
	}
	var rootID int64
	if as := s.m.Assets(); as != nil {
		taskID, _ := strconv.ParseInt(t.ID, 10, 64)
		if net.ParseIP(host) != nil {
			rootID, _ = as.UpsertIP(db.UpsertIPReq{IP: host, TaskID: taskID})
		} else if scheme == "https" || scheme == "http" {
			rootID, _ = as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: u, TaskID: taskID})
		} else {
			rootID, _ = as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: host, TaskID: taskID})
		}
		if rootID > 0 {
			_ = as.SetTaskAssetSource(taskID, rootID, "task", "由任务描述或目标初始化", nil)
		}
	}
	// anchor the seeded assets to this task's begin root as lineage/provenance
	// (the asset graph is global and shared; anchoring no longer gates reads).
	if rootID > 0 {
		if begin, _ := t.Store.OriginFactID(); begin > 0 {
			_ = t.Store.Anchor(begin, rootID)
		}
	}
	log.Printf("[seed] task %s: 目标站点 %s", t.ID, u)
	// 不在这里 Notify:首轮是否触发统一由 engine.Run 的 HasActiveIntent 决定(种子意图任务
	// 跳过首轮)。seed 早于 Run 执行,若在此 Notify 会 buffered 到通道、被 plannerLoop 启动时
	// 消费掉而绕过 Run 的门控 → 种子任务仍误触发首轮。
}

// seedFirstIntent writes ONE open intent (summary = 描述+目标) into the task's
// frontier at creation, so a worker can claim and run it immediately without first
// waiting a planner round. Mirrors a planner top-level intent: it links from the
// origin fact (RelDerivedFrom) so it still traces back to a fact node. Best-effort —
// a failure just falls back to the normal planner-driven flow.
func (s *Runtime) seedFirstIntent(t *Task) {
	summary := fmt.Sprintf("完成任务目标：%s（任务：%s）", t.Goal, t.Description)
	id, err := t.Store.AddIntent(map[string]any{"summary": summary}, 8, nil, "seed")
	if err != nil {
		log.Printf("[seed] task %s: 下发种子意图失败: %v", t.ID, err)
		return
	}
	if origin, _ := t.Store.OriginFactID(); origin > 0 {
		_ = t.Store.Link(origin, db.RelDerivedFrom, id)
	}
	log.Printf("[seed] task %s: 已下发种子意图 #%d", t.ID, id)
}

// resolveFindingAssets loads every asset anchored by the given findings, keyed by
// id, so each finding DTO can render its assets' labels.
func (s *Runtime) resolveFindingAssets(fs []*db.DBFinding) map[int64]*db.Asset {
	var ids []int64
	for _, f := range fs {
		ids = append(ids, f.AssetIDs...)
	}
	return s.resolveAssetIDs(ids)
}

// resolveAssetIDs de-dupes ids and loads their asset rows into an id→asset map.
func (s *Runtime) resolveAssetIDs(ids []int64) map[int64]*db.Asset {
	assets := map[int64]*db.Asset{}
	seen := map[int64]bool{}
	var uniq []int64
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return assets
	}
	list, err := s.m.pg.Assets().GetByIDs(uniq)
	if err != nil {
		log.Printf("[findings] resolve assets: %v", err)
	}
	for _, a := range list {
		assets[a.ID] = a
	}
	return assets
}

// nodeAnchoredAssets resolves the exploration_anchors of the given nodes into
// display-ready asset labels, keyed by node id. Anchors are provenance decoration
// for the 播报板 — a failure here must not cost the caller its page, so errors are
// logged and degrade to "no assets".
func (s *Runtime) nodeAnchoredAssets(t *Task, nodeIDs []int64) map[string][]FindingAssetDTO {
	out := map[string][]FindingAssetDTO{}
	anchors, err := t.Store.NodeAssets(nodeIDs)
	if err != nil {
		log.Printf("[broadcast] node assets: %v", err)
		return out
	}
	var flat []int64
	for _, ids := range anchors {
		flat = append(flat, ids...)
	}
	assets := s.resolveAssetIDs(flat)
	for nodeID, ids := range anchors {
		if dtos := findingAssetDTOs(ids, assets); len(dtos) > 0 {
			out[i64s(nodeID)] = dtos
		}
	}
	return out
}

func (s *Runtime) settingsPayload() map[string]any {
	on, backend, braveKey, tavilyKey, proxy := s.m.WebSearch()
	pyStored, _, _ := s.m.pg.GetSetting(settingPythonInterp)
	concOn, concLimit := s.m.ConcurrencyLimit()
	if concLimit == 0 {
		concLimit = defaultConcurrencyLimit // 关闭时也回显一个合理默认值给 UI
	}
	return map[string]any{
		"traffic_capture":                      s.m.TrafficEnabled(),
		"browser_screenshot_enabled":           s.m.pg.GetBool(settingBrowserScreenshot, false),
		"browser_screenshot_timestamp_enabled": s.m.pg.GetBool(settingScreenshotTimestamp, false),
		"opsec_enabled":                        s.m.pg.GetBool(settingOpsecEnabled, false),
		"agent_traffic_binding":                s.m.pg.GetBool(settingAgentTrafficBinding, false),
		"task_intelligence_enabled":            s.m.pg.GetBool(db.SettingTaskIntelligence, false),
		db.SettingTaskAutoTitle:                s.m.pg.GetBool(db.SettingTaskAutoTitle, false),
		db.SettingChatAutoTitle:                s.m.pg.GetBool(db.SettingChatAutoTitle, false),
		db.SettingCredentialAgent:              s.m.pg.GetBool(db.SettingCredentialAgent, false),
		db.SettingCredentialTraffic:            s.m.pg.GetBool(db.SettingCredentialAgent, false) && s.m.pg.GetBool(db.SettingCredentialTraffic, false),
		"llm_record":                           s.m.LLMRecordEnabled(),
		"web_search_enabled":                   on,
		"web_search_backend":                   backend,
		"brave_key_set":                        strings.TrimSpace(braveKey) != "",
		"tavily_key_set":                       strings.TrimSpace(tavilyKey) != "",
		"deepseek_search_key_set":              s.m.deepSeekSearchKey() != "",
		"web_search_proxy":                     maskSecrets(proxy),             // 独立出口代理(http/https/socks5)，空=直连
		"global_proxy":                         maskSecrets(s.m.GlobalProxy()), // 全局出口代理(http/https/socks5)，所有目标流量走它，空=直连
		"global_proxy_transparent":             s.m.TransparentProxy(),
		"egress_allow_ipv6":                    s.m.EgressAllowIPv6(),
		"llm_proxy":                            s.m.LLMProxy(),
		"proxy_pool":                           func() []ProxyPoolEntry { e, _, _ := s.m.ProxyPoolSnapshot(); return e }(),
		"proxy_pool_active_id":                 func() string { _, a, _ := s.m.ProxyPoolSnapshot(); return a }(),
		"proxy_pool_switch_log":                func() []ProxySwitchLog { _, _, l := s.m.ProxyPoolSnapshot(); return l }(),
		"proxy_rotate_timed_enabled":           s.m.pg.GetBool(settingProxyRotateTimed, false),
		"proxy_rotate_interval_seconds":        proxyIntSetting(s.m.pg, settingProxyRotateInterval, defaultProxyRotateInterval),
		"proxy_rotate_on_failure":              s.m.pg.GetBool(settingProxyRotateFailure, false),
		"proxy_rotate_strategy": func() string {
			v, ok, _ := s.m.pg.GetSetting(settingProxyRotateStrategy)
			if ok && (v == "random" || v == "sequential") {
				return v
			}
			return "sequential"
		}(),
		"proxy_health_fail_threshold":         proxyIntSetting(s.m.pg, settingProxyHealthThreshold, defaultProxyHealthThreshold),
		"proxy_health_cooldown_seconds":       proxyIntSetting(s.m.pg, settingProxyHealthCooldown, defaultProxyHealthCooldown),
		"proxy_health_check_interval_seconds": proxyIntSetting(s.m.pg, settingProxyHealthInterval, defaultProxyHealthInterval),
		"proxy_reconnect_max_attempts":        func() int { n, _ := s.proxyReconnectConfig(); return n }(),
		"proxy_reconnect_interval_seconds":    func() int { _, d := s.proxyReconnectConfig(); return int(d / time.Second) }(),
		"show_egress_ip":                      s.m.pg.GetBool(settingShowEgressIP, false),
		"egress_probe_url":                    s.m.EgressProbeURL(),
		"python_interpreter":                  strings.TrimSpace(pyStored), // 用户/自动设的值(空=用运行时检测)
		"workers":                             s.m.Workers(),               // 并发工作 agent 数(默认3)；对之后启动的任务生效
		"worker_pools":                        s.m.WorkerPools(),           // 非空时按池顺序绑定 worker 会话
		"task_concurrency_enabled":            concOn,                      // 任务并发上限开关(默认关)
		"task_concurrency_limit":              concLimit,                   // 同时运行任务上限(开启后默认5)
		// LLM 轮询(故障转移)。默认关；开启后走全局激活配置的 agent 在当前配置不可用时
		// 自动切到下一个配置。bind_fallback 仅在轮询开启时有意义(默认关)。
		"llm_pool_enabled":       s.m.LLMPoolEnabled(),
		"llm_pool_bind_fallback": s.m.LLMPoolBindFallback(),
		// 操作约束注入范围(默认都开):把本任务的 allow/deny 约束拼进对应 agent 的系统提示。
		"constraints_inject_planner": s.constraintInjectPlanner(),
		"constraints_inject_worker":  s.constraintInjectWorker(),
		// 实验功能:noa 模型驱动上下文压缩(默认关)。开启后平台接入的四类 agent 由 noa
		// 接管上下文压缩,取代内置 compaction;每 run 读一次,对之后启动的 run 生效。
		"noa_compaction": s.m.NoaCompactionEnabled(),
		// 漏洞 IM 推送的全局项。渠道本身是独立资源，走 /api/notify/* 管理；
		// 这里只放「作用于全部渠道」的三项。
		"notify_enabled":             s.m.pg.GetBool(settingNotifyEnabled, true),
		"notify_public_base_url":     notifyPublicBaseURL(s.m.pg),
		"notify_digest_interval_min": notifyDigestIntervalMin(s.m.pg),
	}
}

// setReporterBrowserScreenshotVisibility keeps the report writer's browser MCP
// permission in sync with the screenshot setting. Other agents retain their
// explicit MCP permissions.
func (s *Runtime) setReporterBrowserScreenshotVisibility(enabled bool) error {
	reporter, err := s.m.pg.GetAgentByKey("reporter")
	if err != nil || reporter == nil {
		return err
	}
	servers, err := s.m.pg.ListMCP()
	if err != nil {
		return err
	}
	for _, server := range servers {
		if server.Name == browserMCPName {
			// Turning screenshots on is an explicit opt-in. Make the browser MCP
			// usable in the shipped root-based container at the same time as the
			// permission toggle, so the next reporter run gets a working browser
			// instead of a deterministic Chromium sandbox error.
			if enabled {
				server.Args = encodeJSON(ensureBrowserMCPArgs(decodeStrSlice(server.Args), true))
				server.Enabled = true
				if _, err := s.m.pg.SaveMCP(server); err != nil {
					return err
				}
			}
			return s.m.pg.ToggleVisibility(reporter.ID, "mcp", server.ID, enabled)
		}
	}
	// The built-in browser MCP may be deleted by an administrator. Keep the
	// setting writable so it takes effect automatically if the server returns.
	return nil
}

func (s *Runtime) cancelTaskChat(taskID string, cause error) bool {
	s.chatMu.Lock()
	cancel := s.chatCancel[taskID]
	if cancel != nil {
		cancel(cause)
	}
	s.chatMu.Unlock()
	return cancel != nil
}

func (s *Runtime) finishTaskChat(taskID string, cancel context.CancelCauseFunc) {
	cancel(agent.AbortChatTurnFinished)
	s.chatMu.Lock()
	delete(s.chatBusy, taskID)
	delete(s.chatCancel, taskID)
	s.chatMu.Unlock()
}

// fallbackChat is the no-LLM human-steering handler: simple命令 + 态势摘要.
func (s *Runtime) fallbackChat(t *Task, msg string) string {
	m := strings.TrimSpace(msg)
	lower := strings.ToLower(m)
	switch {
	case strings.HasPrefix(m, "意图") || strings.HasPrefix(lower, "intent"):
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(m, "意图"), "intent"))
		_, _ = t.Store.AddIntent(map[string]any{"summary": text}, 9, nil, "human")
		return "已注入一条高优先级意图：" + text
	case strings.HasPrefix(m, "提示") || strings.HasPrefix(lower, "hint"):
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(m, "提示"), "hint"))
		_, _ = t.Store.AddNode(db.KindHint, map[string]any{"text": text}, 0, "active", "human", nil)
		return "已记录提示，规划者下次会读到：" + text
	default:
		assetCounts, _ := s.m.Assets().CountsByType()
		assets := 0
		for _, c := range assetCounts {
			assets += c
		}
		fnd, _ := t.Store.ListByKind(db.KindFinding, 1000)
		fr, _ := t.Store.Frontier(1000)
		return fmt.Sprintf("（规则模式，未配置 LLM）当前态势：资产 %d，待领意图 %d，确认发现 %d。\n可用指令：以\"意图 ...\"注入意图，\"提示 ...\"给规划者提示。", assets, len(fr), len(fnd))
	}
}
