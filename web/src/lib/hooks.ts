import * as React from "react";
import { ApiError } from "./api";

// useAsync is the whole data layer: a fetch, a loading flag, an error, and a
// reload. A query library would be more than this project needs.
export function useAsync<T>(fn: () => Promise<T>, deps: React.DependencyList = []) {
  const [data, setData] = React.useState<T | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [nonce, setNonce] = React.useState(0);

  const fnRef = React.useRef(fn);
  fnRef.current = fn;

  React.useEffect(() => {
    let cancelled = false;
    setLoading(true);
    fnRef
      .current()
      .then((v) => {
        if (!cancelled) {
          setData(v);
          setError("");
        }
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(errorMessage(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const reload = React.useCallback(() => setNonce((n) => n + 1), []);
  return { data, loading, error, reload, setData };
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
    const id = setInterval(() => saved.current(), delayMs);
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
