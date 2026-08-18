import * as React from "react";
import { ScrollText, RefreshCw, ChevronDown } from "lucide-react";
import { api, type FidelityNote, type RequestLog, type RequestLogDetail } from "@/lib/api";
import { errorMessage, useAsync, useInterval } from "@/lib/hooks";
import { useT, type TFunction } from "@/lib/i18n";
import { cn, formatDuration, formatTime, formatUSD } from "@/lib/utils";
import { PageHeader } from "@/components/layout";
import { ProtocolFlow, StatusBadge } from "@/pages/overview";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Badge,
  EmptyState,
  ErrorBanner,
  Select,
  Spinner,
  Table,
  Td,
  Th,
  Tr,
} from "@/components/ui/misc";

type PageSize = 25 | 50 | 100 | 200;

const PAGE_SIZES: PageSize[] = [25, 50, 100, 200];

type RefreshMode = "off" | "live" | "5000" | "10000" | "20000" | "30000";

const REFRESH_DELAYS: Record<RefreshMode, number | null> = {
  off: null,
  live: 1000,
  "5000": 5000,
  "10000": 10000,
  "20000": 20000,
  "30000": 30000,
};

function initialRefreshMode(): RefreshMode {
  const saved = localStorage.getItem("polyglot-logs-refresh");
  return saved !== null && saved in REFRESH_DELAYS ? (saved as RefreshMode) : "off";
}

function initialPageSize(): PageSize {
  const saved = Number(localStorage.getItem("polyglot-logs-page-size"));
  return PAGE_SIZES.includes(saved as PageSize) ? (saved as PageSize) : 50;
}

