"use client";

import * as React from "react";

import type { TFunction } from "i18next";
import { Globe2Icon, Loader2Icon, RefreshCwIcon, XIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { usePolling } from "@/hooks/use-polling";
import { useLiveTranslation } from "@/i18n/use-live-translation";
import { api } from "@/lib/api";
import type { EgressCheck, EgressReport } from "@/lib/types";
import { cn } from "@/lib/utils";

type EgressIpContextValue = {
  enabled: boolean;
  isLoading: boolean;
  report: EgressReport | null;
  error: string | null;
  setEnabled: (enabled: boolean) => Promise<void>;
  probeEgress: () => Promise<EgressReport>;
  dismiss: () => Promise<void>;
};

const EgressIpContext = React.createContext<EgressIpContextValue | null>(null);

export function EgressIpProvider({ children }: { children: React.ReactNode }) {
  const [enabled, setEnabledState] = React.useState(false);
  const [report, setReport] = React.useState<EgressReport | null>(null);
  const [error, setError] = React.useState<string | null>(null);
  const [isLoading, setIsLoading] = React.useState(false);
  const probeSequence = React.useRef(0);
  const inFlight = React.useRef<{ sequence: number; promise: Promise<EgressReport> } | null>(null);

  // Include the settings read in single-flight protection: manual and scheduled
  // refreshes share one request, even while the configured proxy is being read.
  const probeEgress = React.useCallback((): Promise<EgressReport> => {
    if (inFlight.current?.sequence === probeSequence.current) return inFlight.current.promise;
    const sequence = ++probeSequence.current;
    setError(null);
    setIsLoading(true);
    const promise = (async () => {
      try {
        const settings = await api.settings();
        if (sequence !== probeSequence.current) throw new Error("settings.noProbeResult");
        const nextReport = await api.testEgressProxy({ global_proxy: settings.global_proxy ?? "" });
        if (sequence === probeSequence.current) setReport(nextReport);
        return nextReport;
      } catch (cause) {
        const message = cause instanceof Error ? cause.message : "settings.unknownError";
        if (sequence === probeSequence.current) setError(message);
        throw cause;
      } finally {
        if (sequence === probeSequence.current) setIsLoading(false);
        if (inFlight.current?.sequence === sequence) inFlight.current = null;
      }
    })();
    inFlight.current = { sequence, promise };
    return promise;
  }, []);

  React.useEffect(() => {
    let cancelled = false;
    api
      .settings()
      .then((settings) => {
        if (cancelled) return;
        const nextEnabled = !!settings.show_egress_ip;
        setEnabledState(nextEnabled);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
      probeSequence.current += 1;
    };
  }, []);

  usePolling(probeEgress, enabled ? 60_000 : null);

  const setEnabled = React.useCallback(
    async (nextEnabled: boolean) => {
      const settings = await api.setSettings({ show_egress_ip: nextEnabled });
      setEnabledState(!!settings.show_egress_ip);
      if (!nextEnabled) {
        probeSequence.current += 1;
        setIsLoading(false);
        setReport(null);
        setError(null);
      }
    },
    [],
  );

  const dismiss = React.useCallback(() => setEnabled(false), [setEnabled]);

  const value = React.useMemo(
    () => ({ enabled, isLoading, report, error, setEnabled, probeEgress, dismiss }),
    [dismiss, enabled, error, isLoading, probeEgress, report, setEnabled],
  );

  return <EgressIpContext.Provider value={value}>{children}</EgressIpContext.Provider>;
}

export function useEgressIp() {
  const context = React.useContext(EgressIpContext);
  if (!context) throw new Error("useEgressIp must be used within EgressIpProvider");
  return context;
}

function checkMessage(check: EgressCheck | undefined, t: TFunction) {
  if (!check) return t("settings.noProbeResult");
  if (check.skipped)
    return check.skip_code
      ? t(`settings.egressSkip.${check.skip_code}`, { defaultValue: check.skip_reason ?? t("settings.notConfigured") })
      : (check.skip_reason ?? t("settings.notConfigured"));
  if (check.result.ok) return check.result.exit_ip || t("settings.connected");
  return t("settings.probeFailed");
}

function IpValue({ label, check }: { label: string; check: EgressCheck | undefined }) {
  const { t } = useLiveTranslation();
  const failed = !!check && !check.skipped && !check.result.ok;
  return (
    <span className="min-w-0 truncate" title={`${label}:${checkMessage(check, t)}`}>
      <span className="text-muted-foreground">{label}</span> {"· "}
      <span className={cn("font-mono", failed && "text-destructive")}>{checkMessage(check, t)}</span>
    </span>
  );
}

export function EgressIpNavbar() {
  const { t } = useLiveTranslation();
  const { enabled, report, error, isLoading, probeEgress, dismiss } = useEgressIp();
  if (!enabled) return null;

  const direct = report?.checks.find((check) => check.key === "direct");
  const global = report?.checks.find((check) => check.key === "global");
  const ipv6 = report?.checks.find((check) => check.key === "global_ipv6");
  return (
    <div className="flex min-w-0 items-center justify-center gap-2 text-xs" role="status" aria-live="polite">
      <Globe2Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      {isLoading && !report ? (
        <span className="flex items-center gap-1 text-muted-foreground">
          <Loader2Icon className="size-3 animate-spin" aria-hidden="true" /> {t("settings.probingIp")}
        </span>
      ) : error && !report ? (
        <span className="truncate text-destructive" title={t(error, { defaultValue: error })}>
          {t("settings.probeFailedPrefix")}
          {t(error, { defaultValue: error })}
        </span>
      ) : report ? (
        <div className="flex min-w-0 items-center gap-3">
          <IpValue label={t("settings.directIp")} check={direct} />
          <IpValue label={t("settings.proxyIPv4")} check={global} />
          {ipv6 && <IpValue label={t("settings.proxyIPv6")} check={ipv6} />}
          {error && <span className="text-destructive" title={t(error, { defaultValue: error })}>{t("settings.probeFailed")}</span>}
        </div>
      ) : (
        <span className="text-muted-foreground">{t("settings.noEgressResults")}</span>
      )}
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        onClick={() => void probeEgress().catch(() => undefined)}
        disabled={isLoading}
        aria-label={t("settings.refreshEgress")}
      >
        <RefreshCwIcon className={cn("size-3.5", isLoading && "animate-spin")} aria-hidden="true" />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        onClick={() => void dismiss().catch(() => undefined)}
        aria-label={t("settings.hideEgress")}
      >
        <XIcon className="size-3.5" aria-hidden="true" />
      </Button>
    </div>
  );
}
