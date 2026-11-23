import * as React from "react";
import { toast as sonner } from "sonner";

type ToastKind = "success" | "error";

const api = {
  toast: (message: string, kind: ToastKind = "success") => {
    if (kind === "error") sonner.error(message, { duration: 6000 });
    else sonner.success(message);
  },
  /**
   * Progress for work that outlives the click: one toast under a fixed id that
   * is updated in place while the work runs, then turned into its result by
   * `finish`. It stays until finished or dismissed. `onCancel` adds a cancel
   * button. Calling it again with the same id only changes the text.
   */
  progress: (id: string, message: string, opts: { onCancel?: () => void; cancelLabel?: string } = {}) => {
    sonner.loading(message, {
      id,
      duration: Infinity,
      cancel: opts.onCancel
        ? { label: opts.cancelLabel ?? "×", onClick: opts.onCancel }
        : undefined,
    });
  },
  /** Replaces the progress toast with its result, in the same place. */
  finish: (id: string, message: string, kind: ToastKind = "success") => {
    if (kind === "error") sonner.error(message, { id, duration: 6000, cancel: undefined });
    else sonner.success(message, { id, duration: 3500, cancel: undefined });
  },
  dismiss: (id: string) => {
    sonner.dismiss(id);
  },
};

/** The page-facing toast API, backed by sonner's Toaster in the root route. */
export function useToast() {
  return React.useMemo(() => api, []);
}
