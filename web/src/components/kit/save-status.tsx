import { Check } from "lucide-react";
import { useT } from "@/lib/i18n";
import type { SaveState } from "@/lib/optimistic";

/**
 * The quiet line beside an optimistically saved row: "Saving…", then "Saved",
 * or "Save failed" with a retry. It always takes the same room, so the row
 * does not move, and it is a polite live region for screen readers.
 */
export function SaveStatus({ state, onRetry }: { state?: SaveState; onRetry: () => void }) {
  const t = useT();
  return (
    <span role="status" aria-live="polite" className="inline-flex min-w-16 items-center text-xs text-muted-foreground">
      {state?.state === "saving" && t("common.saving")}
      {state?.state === "saved" && (
        <span className="animate-in-soft inline-flex items-center gap-1">
          <Check className="size-3 text-success" aria-hidden="true" />
          {t("common.saved")}
        </span>
      )}
      {state?.state === "failed" && (
        <span className="inline-flex items-center gap-1.5" title={state.message}>
          <span className="text-destructive">{t("common.saveFailed")}</span>
          <button type="button" onClick={onRetry} className="underline underline-offset-2 hover:text-foreground">
            {t("common.retry")}
          </button>
        </span>
      )}
    </span>
  );
}
