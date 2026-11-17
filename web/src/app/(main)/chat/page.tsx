"use client";

import * as React from "react";
import type { ConversationListPage } from "@/lib/types";
import Link from "next/link";

import {
  ArrowDownIcon,
  ArrowUpIcon,
  Bot,
  ChevronDownIcon,
  ChevronRightIcon,
  ExternalLinkIcon,
  ListChecksIcon,
  Loader2Icon,
  MoreHorizontalIcon,
  PaperclipIcon,
  PencilIcon,
  PinIcon,
  PinOffIcon,
  PlusIcon,
  SearchIcon,
  Square,
  Trash2Icon,
  XIcon,
  ZapIcon,
} from "lucide-react";
import { toast } from "sonner";

import { ApprovalExecutionFocus, useApprovalFocus, useApprovalHistory } from "@/components/approval-execution-focus";
import { MentionTextarea } from "@/components/mention-textarea";
import { SideQuestionButton, SideQuestionWorkspace } from "@/components/side-question-workspace";
import { TodoPopover } from "@/components/todo-popover";
import { Transcript } from "@/components/transcript";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { useSideQuestions } from "@/hooks/use-side-questions";
import { agentDisplayName } from "@/i18n/agent-display";
import { localizedError } from "@/i18n/errors";
import { useLiveTranslation } from "@/i18n/use-live-translation";
import { mergeActivities } from "@/lib/activity-merge";
import { api } from "@/lib/api";
import { reconcileList, sameSnapshot } from "@/lib/list-snapshot";
import { shouldSubmitOnKey, useChatSendMode } from "@/lib/chat-send-mode";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { isBtwCommand } from "@/lib/side-questions";
import type { Activity, Agent, ChatAttachment, Conversation, ConversationStats, LLMProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

// fmtBytes renders a human file size for attachment chips (mirrors transcript.tsx).
function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

// fmtTokens renders a compact token count (1234 → 1.2k, 2_000_000 → 2M).
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

// fmtDuration renders an elapsed milliseconds span compactly (90s → 1m30s).
function fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

// HISTORY_PAGE is how many steps one history page loads: the latest page on open,
// then one more page each time the user scrolls to the top. Kept modest so a long
// thread stays snappy (only ~a page of rows is in the DOM until you scroll up).
const HISTORY_PAGE = 200;
const CONVERSATION_LIST_PAGE = 100;

// Which agent groups the user has collapsed in the left rail. Persisted so the
// rail looks the same after a reload; unknown keys are harmless (a deleted agent
// simply never renders a group again).
const COLLAPSED_AGENTS_KEY = "artex.chat.collapsed-agents";

function conversationIsPinned(conversation: Conversation): boolean {
  return conversation.pinned ?? Boolean(conversation.pinned_at);
}

// AgentGroup is one collapsible section of the left rail: all unpinned
// conversations of a single agent (or originating automation task), newest activity first.
interface AgentGroup {
  key: string;
  name: string;
  conversations: Conversation[];
  runningCount: number;
  totalCount?: number;
  sourceTaskId?: number;
}

// groupByAgent buckets conversations by agent (or originating task for automations),
// preserving the incoming order both inside a group and across groups. The server
// already sorts by updated_at DESC, so first-appearance order == most-recently-active group first.
function groupByAgent(
  conversations: Conversation[],
  agentByKey: Map<string, Agent>,
  translate: (key: string) => string,
  executionName?: (task?: number) => string,
): AgentGroup[] {
  const groups = new Map<string, AgentGroup>();
  for (const conversation of conversations) {
    const isAutomationGroup = conversation.source === "automation" && Boolean(executionName);
    const key = isAutomationGroup ? `task:${conversation.source_task_id ?? 0}` : conversation.agent_key;
    let group = groups.get(key);
    if (!group) {
      const agent = agentByKey.get(conversation.agent_key);
      group = {
        key,
        name:
          isAutomationGroup && executionName
            ? executionName(conversation.source_task_id)
            : agent
              ? agentDisplayName(agent, translate)
              : conversation.agent_key,
        conversations: [],
        runningCount: 0,
        sourceTaskId: isAutomationGroup && conversation.source_task_id ? conversation.source_task_id : undefined,
      };
      groups.set(key, group);
    }
    group.conversations.push(conversation);
    if (conversation.running) group.runningCount++;
  }
  return [...groups.values()];
}

// LiveBadge is the small pulsing "实时" chip reused from the task's main-agent
// console — shown while a turn is streaming.
function LiveBadge() {
  const { t: translate } = useLiveTranslation();
  return (
    <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-medium text-blue-600 dark:text-blue-400">
      <span className="size-1 animate-pulse rounded-full bg-blue-500" />
      {translate("overview.live")}
    </span>
  );
}

// Composer is the shared bottom input (textarea grows to a cap, Enter sends,
// Shift+Enter newlines) — the same affordance across DraftChat and ChatView.
function Composer({
  value,
  onChange,
  onSend,
  disabled,
  placeholder,
  leftSlot,
  running,
  onStop,
  stopDisabled,
  attachments,
  onPickFiles,
  onRemoveAttachment,
  uploading,
  allowBtw,
}: {
  value: string;
  onChange: (v: string) => void;
  onSend: () => void;
  disabled: boolean;
  placeholder: string;
  leftSlot?: React.ReactNode;
  running?: boolean;
  onStop?: () => void;
  stopDisabled?: boolean;
  // 方式1 文件上传:传了 onPickFiles 才显示回形针按钮 + 附件 chip 预览。
  attachments?: ChatAttachment[];
  onPickFiles?: (files: File[]) => void;
  onRemoveAttachment?: (path: string) => void;
  uploading?: boolean;
  allowBtw?: boolean;
}) {
  const { t: translate } = useLiveTranslation();
  const fileInputRef = React.useRef<HTMLInputElement>(null);
  const atts = attachments ?? [];
  // 发送键位由系统设置决定（localStorage），默认 Enter 发送。
  const sendMode = useChatSendMode();
  function onKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (!shouldSubmitOnKey(e, sendMode)) return;
    e.preventDefault();
    onSend();
  }
  return (
    <div className="border-t p-3">
      {atts.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {atts.map((a) => (
            <div
              key={a.path}
              className="flex items-center gap-1.5 rounded-md border bg-muted/50 px-2 py-1 text-xs"
              title={a.path}
            >
              <PaperclipIcon className="size-3 shrink-0 text-primary" />
              <span className="max-w-[160px] truncate">{a.name}</span>
              <span className="text-muted-foreground">{fmtBytes(a.size)}</span>
              {onRemoveAttachment && (
                <button
                  type="button"
                  className="ml-0.5 text-muted-foreground hover:text-foreground"
                  onClick={() => onRemoveAttachment(a.path)}
                  title={translate("overview.remove")}
                >
                  <XIcon className="size-3" />
                </button>
              )}
            </div>
          ))}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {leftSlot ? <div className="w-full sm:w-auto">{leftSlot}</div> : null}
        {onPickFiles && (
          <>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => {
                // FileList 与 input 元素活绑定:必须先快照成数组,再清空 value,
                // 否则异步的 onPickFiles(比如草稿态要先建会话)恢复执行时会拿到空列表。
                const picked = Array.from(e.target.files ?? []);
                e.target.value = ""; // allow re-picking the same file
                if (picked.length > 0) onPickFiles(picked);
              }}
            />
            <Button
              size="icon"
              variant="ghost"
              onClick={() => fileInputRef.current?.click()}
              disabled={disabled || uploading}
              title={translate("overview.uploadFile")}
            >
              {uploading ? <Loader2Icon className="size-4 animate-spin" /> : <PaperclipIcon className="size-4" />}
            </Button>
          </>
        )}
        <MentionTextarea
          className="max-h-40 min-h-10 min-w-0 flex-1 resize-none"
          rows={1}
          placeholder={placeholder}
          value={value}
          disabled={disabled && !(running && allowBtw)}
          onValueChange={onChange}
          onKeyDown={onKeyDown}
        />
        {running && allowBtw && isBtwCommand(value) && (
          <Button
            size="icon"
            onClick={onSend}
            aria-label={translate("overview.sendSideQuestion")}
            title={translate("overview.sendSideQuestion")}
          >
            <ArrowUpIcon />
          </Button>
        )}
        {running ? (
          // while a run is in flight the send button becomes a stop button —
          // aborts just this session (the trigger queue keeps going).
          <Button
            size="icon"
            variant="destructive"
            onClick={onStop}
            disabled={stopDisabled}
            title={translate("overview.stopRun")}
          >
            <Square className="size-3.5 fill-current" />
          </Button>
        ) : (
          <Button
            size="icon"
            onClick={onSend}
            disabled={disabled || (!value.trim() && atts.length === 0)}
            title={translate("overview.sendMessage")}
            aria-label={translate("overview.sendMessage")}
          >
            <ArrowUpIcon />
          </Button>
        )}
      </div>
    </div>
  );
}

