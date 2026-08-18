import * as React from "react";
import { Boxes, Info } from "lucide-react";
import { api, type ModelStat, type Model, type Provider } from "@/lib/api";
import { errorMessage, useAsync } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { formatDuration, formatNumber, formatRelative } from "@/lib/utils";
import { PageHeader } from "@/components/layout";
import { Aliases } from "@/pages/aliases";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Badge,
  EmptyState,
  ErrorBanner,
  Select,
  Spinner,
  Switch,
  Table,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
  Td,
  Th,
  Tr,
} from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";

// The Models page shows the real models each provider offers. Aliases are a
// second tab, because they are an optional convenience rather than a step
// anyone has to take.
export function Models() {
  const t = useT();
  const providers = useAsync(() => api.providers(), []);

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
  const { toast } = useToast();

  const [providerID, setProviderID] = React.useState("");
  const [searchInput, setSearchInput] = React.useState("");
  const [search, setSearch] = React.useState("");

  React.useEffect(() => {
    const id = setTimeout(() => setSearch(searchInput.trim()), 300);
    return () => clearTimeout(id);
  }, [searchInput]);

  const { data, loading, error, reload } = useAsync(
    () => api.models({ provider_id: providerID, search, limit: 500 }),
    [providerID, search],
  );
  // Loaded separately from the list: this is an aggregate over the request
  // log, and it must never be what decides whether the page appears.
  const stats = useAsync(() => api.modelStats(24), []);

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


  async function toggle(m: Model, enabled: boolean) {
    try {
      await api.updateModel(m.id, m.display_name, enabled);
      reload();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  }

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

      <ErrorBanner message={error} />

      <Card>
        <CardContent className="p-0">
          {loading && !data ? (
            <div className="flex justify-center py-12">
              <Spinner className="text-muted-foreground" />
            </div>
          ) : models.length === 0 ? (
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
          ) : (
            <Table>
              <thead>
                <Tr>
                  <Th className="pl-5">{t("models.modelID")}</Th>
                  <Th>{t("common.provider")}</Th>
                  <Th className="text-right">{t("models.requests24h")}</Th>
                  <Th className="text-right">{t("models.successRate")}</Th>
                  <Th className="hidden text-right sm:table-cell">{t("models.ttftP95")}</Th>
                  <Th className="hidden text-right sm:table-cell">{t("models.tpsMedian")}</Th>
                  <Th className="hidden text-right md:table-cell">{t("models.tokens")}</Th>
                  <Th className="hidden text-right lg:table-cell">{t("models.cacheHit")}</Th>
                  <Th className="hidden lg:table-cell">{t("models.lastSeen")}</Th>
                  <Th className="pr-5">{t("common.enabled")}</Th>
                </Tr>
              </thead>
              <tbody>
                {models.map((m) => (
                  <Tr key={m.id}>
                    <Td className="pl-5">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-mono text-xs font-medium">{m.upstream_model_id}</span>
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
                      {m.display_name && m.display_name !== m.upstream_model_id && (
                        <p className="mt-0.5 text-xs text-muted-foreground">{m.display_name}</p>
                      )}
                    </Td>
                    <Td>
                      <div className="flex items-center gap-1.5">
                        <span className="text-sm">{m.provider_name}</span>
                        <Badge variant="accent">{m.protocol}</Badge>
                      </div>
                    </Td>
                    <ModelStatCells stat={statOf(m)} />
                    <Td className="hidden text-xs text-muted-foreground lg:table-cell">
                      {formatRelative(m.last_seen_at, {
                        never: t("common.never"),
                        justNow: t("common.justNow"),
                      })}
                    </Td>
                    <Td className="pr-5">
                      <Switch checked={m.enabled} onCheckedChange={(v) => void toggle(m, v)} />
                    </Td>
                  </Tr>
                ))}
              </tbody>
            </Table>
          )}
        </CardContent>
      </Card>



    </>
  );
}

// ModelStatCells renders the four numbers that say how a model has actually
// behaved. Every one of them is a dash when it could not be measured, never a
// zero — a zero reads as a measurement, and "no data" is a different answer.
function ModelStatCells({ stat }: { stat?: ModelStat }) {
  const t = useT();
  if (!stat) {
    return (
      <>
        <Td className="text-right text-xs text-muted-foreground" title={t("models.noTraffic")}>
          —
        </Td>
        <Td className="text-right text-muted-foreground">—</Td>
        <Td className="hidden text-right text-muted-foreground sm:table-cell">—</Td>
        <Td className="hidden text-right text-muted-foreground sm:table-cell">—</Td>
        <Td className="hidden text-right text-muted-foreground md:table-cell">—</Td>
        <Td className="hidden text-right text-muted-foreground lg:table-cell">—</Td>
      </>
    );
  }

  // A rate below 100% raises "why", and the error class is the answer.
  const rate = Math.round(stat.success_rate * 100);
  const speedTitle =
    stat.streamed > 0 ? t("models.speedSample", { n: String(stat.streamed) }) : t("models.speedNone");

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
  // is denominated in.
  const cacheTitle =
    stat.cache_hit_rate !== null
      ? t("models.cacheHitDetail", {
          cached: formatNumber(stat.cached_input_tokens),
          total: formatNumber(stat.input_tokens),
        })
      : t("models.cacheHitNone");

  return (
    <>
      <Td className="text-right tabular-nums text-muted-foreground">{stat.requests}</Td>
      <Td
        className="text-right tabular-nums"
        title={stat.top_error ? t("models.topError", { kind: stat.top_error }) : undefined}
      >
        <span className={rate < 100 ? "text-destructive" : "text-muted-foreground"}>{rate}%</span>
      </Td>
      {/* Readable units in the column, the exact millisecond on hover. The
          stored value is whole milliseconds, and rendering it as "2.13 s"
          rounds to the nearest 10 — fine for reading, not for comparing two
          close providers, so the precise number stays one hover away. */}
      <Td
        className="hidden text-right tabular-nums text-muted-foreground sm:table-cell"
        title={ttftTitle}
      >
        {stat.ttft_p95_ms !== null ? formatDuration(stat.ttft_p95_ms) : "—"}
      </Td>
      <Td
        className="hidden text-right tabular-nums text-muted-foreground sm:table-cell"
        title={speedTitle}
      >
        {stat.tps_median !== null ? stat.tps_median.toFixed(0) : "—"}
      </Td>
      <Td
        className="hidden text-right tabular-nums text-muted-foreground md:table-cell"
        title={tokenTitle}
      >
        {stat.input_tokens + stat.output_tokens > 0
          ? `${formatNumber(stat.input_tokens)} / ${formatNumber(stat.output_tokens)}`
          : "—"}
      </Td>
      {/* 0% is a real answer here — input was measured and none of it was
          cached. A dash means no input was reported at all, which is not the
          same thing and must not read as a cache that never hit. */}
      <Td
        className="hidden text-right tabular-nums text-muted-foreground lg:table-cell"
        title={cacheTitle}
      >
        {stat.cache_hit_rate !== null ? `${Math.round(stat.cache_hit_rate * 100)}%` : "—"}
      </Td>
    </>
  );
}
