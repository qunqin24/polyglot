import * as React from "react";
import { CircleDollarSign, Pencil, RefreshCw } from "lucide-react";
import {
  api,
  type CatalogStatus,
  type Price,
  type PriceTier,
  type PricedModel,
  type Provider,
} from "@/lib/api";
import { errorMessage, isAbort, useAsync, useCancellable, useSlowHint } from "@/lib/hooks";
import { useT, type TFunction } from "@/lib/i18n";
import { cn, formatNumber, formatPrice, formatRelative } from "@/lib/utils";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/kit/dialog";
import { LoadingButton } from "@/components/kit/loading-button";
import { CardGridSkeleton, LoadingGate, SkeletonLine } from "@/components/kit/skeletons";
import { Field, Input } from "@/components/kit/input";
import {
  Badge,
  EmptyState,
  ErrorBanner,
  Select,
} from "@/components/kit/misc";
import { ModelIcon } from "@/components/model-icon";
import { useToast } from "@/components/kit/toast";

// The Pricing page answers what every model costs and which ones nobody knows
// the price of. It is cost visibility, not billing: nothing here charges
// anyone, and every number is an estimate from a published price list.
//
// Prices come from models.dev — the vendor's own price, never a reseller's —
// unless the operator typed something, which always wins. Buying a model
// cheaper through a router is exactly what an override is for, so the
// "unpriced" filter is a main route through this page rather than an edge case.
export function Pricing() {
  const t = useT();
  const { toast } = useToast();

  const providers = useAsync(() => api.providers(), ["providers"]);
  const [providerID, setProviderID] = React.useState("");
  const [filter, setFilter] = React.useState("");
  const [searchInput, setSearchInput] = React.useState("");
  const [search, setSearch] = React.useState("");
  const [editing, setEditing] = React.useState<PricedModel | null>(null);
  const [refreshing, setRefreshing] = React.useState(false);
  const [refreshError, setRefreshError] = React.useState("");
  const refreshCall = useCancellable();
  const refreshSlow = useSlowHint(refreshing);

  React.useEffect(() => {
    const id = setTimeout(() => setSearch(searchInput.trim()), 300);
    return () => clearTimeout(id);
  }, [searchInput]);

  const { data, initialLoading, refreshing: reloading, error, reload } = useAsync(
    () => api.pricing({ provider_id: providerID, search, filter }),
    ["pricing", providerID, search, filter],
    { keepPrevious: true },
  );

  async function refreshCatalog() {
    setRefreshing(true);
    setRefreshError("");
    const signal = refreshCall.start();
    try {
      const res = await api.refreshCatalog(signal);
      // A failed refresh is information, not an error to fix: the catalog
      // already loaded stays in place and prices keep resolving. It is said
      // here, beside the catalog line, and stays until the next try.
      if (!res.ok) {
        setRefreshError(t("pricing.refreshFailed", { error: res.error ?? "" }));
      } else {
        toast(t("pricing.refreshed", { models: String(res.catalog?.models ?? 0) }));
        void reload();
      }
    } catch (e) {
      if (isAbort(e)) toast(t("pricing.refreshCancelled"));
      else setRefreshError(errorMessage(e));
    } finally {
      setRefreshing(false);
    }
  }

  const models = data?.models ?? [];
  const filtered = providerID !== "" || search !== "" || filter !== "";

  return (
    <>
      <PageHeader
        title={t("pricing.title")}
        description={t("pricing.description")}
        action={
          <LoadingButton
            variant="outline"
            busy={refreshing}
            icon={<RefreshCw />}
            iconIsAction
            onClick={() => void refreshCatalog()}
          >
            {t("pricing.refresh")}
          </LoadingButton>
        }
      />

      <CatalogLine
        status={data?.catalog ?? null}
        unpriced={data?.unpriced ?? 0}
        loading={initialLoading}
        refreshing={refreshing}
        slow={refreshSlow}
        onCancel={refreshCall.cancel}
        t={t}
      />
      <ErrorBanner message={refreshError} onRetry={() => void refreshCatalog()} retrying={refreshing} />

      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Input
          placeholder={t("pricing.search")}
          value={searchInput}
          onChange={(e) => setSearchInput(e.target.value)}
          className="w-full sm:w-64"
        />
        {/* The "all" choice is the empty value, which Radix reads as "nothing
            selected" — so the label it should wear lives in the placeholder,
            not only in the option list. */}
        <Select
          value={providerID}
          onValueChange={setProviderID}
          className="w-44"
          placeholder={t("models.allProviders")}
          options={[
            { value: "", label: t("models.allProviders") },
            ...(providers.data ?? []).map((p: Provider) => ({ value: String(p.id), label: p.name })),
          ]}
        />
        <Select
          value={filter}
          onValueChange={setFilter}
          className="w-40"
          placeholder={t("pricing.filterAll")}
          options={[
            { value: "", label: t("pricing.filterAll") },
            { value: "unpriced", label: t("pricing.filterUnpriced") },
            { value: "custom", label: t("pricing.filterCustom") },
          ]}
        />
        <div className="ml-auto flex items-center gap-2">
          <span className="text-xs text-muted-foreground">{t("pricing.unit")}</span>
          {data && (
            <span className="text-xs text-muted-foreground">
              {t("models.count", { shown: models.length, total: data.total })}
            </span>
          )}
        </div>
      </div>

      <ErrorBanner message={error} onRetry={() => void reload()} />

      <LoadingGate loading={initialLoading} skeleton={<CardGridSkeleton count={8} metrics={4} />}>
      {models.length === 0 ? (
        <Card>
          <CardContent className="p-0">
            <EmptyState
              icon={CircleDollarSign}
              title={filtered ? t("pricing.emptyFiltered") : t("pricing.empty")}
              description={filtered ? t("pricing.emptyFilteredHint") : t("pricing.emptyHint")}
            />
          </CardContent>
        </Card>
      ) : (
        <div
          aria-busy={reloading || undefined}
          className={cn(
            "grid gap-4 transition-opacity duration-200 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4",
            reloading && "opacity-60",
          )}
        >
          {models.map((m) => (
            <PriceCard key={m.id} model={m} onEdit={() => setEditing(m)} t={t} />
          ))}
        </div>
      )}
      </LoadingGate>

      {editing && (
        <PriceDialog
          model={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void reload();
          }}
        />
      )}

    </>
  );
}