// LLMProfileRow shows the active LLM config below the composer and lets the user
// switch it via a Popover. `selected` is the profile id or null for default.
function LLMProfileRow({
  profiles,
  selected,
  onChange,
  disabled,
  rightSlot,
}: {
  profiles: LLMProfile[];
  selected: number | null;
  onChange: (id: number | null) => void;
  disabled?: boolean;
  rightSlot?: React.ReactNode;
}) {
  const { t: translate } = useLiveTranslation();
  const [open, setOpen] = React.useState(false);
  const activeDefault = profiles.find((p) => p.is_default);
  const current = selected != null ? profiles.find((p) => Number(p.id) === selected) : null;
  const label = current
    ? current.name
    : activeDefault
      ? translate("overview.defaultProfileWithName", { name: activeDefault.name })
      : translate("overview.default");

  return (
    <div className="flex min-w-0 shrink-0 items-center gap-1 px-1 pt-0.5 pb-1">
      <ZapIcon className="text-muted-foreground/50 size-3 shrink-0" />
      <span className="truncate text-muted-foreground/70 text-xs" title={label}>
        {label}
      </span>
      <Popover open={open} onOpenChange={disabled ? undefined : setOpen}>
        <PopoverTrigger asChild>
          <button
            type="button"
            disabled={disabled}
            className="flex shrink-0 items-center gap-0.5 text-primary text-xs hover:underline disabled:pointer-events-none disabled:opacity-40"
          >
            {translate("overview.change")}
            <ChevronDownIcon className="size-3" />
          </button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-64 p-1">
          <p className="text-muted-foreground px-2 py-1 text-[11px] font-medium">
            {translate("overview.chooseProfile")}
          </p>
          {/* default option */}
          <button
            type="button"
            onClick={() => {
              onChange(null);
              setOpen(false);
            }}
            className={cn(
              "flex w-full flex-col rounded px-2 py-1.5 text-left hover:bg-accent",
              selected == null && "bg-accent",
            )}
          >
            <span className="text-sm">
              {activeDefault
                ? translate("overview.defaultProfileWithName", { name: activeDefault.name })
                : translate("overview.default")}
            </span>
            {activeDefault && (
              <span className="text-muted-foreground text-[11px]">
                {activeDefault.format} · {activeDefault.model}
              </span>
            )}
          </button>
          {profiles
            .filter((p) => p.format !== "jev")
            .map((p) => (
              <button
                key={p.id}
                type="button"
                onClick={() => {
                  onChange(Number(p.id));
                  setOpen(false);
                }}
                className={cn(
                  "flex w-full flex-col rounded px-2 py-1.5 text-left hover:bg-accent",
                  selected === Number(p.id) && "bg-accent",
                )}
              >
                <span className="text-sm">{p.name}</span>
                <span className="text-muted-foreground text-[11px]">
                  {p.format} · {p.model}
                </span>
              </button>
            ))}
        </PopoverContent>
      </Popover>
      {rightSlot}
    </div>
  );
}

