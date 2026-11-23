import { LoadingButton } from "@/components/kit/loading-button";
import { isAbort, useCancellable, useSlowHint } from "@/lib/hooks";
import * as React from "react";
import { CheckCircle2, Plus, RefreshCw } from "lucide-react";
import { api, type ProtocolName } from "@/lib/api";
import { errorMessage } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { formatDuration } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ErrorBanner } from "@/components/kit/misc";
import { PanelSection } from "./panel-section";

export type Connection = {
  protocol: ProtocolName;
  vendor: string;
  base_url: string;
  api_key: string;
  headers: Record<string, string> | null;
  timeout_secs: number;
};

type Row = { id: string; display_name: string; manual: boolean };

function isLocal(url: string): boolean {
  try {
    const host = new URL(url).hostname;
    return host === "localhost" || host === "127.0.0.1" || host === "::1" || host === "[::1]";
  } catch {
    return false;
  }
}

/**
 * The draft model selection for a provider that does not exist yet. Fetching
 * and ticking never save anything. The parent remounts this whenever the
 * connection changes, so a list fetched with the previous key or URL can
 * never be submitted for a different one.
 */
export function ModelChecklist({
  connection,
  canList,
  hint,
  selected,
  onChange,
  onFetchingChange,
}: {
  connection: Connection;
  /** False where the upstream has no model-list endpoint (Agent Platform). */
  canList: boolean;
  hint?: string;
  selected: Set<string>;
  onChange: (next: Set<string>) => void;
  onFetchingChange: (fetching: boolean) => void;
}) {
  const t = useT();
  const uid = React.useId();
  const [rows, setRows] = React.useState<Row[]>([]);
  const [search, setSearch] = React.useState("");
  const [manual, setManual] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState("");
  const [result, setResult] = React.useState<{ latency: number; count: number } | null>(null);
  const [unsupported, setUnsupported] = React.useState(false);
  const alive = React.useRef(true);
  const call = useCancellable();
  const slow = useSlowHint(loading);

  React.useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  React.useEffect(() => onFetchingChange(loading), [loading, onFetchingChange]);

  // A key is needed to list, except for a local server such as Ollama.
  const canFetch = canList && connection.headers !== null && (connection.api_key.trim() !== "" || isLocal(connection.base_url));

  async function fetchModels() {
    if (!connection.headers) return;
    setLoading(true);
    setError("");
    setUnsupported(false);
    try {
      const offer = await api.discoverModels(
        {
          id: 0,
          protocol: connection.protocol,
          vendor: connection.vendor,
          base_url: connection.base_url,
          api_key: connection.api_key,
          headers: connection.headers,
          timeout_secs: connection.timeout_secs,
        },
        call.start(),
      );
      if (!alive.current) return;
      if (!offer.ok) {
        if (offer.supported) setError(t("providers.listFailed", { error: offer.error ?? "" }));
        else setUnsupported(true);
        return;
      }
      // Hand-typed ids stay, even if the upstream also lists them.
      const kept = rows.filter((r) => r.manual);
      const keptIds = new Set(kept.map((r) => r.id));
      const next = [
        ...kept,
        ...offer.models
          .filter((m) => !keptIds.has(m.id))
          .map((m) => ({ id: m.id, display_name: m.display_name ?? "", manual: false })),
      ];
      setRows(next);
      setResult({ latency: offer.latency_ms, count: offer.models.length });
      const ids = new Set(next.map((r) => r.id));
      onChange(new Set([...selected].filter((id) => ids.has(id))));
    } catch (e) {
      if (alive.current && !isAbort(e)) setError(errorMessage(e));
    } finally {
      if (alive.current) setLoading(false);
    }
  }

  const q = search.trim().toLowerCase();
  const visible = rows.filter((r) => `${r.id} ${r.display_name}`.toLowerCase().includes(q));
  const typed = manual.trim();

  function toggle(id: string, on: boolean) {
    const next = new Set(selected);
    if (on) next.add(id);
    else next.delete(id);
    onChange(next);
  }

  // Acts on what the search is showing, not on the whole list.
  function selectVisible(on: boolean) {
    const next = new Set(selected);
    for (const r of visible) {
      if (on) next.add(r.id);
      else next.delete(r.id);
    }
    onChange(next);
  }

  function addManual() {
    if (!typed || rows.some((r) => r.id === typed)) return;
    setRows((r) => [{ id: typed, display_name: "", manual: true }, ...r]);
    onChange(new Set(selected).add(typed));
    setManual("");
    setSearch("");
  }

  return (
    <PanelSection
      title={t("providers.models")}
      action={
        canList ? (
          <LoadingButton
            type="button"
            variant="outline"
            size="sm"
            busy={loading}
            disabled={!canFetch}
            icon={<RefreshCw aria-hidden="true" />}
            iconIsAction
            onClick={() => void fetchModels()}
          >
            {t("providers.fetchModels")}
          </LoadingButton>
        ) : undefined
      }
    >
      <p className="text-xs text-muted-foreground">{hint ?? t("providers.form.modelsHint")}</p>
      {canList && !canFetch && <p className="text-xs text-muted-foreground">{t("providers.form.enterKey")}</p>}
      {result && (
        <p className="flex items-center gap-1.5 text-xs text-success">
          <CheckCircle2 className="size-3.5" />
          {t("providers.wizard.connected", { latency: formatDuration(result.latency), count: result.count })}
        </p>
      )}
      {unsupported && <p className="text-xs text-muted-foreground">{t("providers.listUnsupported")}</p>}
      <ErrorBanner message={error} />

      {rows.length > 0 ? (
        <>
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("models.pickerSearch")}
            aria-label={t("models.pickerSearch")}
            autoComplete="off"
            spellCheck={false}
          />
          <div className="flex flex-wrap items-center gap-2">
            <span className="me-auto text-xs text-muted-foreground tabular-nums" role="status">
              {t("models.pickerCount", { selected: selected.size, total: rows.length })}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={visible.every((r) => selected.has(r.id))}
              onClick={() => selectVisible(true)}
            >
              {t("models.pickerAll")}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={!visible.some((r) => selected.has(r.id))}
              onClick={() => selectVisible(false)}
            >
              {t("models.pickerNone")}
            </Button>
          </div>
          <div className="max-h-72 divide-y overflow-y-auto rounded-md border" aria-busy={loading}>
            {visible.map((r, i) => (
              <div key={r.id} className="flex items-center gap-3 px-3 py-2.5">
                <Checkbox
                  id={`${uid}-${i}`}
                  checked={selected.has(r.id)}
                  onCheckedChange={(v) => toggle(r.id, v === true)}
                />
                <Label htmlFor={`${uid}-${i}`} className="min-w-0 flex-1 cursor-pointer font-normal">
                  <span className="flex min-w-0 flex-col gap-1">
                    <span className="truncate">{r.display_name || r.id}</span>
                    {r.display_name && r.display_name !== r.id && (
                      <span className="truncate font-mono text-xs text-muted-foreground">{r.id}</span>
                    )}
                  </span>
                </Label>
                {r.manual && <Badge variant="outline">{t("providers.form.manual")}</Badge>}
              </div>
            ))}
            {visible.length === 0 && (
              <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t("models.pickerNoMatch")}</p>
            )}
          </div>
        </>
      ) : (
        !error && (
          <p className="rounded-md border border-dashed px-4 py-6 text-center text-sm text-muted-foreground" role="status">
            {loading ? (
              <>
                {slow ? t("common.slowHint") : t("providers.fetchingModels")}{" "}
                <button type="button" onClick={call.cancel} className="underline underline-offset-2 hover:text-foreground">
                  {t("common.cancel")}
                </button>
              </>
            ) : (
              t("providers.form.modelsEmpty")
            )}
          </p>
        )
      )}

      <div className="flex items-center gap-2">
        <Input
          value={manual}
          onChange={(e) => setManual(e.target.value)}
          maxLength={200}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              addManual();
            }
          }}
          placeholder={t("providers.manualModelPlaceholder")}
          aria-label={t("providers.wizard.manualTitle")}
          autoComplete="off"
          spellCheck={false}
          className="font-mono text-xs"
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!typed || rows.some((r) => r.id === typed)}
          onClick={addManual}
        >
          <Plus aria-hidden="true" />
          {t("common.add")}
        </Button>
      </div>
    </PanelSection>
  );
}
