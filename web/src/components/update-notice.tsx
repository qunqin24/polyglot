import * as React from "react";
import { ArrowUpCircle, ExternalLink, RefreshCw, X } from "lucide-react";
import { api, type UpdateStatus } from "@/lib/api";
import { errorMessage } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/misc";

const dismissedKey = "polyglot-dismissed-update";
const browserCheckInterval = 6 * 60 * 60 * 1000;

// UpdateNotice stays deliberately passive: it reports a release and links to
// its tag, but never downloads an image or restarts the gateway by itself.
export function UpdateNotice() {
  const t = useT();
  const [status, setStatus] = React.useState<UpdateStatus | null>(null);
  const [dismissed, setDismissed] = React.useState(() => {
    try {
      return window.localStorage.getItem(dismissedKey) ?? "";
    } catch {
      return "";
    }
  });

  React.useEffect(() => {
    let active = true;
    const check = () => {
      void api
        .updateStatus()
        .then((next) => {
          if (active) setStatus(next);
        })
        .catch(() => undefined);
    };
    check();
    const timer = window.setInterval(check, browserCheckInterval);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, []);

  if (
    !status?.enabled ||
    !status.supported ||
    !status.update_available ||
    !status.latest_tag ||
    !status.latest_version ||
    !status.version_url ||
    dismissed === status.latest_tag
  ) {
    return null;
  }

  function dismiss() {
    if (!status?.latest_tag) return;
    setDismissed(status.latest_tag);
    try {
      window.localStorage.setItem(dismissedKey, status.latest_tag);
    } catch {
      // Storage can be unavailable in private browsing. Dismiss for this tab.
    }
  }

  return (
    <section
      role="status"
      aria-live="polite"
      className="mb-6 flex flex-wrap items-center gap-3 rounded-lg border border-primary/25 bg-primary/5 px-4 py-3"
    >
      <ArrowUpCircle className="size-5 shrink-0 text-primary" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{t("updates.availableTitle")}</p>
        <p className="text-xs text-muted-foreground">
          {t("updates.availableDescription", {
            current: status.current_version,
            latest: status.latest_version,
          })}
        </p>
      </div>
      <Button asChild size="sm">
        <a href={status.version_url} target="_blank" rel="noreferrer">
          {t("updates.viewVersion")} <ExternalLink aria-hidden="true" />
        </a>
      </Button>
      <Button variant="ghost" size="icon-sm" onClick={dismiss} aria-label={t("updates.dismiss")}>
        <X aria-hidden="true" />
      </Button>
    </section>
  );
}

export function UpdateSettings() {
  const t = useT();
  const [status, setStatus] = React.useState<UpdateStatus | null>(null);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(true);

  const check = React.useCallback(async (refresh: boolean) => {
    setBusy(true);
    setError("");
    try {
      setStatus(await api.updateStatus(refresh));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }, []);

  React.useEffect(() => {
    void check(false);
  }, [check]);

  let message = t("updates.checking");
  if (!busy) {
    if (error) message = t("updates.checkFailed", { message: error });
    else if (!status?.enabled) message = t("updates.disabled");
    else if (!status.supported) message = t("updates.development");
    else if (status.error) message = t("updates.checkFailed", { message: status.error });
    else if (status.update_available && status.latest_version) {
      message = t("updates.availableDescription", {
        current: status.current_version,
        latest: status.latest_version,
      });
    } else if (status.latest_version) {
      message = t("updates.upToDate", { latest: status.latest_version });
    }
  }

  return (
    <section aria-labelledby="update-settings-title" aria-busy={busy}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="space-y-1">
          <h3 id="update-settings-title" className="text-sm font-medium">
            {t("updates.settingsTitle")}
          </h3>
          <p className="text-xs text-muted-foreground" aria-live="polite">
            {message}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {status?.update_available && status.version_url && (
            <Button asChild size="sm" variant="outline">
              <a href={status.version_url} target="_blank" rel="noreferrer">
                {t("updates.viewVersion")} <ExternalLink aria-hidden="true" />
              </a>
            </Button>
          )}
          <Button size="sm" variant="outline" disabled={busy || status?.enabled === false} onClick={() => void check(true)}>
            {busy ? <Spinner /> : <RefreshCw aria-hidden="true" />}
            {t("updates.check")}
          </Button>
        </div>
      </div>
    </section>
  );
}