// CatalogLine says which price snapshot is loaded and how many models it could
// not price. The second number is the one that matters day to day: a reseller's
// models rarely appear in a vendor catalog, so they wait here to be typed in.
function CatalogLine({
  status,
  unpriced,
  loading,
  refreshing,
  slow,
  onCancel,
  t,
}: {
  status: CatalogStatus | null;
  unpriced: number;
  loading: boolean;
  refreshing: boolean;
  slow: boolean;
  onCancel: () => void;
  t: TFunction;
}) {
  return (
    <div className="mb-4 flex min-h-5 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground" aria-live="polite">
      <span>
        {loading ? (
          <SkeletonLine className="inline-block w-56 align-middle" />
        ) : refreshing ? (
          slow
            ? t("pricing.refreshingSlow")
            : t("pricing.refreshing")
        ) : status
          ? t("pricing.catalogLine", {
              version: status.version,
              models: String(status.models),
              origin:
                status.source === "embedded"
                  ? t("pricing.catalogEmbedded")
                  : formatRelative(status.fetched_at, {
                      never: t("common.never"),
                      justNow: t("common.justNow"),
                    }),
            })
          : t("pricing.catalogNone")}
      </span>
      {refreshing && (
        <button type="button" onClick={onCancel} className="underline underline-offset-2 hover:text-foreground">
          {t("common.cancel")}
        </button>
      )}
      {unpriced > 0 && (
        <Badge variant="warning">{t("pricing.unpricedCount", { count: String(unpriced) })}</Badge>
      )}
    </div>
  );
}

// One card per model. The four numbers show what a normal prompt costs. A
// model whose vendor charges more for a long context says so in a line under
// them rather than in extra cells that would be blank on 90% of the cards.
function PriceCard({
  model: m,
  onEdit,
  t,
}: {
  model: PricedModel;
  onEdit: () => void;
  t: TFunction;
}) {
  return (
    <div className="flex flex-col rounded-xl border bg-card p-4 text-card-foreground shadow-xs transition-colors hover:border-foreground/20">
      <div className="flex items-start gap-3">
        <ModelIcon id={m.upstream_model_id} maker={m.maker} />
        <div className="min-w-0 flex-1">
          <p className="font-mono text-sm leading-5 font-medium break-all">{m.upstream_model_id}</p>
          <p className="mt-0.5 truncate text-xs text-muted-foreground" title={m.provider_name}>
            {m.provider_name}
          </p>
        </div>
        <Button variant="ghost" size="icon-sm" onClick={onEdit} className="-mt-1 -mr-1">
          <Pencil />
          <span className="sr-only">{t("common.edit")}</span>
        </Button>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-1.5">
        <SourceBadge source={m.source} t={t} />
        <TierBadge tier={m.effective.tier} t={t} />
      </div>

      <dl className="mt-4 grid grid-cols-2 gap-x-3 gap-y-3 border-t pt-3">
        <Metric label={t("pricing.input")} value={m.effective.input} />
        <Metric label={t("pricing.output")} value={m.effective.output} />
        <Metric label={t("pricing.cacheRead")} value={m.effective.cache_read} />
        <Metric label={t("pricing.cacheWrite")} value={m.effective.cache_write} />
      </dl>

      {m.effective.tier && <TierLine tier={m.effective.tier} t={t} />}
    </div>
  );
}

function TierBadge({ tier, t }: { tier?: PriceTier; t: TFunction }) {
  if (!tier) return null;
  return (
    <span title={tierHint(tier, t)}>
      <Badge variant="accent">{t("pricing.tier")}</Badge>
    </span>
  );
}

function tierHint(tier: PriceTier, t: TFunction): string {
  const show = (v: number | null) => (v === null ? "—" : formatPrice(v));
  return t("pricing.tierHint", {
    tokens: formatNumber(tier.above_tokens),
    input: show(tier.input),
    output: show(tier.output),
  });
}

