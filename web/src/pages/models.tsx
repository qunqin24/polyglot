import * as React from "react";
import { Boxes, Info } from "lucide-react";
import { api, type ModelStat, type Model, type Provider } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useOptimisticFlags } from "@/lib/optimistic";
import { useT } from "@/lib/i18n";
import { cn, formatDuration, formatNumber, formatRelative } from "@/lib/utils";
import { PageHeader } from "@/components/layout/page-header";
import { Aliases } from "@/pages/aliases";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/kit/input";
import {
  Badge,
  EmptyState,
  ErrorBanner,
  Select,
  Switch,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/kit/misc";
import { ModelIcon } from "@/components/model-icon";
import { SaveStatus } from "@/components/kit/save-status";
import { CardGridSkeleton, LoadingGate, SkeletonLine } from "@/components/kit/skeletons";

// The Models page shows the real models each provider offers. Aliases are a
// second tab, because they are an optional convenience rather than a step
// anyone has to take.
export function Models() {
  const t = useT();
  const providers = useAsync(() => api.providers(), ["providers"]);

  return (
    <>
      <PageHeader title={t("models.title")} description={t("models.description")} />
      <Tabs defaultValue="models">
        <TabsList className="mb-4">
          <TabsTrigger value="models">{t("models.tabModels")}</TabsTrigger>
          <TabsTrigger value="aliases">{t("models.tabAliases")}</TabsTrigger>
        </TabsList>
        <TabsContent value="models">
          <Registry providers={providers.data ?? []} />
        </TabsContent>
        <TabsContent value="aliases">
          <Aliases providers={providers.data ?? []} />
        </TabsContent>
      </Tabs>
    </>
  );
}

function Registry({ providers }: { providers: Provider[] }) {
  const t = useT();
  const [providerID, setProviderID] = React.useState("");
  const [searchInput, setSearchInput] = React.useState("");
  const [search, setSearch] = React.useState("");

  React.useEffect(() => {
    const id = setTimeout(() => setSearch(searchInput.trim()), 300);
    return () => clearTimeout(id);
  }, [searchInput]);

  // keepPrevious: a new filter keeps the cards on screen, dimmed, until the
  // answer arrives, instead of replacing the grid with a spinner.
  const { data, initialLoading, refreshing, error, reload } = useAsync(
    () => api.models({ provider_id: providerID, search, limit: 500 }),
    ["models", providerID, search, 500],
    { keepPrevious: true },
  );
  // Loaded separately from the list: this is an aggregate over the request
  // log, and it must never be what decides whether the page appears.
  const stats = useAsync(() => api.modelStats(24), ["model-stats", 24]);

  // Keyed by provider *and* model. The same upstream id on two providers is
  // two different things to call, and telling them apart is the comparison
  // worth having.
  const statsByKey = React.useMemo(() => {
    const m = new Map<string, ModelStat>();
    for (const s of stats.data?.stats ?? []) {
      m.set(`${s.provider_name}\u0000${s.upstream_model}`, s);
    }
    return m;
  }, [stats.data]);

  const statOf = (m: Model) =>
    statsByKey.get(`${m.provider_name}\u0000${m.upstream_model_id}`);


  const flags = useOptimisticFlags<number>((id, enabled) => {
    const m = data?.models.find((x) => x.id === id);
    return m ? api.updateModel(m.id, m.display_name, enabled) : Promise.resolve();
  }, reload);

  const models = data?.models ?? [];
  const filtered = providerID !== "" || search !== "";

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Input
          placeholder={t("models.search")}
          value={searchInput}
          onChange={(e) => setSearchInput(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select
          value={providerID}
          onValueChange={setProviderID}
          className="w-44"
          placeholder={t("models.allProviders")}
          options={[
            { value: "", label: t("models.allProviders") },
            ...providers.map((p) => ({ value: String(p.id), label: p.name })),
          ]}
        />
        <div className="ml-auto flex items-center gap-2">
          {stats.data && (
            <span className="text-xs text-muted-foreground">
              {t("models.statsWindow", { hours: String(stats.data.hours) })}
            </span>
          )}
          {data && (
            <span className="text-xs text-muted-foreground">
              {t("models.count", { shown: models.length, total: data.total })}
            </span>
          )}
        </div>
      </div>

      <ErrorBanner message={error} onRetry={() => void reload()} />

      <LoadingGate loading={initialLoading} skeleton={<CardGridSkeleton count={8} />}>
      {models.length === 0 ? (
        <Card>
          <CardContent className="p-0">
            <EmptyState
              icon={Boxes}
              title={filtered ? t("models.emptyFiltered") : t("models.empty")}
              description={
                filtered
                  ? t("models.emptyFilteredHint")
                  : providers.length === 0
                    ? t("models.emptyNoProvider")
                    : t("models.emptyHint")
              }
            />
          </CardContent>
        </Card>
      ) : (
        <div
          aria-busy={refreshing || undefined}
          className={cn(
            "grid gap-4 transition-opacity duration-200 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4",
            refreshing && "opacity-60",
          )}
        >
          {models.map((m) => (
            <ModelCard
              key={m.id}
              model={{ ...m, enabled: flags.value(m.id, m.enabled) }}
              stat={statOf(m)}
              statsLoading={stats.initialLoading}
              saveStatus={
                <SaveStatus state={flags.status(m.id)} onRetry={() => flags.retry(m.id)} />
              }
              onToggle={(v) => void flags.toggle(m.id, v)}
            />
          ))}
        </div>
      )}
      </LoadingGate>
    </>
  );
}

function ModelCard({
  model: m,
  stat,
  statsLoading,
  saveStatus,
  onToggle,
}: {
  model: Model;
  stat?: ModelStat;
  statsLoading?: boolean;
  saveStatus?: React.ReactNode;
  onToggle: (enabled: boolean) => void;
}) {
  const t = useT();
  return (
    <div className="flex flex-col rounded-xl border bg-card p-4 text-card-foreground shadow-xs transition-colors hover:border-foreground/20">
      <div className="flex items-start gap-3">
        <ModelIcon
          id={m.upstream_model_id}
          maker={m.maker}
          className={cn(!m.enabled && "opacity-50 grayscale")}
        />
        <div className="min-w-0 flex-1">
          <p
            className={cn(
              "font-mono text-sm leading-5 font-medium break-all",
              !m.enabled && "text-muted-foreground",
            )}
          >
            {m.upstream_model_id}
          </p>
          {m.display_name && m.display_name !== m.upstream_model_id && (
            <p className="mt-0.5 truncate text-xs text-muted-foreground" title={m.display_name}>
              {m.display_name}
            </p>
          )}
        </div>
        <Switch
          checked={m.enabled}
          onCheckedChange={onToggle}
          aria-label={t("common.enabled")}
          className="mt-0.5"
        />
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-1.5">
        <span className="text-xs text-muted-foreground">{m.provider_name}</span>
        <Badge variant="accent">{m.protocol}</Badge>
        <span className="ms-auto">{saveStatus}</span>
        {m.ambiguous && (
          <span
            title={t("models.ambiguousHint", {
              qualified: `${m.provider_name}::${m.upstream_model_id}`,
            })}
          >
            <Badge variant="warning">
              <Info className="size-3" /> {t("models.ambiguous")}
            </Badge>
          </span>
        )}
      </div>

      <ModelStats stat={stat} loading={statsLoading} />

      <p className="mt-auto pt-3 text-xs text-muted-foreground">
        {t("models.lastSeen")}{" · "}
        {formatRelative(m.last_seen_at, {
          never: t("common.never"),
          justNow: t("common.justNow"),
        })}
      </p>
    </div>
  );
}

function Metric({
  label,
  value,
  title,
  className,
}: {
  label: string;
  value: React.ReactNode;
  title?: string;
  className?: string;
}) {
  return (
    <div className="min-w-0" title={title}>
      <dt className="truncate text-[11px] text-muted-foreground">{label}</dt>
      <dd className={cn("mt-0.5 truncate text-sm font-medium tabular-nums", className)}>{value}</dd>
    </div>
  );
}

// ModelStats shows how a model has actually behaved. Every number is a dash
// when it could not be measured, never a zero — a zero reads as a
// measurement, and "no data" is a different answer.
function ModelStats({ stat, loading }: { stat?: ModelStat; loading?: boolean }) {
  const t = useT();
  const grid = "mt-4 grid grid-cols-3 gap-x-3 gap-y-3 border-t pt-3";
  const dash = "text-muted-foreground";

  // Still waiting for the numbers: a dash here would say "no traffic", which
  // is a different answer, so the slots show placeholders instead.
  if (!stat && loading) {
    return (
      <dl className={grid} aria-busy="true">
        {Array.from({ length: 6 }, (_, i) => (
          <div key={i} className="space-y-2 pt-0.5">
            <SkeletonLine className="h-2.5 w-3/4" />
            <SkeletonLine className="h-3.5 w-1/2" />
          </div>
        ))}
      </dl>
    );
  }

  if (!stat) {
    return (
      <dl className={grid} title={t("models.noTraffic")}>
        <Metric label={t("models.requests24h")} value="—" className={dash} />
        <Metric label={t("models.successRate")} value="—" className={dash} />
        <Metric label={t("models.ttftP95")} value="—" className={dash} />
        <Metric label={t("models.tpsMedian")} value="—" className={dash} />
        <Metric label={t("models.tokens")} value="—" className={dash} />
        <Metric label={t("models.cacheHit")} value="—" className={dash} />
      </dl>
    );
  }

  // A rate below 100% raises "why", and the error class is the answer.
  const rate = Math.round(stat.success_rate * 100);
  const speedTitle =
    stat.streamed > 0 ? t("models.speedSample", { n: String(stat.streamed) }) : t("models.speedNone");

  // Readable units on the card, the exact millisecond on hover. The stored
  // value is whole milliseconds, and rendering it as "2.13 s" rounds to the
  // nearest 10 — fine for reading, not for comparing two close providers.
  const ttftTitle =
    stat.ttft_p95_ms !== null
      ? t("models.ttftExact", { ms: String(stat.ttft_p95_ms), sample: speedTitle })
      : speedTitle;

  // Reasoning is shown beside the totals rather than added to them: providers
  // disagree about whether it is part of the output count or separate, so any
  // sum would be wrong for some of them.
  const tokenTitle =
    stat.reasoning_tokens > 0
      ? t("models.tokensWithReasoning", {
          in: formatNumber(stat.input_tokens),
          out: formatNumber(stat.output_tokens),
          reasoning: formatNumber(stat.reasoning_tokens),
        })
      : t("models.tokensDetail", {
          in: formatNumber(stat.input_tokens),
          out: formatNumber(stat.output_tokens),
        });

  // The hit rate is a share of tokens, not of requests: one long cached prompt
  // and one short uncached one is not a 50% hit, and tokens are what the bill
  // is denominated in. 0% is a real answer — input was measured and none of it
  // was cached — while a dash means no input was reported at all.
  const cacheTitle =
    stat.cache_hit_rate !== null
      ? t("models.cacheHitDetail", {
          cached: formatNumber(stat.cached_input_tokens),
          total: formatNumber(stat.input_tokens),
        })
      : t("models.cacheHitNone");

  return (
    <dl className={grid}>
      <Metric label={t("models.requests24h")} value={formatNumber(stat.requests)} />
      <Metric
        label={t("models.successRate")}
        value={`${rate}%`}
        title={stat.top_error ? t("models.topError", { kind: stat.top_error }) : undefined}
        className={rate < 100 ? "text-destructive" : undefined}
      />
      <Metric
        label={t("models.ttftP95")}
        value={stat.ttft_p95_ms !== null ? formatDuration(stat.ttft_p95_ms) : "—"}
        title={ttftTitle}
        className={stat.ttft_p95_ms === null ? dash : undefined}
      />
      <Metric
        label={t("models.tpsMedian")}
        value={stat.tps_median !== null ? stat.tps_median.toFixed(0) : "—"}
        title={speedTitle}
        className={stat.tps_median === null ? dash : undefined}
      />
      <Metric
        label={t("models.tokens")}
        value={
          stat.input_tokens + stat.output_tokens > 0
            ? `${formatNumber(stat.input_tokens)} / ${formatNumber(stat.output_tokens)}`
            : "—"
        }
        title={tokenTitle}
        className={stat.input_tokens + stat.output_tokens > 0 ? undefined : dash}
      />
      <Metric
        label={t("models.cacheHit")}
        value={stat.cache_hit_rate !== null ? `${Math.round(stat.cache_hit_rate * 100)}%` : "—"}
        title={cacheTitle}
        className={stat.cache_hit_rate === null ? dash : undefined}
      />
    </dl>
  );
}
