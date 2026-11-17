"use client";

import * as React from "react";
import { ChevronDownIcon, CpuIcon, Loader2Icon, PlusIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { localizedError } from "@/i18n/errors";
import { useUITranslation } from "@/i18n/use-ui-translation";
import { api } from "@/lib/api";
import { type LLMProfile, MAX_TASK_WORKER_COUNT, type WorkerPool } from "@/lib/types";

type Row = { profile_id: string; workers: string };

export function WorkerPoolsPanel({ profiles }: { profiles: LLMProfile[] }) {
  const { ui, liveUI, i18n } = useUITranslation();
  const [open, setOpen] = React.useState(false);
  const [ready, setReady] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [rows, setRows] = React.useState<Row[]>([]);
  const [savedPools, setSavedPools] = React.useState<WorkerPool[]>([]);
  const [workers, setWorkers] = React.useState(3);
  const [error, setError] = React.useState("");

  const apply = React.useCallback((pools: WorkerPool[], count: number) => {
    setSavedPools(pools);
    setRows(pools.map((pool) => ({ profile_id: pool.profile_id, workers: String(pool.workers) })));
    setWorkers(count);
    setReady(true);
  }, []);

  const load = React.useCallback(async () => {
    setReady(false);
    setError("");
    try {
      const settings = await api.settings();
      apply(settings.worker_pools, settings.workers ?? 3);
      if (settings.worker_pools.length || window.location.hash === "#worker-pools") setOpen(true);
    } catch {
      setError(liveUI("settings.workerConfigLockedHelp"));
      setOpen(true);
    }
  }, [apply, liveUI]);
  React.useEffect(() => {
    void load();
  }, [load]);

  const persist = async (pools: WorkerPool[]) => {
    if (!ready || saving) return;
    setSaving(true);
    setError("");
    try {
      const settings = await api.setSettings({ worker_pools: pools });
      apply(settings.worker_pools, settings.workers ?? 3);
      toast.success(ui(pools.length ? "settings.workerPoolsSaved" : "settings.workerPoolsCleared"));
    } catch (failure) {
      setError(localizedError(failure, i18n.t));
    } finally {
      setSaving(false);
    }
  };

  const save = () => {
    const counts = rows.map((row) => Number(row.workers.trim()));
    if (rows.some((row) => !row.profile_id)) return setError(ui("settings.choosePoolProfile"));
    if (counts.some((count) => !Number.isInteger(count) || count <= 0))
      return setError(ui("settings.positivePoolWorkers"));
    if (counts.reduce((sum, count) => sum + count, 0) > MAX_TASK_WORKER_COUNT)
      return setError(ui("settings.workerLimit"));
    if (new Set(rows.map((row) => row.profile_id)).size !== rows.length)
      return setError(ui("settings.duplicatePoolProfile"));
    void persist(rows.map((row, index) => ({ profile_id: row.profile_id, workers: counts[index] })));
  };

  return (
    <Card id="worker-pools" className="mt-4">
      <CardContent className="pt-4">
        <Collapsible open={open} onOpenChange={setOpen}>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" className="w-full justify-between">
              <span className="flex items-center gap-2">
                <CpuIcon className="size-4" />
                {ui("settings.workerPools")}
              </span>
              <ChevronDownIcon className="size-4" />
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="space-y-3 pt-3">
            <p className="text-muted-foreground text-xs leading-relaxed">{ui("settings.workerPoolsHelp")}</p>
            {error ? (
              <p role="alert" className="text-destructive text-xs">
                {error}
              </p>
            ) : null}
            {!ready ? (
              <Button variant="outline" size="sm" onClick={() => void load()}>
                {ui("settings.reload")}
              </Button>
            ) : null}
            {rows.map((row, index) => {
              const selected = profiles.find((profile) => profile.id === row.profile_id);
              return (
                // biome-ignore lint/suspicious/noArrayIndexKey: the pool rows are controlled and have no independent local state.
                <div key={`${row.profile_id}-${index}`} className="flex items-center gap-2">
                  <Select
                    value={row.profile_id || undefined}
                    disabled={!ready || saving}
                    onValueChange={(value) => {
                      setRows((current) =>
                        current.map((item, i) => (i === index ? { ...item, profile_id: value } : item)),
                      );
                    }}
                  >
                    <SelectTrigger className="min-w-0 flex-1" aria-label={ui("settings.chooseProfile")}>
                      <SelectValue placeholder={ui("settings.chooseProfile")} />
                    </SelectTrigger>
                    <SelectContent>
                      {!selected && row.profile_id ? (
                        <SelectItem value={row.profile_id}>
                          {ui("settings.unavailableProfile", { id: row.profile_id })}
                        </SelectItem>
                      ) : null}
                      {profiles
                        .filter((profile) => profile.format !== "jev")
                        .map((profile) => (
                          <SelectItem key={profile.id} value={profile.id}>
                            {profile.name} · {profile.model}
                          </SelectItem>
                        ))}
                    </SelectContent>
                  </Select>
                  <Input
                    className="w-24"
                    inputMode="numeric"
                    aria-label={ui("settings.workers")}
                    value={row.workers}
                    disabled={!ready || saving}
                    onChange={(event) =>
                      setRows((current) =>
                        current.map((item, i) => (i === index ? { ...item, workers: event.target.value } : item)),
                      )
                    }
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={ui("settings.delete")}
                    disabled={!ready || saving}
                    onClick={() => setRows((current) => current.filter((_, i) => i !== index))}
                  >
                    <Trash2Icon className="size-4" />
                  </Button>
                </div>
              );
            })}
            <p className="text-muted-foreground text-xs">
              {rows.length
                ? ui("settings.workerTotal", { count: rows.reduce((sum, row) => sum + (Number(row.workers) || 0), 0) })
                : savedPools.length
                  ? ui("settings.workerPoolsUnsavedClearHint")
                  : ui("settings.workerPoolsEmptyHint", { workers })}
            </p>
            <div className="flex items-center justify-between gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={!ready || saving}
                onClick={() => setRows((current) => [...current, { profile_id: "", workers: "1" }])}
              >
                <PlusIcon className="size-4" />
                {ui("settings.addPool")}
              </Button>
              <div className="flex gap-2">
                {savedPools.length > 0 ? (
                  <Button size="sm" variant="outline" disabled={!ready || saving} onClick={() => void persist([])}>
                    {ui("settings.clearPools")}
                  </Button>
                ) : null}
                <Button size="sm" disabled={!ready || saving || (!rows.length && !savedPools.length)} onClick={save}>
                  {saving ? <Loader2Icon className="size-4 animate-spin" /> : null}
                  {ui("settings.save")}
                </Button>
              </div>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </CardContent>
    </Card>
  );
}
