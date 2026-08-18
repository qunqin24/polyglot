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
import { errorMessage, useAsync } from "@/lib/hooks";
import { useT, type TFunction } from "@/lib/i18n";
import { formatNumber, formatPrice, formatRelative } from "@/lib/utils";
import { PageHeader } from "@/components/layout";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/input";
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
import { useToast } from "@/components/ui/toast";

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

  const providers = useAsync(() => api.providers(), []);
  const [providerID, setProviderID] = React.useState("");
  const [filter, setFilter] = React.useState("");
  const [searchInput, setSearchInput] = React.useState("");
  const [search, setSearch] = React.useState("");
  const [editing, setEditing] = React.useState<PricedModel | null>(null);
  const [refreshing, setRefreshing] = React.useState(false);

  React.useEffect(() => {
    const id = setTimeout(() => setSearch(searchInput.trim()), 300);
    return () => clearTimeout(id);
  }, [searchInput]);

  const { data, loading, error, reload } = useAsync(
    () => api.pricing({ provider_id: providerID, search, filter }),
    [providerID, search, filter],
  );

  async function refreshCatalog() {
    setRefreshing(true);
    try {
      const res = await api.refreshCatalog();
      // A failed refresh is information, not an error to fix: the catalog
      // already loaded stays in place and prices keep resolving.
      if (!res.ok) {
        toast(t("pricing.refreshFailed", { error: res.error ?? "" }), "error");
      } else {
        toast(t("pricing.refreshed", { models: String(res.catalog?.models ?? 0) }));
        reload();
      }
    } catch (e) {
      toast(errorMessage(e), "error");
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
          <Button variant="outline" onClick={() => void refreshCatalog()} disabled={refreshing}>
            {refreshing ? <Spinner className="size-4" /> : <RefreshCw />}
            {t("pricing.refresh")}
          </Button>
        }
      />

      <CatalogLine status={data?.catalog ?? null} unpriced={data?.unpriced ?? 0} t={t} />

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

      <ErrorBanner message={error} />

      <Card>
        <CardContent className="p-0">
          {loading && !data ? (
            <div className="flex justify-center py-12">
              <Spinner className="text-muted-foreground" />
            </div>
          ) : models.length === 0 ? (
            <EmptyState
              icon={CircleDollarSign}
              title={filtered ? t("pricing.emptyFiltered") : t("pricing.empty")}
              description={filtered ? t("pricing.emptyFilteredHint") : t("pricing.emptyHint")}
            />
          ) : (
            <Table>
              <thead>
                <Tr>
                  <Th className="pl-5">{t("models.modelID")}</Th>
                  <Th>{t("common.provider")}</Th>
                  <Th className="text-right">{t("pricing.input")}</Th>
                  <Th className="text-right">{t("pricing.output")}</Th>
                  <Th className="hidden text-right sm:table-cell">{t("pricing.cacheRead")}</Th>
                  <Th className="hidden text-right sm:table-cell">{t("pricing.cacheWrite")}</Th>
                  <Th>{t("pricing.source")}</Th>
                  <Th className="pr-5" />
                </Tr>
              </thead>
              <tbody>
                {models.map((m) => (
                  <Tr key={m.id}>
                    <Td className="pl-5">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-mono text-xs font-medium">{m.upstream_model_id}</span>
                        <TierBadge tier={m.effective.tier} t={t} />
                      </div>
                    </Td>
                    <Td className="text-sm">{m.provider_name}</Td>
                    <Cell value={m.effective.input} />
                    <Cell value={m.effective.output} />
                    <Cell value={m.effective.cache_read} className="hidden sm:table-cell" />
                    <Cell value={m.effective.cache_write} className="hidden sm:table-cell" />
                    <Td>
                      <SourceBadge source={m.source} t={t} />
                    </Td>
                    <Td className="pr-5 text-right">
                      <Button variant="ghost" size="icon-sm" onClick={() => setEditing(m)}>
                        <Pencil />
                        <span className="sr-only">{t("common.edit")}</span>
                      </Button>
                    </Td>
                  </Tr>
                ))}
              </tbody>
            </Table>
          )}
        </CardContent>
      </Card>

      {editing && (
        <PriceDialog
          model={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reload();
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
  t,
}: {
  status: CatalogStatus | null;
  unpriced: number;
  t: TFunction;
}) {
  return (
    <div className="mb-4 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
      <span>
        {status
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
      {unpriced > 0 && (
        <Badge variant="warning">{t("pricing.unpricedCount", { count: String(unpriced) })}</Badge>
      )}
    </div>
  );
}

// The columns show what a normal prompt costs. A model whose vendor charges
// more for a long context says so here rather than in a sixth and seventh
// column that would be blank on 90% of the rows.
function TierBadge({ tier, t }: { tier?: PriceTier; t: TFunction }) {
  if (!tier) return null;
  const show = (v: number | null) => (v === null ? "—" : formatPrice(v));
  return (
    <span
      title={t("pricing.tierHint", {
        tokens: formatNumber(tier.above_tokens),
        input: show(tier.input),
        output: show(tier.output),
      })}
    >
      <Badge variant="accent">{t("pricing.tier")}</Badge>
    </span>
  );
}

// A dash, never a zero. A zero is a claim that the model is free, and a model
// nobody has a price for is not a free one.
function Cell({ value, className }: { value: number | null; className?: string }) {
  return (
    <Td className={`text-right tabular-nums ${className ?? ""}`}>
      {value === null ? (
        <span className="text-muted-foreground">—</span>
      ) : (
        formatPrice(value)
      )}
    </Td>
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
  const [saving, setSaving] = React.useState(false);
  const [invalid, setInvalid] = React.useState(false);

  async function save(price: Price) {
    setSaving(true);
    try {
      await api.setModelPrice(model.id, price);
      toast(t("pricing.saved", { model: model.upstream_model_id }));
      onSaved();
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setSaving(false);
    }
  }

  function submit() {
    const price = parsePrice(form);
    if (!price) {
      setInvalid(true);
      return;
    }
    void save(price);
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
          <Button
            variant="ghost"
            disabled={saving}
            onClick={() =>
              void save({ input: null, output: null, cache_read: null, cache_write: null })
            }
          >
            {t("pricing.clear")}
          </Button>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} disabled={saving}>
            {t("common.save")}
          </Button>
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
