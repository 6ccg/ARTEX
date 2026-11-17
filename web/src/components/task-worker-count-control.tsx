"use client";

import * as React from "react";
import { CpuIcon, Loader2Icon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { localizedError } from "@/i18n/errors";
import { useUITranslation } from "@/i18n/use-ui-translation";
import { api } from "@/lib/api";
import { MAX_TASK_WORKER_COUNT, parseWorkerCountInput, type Task } from "@/lib/types";

export function TaskWorkerCountControl({ task, onUpdated }: { task: Task; onUpdated: (task: Task) => void }) {
  const { ui, i18n } = useUITranslation();
  const [open, setOpen] = React.useState(false);
  const [draft, setDraft] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState("");
  const inputId = React.useId();
  const saved = task.worker_count ?? 0;
  const terminal = ["done", "failed", "timeout"].includes(task.status);

  const save = async (count: number) => {
    if (saving || terminal) return;
    setSaving(true);
    setError("");
    try {
      const updated = await api.updateTask(task.id, { worker_count: count });
      onUpdated(updated);
      setOpen(false);
      toast.success(count ? ui("taskUI.workerOverrideSaved", { workers: count }) : ui("taskUI.workerResetGlobalSaved"));
    } catch (failure) {
      setError(localizedError(failure, i18n.t));
    } finally {
      setSaving(false);
    }
  };

  const submit = () => {
    const count = parseWorkerCountInput(draft);
    if (count === null) {
      setError(ui("taskUI.invalidWorkers", { max: MAX_TASK_WORKER_COUNT }));
      return;
    }
    void save(count);
  };

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        if (saving) return;
        if (next) {
          setDraft(saved > 0 ? String(saved) : "");
          setError("");
        }
        setOpen(next);
      }}
    >
      <PopoverTrigger asChild>
        <Button size="sm" variant="outline" title={ui("taskUI.workerConcurrency")}>
          <CpuIcon className="size-4" />
          {saved > 0 ? ui("taskUI.workerTriggerOverride", { workers: saved }) : ui("taskUI.workerTriggerGlobal")}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-96 space-y-3 p-4">
        <div className="space-y-1">
          <h2 className="font-semibold">{ui("taskUI.workerConcurrency")}</h2>
          <p className="text-muted-foreground text-xs">
            {saved > 0 ? ui("taskUI.workerModeOverride", { workers: saved }) : ui("taskUI.workerModeGlobal")}
          </p>
        </div>
        <div className="space-y-2">
          <Label htmlFor={inputId}>{ui("taskUI.workerCountInputLabel")}</Label>
          <Input
            id={inputId}
            inputMode="numeric"
            value={draft}
            disabled={saving || terminal}
            placeholder={ui("taskUI.workerCountInputPlaceholder")}
            aria-invalid={Boolean(error)}
            onChange={(event) => {
              setDraft(event.target.value);
              setError("");
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.nativeEvent.isComposing) {
                event.preventDefault();
                submit();
              }
            }}
          />
          {error ? (
            <p role="alert" className="text-destructive text-xs">
              {error}
            </p>
          ) : null}
        </div>
        <p className="text-muted-foreground text-xs leading-relaxed">
          {terminal ? ui("taskUI.workerTerminalReadonly") : ui("taskUI.workerLiveHelp")}
        </p>
        <div className="flex items-center justify-between gap-2">
          <Button size="sm" variant="ghost" disabled={saving || terminal || saved === 0} onClick={() => void save(0)}>
            {ui("taskUI.workerFollowGlobalAction")}
          </Button>
          <Button size="sm" disabled={saving || terminal} onClick={submit}>
            {saving ? <Loader2Icon className="size-4 animate-spin" /> : null}
            {ui("taskUI.save")}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}