// DraftChat is the default right-pane view: a fresh chat (agent picker in the
// header, centered empty state, composer) with NO conversation created yet. The
// conversation is created lazily on the first send (ChatGPT-style), then the
// parent switches to the real ChatView.
function DraftChat({
  agents,
  profiles,
  onStarted,
}: {
  agents: Agent[];
  profiles: LLMProfile[];
  onStarted: (c: Conversation, pending?: { input?: string; attachments?: ChatAttachment[] }) => void;
}) {
  const { t: translate } = useLiveTranslation();
  const [agentKey, setAgentKey] = React.useState("");
  const [llmProfileId, setLlmProfileId] = React.useState<number | null>(null);
  const [input, setInput] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const [uploading, setUploading] = React.useState(false);

  // default the agent to Auto once agents load.
  React.useEffect(() => {
    if (!agentKey && agents.some((a) => a.key === "auto")) setAgentKey("auto");
  }, [agentKey, agents]);

  const agent = agents.find((a) => a.key === agentKey);

  async function send() {
    const msg = input.trim();
    if (!msg || !agentKey || sending) return;
    setSending(true);
    try {
      const c = await api.createConversation(agentKey, "", llmProfileId);
      await api.sendConversationMessage(c.id, msg);
      onStarted(c);
    } catch (e) {
      toast.error(translate("overview.sendFailedPrefix") + localizedError(e, translate));
      setSending(false);
    }
  }

  // Attachments need a conversation to own the uploads dir (sessions/conv-<id>/),
  // and a draft has none yet — so picking a file CREATES the conversation, uploads
  // into it, then hands off to ChatView carrying the typed text + attachments (the
  // user sends from there). Mirrors the task main-agent console's upload, adapted to
  // the ChatGPT-style lazy-create flow.
  async function pickFiles(files: File[]) {
    if (files.length === 0 || !agentKey || uploading || sending) return;
    setUploading(true);
    try {
      const c = await api.createConversation(agentKey, "", llmProfileId);
      const r = await api.chatUpload("session", `conv-${c.id}`, files);
      onStarted(c, { input, attachments: r.attachments });
    } catch (e) {
      toast.error(translate("overview.uploadFailedPrefix") + localizedError(e, translate));
      setUploading(false);
    }
  }

  const agentPicker = (
    <Select value={agentKey} onValueChange={setAgentKey}>
      <SelectTrigger className="w-full sm:w-40">
        <SelectValue placeholder={translate("overview.chooseAgent")} />
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          {agents.map((a) => (
            <SelectItem key={a.key} value={a.key}>
              <span className="flex items-center gap-2">
                <Bot className="size-3.5" />
                {agentDisplayName(a, translate)}
                {!a.builtin && (
                  <Badge variant="outline" className="px-1 py-0 text-[9px]">
                    {translate("overview.custom")}
                  </Badge>
                )}
              </span>
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  );

  return (
    <>
      {/* empty / landing state fills the panel */}
      <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-4 text-center">
        <div className="bg-primary/10 flex size-12 items-center justify-center rounded-full">
          <Bot className="text-primary size-6" />
        </div>
        <div className="text-sm font-medium">
          {translate("overview.startChat", { name: agent ? agentDisplayName(agent, translate) : "Agent" })}
        </div>
        {agent?.description && <p className="text-muted-foreground max-w-md text-xs">{agent.description}</p>}
      </div>

      <Composer
        value={input}
        onChange={setInput}
        onSend={send}
        disabled={sending || uploading || !agentKey}
        placeholder={translate("overview.messagePlaceholder")}
        leftSlot={agentPicker}
        onPickFiles={pickFiles}
        uploading={uploading}
      />
      <LLMProfileRow
        profiles={profiles}
        selected={llmProfileId}
        onChange={setLlmProfileId}
        disabled={sending || uploading}
      />
    </>
  );
}

// ChatView is the right pane for one conversation — mirrors the task detail's
// main-agent console: header with agent + live badge + token/duration meta, a
// scroll-stick transcript (chat mode), and the composer.
function ChatView({
  conv,
  agents,
  profiles,
  initial,
  onTitleMaybeChanged,
  onConvUpdated,
}: {
  conv: Conversation;
  agents: Agent[];
  profiles: LLMProfile[];
  // pending text + already-uploaded attachments handed off from a draft that
  // created this conversation via the paperclip (consumed once, on mount).
  initial?: { input?: string; attachments?: ChatAttachment[] };
  onTitleMaybeChanged: () => void;
  onConvUpdated: () => void;
}) {
  const { t: translate, liveT } = useLiveTranslation();
  const approvalFocus = useApprovalFocus({ conversationId: conv.id });
  const [messages, setMessages] = React.useState<Activity[]>([]);
  const [running, setRunning] = React.useState(false);
  const [input, setInput] = React.useState(initial?.input ?? "");
  const [sending, setSending] = React.useState(false);
  const [stopping, setStopping] = React.useState(false);
  const [attachments, setAttachments] = React.useState<ChatAttachment[]>(initial?.attachments ?? []);
  const [uploading, setUploading] = React.useState(false);
  const cursorRef = React.useRef(0); // newest loaded id — incremental-tail anchor
  const earliestRef = React.useRef(0); // earliest loaded id — reverse-pagination anchor
  const hasMoreRef = React.useRef(false); // older history remains above the loaded window
  const loadingMoreRef = React.useRef(false); // guard: one scroll-up load at a time
  const [historyLoaded, setHistoryLoaded] = React.useState(false);
  const [historyError, setHistoryError] = React.useState("");
  const [historyRetry, setHistoryRetry] = React.useState(0);
  const [loadingEarlier, setLoadingEarlier] = React.useState(false);
  const [stats, setStats] = React.useState<ConversationStats>();
  const [awayFromBottom, setAwayFromBottom] = React.useState(false);
  const [hasMore, setHasMore] = React.useState(false); // drives the "load earlier" control
  const agent = agents.find((a) => a.key === conv.agent_key);
  const currentProfileId = conv.llm_profile_id ?? null;
  const side = useSideQuestions(`/api/conversations/${conv.id}`);

  async function changeProfile(id: number | null) {
    try {
      await api.updateConversationProfile(conv.id, id);
      onConvUpdated();
    } catch (e) {
      toast.error(translate("overview.switchProfileFailedPrefix") + localizedError(e, translate));
    }
  }

  // conversation-scoped detail fetcher for the reused Transcript renderer.
  const fetchDetail = React.useCallback((seq: number) => api.conversationMsgDetail(conv.id, seq), [conv.id]);

  // seq of the most-recent TodoWrite tool call (for the Todo popover); null if none.
  const latestTodoSeq = React.useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      const a = messages[i];
      if (a.kind === "tool_use" && a.tool === "TodoWrite") return a.seq;
    }
    return null;
  }, [messages]);

  // reset + load whenever the selected conversation changes. Load only the LATEST
  // page on open — older history streams in on scroll-up or button click (loadEarlier below).
  // biome-ignore lint/correctness/useExhaustiveDependencies: historyRetry explicitly retries the same initial history request.
  React.useEffect(() => {
    cursorRef.current = 0;
    earliestRef.current = 0;
    hasMoreRef.current = false;
    setHasMore(false);
    setMessages([]);
    setHistoryLoaded(false);
    setHistoryError("");
    setStats(undefined);
    setRunning(false);
    setAwayFromBottom(false);
    let live = true;
    api
      .conversationHistory(conv.id, 0, HISTORY_PAGE)
      .then((r) => {
        if (!live) return;
        setMessages((current) => mergeActivities(r.items, current));
        setStats(r.stats);
        cursorRef.current = Math.max(cursorRef.current, r.cursor);
        earliestRef.current = r.items.length ? r.items[0].seq : 0;
        hasMoreRef.current = r.hasMore;
        setHasMore(r.hasMore);
        setRunning((current) => current || r.running);
        setHistoryLoaded(true);
      })
      .catch((error) => {
        if (live) {
          setHistoryLoaded(true);
          setHistoryError(localizedError(error, liveT));
        }
      });
    return () => {
      live = false;
    };
  }, [conv.id, historyRetry, liveT]);

  // A trigger or another tab may start a turn while this conversation is open.
  // A false list snapshot must not stop the tail before its final messages load.
  React.useEffect(() => {
    if (conv.running) setRunning(true);
  }, [conv.running]);

  // poll while a turn is running: pull new steps after the cursor.
  React.useEffect(() => {
    if (!running) return;
    let live = true;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      let keepPolling = true;
      try {
        const r = await api.conversationMessages(conv.id, cursorRef.current);
        if (!live) return;
        if (r.items.length) {
          setMessages((prev) => mergeActivities(prev, r.items));
        }
        cursorRef.current = Math.max(cursorRef.current, r.cursor);
        if (r.stats) setStats(r.stats);
        keepPolling = r.running;
        setRunning(r.running);
        if (!r.running) onTitleMaybeChanged(); // first-turn auto-title landed
      } catch {
        /* transient — keep polling */
      } finally {
        // Slow responses must not overlap another poll with the same cursor.
        if (live && keepPolling) timer = setTimeout(() => void tick(), 1000);
      }
    };
    void tick();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [running, conv.id, onTitleMaybeChanged]);

  const loadFocusPage = React.useCallback(
    (before: number) => api.conversationHistory(conv.id, before, HISTORY_PAGE),
    [conv.id],
  );
  const mergeFocusPage = React.useCallback((page: { items: Activity[]; hasMore: boolean }) => {
    setMessages((prev) => mergeActivities(page.items, prev));
    earliestRef.current = page.items[0]?.seq ?? earliestRef.current;
    hasMoreRef.current = page.hasMore;
    setHasMore(page.hasMore);
  }, []);
  const focusHistory = useApprovalHistory(
    approvalFocus.state?.source,
    historyLoaded,
    messages,
    loadFocusPage,
    mergeFocusPage,
  );

  // ---- transcript auto-scroll (open → bottom; stick to bottom unless scrolled up) ----
  const contentRef = React.useRef<HTMLDivElement | null>(null);
  const atBottomRef = React.useRef(true);
  const viewportRef = React.useRef<HTMLDivElement>(null);
  const viewport = React.useCallback(() => viewportRef.current, []);
  // Scroll-up or explicit button click loads one older page and prepends it,
  // preserving the visual position so the view doesn't jump.
  const loadEarlier = React.useCallback(async () => {
    if (loadingMoreRef.current || !hasMoreRef.current) return;
    const vp = viewport();
    if (!vp) return;
    loadingMoreRef.current = true;
    setLoadingEarlier(true);
    setHistoryError("");
    const prevH = vp.scrollHeight;
    const prevTop = vp.scrollTop;
    try {
      const r = await api.conversationHistory(conv.id, earliestRef.current, HISTORY_PAGE);
      if (r.items.length) {
        setMessages((prev) => mergeActivities(r.items, prev));
        earliestRef.current = r.items[0].seq;
      }
      hasMoreRef.current = r.hasMore;
      setHasMore(r.hasMore);
      requestAnimationFrame(() => {
        const v = viewport();
        if (v) v.scrollTop = prevTop + (v.scrollHeight - prevH);
      });
    } catch (error) {
      setHistoryError(localizedError(error, liveT));
    } finally {
      loadingMoreRef.current = false;
      setLoadingEarlier(false);
    }
  }, [conv.id, liveT, viewport]);
  React.useEffect(() => {
    const vp = viewport();
    if (!vp) return;
    const onScroll = () => {
      if (approvalFocus.state && !focusHistory.ready) return;
      atBottomRef.current = vp.scrollTop + vp.clientHeight >= vp.scrollHeight - 60;
      setAwayFromBottom(!atBottomRef.current);
      if (vp.scrollTop <= 80) void loadEarlier(); // near top → pull an older page
    };
    vp.addEventListener("scroll", onScroll, { passive: true });
    return () => vp.removeEventListener("scroll", onScroll);
  }, [viewport, loadEarlier, approvalFocus.state, focusHistory.ready]);
  // open/switch a conversation → jump to the latest (bottom)
  // biome-ignore lint/correctness/useExhaustiveDependencies: changing conversations intentionally retriggers the scroll reset.
  React.useLayoutEffect(() => {
    const vp = viewport();
    if (vp) {
      vp.scrollTop = vp.scrollHeight;
      atBottomRef.current = true;
    }
  }, [conv.id, viewport]);
  // new activity → stick to bottom only if the user is already pinned there
  // biome-ignore lint/correctness/useExhaustiveDependencies: message and running changes intentionally retrigger bottom anchoring.
  React.useLayoutEffect(() => {
    if (approvalFocus.state || !atBottomRef.current) return;
    const vp = viewport();
    if (vp) vp.scrollTop = vp.scrollHeight;
  }, [messages, running, viewport, approvalFocus.state]);
  // Re-pin to bottom when lazy details expand while user is already at the bottom.
  // biome-ignore lint/correctness/useExhaustiveDependencies: conv.id intentionally rebinds ResizeObserver per conversation.
  React.useEffect(() => {
    const el = contentRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (approvalFocus.state || !atBottomRef.current) return;
      const vp = viewport();
      if (vp) vp.scrollTop = vp.scrollHeight;
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [conv.id, viewport, approvalFocus.state]);

  // Per-conversation token total: prefer server-computed thread stats (includes
  // older unloaded pages), falling back to loaded messages when stats are absent.
  const tokenTotal = React.useMemo(() => {
    if (stats) {
      const i = stats.input_tokens ?? 0;
      const o = stats.output_tokens ?? 0;
      const cr = stats.cache_read_tokens ?? 0;
      return { i, o, cr, turns: stats.turns ?? 0, any: i + o + cr > 0 };
    }
    let i = 0,
      o = 0,
      cr = 0;
    let li = 0,
      lo = 0,
      lcr = 0;
    let turns = 0;
    for (const a of messages) {
      if (a.kind === "result") {
        i += a.input_tokens ?? 0;
        o += a.output_tokens ?? 0;
        cr += a.cache_read_tokens ?? 0;
        li = lo = lcr = 0;
      } else if (a.kind === "usage") {
        turns += 1;
        li = a.input_tokens ?? 0;
        lo = a.output_tokens ?? 0;
        lcr = a.cache_read_tokens ?? 0;
      }
    }
    const I = i + li,
      O = o + lo,
      CR = cr + lcr;
    return { i: I, o: O, cr: CR, turns, any: I + O + CR > 0 };
  }, [stats, messages]);

  // pickFiles uploads into this conversation's session dir (sessions/conv-<id>/
  // uploads/) and queues the returned metadata to send with the next message.
  async function pickFiles(files: File[]) {
    if (files.length === 0) return;
    setUploading(true);
    try {
      const r = await api.chatUpload("session", `conv-${conv.id}`, files);
      setAttachments((prev) => [...prev, ...r.attachments]);
    } catch (e) {
      toast.error(translate("overview.uploadFailedPrefix") + localizedError(e, translate));
    } finally {
      setUploading(false);
    }
  }

  async function send() {
    const msg = input.trim();
    const atts = attachments;
    if (side.handleCommand(msg, () => setInput(""))) return;
    if ((!msg && atts.length === 0) || sending || running) return;
    setSending(true);
    setInput("");
    setAttachments([]);
    try {
      await api.sendConversationMessage(conv.id, msg, atts.length ? atts : undefined);
      // The live loop pulls the persisted human turn immediately. Sharing that
      // fetch avoids racing a separate post-send request against the poller.
      setRunning(true);
    } catch (e) {
      toast.error(translate("overview.sendFailedPrefix") + localizedError(e, translate));
      setInput(msg); // restore so the user doesn't lose their text
      setAttachments(atts); // and their attachments
    } finally {
      setSending(false);
    }
  }

  // stop aborts the in-flight run for this conversation. running flips to false on
  // the next 1s poll once the backend unwinds the agent; the trigger queue is not
  // affected — the agent's next queued fire still starts.
  async function stop() {
    if (stopping) return;
    setStopping(true);
    try {
      await api.stopConversation(conv.id);
    } catch (e) {
      toast.error(translate("overview.stopFailedPrefix") + localizedError(e, translate));
    } finally {
      setStopping(false);
    }
  }

  const displayName = agent ? agentDisplayName(agent, translate) : conv.agent_key;

  return (
    <SideQuestionWorkspace
      side={side}
      label={displayName}
      composerLayout="inline"
      noSnapshotHint={translate("overview.sideQuestionUnavailable")}
      emptyDescription={translate("overview.sideQuestionHelp")}
    >
      {/* header: which agent + live/outcome + originating task link + token meta */}
      <div className="flex min-w-0 flex-wrap items-center gap-2 border-b px-4 py-2.5">
        <Bot className="text-muted-foreground size-4 shrink-0" />
        <span className="min-w-0 max-w-48 truncate text-sm font-medium">{displayName}</span>
        <span className="text-muted-foreground hidden shrink-0 font-mono text-xs sm:inline">{conv.agent_key}</span>
        {agent && !agent.builtin && (
          <Badge variant="outline" className="shrink-0 px-1.5 py-0 text-[10px]">
            {translate("overview.custom")}
          </Badge>
        )}
        {agent?.description && (
          <span className="text-muted-foreground min-w-0 truncate text-xs">{agent.description}</span>
        )}
        {running ? (
          <LiveBadge />
        ) : conv.source === "automation" && conv.outcome ? (
          <Badge
            variant="secondary"
            className={cn(
              "shrink-0 px-1.5 py-0 text-[10px]",
              conv.outcome === "completed"
                ? "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400"
                : "bg-amber-500/15 text-amber-700 dark:text-amber-400",
            )}
          >
            {translate(`opt.execution.${conv.outcome}`)}
          </Badge>
        ) : null}
        {conv.source_task_id ? (
          <Link
            href={`/function/tasks/detail?id=${conv.source_task_id}`}
            className="text-primary hover:underline inline-flex shrink-0 items-center gap-1 text-xs font-medium"
          >
            {translate("opt.taskExecutions", { id: conv.source_task_id })}
            <ExternalLinkIcon className="size-3" />
          </Link>
        ) : null}
        <SideQuestionButton side={side} />
        <div className="text-muted-foreground ml-auto flex min-w-0 max-w-full items-center justify-end gap-x-3 gap-y-1 text-xs max-sm:w-full max-sm:flex-wrap">
          {tokenTotal.turns > 0 && (
            <span title={translate("opt.threadTotals")} className="tabular-nums">
              {translate("opt.modelCalls", { count: tokenTotal.turns })}
            </span>
          )}
          {tokenTotal.any && (
            <span title="input / cache(read) / output tokens" className="min-w-0 truncate tabular-nums">
              input {fmtTokens(tokenTotal.i)} · cache {fmtTokens(tokenTotal.cr)} · output {fmtTokens(tokenTotal.o)}
            </span>
          )}
        </div>
      </div>

      <ApprovalExecutionFocus focus={approvalFocus} history={focusHistory} />

      {/* messages */}
      <ScrollArea viewportRef={viewportRef} type="auto" className="min-h-0 min-w-0 flex-1">
        <div className="min-w-0 max-w-full px-4 py-3" ref={contentRef}>
          {historyError ? (
            <div className="mb-2 flex items-center gap-2 text-sm text-destructive">
              <span className="min-w-0 flex-1">{historyError}</span>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => (messages.length ? void loadEarlier() : setHistoryRetry((x) => x + 1))}
              >
                {translate("common.retry")}
              </Button>
            </div>
          ) : null}
          {messages.length === 0 && !running ? (
            <div className="text-muted-foreground py-10 text-center text-sm">
              {translate("overview.startChat", { name: displayName })}
            </div>
          ) : (
            <>
              {hasMore && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="text-muted-foreground mb-2 w-full text-xs"
                  disabled={loadingEarlier}
                  onClick={() => void loadEarlier()}
                >
                  {loadingEarlier && <Loader2Icon className="size-3.5 animate-spin" />}
                  {translate("opt.loadEarlier")}
                </Button>
              )}
              <Transcript
                activity={messages}
                live={running}
                chat
                fetchDetail={fetchDetail}
                focusedSeq={focusHistory.ready ? approvalFocus.state?.source?.seq : undefined}
              />
            </>
          )}
        </div>
      </ScrollArea>

      {awayFromBottom && (
        <div className="flex justify-center border-t px-4 py-1">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 gap-1 text-xs"
            onClick={() => {
              const vp = viewport();
              if (vp) {
                atBottomRef.current = true;
                vp.scrollTop = vp.scrollHeight;
                setAwayFromBottom(false);
              }
            }}
          >
            <ArrowDownIcon className="size-3" />
            {translate("opt.latest")}
          </Button>
        </div>
      )}

      <Composer
        value={input}
        onChange={setInput}
        onSend={send}
        disabled={running || sending}
        allowBtw
        placeholder={running ? translate("overview.runningPlaceholder") : translate("overview.messagePlaceholder")}
        running={running}
        onStop={stop}
        stopDisabled={stopping}
        attachments={attachments}
        onPickFiles={pickFiles}
        onRemoveAttachment={(path) => setAttachments((p) => p.filter((x) => x.path !== path))}
        uploading={uploading}
      />
      <LLMProfileRow
        profiles={profiles}
        selected={currentProfileId}
        onChange={changeProfile}
        disabled={running || sending}
        rightSlot={<TodoPopover seq={latestTodoSeq} fetchDetail={fetchDetail} />}
      />
    </SideQuestionWorkspace>
  );
}

