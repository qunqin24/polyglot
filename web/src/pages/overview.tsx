import * as React from "react";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Server, KeyRound } from "lucide-react";
import {
  api,
  type ConversionStats,
  type LatencyStats,
  type ModelStat,
  type RequestLog,
  type SpendDimension,
  type SpendRange,
} from "@/lib/api";
import { useAsync, useInterval } from "@/lib/hooks";
import { BlockSkeleton, LoadingGate } from "@/components/kit/skeletons";
import { useT, type TFunction } from "@/lib/i18n";
import { cn, formatDuration, formatNumber } from "@/lib/utils";
import { PageHeader } from "@/components/layout/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge, ErrorBanner, Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/kit/misc";
import {
  FlowChart,
  HeatMatrix,
  Histogram,
  Legend,
  PercentileChart,
  Scatter,
  seriesColor,
} from "@/components/kit/chart";
import { LivePanel } from "@/components/overview/live";
import { Budgets, SpendChart, SpendFacts, SpendRuler, SpendTable, TokenSplit } from "@/components/overview/spend";
import { SPEND_DIMENSIONS, SPEND_RANGES, spendColor, usd } from "@/components/overview/spend-format";

/**
 * The Overview answers two questions. Above: what this period is costing,
 * where it is heading and where the money went — the spend report. In the
 * middle: what is happening right now, from the live stream. Below: how the
 * protocol conversion and latency look, one panel mounted at a time so the
 * page people leave open only pays for what somebody is looking at.
 */
