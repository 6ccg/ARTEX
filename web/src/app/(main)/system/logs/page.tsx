"use client";
import * as React from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useUITranslation } from "@/i18n/use-ui-translation";
import { http, openSSE } from "@/lib/api";
import { appendLiveLog, prependLogHistory } from "@/lib/log-window";
import { MOCK } from "@/lib/mock/enabled";
import type { LogLine } from "@/lib/types";
import { cn } from "@/lib/utils";

// Mock demo：无后端 SSE，塞几行示例日志。
const MOCK_LOGS: LogLine[] = [
  {
    seq: 1,
    ts: "2026-07-26T03:55:00Z",
    level: "info",
    tag: "engine",
    text: "ARTEX v0.1.0 backend listening on :8787 (workers=3)",
  },
  {
    seq: 2,
    ts: "2026-07-26T03:55:01Z",
    level: "info",
    tag: "config",
    text: "LLM configured from DB: anthropic / claude-opus-4-8",
  },
  {
    seq: 3,
    ts: "2026-07-26T03:56:10Z",
    level: "info",
    tag: "planner",
    text: "task t-acme-web: 第 3 轮规划，生成意图 i-4",
  },
  {
    seq: 4,
    ts: "2026-07-26T03:57:00Z",
    level: "warn",
    tag: "guard",
    text: "block bash: 目标越界 out.evil.example 不在 scope 内",
  },
  {
    seq: 5,
    ts: "2026-07-26T03:57:30Z",
    level: "info",
    tag: "work#1",
    text: "report_finding: Default Credentials (high) 已落库",
  },
  {
    seq: 6,
    ts: "2026-07-26T03:58:20Z",
    level: "error",
    tag: "work#3",
    text: "intercept: mysqldump 命中破坏性规则，等待人工审批",
  },
];

const levelTone: Record<LogLine["level"], string> = {
  info: "text-foreground/80",
  warn: "text-amber-600 dark:text-amber-400",
  error: "text-red-600 dark:text-red-400",
};
const levelDot: Record<LogLine["level"], string> = {
  info: "bg-muted-foreground/40",
  warn: "bg-amber-500",
  error: "bg-red-500",
};