// The long-context tier, spelled out on the card as well as in the badge's
// tooltip, so the extra charge is visible without hovering.
function TierLine({ tier, t }: { tier: PriceTier; t: TFunction }) {
  return (
    <p className="mt-auto pt-3 text-xs text-muted-foreground">{tierHint(tier, t)}</p>
  );
}

function Metric({ label, value }: { label: string; value: number | null }) {
  return (
    <div className="min-w-0">
      <dt className="truncate text-[11px] text-muted-foreground">{label}</dt>
      {/* A dash, never a zero. A zero is a claim that the model is free, and a
          model nobody has a price for is not a free one. */}
      <dd
        className={cn(
          "mt-0.5 truncate text-sm font-medium tabular-nums",
          value === null && "text-muted-foreground",
        )}
      >
        {value === null ? "—" : formatPrice(value)}
      </dd>
    </div>
  );
}

function SourceBadge({ source, t }: { source: PricedModel["source"]; t: TFunction }) {
  if (source === "custom") return <Badge variant="accent">{t("pricing.sourceCustom")}</Badge>;
  if (source === "models.dev") return <Badge>{t("pricing.sourceCatalog")}</Badge>;
  return <span className="text-xs text-muted-foreground">{t("pricing.sourceUnknown")}</span>;
}

// PriceDialog edits the four numbers.
//
// It is prefilled with the operator's own override and not with the catalog
// price, because saving a copy of today's catalog would freeze the model at
// that number forever. A blank field means "follow models.dev", which is the
// state a model starts in and the state "clear" returns it to.
function PriceDialog({
  model,
  onClose,
  onSaved,
}: {
  model: PricedModel;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT();
  const { toast } = useToast();
  const [form, setForm] = React.useState({
    input: numberField(model.price.input),
    output: numberField(model.price.output),
    cache_read: numberField(model.price.cache_read),
    cache_write: numberField(model.price.cache_write),
  });
  const [saving, setSaving] = React.useState<"save" | "clear" | null>(null);
  const [invalid, setInvalid] = React.useState(false);

  async function save(price: Price, action: "save" | "clear") {
    setSaving(action);
    try {
      await api.setModelPrice(model.id, price);
      toast(t("pricing.saved", { model: model.upstream_model_id }));
      onSaved();
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setSaving(null);
    }
  }

  function submit() {
    const price = parsePrice(form);
    if (!price) {
      setInvalid(true);
      return;
    }
    void save(price, "save");
  }

  const fields: { key: keyof typeof form; label: string }[] = [
    { key: "input", label: t("pricing.input") },
    { key: "output", label: t("pricing.output") },
    { key: "cache_read", label: t("pricing.cacheRead") },
    { key: "cache_write", label: t("pricing.cacheWrite") },
  ];

  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("pricing.editTitle", { model: model.upstream_model_id })}
      description={
        model.effective.tier ? t("pricing.editHintTiered") : t("pricing.editHint")
      }
      footer={
        <>
          <LoadingButton
            variant="ghost"
            busy={saving === "clear"}
            disabled={saving === "save"}
            onClick={() =>
              void save({ input: null, output: null, cache_read: null, cache_write: null }, "clear")
            }
          >
            {t("pricing.clear")}
          </LoadingButton>
          <Button variant="outline" onClick={onClose} disabled={saving !== null}>
            {t("common.cancel")}
          </Button>
          <LoadingButton busy={saving === "save"} disabled={saving === "clear"} onClick={submit}>
            {t("common.save")}
          </LoadingButton>
        </>
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        {fields.map((f) => (
          <Field
            key={f.key}
            label={f.label}
            hint={catalogHint(model, f.key, t)}
            error={invalid ? t("pricing.invalid") : undefined}
          >
            <Input
              inputMode="decimal"
              value={form[f.key]}
              placeholder={t("pricing.followPlaceholder")}
              onChange={(e) => {
                setInvalid(false);
                setForm({ ...form, [f.key]: e.target.value });
              }}
            />
          </Field>
        ))}
      </div>
    </Dialog>
  );
}

// The catalog number under each box, so an operator can see what they are
// departing from before they type over it.
function catalogHint(model: PricedModel, key: keyof Price, t: TFunction): string {
  const own = model.price[key];
  const effective = model.effective[key];
  if (own !== null && effective !== null) return t("pricing.hintOverride");
  if (effective === null) return t("pricing.hintNoCatalog");
  return t("pricing.hintCatalog", { price: formatPrice(effective) });
}

function numberField(v: number | null): string {
  return v === null ? "" : String(v);
}

// A blank field is null — follow the catalog — and every other value has to be
// a number. Returns null when anything typed is not one, so a slip cannot be
// stored as a price.
function parsePrice(form: Record<keyof Price, string>): Price | null {
  const out: Price = { input: null, output: null, cache_read: null, cache_write: null };
  for (const key of ["input", "output", "cache_read", "cache_write"] as (keyof Price)[]) {
    const raw = form[key].trim();
    if (raw === "") continue;
    const n = Number(raw);
    if (!Number.isFinite(n) || n < 0) return null;
    out[key] = n;
  }
  return out;
}