export function Overview() {
  const t = useT();
  const [range, setRange] = React.useState<SpendRange>("month");
  const [dim, setDim] = React.useState<SpendDimension>("model");

  const spend = useAsync(() => api.spend(range, dim), ["spend", range, dim], { keepPrevious: true });
  // The live stream colours a model the way the spend chart does when it is
  // split by model, whatever the chart is showing at the moment. With the
  // chart on "model" this is the same request, shared through the cache.
  const byModel = useAsync(() => api.spend(range, "model"), ["spend", range, "model"], { keepPrevious: true });
  const providers = useAsync(() => api.providers(), ["providers"]);
  const keys = useAsync(() => api.keys(), ["keys"]);
  const models = useAsync(() => api.models({ limit: 2000 }), ["models", "", "", 2000]);

  useInterval(() => {
    void spend.reload();
    if (dim !== "model") void byModel.reload();
  }, 30_000);

  const names = React.useMemo(() => {
    const out: Record<string, string> = { ...(byModel.data?.names ?? {}) };
    for (const m of models.data?.models ?? []) {
      if (m.catalog_name) out[m.upstream_model_id] = m.catalog_name;
    }
    return out;
  }, [byModel.data, models.data]);

  const colorFor = React.useCallback(
    (model: string) => {
      const stacks = byModel.data?.stacks ?? [];
      const i = stacks.findIndex((b) => b.key === model);
      return i >= 0 ? spendColor(i) : spendColor(5, true);
    },
    [byModel.data],
  );

  // The conversion and latency panels take hours; "this month" is however
  // many hours the month has run.
  const panelHours =
    range === "24h"
      ? 24
      : range === "7d"
        ? 168
        : range === "30d"
          ? 720
          : Math.max(1, Math.ceil((Date.now() / 1000 - (spend.data?.from ?? Date.now() / 1000 - 86400)) / 3600));

  const needsSetup =
    !providers.loading &&
    !keys.loading &&
    ((providers.data?.length ?? 0) === 0 || (keys.data?.length ?? 0) === 0);

  const s = spend.data;
  const rangeLabels: Record<SpendRange, string> = {
    "24h": t("spend.range24h"),
    "7d": t("spend.range7d"),
    "30d": t("spend.range30d"),
    month: t("spend.rangeMonth"),
  };
  const dimLabels: Record<SpendDimension, string> = {
    model: t("spend.byModel"),
    provider: t("spend.byProvider"),
    key: t("spend.byKey"),
  };

  return (
    <>
      <PageHeader
        title={t("overview.title")}
        action={
          <Segmented
            label={t("spend.rangeLabel")}
            value={range}
            onChange={setRange}
            options={SPEND_RANGES.map((r) => ({ value: r, label: rangeLabels[r] }))}
          />
        }
      />

      {needsSetup && (
        <GettingStarted
          providers={providers.data?.length ?? 0}
          keys={keys.data?.length ?? 0}
          t={t}
        />
      )}

      <ErrorBanner message={spend.error} onRetry={() => void spend.reload()} />

      {s ? (
        <div className={cn("transition-opacity", spend.stale && "opacity-60")} aria-busy={spend.refreshing}>
          <SpendRuler stats={s} />
          <SpendFacts stats={s} />
        </div>
      ) : (
        <LoadingGate
          loading={spend.initialLoading}
          skeleton={
            <div className="mb-7 space-y-5">
              <BlockSkeleton className="h-24" />
              <div className="grid gap-3 md:grid-cols-3">
                <BlockSkeleton className="h-[86px]" />
                <BlockSkeleton className="h-[86px]" />
                <BlockSkeleton className="h-[86px]" />
              </div>
            </div>
          }
        >
          {null}
        </LoadingGate>
      )}

      <LivePanel colorFor={colorFor} names={names} />

      <Card>
        <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-3">
          <CardTitle>{s && s.bucket_seconds < 86400 ? t("spend.perHour") : t("spend.perDay")}</CardTitle>
          <Segmented
            label={t("spend.dimLabel")}
            value={dim}
            onChange={setDim}
            options={SPEND_DIMENSIONS.map((d) => ({ value: d, label: dimLabels[d] }))}
          />
        </CardHeader>
        <CardContent className={cn("transition-opacity", spend.stale && "opacity-60")}>
          {s ? (
            <SpendChart stats={s} />
          ) : (
            <LoadingGate loading={spend.initialLoading} skeleton={<BlockSkeleton className="h-[220px]" />}>
              {null}
            </LoadingGate>
          )}
        </CardContent>
      </Card>

      <div className="mt-4 grid gap-4 xl:grid-cols-[1.6fr_1fr]">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between">
            <CardTitle>{dimLabels[dim]}</CardTitle>
            {s && (
              <span className="text-xs text-muted-foreground">
                {t("spend.shareOf", { total: usd(s.cost_usd) })}
              </span>
            )}
          </CardHeader>
          <CardContent className={cn("transition-opacity", spend.stale && "opacity-60")}>
            {s ? (
              <SpendTable stats={s} />
            ) : (
              <LoadingGate loading={spend.initialLoading} skeleton={<BlockSkeleton className="h-64" />}>
                {null}
              </LoadingGate>
            )}
          </CardContent>
        </Card>
        <div className="grid content-start gap-4">
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle>{t("spend.tokens")}</CardTitle>
              {s && (
                <span className="text-xs text-muted-foreground">
                  {formatNumber(s.input_tokens + s.output_tokens)} Token
                </span>
              )}
            </CardHeader>
            <CardContent>
              {s ? (
                <TokenSplit stats={s} />
              ) : (
                <LoadingGate loading={spend.initialLoading} skeleton={<BlockSkeleton className="h-28" />}>
                  {null}
                </LoadingGate>
              )}
            </CardContent>
          </Card>
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle>{t("spend.budgets")}</CardTitle>
              <span className="text-xs text-muted-foreground">{t("spend.budgetsNote")}</span>
            </CardHeader>
            <CardContent>
              {keys.data ? (
                <Budgets keys={keys.data} />
              ) : (
                <LoadingGate loading={keys.initialLoading} skeleton={<BlockSkeleton className="h-28" />}>
                  {null}
                </LoadingGate>
              )}
            </CardContent>
          </Card>
        </div>
      </div>

      {/* Radix unmounts the panel that is not selected, which is the point:
          each one fetches its own numbers when it is opened and stops costing
          anything the moment it is not. */}
      <Tabs defaultValue="conversion" className="mt-8">
        <TabsList>
          <TabsTrigger value="conversion">{t("overview.panelConversion")}</TabsTrigger>
          <TabsTrigger value="latency">{t("overview.panelLatency")}</TabsTrigger>
        </TabsList>
        {/* A floor under the panels keeps the scroll position when one is
            swapped for the other and the new one is still loading. */}
        <div className="mt-4 min-h-[36rem]">
          <TabsContent value="conversion">
            <ConversionPanel hours={panelHours} />
          </TabsContent>
          <TabsContent value="latency">
            <LatencyPanel hours={panelHours} />
          </TabsContent>
        </div>
      </Tabs>
    </>
  );
}

