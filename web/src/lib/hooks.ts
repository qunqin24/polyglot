import * as React from "react";
import { ApiError } from "./api";
import { getQuery } from "./query-cache";

interface AsyncOptions {
  /** While the key changed and the new data has not arrived, keep showing the
   *  last data of the previous key. For filters, pages and time windows. */
  keepPrevious?: boolean;
}

// Keys identify the data, including every filter, so pages share requests and
// can render their previous result immediately when the operator comes back.
export function useAsync<T>(
  fn: () => Promise<T>,
  queryKey: readonly (string | number | boolean | null)[],
  options: AsyncOptions = {},
) {
  const key = JSON.stringify(queryKey);
  const query = React.useMemo(() => getQuery<T>(key), [key]);
  const snapshot = React.useSyncExternalStore(query.subscribe, query.getSnapshot);
  const fnRef = React.useRef(fn);
  fnRef.current = fn;
  const previous = React.useRef<T | null>(null);
  if (snapshot.data !== null) previous.current = snapshot.data;

  React.useEffect(() => {
    void query.load(fnRef.current);
  }, [query, snapshot.revision]);

  const reload = React.useCallback(() => query.load(fnRef.current, true), [query]);
  const data = snapshot.data ?? (options.keepPrevious ? previous.current : null);
  const loading = snapshot.loading || !snapshot.settled;
  return {
    data,
    loading,
    /** First load of this data: nothing to show yet, so a skeleton fits. */
    initialLoading: loading && data === null,
    /** Data is on screen and is being revalidated or replaced. */
    refreshing: loading && data !== null,
    /** The data on screen belongs to the previous key (keepPrevious). */
    stale: snapshot.data === null && data !== null,
    error: snapshot.error === null ? "" : errorMessage(snapshot.error),
    reload,
  };
}

/**
 * Turns a busy flag into one that appears only after `delay` ms and, once
 * shown, stays for at least `minVisible` ms. Anything faster than the delay
 * shows no indicator at all, and nothing flashes for a frame or two.
 */
export function useDelayedFlag(active: boolean, delay = 300, minVisible = 500): boolean {
  const [visible, setVisible] = React.useState(false);
  const shownAt = React.useRef(0);

  React.useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    if (active && !visible) {
      timer = setTimeout(() => {
        shownAt.current = Date.now();
        setVisible(true);
      }, delay);
    } else if (!active && visible) {
      const left = Math.max(0, minVisible - (Date.now() - shownAt.current));
      timer = setTimeout(() => setVisible(false), left);
    }
    return () => clearTimeout(timer);
  }, [active, visible, delay, minVisible]);

  return visible;
}

/** True once `active` has lasted `ms`: "this is taking longer than usual". */
export function useSlowHint(active: boolean, ms = 10_000): boolean {
  return useDelayedFlag(active, ms, 0);
}

/** One cancellable request at a time. Starting a new one cancels the old. */
export function useCancellable() {
  const ref = React.useRef<AbortController | null>(null);
  const cancel = React.useCallback(() => {
    ref.current?.abort();
  }, []);
  /** False once a newer request has replaced this one. */
  const isCurrent = React.useCallback((signal: AbortSignal) => ref.current?.signal === signal, []);
  const start = React.useCallback(() => {
    ref.current?.abort();
    const controller = new AbortController();
    ref.current = controller;
    return controller.signal;
  }, []);
  React.useEffect(() => cancel, [cancel]);
  return React.useMemo(() => ({ start, cancel, isCurrent }), [start, cancel, isCurrent]);
}

export function isAbort(e: unknown): boolean {
  return e instanceof DOMException && e.name === "AbortError";
}

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}

export function useInterval(callback: () => void, delayMs: number | null) {
  const saved = React.useRef(callback);
  saved.current = callback;

  React.useEffect(() => {
    if (delayMs === null) return;
    const id = setInterval(() => {
      if (document.visibilityState !== "hidden") saved.current();
    }, delayMs);
    return () => clearInterval(id);
  }, [delayMs]);
}

export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // Clipboard API needs a secure context; fall back for plain-HTTP installs.
    try {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand("copy");
      document.body.removeChild(ta);
      return ok;
    } catch {
      return false;
    }
  }
}