// ConversationItem is one row in the left rail: title, agent subtitle, inline
// rename, pin marker, and a compact action menu. Rows nested under an agent
// group drop the agent subtitle (showAgent=false) — the header already says it.
const ConversationItem = React.memo(function ConversationItem({
  conv,
  agent,
  showAgent = true,
  active,
  renaming,
  renameText,
  onSelect,
  onStartRename,
  onRenameText,
  onCommitRename,
  onCancelRename,
  onTogglePinned,
  onDelete,
  selectionMode,
  selectedForDelete,
  onSelectedForDeleteChange,
}: {
  conv: Conversation;
  agent?: Agent;
  showAgent?: boolean;
  active: boolean;
  renaming: boolean;
  renameText: string;
  onSelect: (id: number) => void;
  onStartRename: (conversation: Conversation) => void;
  onRenameText: (v: string) => void;
  onCommitRename: (id: number, title: string) => void;
  onCancelRename: () => void;
  onTogglePinned: (conversation: Conversation) => void;
  onDelete: (id: number) => void;
  selectionMode: boolean;
  selectedForDelete: boolean;
  onSelectedForDeleteChange: (id: number, checked: boolean) => void;
}) {
  const { t: translate, i18n } = useLiveTranslation();
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const renameInputRef = React.useRef<HTMLInputElement>(null);
  const cancelRenameRef = React.useRef(false);
  const pinned = conversationIsPinned(conv);
  const displayTitle = conv.title || translate("overview.newConversation");

  React.useEffect(() => {
    if (!renaming) return;
    cancelRenameRef.current = false;
    renameInputRef.current?.focus();
    renameInputRef.current?.select();
  }, [renaming]);

  return (
    <div
      className={cn(
        "group flex min-w-0 items-center gap-1 rounded-md pr-1 transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      {selectionMode && (
        <Checkbox
          checked={selectedForDelete}
          onCheckedChange={(checked) => onSelectedForDeleteChange(conv.id, checked === true)}
          aria-label={translate("overview.selectConversation", {
            title: displayTitle,
          })}
          className="ml-1 shrink-0"
        />
      )}
      {renaming ? (
        <input
          ref={renameInputRef}
          value={renameText}
          onChange={(e) => onRenameText(e.target.value)}
          onBlur={() => {
            if (!cancelRenameRef.current) onCommitRename(conv.id, renameText);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") onCommitRename(conv.id, renameText);
            if (e.key === "Escape") {
              e.preventDefault();
              cancelRenameRef.current = true;
              onCancelRename();
            }
          }}
          className="border-input bg-background min-w-0 flex-1 rounded-md border px-2 py-1 text-sm"
        />
      ) : (
        <button
          type="button"
          onClick={() => onSelect(conv.id)}
          onDoubleClick={() => onStartRename(conv)}
          title={translate("overview.renameHint")}
          className="min-w-0 flex-1 rounded-md px-2 py-1.5 text-left"
        >
          <div className="flex min-w-0 items-center gap-1.5">
            {pinned && <PinIcon className="text-primary size-3 shrink-0" aria-label={translate("overview.pinned")} />}
            <div className="truncate text-sm">{displayTitle}</div>
            {conv.running ? (
              <Badge variant="secondary" className="shrink-0 gap-1" title={translate("overview.agentRunning")}>
                <Spinner className="size-3" aria-hidden="true" />
                {translate("overview.running")}
              </Badge>
            ) : conv.source === "automation" && conv.outcome ? (
              <Badge
                variant="secondary"
                className={cn(
                  "shrink-0 px-1.5 py-0 text-[10px]",
                  conv.outcome === "completed"
                    ? "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400"
                    : "bg-amber-500/15 text-amber-700 dark:text-amber-400",
                )}
              >
                {translate(`opt.execution.${conv.outcome}`)}
              </Badge>
            ) : null}
          </div>
          <div className="text-muted-foreground flex min-w-0 items-center gap-1 text-[11px]">
            {showAgent && (
              <>
                <Bot className="size-3 shrink-0" />
                <span className="min-w-0 truncate">{agent ? agentDisplayName(agent, translate) : conv.agent_key}</span>
                <span className="shrink-0">·</span>
              </>
            )}
            <span className="shrink-0">
              {new Date(conv.created_at).toLocaleDateString(i18n.resolvedLanguage, {
                month: "numeric",
                day: "numeric",
                hour: "2-digit",
                minute: "2-digit",
              })}
            </span>
            <span className="shrink-0 opacity-60">#{conv.id}</span>
          </div>
        </button>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground shrink-0"
            aria-label={translate("overview.manageConversation", {
              title: displayTitle,
            })}
          >
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuGroup>
            <DropdownMenuItem onSelect={() => onStartRename(conv)}>
              <PencilIcon />
              {translate("overview.rename")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onTogglePinned(conv)}>
              {pinned ? <PinOffIcon /> : <PinIcon />}
              {pinned ? translate("overview.unpin") : translate("overview.pin")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem variant="destructive" onSelect={() => setDeleteOpen(true)}>
              <Trash2Icon />
              {translate("overview.delete")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {translate("overview.deleteConversation", { title: displayTitle })}
            </AlertDialogTitle>
            <AlertDialogDescription>{translate("overview.irreversible")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{translate("overview.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => onDelete(conv.id)}>{translate("overview.delete")}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
});

// AgentGroupHeader is the sticky, clickable divider above one agent's (or task's) rows:
// collapse chevron, group name, optional originating task link, running marker, and row count.
function AgentGroupHeader({
  group,
  collapsed,
  hasActive,
  onToggle,
}: {
  group: AgentGroup;
  collapsed: boolean;
  hasActive: boolean;
  onToggle: (key: string) => void;
}) {
  const { t: translate } = useLiveTranslation();
  return (
    <div className="bg-card sticky top-0 z-10 flex min-w-0 items-center gap-0.5 rounded-md">
      <button
        type="button"
        onClick={() => onToggle(group.key)}
        aria-expanded={!collapsed}
        title={
          collapsed
            ? translate("overview.expandGroup", { name: group.name })
            : translate("overview.collapseGroup", { name: group.name })
        }
        className={cn(
          "flex min-w-0 flex-1 items-center gap-1.5 rounded-md px-1.5 py-1 text-left text-[11px] font-medium transition-colors hover:bg-accent/50",
          collapsed && hasActive ? "text-foreground" : "text-muted-foreground",
        )}
      >
        <ChevronRightIcon className={cn("size-3 shrink-0 transition-transform", !collapsed && "rotate-90")} />
        <Bot className="size-3 shrink-0" />
        <span className="min-w-0 flex-1 truncate">{group.name}</span>
        {collapsed && hasActive && (
          <span
            className="bg-primary size-1.5 shrink-0 rounded-full"
            title={translate("overview.activeConversationGroup")}
          />
        )}
        {group.runningCount > 0 && (
          <Spinner
            className="size-3 shrink-0"
            aria-label={translate("overview.runningConversations", { count: group.runningCount })}
          />
        )}
        <span className="shrink-0 tabular-nums opacity-60">
          {group.totalCount && group.totalCount !== group.conversations.length
            ? `${group.conversations.length}/${group.totalCount}`
            : group.conversations.length}
        </span>
      </button>
      {group.sourceTaskId ? (
        <Link
          href={`/function/tasks/detail?id=${group.sourceTaskId}`}
          title={group.name}
          aria-label={group.name}
          className="text-muted-foreground hover:text-foreground hover:bg-accent/50 inline-flex size-6 shrink-0 items-center justify-center rounded-md transition-colors"
        >
          <ExternalLinkIcon className="size-3" />
        </Link>
      ) : null}
    </div>
  );
}

export default function ChatPage() {
  const { t: translate } = useLiveTranslation();
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [convs, setConvs] = React.useState<Conversation[]>([]);
  const [agentFilter, setAgentFilter] = React.useState<string | null>(null);
  const [selectedId, setSelectedId] = React.useState<number | null>(null);
  const [sourceRequested, setSourceRequested] = React.useState(false);
  const [convsLoaded, setConvsLoaded] = React.useState(false);
  const [listScope, setListScope] = React.useState<"manual" | "automation" | "all">("manual");
  const [search, setSearch] = React.useState("");
  const listLoadedRef = React.useRef(false);
  const runningListRef = React.useRef(false);
  const selectConversation = React.useCallback(
    (id: number | null) => {
      if (id !== selectedId) {
        const url = new URL(window.location.href);
        url.searchParams.delete("approval");
        setSourceRequested(false);
        window.history.replaceState(null, "", url);
      }
      setSelectedId(id);
    },
    [selectedId],
  );

  const [renamingId, setRenamingId] = React.useState<number | null>(null);
  const [renameText, setRenameText] = React.useState("");
  const [selectedConversationIds, setSelectedConversationIds] = React.useState<Set<number>>(() => new Set());
  const [selectionMode, setSelectionMode] = React.useState(false);
  const [bulkDeleteOpen, setBulkDeleteOpen] = React.useState(false);
  const [bulkDeleting, setBulkDeleting] = React.useState(false);
  const [visibleConversationCount, setVisibleConversationCount] = React.useState(CONVERSATION_LIST_PAGE);
  // Collapsed agent groups. Hydrated from localStorage after mount (not a lazy
  // useState init) so the server and client render the same first pass.
  const [collapsedAgents, setCollapsedAgents] = React.useState<Set<string>>(() => new Set());
  // Pending text + uploaded attachments handed off from a draft that created a
  // conversation via the paperclip, keyed by the new conversation id (consumed once
  // by ChatView on mount; ids never repeat, so leftover entries are harmless).
  const [pendingByConv, setPendingByConv] = React.useState<
    Record<number, { input?: string; attachments?: ChatAttachment[] }>
  >({});

  const [listMeta, setListMeta] = React.useState<ConversationListPage | null>(null);
  const [searchQuery, setSearchQuery] = React.useState("");
  const [selectedFallback, setSelectedFallback] = React.useState<Conversation | null>(null);
  const selectionFilterRef = React.useRef("");
  const selectedIdRef = React.useRef(selectedId);
  selectedIdRef.current = selectedId;
  selectionFilterRef.current = JSON.stringify([listScope, agentFilter, searchQuery]);
  React.useEffect(() => {
    const timer = setTimeout(() => {
      setSearchQuery(search.trim());
      setVisibleConversationCount(CONVERSATION_LIST_PAGE);
      setSelectedConversationIds(new Set());
    }, 300);
    return () => clearTimeout(timer);
  }, [search]);
  React.useEffect(() => {
    if (selectedId === null) {
      setSelectedFallback(null);
      return;
    }
    let live = true;
    api
      .getConversation(selectedId)
      .then((conversation) => {
        if (live) setSelectedFallback(conversation);
      })
      .catch(() => {
        if (live) setSelectedFallback(null);
      });
    return () => {
      live = false;
    };
  }, [selectedId]);
  React.useEffect(() => {
    const id = Number(new URLSearchParams(window.location.search).get("c"));
    let live = true;
    if (id > 0)
      void api
        .getConversation(id)
        .then((conversation) => {
          if (live && conversation.source === "automation") setListScope("automation");
        })
        .catch(() => undefined);
    return () => {
      live = false;
    };
  }, []);
  const conversationListSeq = React.useRef(0);
  const statsCache = React.useRef({ key: "", until: 0 });
  const reloadConvs = React.useCallback(
    async (refreshStats = true) => {
      const seq = ++conversationListSeq.current;
      try {
        const key = JSON.stringify([listScope, agentFilter, searchQuery]);
        const includeStats = refreshStats || statsCache.current.key !== key || Date.now() >= statsCache.current.until;
        const page = await api.conversations({
          scope: listScope,
          agent: agentFilter ?? "",
          q: searchQuery,
          limit: visibleConversationCount,
          stats: includeStats ? 1 : 0,
        });
        const items = page.conversations;
        if (seq === conversationListSeq.current) {
          runningListRef.current = page.running;
          if (includeStats) {
            setListMeta((current) => (sameSnapshot(current, page) ? current : page));
            statsCache.current = { key, until: Date.now() + 30000 };
          }
          if (!listLoadedRef.current) {
            const id = Number(new URLSearchParams(window.location.search).get("c"));
            if (items.some((c) => c.id === id && c.source === "automation")) setListScope("automation");
            listLoadedRef.current = true;
          }
          setConvs((current) => reconcileList(current, items, (conversation) => String(conversation.id)));
          setConvsLoaded(true);
          const id = selectedIdRef.current;
          if (id !== null) {
            const loaded = items.find((conversation) => conversation.id === id);
            if (loaded) setSelectedFallback(loaded);
            else if (refreshStats) {
              const conversation = await api.getConversation(id);
              if (seq === conversationListSeq.current && selectedIdRef.current === id)
                setSelectedFallback(conversation);
            }
          }
        }
      } catch {
        // Preserve the selected transcript and list on a transient poll failure.
      }
    },
    [listScope, agentFilter, searchQuery, visibleConversationCount],
  );
  React.useEffect(() => {
    api
      .agents()
      .then(setAgents)
      .catch(() => {
        /* Agent metadata is optional for rendering an existing conversation. */
      });
    api
      .llmProfiles()
      .then(setProfiles)
      .catch(() => {
        /* The conversation remains usable without profile labels. */
      });
  }, []);

  // One list poll supplies runtime state for all sidebar rows, including the
  // unselected ones. Await completion so slow requests do not overlap, and back
  // off to 10s when idle or skip while hidden.
  React.useEffect(() => {
    let disposed = false;
    let inFlight = false;
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      if (disposed || inFlight || document.hidden) return;
      inFlight = true;
      try {
        await reloadConvs(false);
      } finally {
        inFlight = false;
        if (!disposed && !document.hidden) timer = setTimeout(() => void poll(), runningListRef.current ? 2000 : 10000);
      }
    }
    const onVisibilityChange = () => {
      clearTimeout(timer);
      if (!document.hidden && !disposed) void poll();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    void poll();
    return () => {
      disposed = true;
      conversationListSeq.current++;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [reloadConvs]);

  React.useEffect(() => {
    const raw = getLocalStorageValue(COLLAPSED_AGENTS_KEY);
    if (!raw) return;
    try {
      const keys = JSON.parse(raw);
      if (Array.isArray(keys))
        setCollapsedAgents(new Set(keys.filter((key): key is string => typeof key === "string")));
    } catch {
      // Corrupted entry → start with everything expanded.
    }
  }, []);

  const toggleAgentCollapsed = React.useCallback((key: string) => {
    setCollapsedAgents((current) => {
      const next = new Set(current);
      if (!next.delete(key)) next.add(key);
      setLocalStorageValue(COLLAPSED_AGENTS_KEY, JSON.stringify([...next]));
      return next;
    });
  }, []);

  // Restore the open conversation from the URL (?c=<id>) on mount, so a refresh
  // returns to the same thread instead of the empty draft view. Runs after
  // hydration (not a lazy useState init) to avoid a server/client mismatch.
  React.useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    setSourceRequested(params.has("approval"));
    const c = params.get("c");
    const id = c ? Number(c) : NaN;
    if (Number.isFinite(id)) setSelectedId(id);
  }, []);
  // Mirror the current selection into the URL (replaceState → no history spam).
  React.useEffect(() => {
    const url = new URL(window.location.href);
    if (selectedId != null) url.searchParams.set("c", String(selectedId));
    else url.searchParams.delete("c");
    window.history.replaceState(null, "", url);
  }, [selectedId]);

  const selected = React.useMemo(
    () => convs.find((c) => c.id === selectedId) ?? (selectedFallback?.id === selectedId ? selectedFallback : null),
    [convs, selectedId, selectedFallback],
  );
  const agentByKey = React.useMemo(() => new Map(agents.map((agent) => [agent.key, agent])), [agents]);
  const filteredConversations = convs;
  const visibleConversations = React.useMemo(
    () => filteredConversations.slice(0, visibleConversationCount),
    [filteredConversations, visibleConversationCount],
  );
  // Pinned rows stay a flat block above the groups (server order = pinned_at DESC);
  // everything else is bucketed per agent (or originating task for automations).
  const pinnedConversations = React.useMemo(
    () => visibleConversations.filter(conversationIsPinned),
    [visibleConversations],
  );
  const agentGroups = React.useMemo(() => {
    const label = (task?: number) =>
      task ? translate("opt.taskExecutions", { id: task }) : translate("opt.otherExecutions");
    const totals = new Map(
      groupByAgent(
        filteredConversations.filter((c) => !conversationIsPinned(c)),
        agentByKey,
        translate,
        label,
      ).map((group) => [group.key, group]),
    );
    return groupByAgent(
      visibleConversations.filter((c) => !conversationIsPinned(c)),
      agentByKey,
      translate,
      label,
    ).map((group) => ({
      ...group,
      totalCount: listMeta?.groups[group.key]?.total ?? totals.get(group.key)?.conversations.length,
      runningCount: listMeta?.groups[group.key]?.running ?? totals.get(group.key)?.runningCount ?? group.runningCount,
    }));
  }, [visibleConversations, filteredConversations, agentByKey, translate, listMeta]);
  // conversation agents: custom agents + conversational built-ins (role=assistant).
  // The orchestration built-ins (goals/planner/mainagent/worker) are task-specific.
  const chatAgents = React.useMemo(() => agents.filter((a) => !a.builtin || a.role === "assistant"), [agents]);
  const agentFilterOptions = React.useMemo(() => {
    const counts = new Map<string, number>(Object.entries(listMeta?.agents ?? {}));
    if (!listMeta)
      for (const conversation of convs) {
        counts.set(conversation.agent_key, (counts.get(conversation.agent_key) ?? 0) + 1);
      }
    // Include historical sources even if their Agent has since been disabled or deleted.
    const keys = new Set([...chatAgents.map((agent) => agent.key), ...counts.keys()]);
    if (agentFilter !== null) keys.add(agentFilter);
    return [...keys]
      .map((key) => ({ key, name: agentByKey.get(key)?.name || key, count: counts.get(key) ?? 0 }))
      .sort((a, b) => a.name.localeCompare(b.name, "zh-CN"));
  }, [convs, chatAgents, agentByKey, agentFilter, listMeta]);
  const conversationCountLabel = translate("opt.listCount", {
    count: listMeta?.total ?? filteredConversations.length,
    total: listMeta?.all_total ?? convs.length,
  });

  function changeListScope(value: "manual" | "automation" | "all") {
    setListScope(value);
    setVisibleConversationCount(CONVERSATION_LIST_PAGE);
    setSelectedConversationIds(new Set());
    setRenamingId(null);
  }

  function changeAgentFilter(key: string | null) {
    setAgentFilter(key);
    setVisibleConversationCount(CONVERSATION_LIST_PAGE);
    setSelectedConversationIds(new Set());
    setBulkDeleteOpen(false);
    setRenamingId(null);
  }

  const selectedConversationCount = selectedConversationIds.size;
  const allConversationsSelected =
    filteredConversations.length > 0 && selectedConversationCount === (listMeta?.total ?? filteredConversations.length);
  const someConversationsSelected = selectedConversationCount > 0 && !allConversationsSelected;
  let conversationHeaderChecked: boolean | "indeterminate" = false;
  if (allConversationsSelected) conversationHeaderChecked = true;
  else if (someConversationsSelected) conversationHeaderChecked = "indeterminate";

  const toggleConversationSelected = React.useCallback((id: number, checked: boolean) => {
    setSelectedConversationIds((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  function toggleAllConversations(checked: boolean) {
    if (!checked) {
      setSelectedConversationIds(new Set());
      return;
    }
    const filter = selectionFilterRef.current;
    void api
      .conversationIDs({ scope: listScope, agent: agentFilter ?? "", q: searchQuery })
      .then((ids) => {
        if (selectionFilterRef.current === filter) setSelectedConversationIds(new Set(ids));
      })
      .catch((error) => toast.error(localizedError(error, translate)));
  }

  function exitSelectionMode() {
    setSelectionMode(false);
    setSelectedConversationIds(new Set());
  }

  const del = React.useCallback(
    async (id: number) => {
      try {
        await api.deleteConversation(id);
        setSelectedId((current) => (current === id ? null : current));
        setSelectedConversationIds((current) => {
          if (!current.has(id)) return current;
          const next = new Set(current);
          next.delete(id);
          return next;
        });
        void reloadConvs();
      } catch (e) {
        toast.error(translate("overview.deleteFailedPrefix") + localizedError(e, translate));
      }
    },
    [reloadConvs, translate],
  );

  async function deleteSelectedConversations() {
    const ids = [...selectedConversationIds];
    if (ids.length === 0 || bulkDeleting) return;
    setBulkDeleting(true);
    const deleted = new Set<number>();
    const failed: { id: number; error: string }[] = [];
    try {
      for (let offset = 0; offset < ids.length; offset += 100) {
        const result = await api.deleteConversations(ids.slice(offset, offset + 100));
        for (const item of result.items) {
          if (item.ok) deleted.add(item.id);
          else failed.push({ id: item.id, error: item.error ?? translate("overview.conversationNotFound") });
        }
      }
      if (deleted.has(selectedId ?? -1)) selectConversation(null);
      setSelectedConversationIds((current) => {
        const next = new Set(current);
        for (const id of deleted) next.delete(id);
        return next;
      });
      if (deleted.size > 0) toast.success(translate("overview.conversationsDeleted", { count: deleted.size }));
      if (failed.length > 0) {
        const details = failed
          .slice(0, 3)
          .map((item) => `#${item.id}\uff08${item.error}\uff09`)
          .join("\uff1b");
        toast.error(
          translate("overview.conversationsDeleteFailed", {
            count: failed.length,
            details: details,
            more: failed.length > 3 ? translate("overview.moreSuffix") : "",
          }),
        );
      }
      setBulkDeleteOpen(false);
      // Fully successful → return to the clean list; keep selection mode on if
      // some failed so the user can retry the remaining ones.
      if (failed.length === 0) setSelectionMode(false);
      void reloadConvs();
    } catch (error) {
      toast.error(translate("overview.bulkDeleteFailed", { message: localizedError(error, translate) }));
      void reloadConvs();
    } finally {
      setBulkDeleting(false);
    }
  }

  const togglePinned = React.useCallback(
    async (conversation: Conversation) => {
      const pinned = conversationIsPinned(conversation);
      try {
        await api.pinConversation(conversation.id, !pinned);
        void reloadConvs();
      } catch (e) {
        toast.error(
          translate("overview.pinFailed", {
            action: translate(pinned ? "overview.unpin" : "overview.pin"),
            message: localizedError(e, translate),
          }),
        );
      }
    },
    [reloadConvs, translate],
  );

  const startRename = React.useCallback((c: Conversation) => {
    setRenamingId(c.id);
    setRenameText(c.title || "");
  }, []);
  const commitRename = React.useCallback(
    async (id: number, value: string) => {
      const title = value.trim();
      setRenamingId(null);
      if (!title) return;
      try {
        await api.renameConversation(id, title);
        void reloadConvs();
      } catch (e) {
        toast.error(translate("overview.renameFailedPrefix") + localizedError(e, translate));
      }
    },
    [reloadConvs, translate],
  );
  const cancelRename = React.useCallback(() => setRenamingId(null), []);

  return (
    <div
      data-content-padding="false"
      className="flex h-[calc(100svh-3rem)] min-w-0 flex-col overflow-hidden p-3 sm:p-4 md:h-[calc(100svh-4rem)] md:p-6"
    >
      <div className="grid min-h-0 min-w-0 flex-1 grid-cols-1 grid-rows-[minmax(10rem,15rem)_minmax(0,1fr)] gap-3 md:grid-cols-[18rem_minmax(0,1fr)] md:grid-rows-[minmax(0,1fr)] md:gap-4">
        {/* left: conversation list */}
        <div className="bg-card flex flex-col overflow-hidden rounded-lg border">
          <div className="flex flex-col gap-2 border-b p-2">
            <Button size="sm" className="w-full" onClick={() => selectConversation(null)}>
              <PlusIcon />
              {translate("overview.createConversation")}
            </Button>
            <fieldset
              aria-label={translate("opt.conversationScope")}
              className="m-0 grid min-w-0 grid-cols-3 gap-0.5 rounded-md border-0 bg-muted p-0.5 text-muted-foreground text-xs"
            >
              {(
                [
                  ["manual", translate("opt.chats")],
                  ["automation", translate("opt.executions")],
                  ["all", translate("common.all")],
                ] as const
              ).map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  aria-pressed={listScope === value}
                  onClick={() => changeListScope(value)}
                  className={cn(
                    "truncate rounded-sm px-1.5 py-1 font-medium transition-colors",
                    listScope === value ? "bg-background text-foreground shadow-xs" : "hover:text-foreground",
                  )}
                >
                  {label}
                </button>
              ))}
            </fieldset>
            <div className="relative">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
              <Input
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setVisibleConversationCount(CONVERSATION_LIST_PAGE);
                  setSelectedConversationIds(new Set());
                }}
                placeholder={translate("opt.conversationSearch")}
                aria-label={translate("opt.conversationSearch")}
                className="h-8 pr-7 pl-8 text-xs"
              />
              {search && (
                <button
                  type="button"
                  aria-label={translate("common.close")}
                  onClick={() => {
                    setSearch("");
                    setVisibleConversationCount(CONVERSATION_LIST_PAGE);
                    setSelectedConversationIds(new Set());
                  }}
                  className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
                >
                  <XIcon className="size-3.5" />
                </button>
              )}
            </div>
            <Select
              value={agentFilter === null ? "all" : `agent:${agentFilter}`}
              onValueChange={(value) => changeAgentFilter(value === "all" ? null : value.slice(6))}
              disabled={bulkDeleting}
            >
              <SelectTrigger size="sm" className="w-full min-w-0" aria-label={translate("overview.filterAgents")}>
                <Bot />
                <SelectValue placeholder={translate("overview.allAgents")} />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">{translate("overview.allAgents")}</SelectItem>
                  {agentFilterOptions.map((agent) => (
                    <SelectItem key={agent.key} value={`agent:${agent.key}`}>
                      {agentDisplayName(agent, translate)} ({agent.count})
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            {(listMeta?.all_total ?? convs.length) > 0 &&
              (selectionMode ? (
                <div className="flex items-center gap-2 px-1">
                  <Checkbox
                    checked={conversationHeaderChecked}
                    onCheckedChange={(checked) => toggleAllConversations(checked === true)}
                    aria-label={translate("overview.selectFilteredConversations")}
                    disabled={filteredConversations.length === 0 || bulkDeleting}
                  />
                  <span className="text-muted-foreground min-w-0 flex-1 text-xs tabular-nums">
                    {selectedConversationCount > 0
                      ? translate("overview.selectedConversations", { count: selectedConversationCount })
                      : conversationCountLabel}
                  </span>
                  {selectedConversationCount > 0 && (
                    <Button
                      size="sm"
                      variant="destructive"
                      disabled={bulkDeleting}
                      onClick={() => setBulkDeleteOpen(true)}
                    >
                      <Trash2Icon data-icon="inline-start" />
                      {translate("overview.delete")}
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" onClick={exitSelectionMode}>
                    {translate("overview.done")}
                  </Button>
                </div>
              ) : (
                <div className="flex items-center gap-2 px-1">
                  <span className="text-muted-foreground min-w-0 flex-1 text-xs tabular-nums">
                    {conversationCountLabel}
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-muted-foreground"
                    onClick={() => setSelectionMode(true)}
                    disabled={filteredConversations.length === 0}
                  >
                    <ListChecksIcon data-icon="inline-start" />
                    {translate("overview.selectMultiple")}
                  </Button>
                </div>
              ))}
          </div>
          <ScrollArea
            key={`${listScope}:${agentFilter ?? "all"}:${search}`}
            type="auto"
            className="min-h-0 min-w-0 flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!"
          >
            <div className="flex min-w-0 flex-col gap-0.5 p-2">
              {filteredConversations.length === 0 && (
                <p className="text-muted-foreground px-2 py-6 text-center text-xs">
                  {agentFilter === null
                    ? translate("overview.emptyConversations")
                    : translate("overview.emptyAgentConversations")}
                </p>
              )}
              {pinnedConversations.map((c) => (
                <ConversationItem
                  key={c.id}
                  conv={c}
                  agent={agentByKey.get(c.agent_key)}
                  active={selectedId === c.id}
                  renaming={renamingId === c.id}
                  renameText={renamingId === c.id ? renameText : ""}
                  onSelect={selectConversation}
                  onStartRename={startRename}
                  onRenameText={setRenameText}
                  onCommitRename={commitRename}
                  onCancelRename={cancelRename}
                  onTogglePinned={togglePinned}
                  onDelete={del}
                  selectionMode={selectionMode}
                  selectedForDelete={selectedConversationIds.has(c.id)}
                  onSelectedForDeleteChange={toggleConversationSelected}
                />
              ))}
              {agentGroups.map((group) => {
                const collapsed = collapsedAgents.has(group.key);
                return (
                  <React.Fragment key={group.key}>
                    <AgentGroupHeader
                      group={group}
                      collapsed={collapsed}
                      hasActive={group.conversations.some((c) => c.id === selectedId)}
                      onToggle={toggleAgentCollapsed}
                    />
                    {!collapsed &&
                      group.conversations.map((c) => (
                        <ConversationItem
                          key={c.id}
                          conv={c}
                          agent={agentByKey.get(c.agent_key)}
                          showAgent={group.key.startsWith("task:")}
                          active={selectedId === c.id}
                          renaming={renamingId === c.id}
                          renameText={renamingId === c.id ? renameText : ""}
                          onSelect={selectConversation}
                          onStartRename={startRename}
                          onRenameText={setRenameText}
                          onCommitRename={commitRename}
                          onCancelRename={cancelRename}
                          onTogglePinned={togglePinned}
                          onDelete={del}
                          selectionMode={selectionMode}
                          selectedForDelete={selectedConversationIds.has(c.id)}
                          onSelectedForDeleteChange={toggleConversationSelected}
                        />
                      ))}
                  </React.Fragment>
                );
              })}
              {visibleConversationCount < (listMeta?.total ?? filteredConversations.length) && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="mt-1 w-full"
                  onClick={() => setVisibleConversationCount((count) => count + CONVERSATION_LIST_PAGE)}
                >
                  {translate("overview.loadMore")}
                </Button>
              )}
            </div>
          </ScrollArea>
        </div>

        {/* right: chat view */}
        <div className="bg-card flex min-w-0 flex-col overflow-hidden rounded-lg border">
          {selected ? (
            <ChatView
              key={selected.id}
              conv={selected}
              agents={agents}
              profiles={profiles}
              initial={pendingByConv[selected.id]}
              onTitleMaybeChanged={reloadConvs}
              onConvUpdated={reloadConvs}
            />
          ) : sourceRequested ? (
            <div role="status" className="p-6 text-sm text-muted-foreground">
              {convsLoaded ? translate("overview.conversationDeleted") : translate("overview.loadingConversation")}
            </div>
          ) : (
            <DraftChat
              agents={chatAgents}
              profiles={profiles}
              onStarted={(c, pending) => {
                if (listScope === "automation") setListScope("manual");
                if (agentFilter !== null && agentFilter !== c.agent_key) changeAgentFilter(null);
                // Insert the new conversation immediately so `selected` resolves to
                // it on this render (switching to ChatView right away, before the
                // async reloadConvs lands); reloadConvs then reconciles titles etc.
                if (pending) setPendingByConv((p) => ({ ...p, [c.id]: pending }));
                setConvs((prev) => {
                  if (prev.some((item) => item.id === c.id)) return prev;
                  const firstUnpinned = prev.findIndex((item) => !conversationIsPinned(item));
                  const insertAt = firstUnpinned < 0 ? prev.length : firstUnpinned;
                  return [...prev.slice(0, insertAt), c, ...prev.slice(insertAt)];
                });
                setSelectedId(c.id);
                void reloadConvs();
              }}
            />
          )}
        </div>
      </div>
      <AlertDialog open={bulkDeleteOpen} onOpenChange={setBulkDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {translate("overview.deleteSelected", { count: selectedConversationCount })}
            </AlertDialogTitle>
            <AlertDialogDescription>{translate("overview.deleteConversationHelp")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={bulkDeleting}>{translate("overview.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={bulkDeleting || selectedConversationCount === 0}
              onClick={(event) => {
                event.preventDefault();
                void deleteSelectedConversations();
              }}
            >
              {bulkDeleting && <Loader2Icon data-icon="inline-start" className="animate-spin" />}
              {bulkDeleting ? translate("overview.deleting") : translate("overview.confirmDelete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