export function Logs() {
  const t = useT();
  const [status, setStatus] = React.useState("");
  const [protocol, setProtocol] = React.useState("");
  const [model, setModel] = React.useState("");
  const [clientIP, setClientIP] = React.useState("");
  const [clientApp, setClientApp] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState<PageSize>(initialPageSize);
  const [pageInput, setPageInput] = React.useState("1");
  const [query, setQuery] = React.useState("");
  const [selected, setSelected] = React.useState<number | null>(null);
  const [refreshMode, setRefreshMode] = React.useState<RefreshMode>(initialRefreshMode);
  const [pageVisible, setPageVisible] = React.useState(() => document.visibilityState !== "hidden");

  // Debounce the free-text filter so typing does not hammer the API.
  React.useEffect(() => {
    const id = setTimeout(() => setQuery(model.trim()), 300);
    return () => clearTimeout(id);
  }, [model]);

  const { data, loading, error, reload } = useAsync(
    () =>
      api.logs({
        limit: pageSize,
        offset: (page - 1) * pageSize,
        status,
        protocol,
        model: query,
        client_ip: clientIP,
        client_app: clientApp,
      }),
    [status, protocol, query, clientIP, clientApp, page, pageSize],
  );

  // A filtered result set has its own page count, so filters always start at
  // its first page.
  React.useEffect(() => {
    setPage(1);
  }, [status, protocol, query, clientIP, clientApp]);

  const total = data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  React.useEffect(() => {
    if (page > totalPages) setPage(totalPages);
  }, [page, totalPages]);

  React.useEffect(() => setPageInput(String(page)), [page]);

  // A background tab should not keep polling an admin endpoint nobody can
  // see. The next interval resumes automatically when the page is visible.
  React.useEffect(() => {
    const onVisibilityChange = () => setPageVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, []);

  const refreshDelay = pageVisible ? REFRESH_DELAYS[refreshMode] : null;
  useInterval(() => {
    // Slow requests never stack up, even in one-second live mode.
    if (!loading) reload();
  }, refreshDelay);

  function changeRefreshMode(value: string) {
    const next = value in REFRESH_DELAYS ? (value as RefreshMode) : "off";
    setRefreshMode(next);
    localStorage.setItem("polyglot-logs-refresh", next);
    if (next !== "off") {
      // Auto-refresh is useful only on the newest page. Returning there also
      // prevents newly inserted rows from shifting a historical page.
      setPage(1);
      reload();
    }
  }

  function changePageSize(value: string) {
    const next = Number(value);
    if (!PAGE_SIZES.includes(next as PageSize)) return;
    setPageSize(next as PageSize);
    localStorage.setItem("polyglot-logs-page-size", String(next));
    setPage(1);
  }

  function goToPage(value: number) {
    const next = Math.min(totalPages, Math.max(1, Math.trunc(value)));
    setPage(next);
    if (next > 1 && refreshMode !== "off") {
      // New rows would continuously shift offset-based historical pages.
      // Leaving the live view therefore turns auto-refresh off explicitly.
      setRefreshMode("off");
      localStorage.setItem("polyglot-logs-refresh", "off");
    }
  }

  function jumpToPage(e: React.FormEvent) {
    e.preventDefault();
    const requested = Number(pageInput);
    if (Number.isFinite(requested)) goToPage(requested);
    else setPageInput(String(page));
  }

  return (
    <>
      <PageHeader
        title={t("logs.title")}
        description={t("logs.description")}
        action={
          <div className="flex flex-wrap items-center justify-end gap-2">
            <span className="text-xs font-medium text-muted-foreground">
              {t("logs.autoRefresh")}
            </span>
            <Select
              value={refreshMode}
              onValueChange={changeRefreshMode}
              className="w-32"
              options={[
                { value: "off", label: t("logs.refreshOff") },
                { value: "live", label: t("logs.refreshLive") },
                { value: "5000", label: t("logs.refreshSeconds", { seconds: "5" }) },
                { value: "10000", label: t("logs.refreshSeconds", { seconds: "10" }) },
                { value: "20000", label: t("logs.refreshSeconds", { seconds: "20" }) },
                { value: "30000", label: t("logs.refreshSeconds", { seconds: "30" }) },
              ]}
            />
            <Button variant="outline" onClick={reload} disabled={loading}>
              {loading && !data ? <Spinner /> : <RefreshCw />} {t("common.refresh")}
            </Button>
          </div>
        }
      />

      <div className="mb-4 flex flex-wrap gap-2">
        <Input
          placeholder={t("logs.filterModel")}
          value={model}
          onChange={(e) => setModel(e.target.value)}
          className="w-full sm:w-56"
        />
        <Input
          placeholder={t("logs.filterApp")}
          value={clientApp}
          onChange={(e) => setClientApp(e.target.value)}
          className="w-full sm:w-44"
        />
        <Input
          placeholder={t("logs.filterIP")}
          value={clientIP}
          onChange={(e) => setClientIP(e.target.value.trim())}
          className="w-full sm:w-44 font-mono text-xs"
        />
        <Select
          value={status}
          onValueChange={setStatus}
          className="w-36"
          placeholder={t("common.anyStatus")}
          options={[
            { value: "", label: t("common.anyStatus") },
            { value: "success", label: t("logs.statusSuccess") },
            { value: "error", label: t("logs.statusError") },
            { value: "cancelled", label: t("logs.statusCancelled") },
          ]}
        />
        <Select
          value={protocol}
          onValueChange={setProtocol}
          className="w-40"
          placeholder={t("common.anyProtocol")}
          options={[
            { value: "", label: t("common.anyProtocol") },
            { value: "openai", label: "OpenAI" },
            { value: "openai-responses", label: "OpenAI Responses" },
            { value: "anthropic", label: "Anthropic" },
            { value: "gemini", label: "Gemini" },
          ]}
        />
      </div>

      <ErrorBanner message={error} />


      <Card>
        <CardContent className="p-0">
          {loading && !data ? (
            <div className="flex justify-center py-12">
              <Spinner className="text-muted-foreground" />
            </div>
          ) : (data?.logs.length ?? 0) === 0 ? (
            <EmptyState
              icon={ScrollText}
              title={t("logs.empty")}
              description={t("logs.emptyHint")}
            />
          ) : (
            <Table>
              <thead>
                <Tr>
                  <Th className="pl-5">{t("overview.time")}</Th>
                  <Th>{t("common.model")}</Th>
                  <Th className="hidden md:table-cell">{t("logs.clientApp")}</Th>
                  <Th className="hidden xl:table-cell">{t("logs.clientIP")}</Th>
                  <Th>{t("overview.route")}</Th>
                  <Th className="text-right">{t("overview.tokens")}</Th>
                  <Th className="hidden text-right md:table-cell">{t("logs.cacheColumn")}</Th>
                  <Th className="hidden text-right lg:table-cell">{t("logs.costColumn")}</Th>
                  <Th className="text-right">{t("overview.latency")}</Th>
                  <Th className="hidden text-right sm:table-cell">{t("logs.tpsColumn")}</Th>
                  <Th className="text-right">{t("common.status")}</Th>
                  <Th className="pr-5" />
                </Tr>
              </thead>
              <tbody>
                {data!.logs.map((l) => (
                  <Tr
                    key={l.id}
                    className="cursor-pointer transition-colors hover:bg-muted/50"
                    onClick={() => setSelected(l.id)}
                  >
                    <Td className="pl-5 whitespace-nowrap text-muted-foreground">
                      {formatTime(l.started_at)}
                    </Td>
                    <Td>
                      <span className="font-mono text-xs">{l.model_alias || "—"}</span>
                      {l.stream && (
                        <Badge variant="outline" className="ml-2">
                          {t("logs.stream")}
                        </Badge>
                      )}
                    </Td>
                    <Td className="hidden max-w-[14rem] truncate md:table-cell">
                      <span className="text-xs text-muted-foreground" title={l.client_app}>
                        {l.client_app || "—"}
                      </span>
                    </Td>
                    <Td className="hidden xl:table-cell">
                      <span className="font-mono text-xs text-muted-foreground">
                        {l.client_ip || "—"}
                      </span>
                    </Td>
                    <Td>
                      <ProtocolFlow log={l} />
                    </Td>
                    <Td className="text-right tabular-nums text-muted-foreground">
                      {l.input_tokens + l.output_tokens > 0
                        ? `${l.input_tokens} / ${l.output_tokens}`
                        : "—"}
                    </Td>
                    {/* Only rows that actually hit the cache show a number.
                        Printing 0% on every uncached row would fill the column
                        with noise and bury the few rows worth looking at; the
                        detail tells 0% and "not reported" apart. */}
                    <Td className="hidden text-right tabular-nums text-muted-foreground md:table-cell">
                      {l.cached_input_tokens > 0 && l.input_tokens > 0
                        ? `${Math.round((l.cached_input_tokens / l.input_tokens) * 100)}%`
                        : "—"}
                    </Td>
                    {/* A dash is an unknown cost — no price for this model, or
                        an upstream that reported no usage. Never $0, which
                        would say the request was free. */}
                    <Td
                      className="hidden text-right tabular-nums text-muted-foreground lg:table-cell"
                      title={l.cost_usd !== null ? costTitle(l, t) : t("logs.costUnknown")}
                    >
                      {l.cost_usd !== null ? formatUSD(l.cost_usd) : "—"}
                    </Td>
                    <Td className="text-right tabular-nums text-muted-foreground">
                      {formatDuration(l.latency_ms)}
                    </Td>
                    {/* A dash means the speed could not be measured — a
                        buffered reply, or an upstream that reported no token
                        usage — never that it was zero. */}
                    <Td className="hidden text-right tabular-nums text-muted-foreground sm:table-cell">
                      {l.output_tps !== null ? l.output_tps.toFixed(0) : "—"}
                    </Td>
                    <Td className="text-right">
                      <StatusBadge log={l} />
                    </Td>
                    <Td className="pr-5 text-right text-muted-foreground/50">
                      <ChevronDown className="inline size-3.5 -rotate-90" />
                    </Td>
                  </Tr>
                ))}
              </tbody>
            </Table>
          )}
        </CardContent>
      </Card>

      {data && total > 0 && (
        <div className="mt-4 flex flex-col gap-3 rounded-lg border border-border/70 bg-card px-3 py-2.5 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <span>{t("logs.rowsPerPage")}</span>
            <Select
              value={String(pageSize)}
              onValueChange={changePageSize}
              className="w-20"
              options={PAGE_SIZES.map((size) => ({ value: String(size), label: String(size) }))}
            />
            <span className="tabular-nums">{t("logs.totalRows", { total: String(total) })}</span>
          </div>

          <div className="flex items-center justify-center gap-2">
            <Button
              variant="outline"
              disabled={page <= 1 || loading}
              onClick={() => goToPage(page - 1)}
            >
              <ChevronDown className="rotate-90" /> {t("logs.newer")}
            </Button>
            <span className="min-w-24 text-center text-xs text-muted-foreground tabular-nums">
              {t("logs.pageOf", { page: String(page), total: String(totalPages) })}
            </span>
            <Button
              variant="outline"
              disabled={page >= totalPages || loading}
              onClick={() => goToPage(page + 1)}
            >
              {t("logs.older")} <ChevronDown className="-rotate-90" />
            </Button>
          </div>

          <form className="flex items-center justify-end gap-2" onSubmit={jumpToPage}>
            <span className="text-xs text-muted-foreground">{t("logs.jumpToPage")}</span>
            <Input
              type="number"
              min={1}
              max={totalPages}
              value={pageInput}
              onChange={(e) => setPageInput(e.target.value)}
              onBlur={() => pageInput === "" && setPageInput(String(page))}
              className="h-9 w-20 text-center tabular-nums"
              aria-label={t("logs.jumpToPage")}
            />
            <Button type="submit" variant="outline" disabled={loading}>
              {t("logs.goToPage")}
            </Button>
          </form>
        </div>
      )}

      <LogDetail id={selected} onClose={() => setSelected(null)} />
    </>
  );
}

function LogDetail({ id, onClose }: { id: number | null; onClose: () => void }) {
  const t = useT();
  const [log, setLog] = React.useState<RequestLogDetail | null>(null);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    if (id === null) {
      setLog(null);
      return;
    }
    let cancelled = false;
    api
      .log(id)
      .then((l) => !cancelled && setLog(l))
      .catch((e) => !cancelled && setError(errorMessage(e)));
    return () => {
      cancelled = true;
    };
  }, [id]);

  return (
    <Dialog
      open={id !== null}
      onOpenChange={(o) => !o && onClose()}
      title={t("logs.detailTitle")}
      className="max-w-2xl"
    >
      {!log ? (
        <div className="flex justify-center py-8">
          {error ? <ErrorBanner message={error} /> : <Spinner className="text-muted-foreground" />}
        </div>
      ) : (
        <div className="space-y-5">
          <dl className="grid grid-cols-2 gap-x-6 gap-y-3 text-sm sm:grid-cols-3">
            <Detail label={t("logs.started")} value={formatTime(log.started_at)} />
            <Detail label={t("overview.latency")} value={formatDuration(log.latency_ms)} />
            {/* Streaming measurements — see streamOnly. */}
            <Detail
              label={t("logs.ttft")}
              value={streamOnly(
                log,
                log.ttft_ms !== null ? formatDuration(log.ttft_ms) : null,
                t("logs.streamOnly"),
              )}
            />
            <Detail label={t("logs.clientProtocol")} value={log.client_protocol} mono />
            <Detail label={t("logs.upstreamProtocol")} value={log.upstream_protocol || "—"} mono />
            <Detail label={t("common.provider")} value={log.provider_name || "—"} />
            <Detail label={t("logs.requestedModel")} value={log.model_alias || "—"} mono />
            <Detail label={t("models.modelID")} value={log.upstream_model || "—"} mono />
            <Detail label={t("providers.apiKey")} value={log.api_key_name || "—"} />
            <Detail
              label={t("logs.cost")}
              value={log.cost_usd !== null ? formatUSD(log.cost_usd) : "—"}
              hint={log.cost_usd !== null ? costTitle(log, t) : t("logs.costUnknown")}
            />
            <Detail label={t("logs.inputTokens")} value={String(log.input_tokens)} />
            <Detail label={t("logs.outputTokens")} value={String(log.output_tokens)} />
            <Detail
              label={t("logs.reasoningTokens")}
              value={log.reasoning_tokens > 0 ? String(log.reasoning_tokens) : "—"}
            />
            {/* The cached share of the prompt. Shown only once an upstream has
                reported a prompt at all: 0 of 0 is not a miss, it is silence. */}
            {log.input_tokens > 0 && (
              <Detail
                label={t("logs.cachedTokens")}
                value={t("logs.cachedTokensValue", {
                  cached: String(log.cached_input_tokens),
                  percent: ((log.cached_input_tokens / log.input_tokens) * 100).toFixed(0),
                })}
                hint={t("logs.cachedTokensHint")}
              />
            )}
            {log.cache_write_tokens > 0 && (
              <Detail
                label={t("logs.cacheWriteTokens")}
                value={String(log.cache_write_tokens)}
                hint={t("logs.cacheWriteTokensHint")}
              />
            )}
            <Detail
              label={t("logs.generation")}
              value={streamOnly(
                log,
                log.generation_ms !== null ? formatDuration(log.generation_ms) : null,
                t("logs.streamOnly"),
              )}
            />
            <Detail
              label={t("logs.tps")}
              value={streamOnly(
                log,
                log.output_tps !== null
                  ? t("logs.tpsUnit", { value: log.output_tps.toFixed(1) })
                  : null,
                t("logs.streamOnly"),
              )}
            />
            {/* The one throughput figure a buffered reply can honestly report:
                tokens over the whole request. It is deliberately a different
                label from output speed, because it is a different quantity. */}
            {!log.stream && log.output_tokens > 0 && log.latency_ms > 0 && (
              <Detail
                label={t("logs.throughput")}
                value={t("logs.tpsUnit", {
                  value: ((log.output_tokens / log.latency_ms) * 1000).toFixed(1),
                })}
                hint={t("logs.throughputHint")}
              />
            )}
            <Detail
              label={t("logs.retries")}
              value={
                log.retry_count > 0
                  ? t("logs.retriesDetail", {
                      retries: String(log.retry_count),
                      fallbacks: String(log.fallback_count),
                    })
                  : "—"
              }
            />
            <Detail label={t("logs.requestId")} value={log.request_id || "—"} mono />
            <Detail label={t("logs.clientIP")} value={log.client_ip || "—"} mono />
            <Detail label={t("logs.clientApp")} value={log.client_app || "—"} />
            <Detail label={t("logs.requestUser")} value={log.request_user || "—"} mono />
          </dl>

          {log.error_message && (
            <div>
              <p className="mb-1.5 text-xs font-medium text-muted-foreground">
                {log.error_type
                  ? t("logs.errorLabelTyped", { type: log.error_type })
                  : t("logs.errorLabel")}
              </p>
              <pre className="overflow-x-auto rounded-md border border-destructive/30 bg-destructive/8 p-3 font-mono text-xs text-destructive">
                {log.error_message}
              </pre>
            </div>
          )}

          <div>
            <p className="mb-2 text-xs font-medium text-muted-foreground">{t("logs.conversion")}</p>
            {(log.fidelity?.length ?? 0) === 0 ? (
              <p className="text-sm text-muted-foreground">{t("logs.noLoss")}</p>
            ) : (
              <ul className="space-y-2">
                {log.fidelity?.map((n, i) => (
                  <FidelityRow key={i} note={n} />
                ))}
              </ul>
            )}
          </div>

          {log.request_metadata && (
            <div>
              <p className="mb-1.5 text-xs font-medium text-muted-foreground">{t("logs.labels")}</p>
              <pre className="overflow-x-auto rounded-md border border-border/70 bg-muted/30 p-3 font-mono text-xs">
                {log.request_metadata}
              </pre>
            </div>
          )}

          <p className="border-t border-border pt-3 text-xs text-muted-foreground">
            {t("logs.privacyNote")}
          </p>
        </div>
      )}
    </Dialog>
  );
}

