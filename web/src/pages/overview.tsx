import * as React from "react";
import { Link } from "react-router-dom";
import { ArrowRight, Server, KeyRound } from "lucide-react";
import {
  api,
  type APIKey,
  type ConversionStats,
  type CostStats,
  type LatencyStats,
  type ModelStat,
  type RequestLog,
} from "@/lib/api";
import { useAsync, useInterval } from "@/lib/hooks";
import { useT, type TFunction } from "@/lib/i18n";
import { cn, formatDuration, formatNumber, formatUSD } from "@/lib/utils";
import { PageHeader } from "@/components/layout";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Badge,
  ErrorBanner,
  Select,
  Spinner,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/misc";
import { Button } from "@/components/ui/button";
import {
  CompositionBar,
  FlowChart,
  HeatMatrix,
  Histogram,
  Legend,
  PercentileChart,
  Ring,
  Scatter,
  seriesColor,
  Sparkline,
  StackedBars,
  TimelineChart,
  Treemap,
} from "@/components/ui/chart";

/**
 * The Overview is two things stacked. Above: the numbers and the one timeline
 * everybody wants, always on screen. Below: three panels — conversion,
 * performance, spend — of which exactly one is mounted, so the page that gets
 * left open all day only pays for the charts somebody is looking at.
 */
