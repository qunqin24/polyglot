import * as React from "react";
import { ApiError } from "./api";
import { getQuery } from "./query-cache";

// Keys identify the data, including every filter, so pages share requests and
// can render their previous result immediately when the operator comes back.
export function useAsync<T>(fn: () => Promise<T>, queryKey: readonly (string | number | boolean | null)[]) {
  const key = JSON.stringify(queryKey);
  const query = React.useMemo(() => getQuery<T>(key), [key]);
  const snapshot = React.useSyncExternalStore(query.subscribe, query.getSnapshot);
  const fnRef = React.useRef(fn);
  fnRef.current = fn;

  React.useEffect(() => {
    void query.load(fnRef.current);
  }, [query, snapshot.revision]);

  const reload = React.useCallback(() => { void query.load(fnRef.current, true); }, [query]);
  return {
    data: snapshot.data,
    loading: snapshot.loading || !snapshot.settled,
    error: snapshot.error === null ? "" : errorMessage(snapshot.error),
    reload,
  };
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

export type Theme = "light" | "dark" | "system";

export function useTheme() {
  const [theme, setThemeState] = React.useState<Theme>(
    () => (localStorage.getItem("polyglot-theme") as Theme) || "system",
  );

  const apply = React.useCallback((t: Theme) => {
    const dark =
      t === "dark" || (t === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", dark);
  }, []);

  React.useEffect(() => {
    apply(theme);
    if (theme !== "system") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => apply("system");
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [theme, apply]);

  const setTheme = React.useCallback((t: Theme) => {
    localStorage.setItem("polyglot-theme", t);
    setThemeState(t);
  }, []);

  return { theme, setTheme };
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