function Detail({
  label,
  value,
  mono,
  hint,
}: {
  label: string;
  value: string;
  mono?: boolean;
  hint?: string;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={cn("truncate", mono && "font-mono text-xs")} title={hint}>
        {value}
      </dd>
    </div>
  );
}

/**
 * Renders a measurement that only a streamed reply can produce.
 *
 * Time to first token, generation time and output speed are all differences
 * between the instants tokens arrived, and a buffered reply has none: the whole
 * body lands at once, so there is no first or last token to measure between.
 * That is a different answer from "measured, and it was zero", and different
 * again from a stream whose upstream reported no token counts — which is why a
 * non-streaming request says so instead of showing the same dash as a real gap.
 */
// What a cost rests on: whose price it used, and whether anything had to be
// assumed to reach the number. An estimate that looks exact is worse than one
// that says what it stands on.
function costTitle(log: RequestLog, t: TFunction): string {
  const source =
    log.cost_source === "custom" ? t("pricing.sourceCustom") : t("pricing.sourceCatalog");
  const parts = [t("logs.costFrom", { source })];
  if (log.cost_note.includes("long_context_price")) parts.push(t("logs.costLongContext"));
  if (log.cost_note.includes("cache_price_assumed")) parts.push(t("logs.costAssumed"));
  if (log.cost_note.includes("historical_backfill_current_price")) {
    parts.push(t("logs.costBackfilled"));
  }
  return parts.join(" ");
}

