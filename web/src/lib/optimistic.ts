import * as React from "react";
import { errorMessage } from "@/lib/hooks";

export type SaveState =
  | { state: "saving" }
  | { state: "saved" }
  | { state: "failed"; next: boolean; message: string };

/**
 * Optimistic on/off switches for rows that live in a list.
 *
 * `toggle` shows the new value at once, saves in the background and then
 * re-reads the list. If saving fails, the value goes back and the row says so
 * (`status(id)`), with the intended value kept so `retry` can send it again.
 * Not for deletions or anything that cannot be quietly undone.
 */
function put<K, V>(id: K, value: V | null) {
  return (m: Map<K, V>) => {
    const next = new Map(m);
    if (value === null) next.delete(id);
    else next.set(id, value);
    return next;
  };
}

export function useOptimisticFlags<K extends string | number>(
  commit: (id: K, next: boolean) => Promise<unknown>,
  refresh: () => Promise<unknown> | void,
) {
  const [over, setOver] = React.useState<Map<K, boolean>>(() => new Map());
  const [states, setStates] = React.useState<Map<K, SaveState>>(() => new Map());
  const commitRef = React.useRef(commit);
  const refreshRef = React.useRef(refresh);
  commitRef.current = commit;
  refreshRef.current = refresh;

  const toggle = React.useCallback(async (id: K, next: boolean) => {
    setOver(put<K, boolean>(id, next));
    setStates(put<K, SaveState>(id, { state: "saving" }));
    try {
      await commitRef.current(id, next);
      try {
        await refreshRef.current();
      } catch {
        // The save worked. A failed re-read is shown by the list itself.
      }
      setOver(put<K, boolean>(id, null));
      setStates(put<K, SaveState>(id, { state: "saved" }));
      setTimeout(() => {
        setStates((m) => (m.get(id)?.state === "saved" ? put<K, SaveState>(id, null)(m) : m));
      }, 1500);
    } catch (e) {
      setOver(put<K, boolean>(id, null));
      setStates(put<K, SaveState>(id, { state: "failed", next, message: errorMessage(e) }));
    }
  }, []);

  return React.useMemo(
    () => ({
      /** The value to draw: the pending one if there is one, else the server's. */
      value: (id: K, server: boolean) => over.get(id) ?? server,
      status: (id: K): SaveState | undefined => states.get(id),
      toggle,
      retry: (id: K) => {
        const s = states.get(id);
        if (s?.state === "failed") void toggle(id, s.next);
      },
    }),
    [over, states, toggle],
  );
}
