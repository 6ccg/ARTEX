"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SortableHead } from "@/components/ui/sortable-head";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useUITranslation } from "@/i18n/use-ui-translation";
import type { TrafficRouteGroup } from "@/lib/types";
import { cn } from "@/lib/utils";

type TrafficSortField = "ts" | "status" | "resp_len";
type TrafficSort = { field: TrafficSortField; direction: "asc" | "desc" };

export function trafficPath(raw: string) {
  try {
    const url = new URL(raw);
    return `${url.pathname}${url.search}`;
  } catch {
    return raw;
  }
}

function fmtGroupBytes(n: number) {
  if (n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  const v = n / 1024 ** i;
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

function statusBadgeClass(code: number) {
  if (code >= 500) return "bg-red-500/15 text-red-700 dark:text-red-400";
  if (code >= 400) return "bg-amber-500/15 text-amber-700 dark:text-amber-400";
  if (code >= 300) return "bg-blue-500/15 text-blue-700 dark:text-blue-400";
  if (code >= 200) return "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400";
  return "bg-muted text-muted-foreground";
}

export function TrafficRouteGroups({
  groups,
  sort,
  onSort,
  onOpen,
  loading,
}: {
  groups: TrafficRouteGroup[];
  sort: TrafficSort;
  onSort: (field: TrafficSortField) => void;
  onOpen: (group: TrafficRouteGroup) => void;
  loading: boolean;
}) {
  const { ui, i18n } = useUITranslation();

  return (
    <Table>
      <TableHeader className="sticky top-0 z-10 bg-card">
        <TableRow>
          <TableHead className="w-24">{ui("screen.22b9f0b6621280")}</TableHead>
          <TableHead className="w-48">Host</TableHead>
          <TableHead>{ui("opt.requestPath")}</TableHead>
          <TableHead className="w-24 text-right">{ui("opt.captureCount")}</TableHead>
          <SortableHead
            field="status"
            label={ui("opt.statusDistribution")}
            activeField={sort.field}
            direction={sort.direction}
            onSort={onSort}
            className="w-52"
          />
          <SortableHead
            field="ts"
            label={ui("opt.lastCapture")}
            activeField={sort.field}
            direction={sort.direction}
            onSort={onSort}
            className="w-40"
          />
          <SortableHead
            field="resp_len"
            label={ui("opt.totalResponse")}
            activeField={sort.field}
            direction={sort.direction}
            onSort={onSort}
            align="right"
            className="w-28 text-right"
          />
        </TableRow>
      </TableHeader>
      <TableBody>
        {groups.map((g) => (
          <TableRow key={g.id} className="cursor-pointer" onClick={() => onOpen(g)}>
            <TableCell>
              <Badge className="shrink-0 font-mono">{g.method}</Badge>
            </TableCell>
            <TableCell className="max-w-48 truncate font-mono text-xs" title={g.host}>
              {g.host}
            </TableCell>
            <TableCell className="max-w-0">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 max-w-full justify-start px-1.5 font-mono text-xs"
                title={g.url}
                onClick={(event) => {
                  event.stopPropagation();
                  onOpen(g);
                }}
              >
                <span className="truncate">{trafficPath(g.url)}</span>
              </Button>
            </TableCell>
            <TableCell className="text-right font-semibold text-sm tabular-nums">{g.count}</TableCell>
            <TableCell>
              <div className="flex flex-wrap gap-1">
                {Object.entries(g.statuses)
                  .sort(([a], [b]) => Number(a) - Number(b))
                  .map(([code, count]) => (
                    <Badge
                      key={code}
                      variant="secondary"
                      className={cn("px-1.5 py-0 font-mono text-[11px] tabular-nums", statusBadgeClass(Number(code)))}
                    >
                      {code} × {count}
                    </Badge>
                  ))}
              </div>
            </TableCell>
            <TableCell className="text-muted-foreground text-xs tabular-nums">
              {new Date(g.last_ts * 1000).toLocaleString(i18n.resolvedLanguage, {
                month: "2-digit",
                day: "2-digit",
                hour: "2-digit",
                minute: "2-digit",
                hour12: false,
              })}
            </TableCell>
            <TableCell className="text-right text-xs tabular-nums">{fmtGroupBytes(g.resp_len)}</TableCell>
          </TableRow>
        ))}
        {groups.length === 0 && (
          <TableRow>
            <TableCell colSpan={7} className="py-12 text-center text-muted-foreground text-sm">
              {ui(loading ? "common.loading" : "screen.2e8f51bea742fa")}
            </TableCell>
          </TableRow>
        )}
      </TableBody>
    </Table>
  );
}