/** A small segmented control: a group of toggle buttons, one pressed. */
function Segmented<T extends string>({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: string }[];
}) {
  return (
    <div role="group" aria-label={label} className="inline-flex rounded-lg bg-muted p-[3px]">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
          className={cn(
            "rounded-md px-2.5 py-1 text-[13px] text-muted-foreground transition-colors",
            o.value === value ? "bg-background text-foreground shadow-xs" : "hover:text-foreground",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

/** The width of one point, said in the unit that reads best for it. */
function bucketLabel(seconds: number, t: TFunction): string {
  if (seconds < 3600) return t("overview.bucketMinutes", { count: Math.round(seconds / 60) });
  if (seconds < 86400) return t("overview.bucketHours", { count: Math.round(seconds / 3600) });
  return t("overview.bucketDays", { count: Math.round(seconds / 86400) });
}

function PanelFrame({
  loading,
  stale,
  error,
  onRetry,
  empty,
  children,
  t,
}: {
  loading: boolean;
  /** The numbers on screen are from the previous time window. */
  stale?: boolean;
  error: string;
  onRetry?: () => void;
  empty: boolean;
  children: React.ReactNode;
  t: TFunction;
}) {
  if (error) return <ErrorBanner message={error} onRetry={onRetry} />;
  return (
    <LoadingGate
      loading={loading}
      skeleton={
        <div className="min-h-[32rem] space-y-4">
          <BlockSkeleton className="h-64 rounded-xl" />
          <div className="grid gap-4 lg:grid-cols-2">
            <BlockSkeleton className="h-48 rounded-xl" />
            <BlockSkeleton className="h-48 rounded-xl" />
          </div>
        </div>
      }
    >
      {empty ? (
        <Card>
          <CardContent className="flex min-h-[28rem] items-center justify-center text-sm text-muted-foreground">
            {t("overview.panelEmpty")}
          </CardContent>
        </Card>
      ) : (
        <div aria-busy={stale || undefined} className={cn("transition-opacity duration-200", stale && "opacity-60")}>
          {children}
        </div>
      )}
    </LoadingGate>
  );
}

// --- conversion -------------------------------------------------------------

/** What only this gateway can be asked: which protocol came in, which went
 *  out, and what did not survive between them. */
function ConversionPanel({ hours }: { hours: number }) {
  const t = useT();
  const conv = useAsync(() => api.conversionStats(hours), ["conversion-stats", hours], { keepPrevious: true });
  const protocols = useAsync(() => api.protocols(), ["protocols"]);

  const c: ConversionStats | null = conv.data;
  const names = protocols.data?.map((p) => p.name) ?? [];
  const label = (name: string) =>
    protocols.data?.find((p) => p.name === name)?.label ?? name;
  const cell = (client: string, upstream: string) =>
    c?.pairs.find((p) => p.client_protocol === client && p.upstream_protocol === upstream)?.count ??
    0;

  return (
    <PanelFrame
      loading={conv.initialLoading}
      stale={conv.stale}
      onRetry={() => void conv.reload()}
      error={conv.error}
      empty={!!c && c.total_requests === 0}
      t={t}
    >
      {c && (
        <div className="space-y-4">
          <div className="grid gap-4 lg:grid-cols-5">
            <Card className="lg:col-span-3">
              <CardHeader className="flex flex-row items-baseline justify-between">
                <CardTitle>{t("overview.matrix")}</CardTitle>
                {/* These two summarise the matrix, so they sit on it rather
                    than in a row of cards of their own. The request count that
                    used to sit beside them is already in the spine above. */}
                <div className="flex gap-4 text-xs text-muted-foreground">
                  <span title={t("overview.convertedHint")}>
                    {t("overview.converted")}{" "}
                    <span className="tabular-nums text-foreground">
                      {share(c.converted_requests, c.total_requests)}
                    </span>
                  </span>
                  <span title={t("overview.lossyHint")}>
                    {t("overview.lossy")}{" "}
                    <span
                      className={cn(
                        "tabular-nums",
                        c.lossy_requests > 0 ? "text-[--color-warning]" : "text-foreground",
                      )}
                    >
                      {share(c.lossy_requests, c.total_requests)}
                    </span>
                  </span>
                </div>
              </CardHeader>
              <CardContent>
                <HeatMatrix
                  rows={names}
                  columns={names}
                  value={cell}
                  label={label}
                  format={formatNumber}
                />
                <p className="mt-3 text-xs text-muted-foreground">{t("overview.matrixHint")}</p>
              </CardContent>
            </Card>

            <Card className="lg:col-span-2">
              <CardHeader>
                <CardTitle>{t("overview.flow")}</CardTitle>
              </CardHeader>
              <CardContent>
                <FlowChart
                  links={c.flows.map((f) => ({
                    from: f.client_protocol,
                    to: f.provider_name,
                    count: f.count,
                  }))}
                />
                <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1">
                  {[...new Set(c.flows.map((f) => f.client_protocol))].map((p, i) => (
                    <span
                      key={p}
                      className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground"
                    >
                      <span
                        className="size-2 rounded-[2px]"
                        style={{ background: seriesColor(i) }}
                      />
                      {label(p)}
                    </span>
                  ))}
                </div>
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle>{t("overview.fidelityFields")}</CardTitle>
            </CardHeader>
            <CardContent>
              {c.fields.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t("overview.noFidelityNotes")}</p>
              ) : (
                <>
                  <div className="space-y-2">
                    {c.fields.slice(0, 8).map((f) => (
                      <div key={`${f.field}-${f.fidelity}`} className="flex items-center gap-3">
                        <span className="w-44 shrink-0 truncate font-mono text-xs">{f.field}</span>
                        <Badge variant={fidelityTone(f.fidelity)}>{t(`fidelity.${f.fidelity}`)}</Badge>
                        <div className="h-1.5 flex-1 rounded-full bg-muted">
                          <div
                            className="h-1.5 rounded-full bg-[--color-warning]"
                            style={{ width: `${(f.count / c.fields[0].count) * 100}%` }}
                          />
                        </div>
                        <span className="w-12 shrink-0 text-right text-xs tabular-nums text-muted-foreground">
                          {formatNumber(f.count)}
                        </span>
                      </div>
                    ))}
                  </div>
                  <p className="mt-3 text-xs text-muted-foreground">{t("overview.fidelityHint")}</p>
                </>
              )}
            </CardContent>
          </Card>
        </div>
      )}
    </PanelFrame>
  );
}

function fidelityTone(f: ConversionStats["fields"][number]["fidelity"]) {
  if (f === "unsupported") return "destructive" as const;
  if (f === "lossy") return "warning" as const;
  return "outline" as const;
}

// --- performance ------------------------------------------------------------

/** Where the time went and what broke. Percentiles rather than an average,
 *  because an average is exactly the number that hides a bad tail. */
function LatencyPanel({ hours }: { hours: number }) {
  const t = useT();
  const lat = useAsync(() => api.latencyStats(hours), ["latency-stats", hours], { keepPrevious: true });
  const models = useAsync(() => api.modelStats(hours), ["model-stats", hours]);

  const l: LatencyStats | null = lat.data;
  const requests = l?.series.reduce((sum, p) => sum + p.count, 0) ?? 0;

  // Empty buckets at either end are dropped before drawing. This chart is the
  // one place where empty means nothing at all: no request means no median,
  // and a stretch with no median says less than the space it takes. The
  // timeline on the spine keeps its empty stretches, because there zero
  // requests is itself the fact being reported. Gaps in the middle stay —
  // they are surrounded by measurements and so have a shape.
  const drawn = trimEmpty(l?.series ?? []);
  const trimmed = !!l && drawn.length < l.series.length;
  // A bucket with no requests has no percentile. Null keeps it out of the
  // line instead of pinning it to zero.
  const pick = (key: "p50" | "p95" | "p99") =>
    drawn.map((p) => (p.count > 0 ? p[key] : null));

  const dots = (models.data?.stats ?? [])
    .filter((m: ModelStat) => m.ttft_p95_ms !== null && m.tps_median !== null)
    .slice(0, 12)
    .map((m: ModelStat) => ({
      key: `${m.provider_name}/${m.upstream_model}`,
      x: m.ttft_p95_ms!,
      y: m.tps_median!,
      size: m.requests,
      title: `${m.upstream_model} · ${m.provider_name} · ${formatDuration(m.ttft_p95_ms!)} · ${m.tps_median!.toFixed(1)} tok/s`,
    }));

  return (
    <PanelFrame loading={lat.initialLoading} stale={lat.stale} onRetry={() => void lat.reload()} error={lat.error} empty={!!l && requests === 0} t={t}>
      {l && (
        <div className="space-y-4">
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle>{t("overview.percentiles")}</CardTitle>
              <Legend
                items={[
                  { label: "p50", color: "var(--color-muted-foreground)" },
                  { label: "p95", color: "var(--color-chart-1)" },
                  { label: "p99", color: "var(--color-warning)" },
                ]}
              />
            </CardHeader>
            <CardContent>
              <PercentileChart p50={pick("p50")} p95={pick("p95")} p99={pick("p99")} />
              <p className="mt-2 text-xs text-muted-foreground">
                {t("overview.percentilesHint")} · {bucketLabel(l.bucket_seconds, t)}
                {trimmed ? ` · ${t("overview.percentilesTrimmed")}` : ""}
              </p>
            </CardContent>
          </Card>

          <div className="grid gap-4 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle>{t("overview.distribution")}</CardTitle>
              </CardHeader>
              <CardContent>
                {/* A bar covers everything over the previous edge and up to
                    its own, so the tick is one number and the tooltip is the
                    span. The symbols read the same in both locales, the way
                    formatDuration's units already do. */}
                <Histogram
                  bars={l.histogram.map((b, i) => {
                    const lower = l.histogram[i - 1]?.upper_ms ?? 0;
                    if (b.upper_ms === 0) {
                      return {
                        label: `> ${formatDuration(lower)}`,
                        range: `> ${formatDuration(lower)}`,
                        count: b.count,
                        overflow: true,
                      };
                    }
                    return {
                      label: formatDuration(b.upper_ms),
                      range:
                        lower === 0
                          ? `< ${formatDuration(b.upper_ms)}`
                          : `${formatDuration(lower)} – ${formatDuration(b.upper_ms)}`,
                      count: b.count,
                    };
                  })}
                />
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>{t("overview.speed")}</CardTitle>
              </CardHeader>
              <CardContent>
                {dots.length === 0 ? (
                  <p className="py-8 text-center text-sm text-muted-foreground">
                    {t("overview.speedNone")}
                  </p>
                ) : (
                  <>
                    <Scatter dots={dots} xLabel="TTFT p95" yLabel="tok/s" />
                    <p className="mt-2 text-xs text-muted-foreground">{t("overview.speedHint")}</p>
                  </>
                )}
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle>{t("overview.errorBreakdown")}</CardTitle>
            </CardHeader>
            <CardContent>
              {l.errors.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t("overview.noErrors")}</p>
              ) : (
                <div className="space-y-2">
                  {l.errors.map((e) => (
                    <div key={`${e.status_code}-${e.error_type}`} className="flex items-center gap-3">
                      <Badge variant={e.status_code >= 500 ? "destructive" : "warning"}>
                        {e.status_code || "—"}
                      </Badge>
                      <span className="w-40 shrink-0 truncate font-mono text-xs text-muted-foreground">
                        {e.error_type || "—"}
                      </span>
                      <div className="h-1.5 flex-1 rounded-full bg-muted">
                        <div
                          className="h-1.5 rounded-full bg-destructive"
                          style={{ width: `${(e.count / l.errors[0].count) * 100}%` }}
                        />
                      </div>
                      <span className="w-12 shrink-0 text-right text-xs tabular-nums text-muted-foreground">
                        {formatNumber(e.count)}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      )}
    </PanelFrame>
  );
}

/** Drops the buckets at both ends that carried no requests, keeping every
 *  bucket between the first and the last that did — including the empty ones,
 *  which are a real gap in a run of traffic rather than time before it
 *  started. */
function trimEmpty<T extends { count: number }>(series: T[]): T[] {
  let first = 0;
  while (first < series.length && series[first].count === 0) first++;
  let last = series.length - 1;
  while (last >= first && series[last].count === 0) last--;
  return series.slice(first, last + 1);
}

/** A count as a share of a total, or a dash when there is no total to be a
 *  share of. Zero out of zero is not zero percent. */
function share(n: number, total: number): string {
  if (total === 0) return "—";
  return `${((n / total) * 100).toFixed(1)}%`;
}

export function StatusBadge({ log }: { log: RequestLog }) {
  const t = useT();
  if (log.status === "success") {
    return <Badge variant="success">{log.status_code}</Badge>;
  }
  if (log.status === "cancelled") {
    return <Badge variant="warning">{t("overview.cancelled")}</Badge>;
  }
  return <Badge variant="destructive">{log.status_code || t("logs.errorLabel")}</Badge>;
}

export function ProtocolFlow({ log }: { log: RequestLog }) {
  if (!log.upstream_protocol) {
    return <span className="text-xs text-muted-foreground">{log.client_protocol}</span>;
  }
  const converted = log.client_protocol !== log.upstream_protocol;
  return (
    <span className="inline-flex items-center gap-1 text-xs">
      <span className="text-muted-foreground">{log.client_protocol}</span>
      <ArrowRight className={cn("size-3", converted ? "text-primary" : "text-muted-foreground/50")} />
      <span className={cn(converted ? "font-medium text-primary" : "text-muted-foreground")}>
        {log.upstream_protocol}
      </span>
      {log.provider_name && (
        <span className="ml-1 text-muted-foreground/70">· {log.provider_name}</span>
      )}
    </span>
  );
}

function GettingStarted({
  providers,
  keys,
  t,
}: {
  providers: number;
  keys: number;
  t: TFunction;
}) {
  const steps = [
    { done: providers > 0, label: t("overview.stepProvider"), to: "/providers", icon: Server },
    { done: keys > 0, label: t("overview.stepKey"), to: "/keys", icon: KeyRound },
  ];
  return (
    <Card className="mb-4 border-primary/25 bg-accent/40">
      <CardContent className="flex flex-wrap items-center gap-x-6 gap-y-3 p-4">
        <p className="text-sm font-medium">{t("overview.gettingStarted")}</p>
        {steps.map((s) => (
          <Link
            key={s.to}
            to={s.to}
            className={cn(
              "inline-flex items-center gap-2 text-sm transition-colors",
              s.done ? "text-muted-foreground line-through" : "text-foreground hover:text-primary",
            )}
          >
            <s.icon className="size-3.5" />
            {s.label}
          </Link>
        ))}
      </CardContent>
    </Card>
  );
}