function fmtTime(ts: string) {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export default function LogsPage() {
  const { ui } = useUITranslation();

  const [lines, setLines] = React.useState<LogLine[]>([]);
  const [q, setQ] = React.useState("");
  const [level, setLevel] = React.useState<"all" | LogLine["level"]>("all");
  const [paused, setPaused] = React.useState(false);
  const [loadingHistory, setLoadingHistory] = React.useState(false);
  const [hasMore, setHasMore] = React.useState(false);
  const liveLines = React.useRef<LogLine[]>([]);
  const viewport = React.useRef<HTMLDivElement>(null);
  const restoreScroll = React.useRef<{ key: string; top: number } | null>(null);
  const historyLoading = React.useRef(false);
  React.useLayoutEffect(() => {
    if (lines.length && restoreScroll.current && viewport.current) {
      const anchor = viewport.current.querySelector(`[data-log-key="${restoreScroll.current.key}"]`);
      if (anchor) viewport.current.scrollTop += anchor.getBoundingClientRect().top - restoreScroll.current.top;
      restoreScroll.current = null;
    }
  }, [lines]);
  const bottom = React.useRef<HTMLDivElement>(null);
  const stick = React.useRef(true);
  const pausedRef = React.useRef(false);
  pausedRef.current = paused;

  // Minimum db_id seen — used as the cursor for loading older history.
  const minDbId = React.useMemo(() => {
    let min = 0;
    for (const l of lines) {
      if (l.db_id && (min === 0 || l.db_id < min)) min = l.db_id;
    }
    return min;
  }, [lines]);

  // Live tail via SSE. on connect, replays whatever is in the ring (includes
  // DB-restored history from the last 100 rows written before restart).
  React.useEffect(() => {
    if (MOCK) {
      liveLines.current = MOCK_LOGS;
      setLines(MOCK_LOGS);
      return;
    }
    const es = openSSE("/api/logs/stream?since=0");
    es.onmessage = (e) => {
      try {
        const l = JSON.parse(e.data) as LogLine;
        liveLines.current = appendLiveLog(liveLines.current, l);
        if (!pausedRef.current) setLines(liveLines.current);
      } catch {
        /* ignore malformed frame */
      }
    };
    return () => es.close();
  }, []);

  // Mark "has more" once we have any db_id in view.
  React.useEffect(() => {
    if (!paused && minDbId > 1) setHasMore(true);
  }, [minDbId, paused]);

  async function loadOlderHistory() {
    if (historyLoading.current) return;
    historyLoading.current = true;
    pausedRef.current = true;
    setPaused(true);
    stick.current = false;
    setLoadingHistory(true);
    try {
      const params = minDbId > 0 ? `?before=${minDbId}&limit=200` : `?limit=200`;
      const data = await http<{ items: LogLine[]; has_more: boolean }>(`/logs/history${params}`);
      if (data.items?.length) {
        // Assign synthetic seq numbers below current minimum to keep dedup working.
        if (viewport.current) {
          const top = viewport.current.getBoundingClientRect().top;
          const anchor = [...viewport.current.querySelectorAll<HTMLElement>("[data-log-key]")].find(
            (row) => row.getBoundingClientRect().bottom > top,
          );
          if (anchor)
            restoreScroll.current = { key: anchor.dataset.logKey ?? "", top: anchor.getBoundingClientRect().top };
        }
        setLines((prev) => {
          const minSeq = prev.reduce((m, l) => Math.min(m, l.seq ?? 0), 0);
          const older = data.items.map((l, i) => ({
            ...l,
            seq: minSeq - data.items.length + i,
          }));
          return prependLogHistory(prev, older);
        });
      }
      setHasMore(data.has_more ?? false);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : ui("auth.backendUnavailable"));
    } finally {
      historyLoading.current = false;
      setLoadingHistory(false);
    }
  }

  const filtered = React.useMemo(() => {
    const needle = q.trim().toLowerCase();
    return lines.filter(
      (l) =>
        (level === "all" || l.level === level) &&
        (!needle || l.text.toLowerCase().includes(needle) || l.tag.toLowerCase().includes(needle)),
    );
  }, [lines, q, level]);

  React.useEffect(() => {
    if (filtered.length && stick.current && !paused) bottom.current?.scrollIntoView();
  }, [filtered, paused]);

  const counts = React.useMemo(() => {
    let warn = 0,
      error = 0;
    for (const l of lines) {
      if (l.level === "warn") warn++;
      else if (l.level === "error") error++;
    }
    return { warn, error, total: lines.length };
  }, [lines]);

  return (
    <div className="flex flex-1 flex-col gap-3">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{ui("screen.366ada1d2fcfc4")}</h1>
        <p className="text-muted-foreground text-sm">{ui("screen.2420e315e38662")}</p>
      </div>
      <div className="flex flex-1 flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            placeholder={ui("screen.b154738eea7450")}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            className="h-8 max-w-xs"
          />
          <div className="flex gap-1">
            {(["all", "info", "warn", "error"] as const).map((lv) => (
              <Button
                key={lv}
                size="sm"
                variant={level === lv ? "default" : "outline"}
                className="h-8"
                onClick={() => setLevel(lv)}
              >
                {lv === "all" ? ui("screen.5c55a67935af8f") : lv}
              </Button>
            ))}
          </div>
          <Button
            size="sm"
            variant={paused ? "default" : "outline"}
            className="h-8"
            disabled={loadingHistory}
            onClick={() => {
              const next = !paused;
              pausedRef.current = next;
              setPaused(next);
              if (!next) {
                stick.current = true;
                setLines(liveLines.current);
                setHasMore(false);
              }
            }}
          >
            {paused ? ui("screen.eb0c326b60ae89") : ui("screen.8d12fc0d4eb260")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="h-8"
            disabled={loadingHistory}
            onClick={() => {
              liveLines.current = [];
              setLines([]);
              setHasMore(false);
            }}
          >
            {ui("screen.1ef3de06b32e52")}
          </Button>
          <span className="ml-auto text-xs text-muted-foreground">
            {counts.total} {ui("screen.7049a63d1e7231")}{" "}
            <span className="text-amber-600 dark:text-amber-400">
              {counts.warn} {ui("screen.a8b7a4480407ac")}
            </span>{" "}
            ·{" "}
            <span className="text-red-600 dark:text-red-400">
              {counts.error} {ui("screen.0bc1fb72ae1be5")}
            </span>
          </span>
        </div>

        <div
          ref={viewport}
          onScroll={(e) => {
            const el = e.currentTarget;
            stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          }}
          className="h-[calc(100vh-14rem)] overflow-auto rounded-lg border bg-card p-2 font-mono text-xs leading-relaxed"
        >
          {hasMore && (
            <div className="flex justify-center py-2">
              <Button
                size="sm"
                variant="outline"
                className="h-7 text-xs"
                disabled={loadingHistory}
                onClick={loadOlderHistory}
              >
                {loadingHistory ? ui("screen.4927a53bcc886a") : ui("screen.ee2719de652ef1")}
              </Button>
            </div>
          )}
          {filtered.length === 0 ? (
            <p className="py-10 text-center text-muted-foreground">{ui("screen.abfa829f432e87")}</p>
          ) : (
            filtered.map((l) => {
              const body =
                l.tag && l.text.startsWith("[" + l.tag + "]") ? l.text.slice(l.tag.length + 2).trimStart() : l.text;
              return (
                <div
                  key={l.seq}
                  data-log-key={l.db_id ? `db${l.db_id}` : `seq${l.seq}`}
                  className="flex items-start gap-2 px-1 py-0.5 hover:bg-muted/40"
                >
                  <span className={cn("mt-1.5 size-1.5 shrink-0 rounded-full", levelDot[l.level])} />
                  <span className="shrink-0 tabular-nums text-muted-foreground">{fmtTime(l.ts)}</span>
                  {l.tag && (
                    <span className="shrink-0 rounded bg-muted px-1 text-[10px] text-foreground/70">{l.tag}</span>
                  )}
                  <span className={cn("min-w-0 whitespace-pre-wrap break-words", levelTone[l.level])}>{body}</span>
                </div>
              );
            })
          )}
          <div ref={bottom} />
        </div>
      </div>
    </div>
  );
}
