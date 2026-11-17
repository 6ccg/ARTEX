"use client";

import { useEffect } from "react";

// Poll visible pages serially. Slow responses cannot accumulate more requests,
// and returning to the tab refreshes immediately instead of waiting a full interval.
export function usePolling(refresh: () => void | Promise<unknown>, interval: number | null): void {
  useEffect(() => {
    if (interval === null) return;
    return startPolling(refresh, interval);
  }, [refresh, interval]);
}

// For effects that also own request cancellation or resource initialization.
export function startPolling(refresh: () => void | Promise<unknown>, interval: number, initialDelay = 0): () => void {
  const visible = () => document.visibilityState !== "hidden";
  let disposed = false;
  let running = false;
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function poll() {
    if (disposed || running || !visible()) return;
    running = true;
    try {
      await refresh();
    } catch {
      // The callback owns its error display; a failed poll remains retryable.
    } finally {
      running = false;
      if (!disposed && visible()) timer = setTimeout(() => void poll(), interval);
    }
  }

  function visibilityChanged() {
    clearTimeout(timer);
    void poll();
  }

  document.addEventListener("visibilitychange", visibilityChanged);
  if (initialDelay > 0 && visible()) timer = setTimeout(() => void poll(), initialDelay);
  else void poll();
  return () => {
    disposed = true;
    clearTimeout(timer);
    document.removeEventListener("visibilitychange", visibilityChanged);
  };
}