export function Overview() {
  const t = useT();
  const [hours, setHours] = React.useState("24");
  const windowHours = Number(hours);

  const stats = useAsync(() => api.stats(windowHours), [hours]);
  const providers = useAsync(() => api.providers(), []);
  const keys = useAsync(() => api.keys(), []);

  // The Overview is the page people leave open, so it refreshes itself. The
  // panels below refresh with it only in the sense that switching tabs
  // remounts them; nothing polls a panel nobody is watching.
  useInterval(() => stats.reload(), 15000);

  const s = stats.data;
  const successRate =
    s && s.total_requests > 0 ? (s.success_count / s.total_requests) * 100 : null;

  // Models no longer need configuring: a provider discovers its own. Setup is
  // done once there is a provider and a key.
  const needsSetup =
    !providers.loading &&
    !keys.loading &&
    ((providers.data?.length ?? 0) === 0 || (keys.data?.length ?? 0) === 0);

  return (
    <>
      <PageHeader
        title={t("overview.title")}
        description={t("overview.description")}
        action={
          <Select
            value={hours}
            onValueChange={setHours}
            className="w-36"
            options={[
              { value: "1", label: t("overview.lastHour") },
              { value: "24", label: t("overview.last24Hours") },
              { value: "168", label: t("overview.last7Days") },
              { value: "720", label: t("overview.last30Days") },
            ]}
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

      <ErrorBanner message={stats.error} />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <Stat
          label={t("overview.requests")}
          value={s ? formatNumber(s.total_requests) : "—"}
          loading={stats.loading}
          trend={s?.series.map((b) => b.count)}
        />
        {/* No sparkline under the next two. A bucket nobody called in has no
            success rate and no latency, and a line has to have a value for
            every point — so it would draw a quiet night as a perfect one. */}
        <Stat
          label={t("overview.successRate")}
          value={successRate === null ? "—" : `${successRate.toFixed(1)}%`}
          tone={successRate !== null && successRate < 95 ? "warn" : undefined}
          loading={stats.loading}
          sub={
            s && s.error_count > 0
              ? t("overview.errorCount", { count: formatNumber(s.error_count) })
              : undefined
          }
        />
        <Stat
          label={t("overview.latency")}
          value={s ? formatDuration(s.avg_latency_ms) : "—"}
          sub={
            s && s.p95_latency_ms > 0
              ? t("overview.p95", { value: formatDuration(s.p95_latency_ms) })
              : undefined
          }
          loading={stats.loading}
        />
        {/* An estimate from a published price list, not a bill. The count of
            requests nobody could price sits under it, because a total with
            silent gaps in it reads as a complete one. */}
        <Stat
          label={t("overview.cost")}
          value={s ? formatUSD(s.cost_usd) : "—"}
          sub={
            s && s.unpriced_requests > 0
              ? t("overview.costUnpriced", { count: formatNumber(s.unpriced_requests) })
              : undefined
          }
          loading={stats.loading}
          trend={s?.series.map((b) => b.cost_usd)}
          trendColor="var(--color-chart-3)"
        />
        <Stat
          label={t("overview.tokens")}
          value={s ? formatNumber(s.input_tokens + s.output_tokens) : "—"}
          sub={
            s
              ? t("overview.tokenSplit", {
                  input: formatNumber(s.input_tokens),
                  output: formatNumber(s.output_tokens),
                })
              : undefined
          }
          loading={stats.loading}
          trend={s?.series.map((b) => b.input_tokens + b.output_tokens)}
          trendColor="var(--color-chart-4)"
        />
      </div>

      {/* Traffic, and where it went, in one card. They were two, and two
          cards cost two headers and two sets of padding to say one thing. */}
      <Card className="mt-4">
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>{t("overview.timeline")}</CardTitle>
          <div className="flex items-center gap-3">
            <Legend
              items={[
                { label: t("overview.timelineTotal"), color: "var(--color-chart-1)" },
                { label: t("overview.timelineErrors"), color: "var(--color-destructive)" },
              ]}
            />
            <span className="text-[11px] text-muted-foreground">
              {s ? bucketLabel(s.bucket_seconds, t) : ""}
            </span>
            <Button variant="ghost" size="sm" asChild>
              <Link to="/logs">
                {t("overview.allLogs")} <ArrowRight className="size-3.5" />
              </Link>
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {s ? (
            <TimelineChart
              total={s.series.map((b) => b.count)}
              errors={s.series.map((b) => b.errors)}
              height={72}
            />
          ) : (
            <div className="flex h-[72px] items-center justify-center">
              <Spinner className="text-muted-foreground" />
            </div>
          )}

          <div className="mt-4 border-t pt-3">
            <p className="mb-2 text-xs text-muted-foreground">{t("nav.providers")}</p>
            {providers.loading ? (
              <Spinner className="text-muted-foreground" />
            ) : (providers.data?.length ?? 0) === 0 ? (
              <p className="text-sm text-muted-foreground">{t("overview.noProviders")}</p>
            ) : (
              <div className="space-y-1.5">
                {providers.data!.map((p) => {
                  const usage = s?.by_provider?.find((x) => x.provider_name === p.name);
                  const share =
                    usage && s && s.total_requests > 0 ? usage.count / s.total_requests : 0;
                  return (
                    <div key={p.id} className="flex items-center gap-3">
                      <span
                        className={cn(
                          "size-1.5 shrink-0 rounded-full",
                          p.enabled ? "bg-[--color-success]" : "bg-muted-foreground/40",
                        )}
                      />
                      <span className="w-36 shrink-0 truncate text-sm">{p.name}</span>
                      <span className="w-28 shrink-0 truncate text-xs text-muted-foreground">
                        {p.protocol}
                      </span>
                      <div className="h-1 min-w-8 flex-1 rounded-full bg-muted">
                        <div
                          className="h-1 rounded-full bg-[--color-chart-1]"
                          style={{ width: `${Math.max(share > 0 ? 2 : 0, share * 100)}%` }}
                        />
                      </div>
                      {usage && usage.error_count > 0 && (
                        <span className="shrink-0 text-xs text-destructive">
                          {t("overview.errorCount", { count: usage.error_count })}
                        </span>
                      )}
                      <span className="w-12 shrink-0 text-right text-sm tabular-nums">
                        {usage ? formatNumber(usage.count) : "0"}
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </CardContent>
      </Card>

      {/* Radix unmounts the panel that is not selected, which is the point:
          each one fetches its own numbers when it is opened and stops costing
          anything the moment it is not. */}
      <Tabs defaultValue="conversion" className="mt-6">
        <TabsList>
          <TabsTrigger value="conversion">{t("overview.panelConversion")}</TabsTrigger>
          <TabsTrigger value="latency">{t("overview.panelLatency")}</TabsTrigger>
          <TabsTrigger value="cost">{t("overview.panelCost")}</TabsTrigger>
        </TabsList>
        {/* The panel being switched away from is unmounted, and the one
            arriving renders a spinner first. Without a floor under them the
            document collapses to that spinner's height, the browser clamps a
            scroll position that no longer exists, and the page jumps to the
            top. The floor is what keeps you where you were standing. */}
        <div className="mt-4 min-h-[36rem]">
          <TabsContent value="conversion">
            <ConversionPanel hours={windowHours} />
          </TabsContent>
          <TabsContent value="latency">
            <LatencyPanel hours={windowHours} />
          </TabsContent>
          <TabsContent value="cost">
            <CostPanel hours={windowHours} keys={keys.data ?? []} />
          </TabsContent>
        </div>
      </Tabs>
    </>
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
  error,
  empty,
  children,
  t,
}: {
  loading: boolean;
  error: string;
  empty: boolean;
  children: React.ReactNode;
  t: TFunction;
}) {
  if (error) return <ErrorBanner message={error} />;
  if (loading) {
    return (
      <div className="flex min-h-[32rem] items-center justify-center">
        <Spinner className="text-muted-foreground" />
      </div>
    );
  }
  if (empty) {
    return (
      <Card>
        <CardContent className="flex min-h-[28rem] items-center justify-center text-sm text-muted-foreground">
          {t("overview.panelEmpty")}
        </CardContent>
      </Card>
    );
  }
  return <>{children}</>;
}

// --- conversion -------------------------------------------------------------

/** What only this gateway can be asked: which protocol came in, which went
 *  out, and what did not survive between them. */
function ConversionPanel({ hours }: { hours: number }) {
  const t = useT();
  const conv = useAsync(() => api.conversionStats(hours), [hours]);
  const protocols = useAsync(() => api.protocols(), []);

  const c: ConversionStats | null = conv.data;
  const names = protocols.data?.map((p) => p.name) ?? [];
  const label = (name: string) =>
    protocols.data?.find((p) => p.name === name)?.label ?? name;
  const cell = (client: string, upstream: string) =>
    c?.pairs.find((p) => p.client_protocol === client && p.upstream_protocol === upstream)?.count ??
    0;

  return (
    <PanelFrame
      loading={conv.loading && !c}
      error={conv.error}
      empty={!!c && c.total_requests === 0}
      t={t}
    >
      {c && (
        <div className="space-y-4">
          <div className="grid gap-4 lg:grid-cols-5">
            <Card className="lg:col-span-3">
              <CardHeader className="flex-row items-baseline justify-between">
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
  const lat = useAsync(() => api.latencyStats(hours), [hours]);
  const models = useAsync(() => api.modelStats(hours), [hours]);

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
    <PanelFrame loading={lat.loading && !l} error={lat.error} empty={!!l && requests === 0} t={t}>
      {l && (
        <div className="space-y-4">
          <Card>
            <CardHeader className="flex-row items-center justify-between">
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

// --- spend ------------------------------------------------------------------

/** Cost visibility, not a bill. Every number here is an estimate from a
 *  published price list, and the requests nobody could price are counted
 *  beside it rather than quietly added in as free. */
function CostPanel({ hours, keys }: { hours: number; keys: APIKey[] }) {
  const t = useT();
  const cost = useAsync(() => api.costStats(hours), [hours]);
  const c: CostStats | null = cost.data;

  const budgeted = keys.filter((k) => k.budget_usd !== null && k.budget_usd > 0);
  const uncached = c ? Math.max(0, c.input_tokens - c.cached_input_tokens - c.cache_write_tokens) : 0;
  const priced = c?.models.filter((m) => m.cost_usd > 0) ?? [];

  return (
    <PanelFrame
      loading={cost.loading && !c}
      error={cost.error}
      empty={!!c && c.models.length === 0}
      t={t}
    >
      {c && (
        <div className="space-y-4">
          <div className="grid gap-4 lg:grid-cols-5">
            <Card className="lg:col-span-3">
              <CardHeader className="flex-row items-center justify-between">
                <CardTitle>{t("overview.spendOverTime")}</CardTitle>
                <span className="text-[11px] text-muted-foreground">
                  {bucketLabel(c.bucket_seconds, t)}
                </span>
              </CardHeader>
              <CardContent>
                {c.stacks.length === 0 ? (
                  <p className="py-10 text-center text-sm text-muted-foreground">
                    {t("overview.noSpend")}
                  </p>
                ) : (
                  <>
                    <StackedBars starts={c.starts} stacks={c.stacks.map((s) => ({
                      label: s.model || t("overview.otherModels"),
                      points: s.points,
                    }))} format={formatUSD} />
                    <div className="mt-3">
                      <Legend
                        items={c.stacks.map((s, i) => ({
                          label: s.model || t("overview.otherModels"),
                          color: seriesColor(i),
                        }))}
                      />
                    </div>
                  </>
                )}
                {c.unpriced_requests > 0 && (
                  <p className="mt-3 text-xs text-muted-foreground">
                    {t("overview.costUnpriced", { count: formatNumber(c.unpriced_requests) })}
                  </p>
                )}
              </CardContent>
            </Card>

            <Card className="lg:col-span-2">
              <CardHeader>
                <CardTitle>{t("overview.spendByModel")}</CardTitle>
              </CardHeader>
              <CardContent>
                {priced.length === 0 ? (
                  <p className="py-10 text-center text-sm text-muted-foreground">
                    {t("overview.noSpend")}
                  </p>
                ) : (
                  <Treemap
                    items={priced.slice(0, 8).map((m) => ({
                      key: `${m.provider_name}/${m.model}`,
                      value: m.cost_usd,
                      title: m.model,
                      subtitle: m.provider_name,
                    }))}
                    label={(item) => `${item.key} · ${formatUSD(item.value)}`}
                  />
                )}
              </CardContent>
            </Card>
          </div>

          <div className="grid gap-4 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle>{t("overview.tokenComposition")}</CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <div>
                  <p className="mb-1.5 text-xs text-muted-foreground">
                    {t("overview.tokensIn", { value: formatNumber(c.input_tokens) })}
                  </p>
                  <CompositionBar
                    parts={[
                      {
                        label: t("overview.cachedRead"),
                        value: c.cached_input_tokens,
                        color: "var(--color-chart-2)",
                      },
                      { label: t("overview.uncached"), value: uncached, color: "var(--color-chart-1)" },
                      {
                        label: t("overview.cacheWrite"),
                        value: c.cache_write_tokens,
                        color: "var(--color-chart-6)",
                      },
                    ]}
                  />
                </div>
                <div>
                  <p className="mb-1.5 text-xs text-muted-foreground">
                    {t("overview.tokensOut", { value: formatNumber(c.output_tokens) })}
                  </p>
                  <CompositionBar
                    parts={[
                      { label: t("overview.tokensOut", { value: "" }), value: c.output_tokens, color: "var(--color-chart-3)" },
                    ]}
                  />
                </div>
                <Legend
                  items={[
                    { label: t("overview.cachedRead"), color: "var(--color-chart-2)" },
                    { label: t("overview.uncached"), color: "var(--color-chart-1)" },
                    { label: t("overview.cacheWrite"), color: "var(--color-chart-6)" },
                  ]}
                />
                {/* Reasoning is reported beside the output, never as a slice of
                    it: providers disagree about whether it is already in
                    there, so any stacked bar would be wrong for some of them. */}
                <div className="flex items-baseline justify-between border-t pt-3">
                  <span className="text-xs text-muted-foreground">{t("overview.reasoning")}</span>
                  <span className="text-sm tabular-nums">{formatNumber(c.reasoning_tokens)}</span>
                </div>
                <p className="text-xs text-muted-foreground">{t("overview.reasoningAside")}</p>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>{t("overview.budgets")}</CardTitle>
              </CardHeader>
              <CardContent>
                {budgeted.length === 0 ? (
                  <p className="text-sm text-muted-foreground">{t("overview.noBudgets")}</p>
                ) : (
                  <>
                    <div className="space-y-3">
                      {budgeted.map((k) => {
                        const spent = k.spent_usd ?? 0;
                        const limit = k.budget_usd!;
                        const fraction = spent / limit;
                        return (
                          <div key={k.id} className="flex items-center gap-3">
                            <Ring
                              fraction={fraction}
                              tone={fraction >= 1 ? "over" : fraction >= 0.8 ? "warn" : "normal"}
                            />
                            <div className="min-w-0">
                              <p className="truncate text-sm">{k.name}</p>
                              <p className="text-xs text-muted-foreground">
                                {t("overview.budgetOf", {
                                  spent: formatUSD(spent),
                                  limit: formatUSD(limit),
                                })}{" "}
                                · {t(`overview.period${periodKey(k.budget_period)}`)}
                              </p>
                            </div>
                          </div>
                        );
                      })}
                    </div>
                    <p className="mt-3 text-xs text-muted-foreground">{t("overview.budgetsHint")}</p>
                  </>
                )}
              </CardContent>
            </Card>
          </div>
        </div>
      )}
    </PanelFrame>
  );
}

function periodKey(p: APIKey["budget_period"]): "Total" | "Daily" | "Weekly" | "Monthly" {
  if (p === "daily") return "Daily";
  if (p === "weekly") return "Weekly";
  if (p === "monthly") return "Monthly";
  return "Total";
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

function Stat({
  label,
  value,
  sub,
  tone,
  loading,
  trend,
  trendColor,
}: {
  label: string;
  value: string;
  sub?: string;
  tone?: "warn";
  loading?: boolean;
  /** Drawn only where a quiet bucket honestly means zero — counts, tokens,
      money. Never a rate or a latency. */
  trend?: number[];
  trendColor?: string;
}) {
  return (
    <Card>
      <CardContent className="p-4">
        <p className="text-xs text-muted-foreground">{label}</p>
        <p
          className={cn(
            "mt-1.5 text-2xl font-semibold tabular-nums tracking-tight",
            tone === "warn" && "text-[--color-warning]",
            loading && "opacity-50",
          )}
        >
          {value}
        </p>
        {sub && <p className="mt-0.5 text-xs text-muted-foreground">{sub}</p>}
        {trend && trend.length > 1 && (
          <Sparkline values={trend} color={trendColor} className="mt-1.5" />
        )}
      </CardContent>
    </Card>
  );
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