function streamOnly(log: RequestLogDetail, value: string | null, notMeasurable: string): string {
  if (!log.stream) return notMeasurable;
  return value ?? "—";
}

export function FidelityRow({ note }: { note: FidelityNote }) {
  const t = useT();
  const variant =
    note.fidelity === "unsupported"
      ? "destructive"
      : note.fidelity === "lossy"
        ? "warning"
        : note.fidelity === "semantic"
          ? "accent"
          : "outline";
  return (
    <li className="flex flex-wrap items-start gap-2 rounded-md border border-border/70 bg-muted/30 px-3 py-2 text-sm">
      <Badge variant={variant}>{t(fidelityKey(note.fidelity))}</Badge>
      <code className="font-mono text-xs text-muted-foreground">{note.field}</code>
      <span className="w-full text-xs text-muted-foreground sm:w-auto sm:flex-1">{note.detail}</span>
      <span className="font-mono text-[10px] text-muted-foreground/60">{note.stage}</span>
    </li>
  );
}

// fidelityKey maps the API's fidelity string onto a catalog key, keeping the
// wire vocabulary and the display vocabulary independent.
function fidelityKey(f: FidelityNote["fidelity"]): Parameters<TFunction>[0] {
  switch (f) {
    case "exact":
      return "fidelity.exact";
    case "semantic":
      return "fidelity.semantic";
    case "lossy":
      return "fidelity.lossy";
    default:
      return "fidelity.unsupported";
  }
}
