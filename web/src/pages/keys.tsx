import * as React from "react";
import { Copy, KeyRound, Plus, Trash2, Check, ChevronRight, MapPin, Pencil, RotateCcw, SlidersHorizontal } from "lucide-react";
import { api, type APIKey, type APIKeyPolicyInput, type BudgetPeriod, type KeyOrigin, type OfferedModel } from "@/lib/api";
import { copyToClipboard, errorMessage, useAsync } from "@/lib/hooks";
import { useT, type TFunction } from "@/lib/i18n";
import { cn, formatRelative, formatTime, formatUSD } from "@/lib/utils";
import { PageHeader } from "@/components/layout";
import { ModelPicker } from "@/components/model-picker";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ConfirmDialog, Dialog, Sheet } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/input";
import {
  Badge,
  EmptyState,
  ErrorBanner,
  Select,
  Spinner,
  Switch,
  Table,
  Tabs,
  TabsList,
  TabsTrigger,
  Td,
  Th,
  Tr,
} from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";

type SortKey = "recent" | "name" | "lastUsed";

export function Keys() {
  const t = useT();
  const { data, loading, error, reload } = useAsync(() => api.keys(), []);
  const { toast } = useToast();

  const [search, setSearch] = React.useState("");
  const [statusFilter, setStatusFilter] = React.useState("");
  const [sort, setSort] = React.useState<SortKey>("recent");

  // Filtering happens here rather than on the server: every key arrives in one
  // response, so a round trip per keystroke would buy nothing.
  const visible = React.useMemo(() => {
    const q = search.trim().toLowerCase();
    const now = Date.now();
    // "Enabled" and "disabled" read the switch, nothing more. Expiry is a
    // separate state — an expired key still has its switch on — so it is its
    // own option and deliberately overlaps the other two.
    const rows = (data ?? []).filter((k) => {
      if (q && !k.name.toLowerCase().includes(q) && !k.prefix.toLowerCase().includes(q)) {
        return false;
      }
      if (statusFilter === "enabled" && !k.enabled) return false;
      if (statusFilter === "disabled" && k.enabled) return false;
      if (statusFilter === "expired") {
        if (!k.expires_at || new Date(k.expires_at).getTime() > now) return false;
      }
      return true;
    });

    // Newest first is the default because that is the order the server returns
    // and the one a freshly created key shows up in.
    return rows.sort((a, b) => {
      switch (sort) {
        case "name":
          return a.name.localeCompare(b.name);
        case "lastUsed":
          // A key never used sorts last rather than as the oldest use.
          return (
            (b.last_used_at ? Date.parse(b.last_used_at) : -Infinity) -
              (a.last_used_at ? Date.parse(a.last_used_at) : -Infinity) ||
            a.name.localeCompare(b.name)
          );
        default:
          return Date.parse(b.created_at) - Date.parse(a.created_at);
      }
    });
  }, [data, search, statusFilter, sort]);

  const filtered = search !== "" || statusFilter !== "";

  const [creating, setCreating] = React.useState(false);
  const [editing, setEditing] = React.useState<APIKey | null>(null);
  const [deleting, setDeleting] = React.useState<APIKey | null>(null);
  const [origins, setOrigins] = React.useState<APIKey | null>(null);

  async function remove() {
    if (!deleting) return;
    try {
      await api.deleteKey(deleting.id);
      toast(t("keys.deleted", { name: deleting.name }));
      reload();
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setDeleting(null);
    }
  }

  async function toggle(k: APIKey, enabled: boolean) {
    try {
      await api.setKeyEnabled(k.id, enabled);
      reload();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  }

  return (
    <>
      <PageHeader
        title={t("keys.title")}
        description={t("keys.description")}
        action={
          <Button onClick={() => setCreating(true)}>
            <Plus /> {t("keys.create")}
          </Button>
        }
      />

      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Input
          placeholder={t("keys.search")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select
          value={statusFilter}
          onValueChange={setStatusFilter}
          className="w-36"
          placeholder={t("common.anyStatus")}
          options={[
            { value: "", label: t("common.anyStatus") },
            { value: "enabled", label: t("common.enabled") },
            { value: "disabled", label: t("common.disabled") },
            { value: "expired", label: t("keys.expired") },
          ]}
        />
        <Select
          value={sort}
          onValueChange={(v) => setSort(v as SortKey)}
          className="w-44"
          options={[
            { value: "recent", label: t("keys.sortRecent") },
            { value: "name", label: t("keys.sortName") },
            { value: "lastUsed", label: t("keys.sortLastUsed") },
          ]}
        />
        {data && (
          <span className="ml-auto text-xs text-muted-foreground">
            {t("keys.count", { shown: visible.length, total: data.length })}
          </span>
        )}
      </div>

      <ErrorBanner message={error} />

      <Card>
        <CardContent className="p-0">
          {loading && !data ? (
            <div className="flex justify-center py-12">
              <Spinner className="text-muted-foreground" />
            </div>
          ) : visible.length === 0 ? (
            <EmptyState
              icon={KeyRound}
              title={filtered ? t("keys.emptyFiltered") : t("keys.empty")}
              description={filtered ? t("keys.emptyFilteredHint") : t("keys.emptyHint")}
              action={
                filtered ? undefined : (
                  <Button onClick={() => setCreating(true)}>
                    <Plus /> {t("keys.create")}
                  </Button>
                )
              }
            />
          ) : (
            <Table>
              <thead>
                <Tr>
                  <Th className="pl-5">{t("common.name")}</Th>
                  <Th>{t("keys.key")}</Th>
                  <Th>{t("keys.limits")}</Th>
                  <Th>{t("keys.budgetColumn")}</Th>
                  <Th>{t("keys.expiration")}</Th>
                  <Th>{t("keys.lastUsed")}</Th>
                  <Th>{t("common.enabled")}</Th>
                  <Th className="pr-5 text-right">{t("common.actions")}</Th>
                </Tr>
              </thead>
              <tbody>
                {visible.map((k) => (
                  <Tr key={k.id}>
                    <Td className="pl-5 font-medium">{k.name}</Td>
                    <Td className="font-mono text-xs text-muted-foreground">{k.prefix}…</Td>
                    <Td><LimitSummary apiKey={k} /></Td>
                    <Td><BudgetCell apiKey={k} /></Td>
                    <Td><Expiration apiKey={k} /></Td>
                    <Td className="text-muted-foreground">{formatRelative(k.last_used_at, { never: t("common.never"), justNow: t("common.justNow") })}</Td>
                    <Td>
                      <Switch checked={k.enabled} onCheckedChange={(v) => void toggle(k, v)} />
                    </Td>
                    <Td className="pr-5">
                      <div className="flex justify-end">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          title={t("common.edit")}
                          onClick={() => setEditing(k)}
                        >
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          title={t("keys.origins")}
                          onClick={() => setOrigins(k)}
                        >
                          <MapPin />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          title={t("common.delete")}
                          onClick={() => setDeleting(k)}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                    </Td>
                  </Tr>
                ))}
              </tbody>
            </Table>
          )}
        </CardContent>
      </Card>

      <KeyDialog
        open={creating || editing !== null}
        apiKey={editing}
        onOpenChange={(open) => {
          if (!open) {
            setCreating(false);
            setEditing(null);
          }
        }}
        onSaved={reload}
      />

      <OriginsDialog apiKey={origins} onClose={() => setOrigins(null)} />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("keys.deleteTitle", { name: deleting?.name ?? "" })}
        description={t("keys.deleteDescription")}
        onConfirm={() => void remove()}
      />
    </>
  );
}

// ---------------------------------------------------------------------------
// Creating a key is two steps: say what the key is for, then take the secret
// away. The middle step a wizard would normally have does not exist, because a
// purpose already filled the settings in — and a step most people skip has no
// business being in a step indicator.
//
// Editing an existing key is the same fields with neither: a wizard is the
// wrong shape for a change to something that is already there.
// ---------------------------------------------------------------------------

type ExpiryChoice = "never" | "7" | "30" | "90" | "custom";
type RateChoice = "unlimited" | "relaxed" | "strict" | "custom";

type PolicyForm = {
  rpm: string;
  rph: string;
  rpd: string;
  tpm: string;
  tpd: string;
  maxConcurrent: string;
  maxOutputTokens: string;
  expiresAt: string;
  expiryChoice: ExpiryChoice;
  rateChoice: RateChoice;
  /** A spending cap in dollars, as typed. Empty is no cap. */
  budget: string;
  budgetPeriod: BudgetPeriod;
  /** The server reads an empty allow-list as "every model", so this flag is
      what stops "restricted, nothing picked yet" from quietly meaning the
      opposite of what the operator just clicked. */
  restrictModels: boolean;
  allowedModels: string[];
};

const emptyPolicy: PolicyForm = {
  rpm: "", rph: "", rpd: "", tpm: "", tpd: "",
  maxConcurrent: "", maxOutputTokens: "", expiresAt: "",
  expiryChoice: "never", rateChoice: "unlimited",
  budget: "", budgetPeriod: "total",
  restrictModels: false, allowedModels: [],
};

/** The two one-number rate choices. A chip means this value and nothing else. */
const RELAXED_RPM = "600";
const STRICT_RPM = "60";

const EXPIRY_DAYS: Record<Exclude<ExpiryChoice, "never" | "custom">, number> = {
  "7": 7, "30": 30, "90": 90,
};

type PresetId = "dev" | "production" | "share" | "script";

type Preset = {
  id: PresetId;
  /** No "custom" here: a purpose states a window, it does not defer one. */
  expiry: Exclude<ExpiryChoice, "custom">;
  restrictModels: boolean;
  rate: RateChoice;
  maxConcurrent?: string;
};

/**
 * A purpose fills the form in. It is not stored, not sent, and not a plan —
 * every value it writes is on screen before the key is created and can be
 * changed first, which is the only reason it is allowed to choose at all.
 *
 * Change a number here and the cards say so by themselves: their summary line
 * is built from the form the preset produces, never from a translated string
 * repeating it.
 */
const PRESETS: Preset[] = [
  { id: "dev", expiry: "never", restrictModels: false, rate: "unlimited" },
  { id: "production", expiry: "never", restrictModels: true, rate: "relaxed" },
  { id: "share", expiry: "7", restrictModels: true, rate: "strict" },
  { id: "script", expiry: "30", restrictModels: false, rate: "custom", maxConcurrent: "4" },
];

function presetLabel(id: PresetId, t: TFunction): string {
  switch (id) {
    case "dev": return t("keys.presetDev");
    case "production": return t("keys.presetProduction");
    case "share": return t("keys.presetShare");
    case "script": return t("keys.presetScript");
  }
}

function applyPreset(p: Preset): PolicyForm {
  const form: PolicyForm = {
    ...emptyPolicy,
    restrictModels: p.restrictModels,
    expiryChoice: p.expiry,
    expiresAt: expiryFromChoice(p.expiry, ""),
    rateChoice: p.rate,
    maxConcurrent: p.maxConcurrent ?? "",
  };
  return { ...form, ...rateValues(p.rate, form) };
}

/** The chosen window as a local datetime-local value. Custom keeps what is
 *  already typed; never has no date at all. */
function expiryFromChoice(choice: ExpiryChoice, current: string): string {
  if (choice === "never") return "";
  // Switching to a custom time from "never" leaves nothing to edit, and an
  // empty field would quietly mean never again — so it starts a month out.
  if (choice === "custom") return current || expiryFromChoice("30", "");
  const at = new Date(Date.now() + EXPIRY_DAYS[choice] * 86_400_000);
  return localDateTime(at.toISOString());
}

/** The numbers a rate choice stands for. Custom leaves the fields alone —
 *  that is what makes it custom. */
function rateValues(choice: RateChoice, current: PolicyForm) {
  const cleared = { rpm: "", rph: "", rpd: "", tpm: "", tpd: "", maxConcurrent: "", maxOutputTokens: "" };
  switch (choice) {
    case "unlimited": return cleared;
    case "relaxed": return { ...cleared, rpm: RELAXED_RPM };
    case "strict": return { ...cleared, rpm: STRICT_RPM };
    case "custom": return {
      rpm: current.rpm, rph: current.rph, rpd: current.rpd, tpm: current.tpm, tpd: current.tpd,
      maxConcurrent: current.maxConcurrent, maxOutputTokens: current.maxOutputTokens,
    };
  }
}

/** Which chip an existing key's numbers correspond to. A chip means one exact
 *  value, so a hand-typed 600 RPM is the relaxed chip — same key either way. */
function rateChoiceOf(form: PolicyForm): RateChoice {
  const others = [form.rph, form.rpd, form.tpm, form.tpd, form.maxConcurrent, form.maxOutputTokens];
  if (others.some((v) => v.trim() !== "")) return "custom";
  if (form.rpm.trim() === "") return "unlimited";
  if (form.rpm === RELAXED_RPM) return "relaxed";
  if (form.rpm === STRICT_RPM) return "strict";
  return "custom";
}

function samePolicy(a: PolicyForm, b: PolicyForm): boolean {
  const flat = (f: PolicyForm) => JSON.stringify({ ...f, allowedModels: [...f.allowedModels].sort() });
  return flat(a) === flat(b);
}

/** The typed cap as a number, or null when there is none. */
function budgetValue(policy: PolicyForm): number | null {
  const usd = Number.parseFloat(policy.budget);
  return Number.isFinite(usd) && usd > 0 ? usd : null;
}

function budgetText(policy: PolicyForm, t: TFunction): string {
  const usd = budgetValue(policy);
  if (usd === null) return t("keys.noBudget");
  return `${formatUSD(usd)} · ${budgetPeriodLabel(policy.budgetPeriod, t)}`;
}

function budgetPeriodLabel(period: BudgetPeriod, t: TFunction): string {
  switch (period) {
    case "daily": return t("keys.budgetDaily");
    case "weekly": return t("keys.budgetWeekly");
    case "monthly": return t("keys.budgetMonthly");
    default: return t("keys.budgetTotal");
  }
}

function expiryText(policy: PolicyForm, t: TFunction): string {
  return policy.expiresAt ? formatTime(policy.expiresAt) : t("keys.neverExpires");
}

function modelsText(policy: PolicyForm, t: TFunction): string {
  return policy.restrictModels
    ? t("keys.modelCount", { count: String(policy.allowedModels.length) })
    : t("keys.modelsAll");
}

function KeyDialog({
  open,
  apiKey,
  onOpenChange,
  onSaved,
}: {
  open: boolean;
  apiKey: APIKey | null;
  onOpenChange: (o: boolean) => void;
  onSaved: () => void;
}) {
  const t = useT();
  const editing = apiKey !== null;
  const [name, setName] = React.useState("");
  const [nameTouched, setNameTouched] = React.useState(false);
  const [policy, setPolicy] = React.useState<PolicyForm>(emptyPolicy);
  const [preset, setPreset] = React.useState<PresetId | null>(null);
  // What the preset wrote, kept so the card can say when it no longer holds.
  const [presetBase, setPresetBase] = React.useState<PolicyForm | null>(null);
  const [adjusting, setAdjusting] = React.useState(false);
  const [issued, setIssued] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");
  // What the key has spent this window. Local, because resetting it changes
  // the number without saving the form.
  const [spent, setSpent] = React.useState(0);
  const { toast } = useToast();
  const availableModels = useAsync(async () => {
    if (!open) return [] as OfferedModel[];
    const [models, aliases] = await Promise.all([
      api.models({ limit: 2000, enabled_only: 1 }),
      api.aliases(),
    ]);
    const choices = new Map<string, OfferedModel>();
    for (const model of models.models) {
      if (!choices.has(model.upstream_model_id)) {
        choices.set(model.upstream_model_id, {
          id: model.upstream_model_id,
          display_name: `${t("keys.registeredModel")} · ${model.provider_name}`,
          registered: false,
        });
      }
    }
    for (const alias of aliases) {
      if (alias.enabled) {
        choices.set(alias.alias, {
          id: alias.alias,
          display_name: `${t("keys.modelAlias")} · ${alias.provider_name}`,
          registered: false,
        });
      }
    }
    return [...choices.values()].sort((a, b) => a.id.localeCompare(b.id));
  }, [open, t]);

  React.useEffect(() => {
    if (!open) return;
    // Creating starts on the first purpose rather than on a blank form: it is
    // the common one, and it makes "create" work without touching anything.
    const first = PRESETS[0];
    const form = apiKey ? policyFromKey(apiKey) : applyPreset(first);
    setName(apiKey?.name ?? presetLabel(first.id, t));
    setNameTouched(false);
    setPolicy(form);
    setPreset(apiKey ? null : first.id);
    setPresetBase(apiKey ? null : form);
    setAdjusting(false);
    setIssued(null);
    setSpent(apiKey?.spent_usd ?? 0);
    setError("");
  }, [open, apiKey, t]);

  // Resetting starts a new total window at once, rather than on save: it is
  // not an edit to the form, it is an event in the key's life.
  async function resetBudget() {
    if (!apiKey) return;
    try {
      const fresh = await api.resetKeyBudget(apiKey.id);
      setSpent(fresh.spent_usd ?? 0);
      toast(t("keys.budgetReset", { name: apiKey.name }));
      onSaved();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  function choosePreset(p: Preset) {
    const form = applyPreset(p);
    setPolicy(form);
    setPreset(p.id);
    setPresetBase(form);
    // The name follows the purpose until it is typed in.
    if (!nameTouched) setName(presetLabel(p.id, t));
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    // An empty allow-list means "every model" to the server, so a restricted
    // key with nothing picked would silently be the opposite of what it says.
    if (policy.restrictModels && policy.allowedModels.length === 0) {
      setAdjusting(true);
      setError(t("keys.modelsPickedEmpty"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const input = policyInput(policy);
      if (apiKey) {
        await api.updateKey(apiKey.id, { name: name.trim(), enabled: apiKey.enabled, policy: input });
        onSaved();
        onOpenChange(false);
      } else {
        const res = await api.createKey(name.trim() || t("keys.defaultName"), input);
        // The list refreshes behind the sheet; the sheet stays open, because
        // this is the only time the secret exists.
        onSaved();
        setIssued(res.secret);
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const customised = presetBase !== null && !samePolicy(policy, presetBase);

  return (
    <Sheet
      open={open}
      onOpenChange={onOpenChange}
      title={issued !== null ? t("keys.issuedTitle") : editing ? t("keys.editTitle") : t("keys.createTitle")}
      description={
        issued !== null
          ? t("keys.issuedDescription")
          : editing
            ? t("keys.editDescription")
            : t("keys.createDescription")
      }
      header={
        editing ? undefined : (
          <Steps steps={[t("keys.stepSettings"), t("keys.stepIssue")]} active={issued === null ? 0 : 1} />
        )
      }
      footer={
        issued !== null ? (
          <>
            <span className="mr-auto text-xs text-muted-foreground">{t("keys.copyBeforeClosing")}</span>
            <Button onClick={() => onOpenChange(false)}>{t("common.done")}</Button>
          </>
        ) : (
          <>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" form="key-form" disabled={busy}>
              {busy && <Spinner />} {editing ? t("common.save") : t("common.create")}
            </Button>
          </>
        )
      }
    >
      {issued !== null ? (
        <IssuedStep secret={issued} name={name} policy={policy} />
      ) : (
        <form id="key-form" autoComplete="off" onSubmit={(e) => void submit(e)} className="space-y-5">
          {!editing && (
            <Field label={t("keys.purpose")}>
              <div className="grid gap-2 sm:grid-cols-2">
                {PRESETS.map((p) => (
                  <PresetCard
                    key={p.id}
                    preset={p}
                    selected={preset === p.id}
                    customised={preset === p.id && customised}
                    onSelect={() => choosePreset(p)}
                  />
                ))}
              </div>
            </Field>
          )}

          <Field label={t("common.name")} hint={t("keys.nameHint")}>
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                setNameTouched(true);
              }}
              placeholder={t("keys.namePlaceholder")}
              required={editing}
              autoFocus={editing}
            />
          </Field>

          {!editing && !adjusting && (
            <div>
              <PolicyReview policy={policy} label={t("keys.presetWrites")} />
              <Button type="button" variant="outline" size="sm" className="mt-2.5" onClick={() => setAdjusting(true)}>
                <SlidersHorizontal /> {t("keys.adjust")}
              </Button>
              <p className="mt-1.5 text-xs text-muted-foreground">{t("keys.adjustHint")}</p>
            </div>
          )}

          {(editing || adjusting) && (
            <PolicyFields
              policy={policy}
              onChange={setPolicy}
              models={availableModels.data ?? []}
              modelsLoading={availableModels.loading}
              modelsError={availableModels.error}
              disabled={busy}
              apiKey={apiKey}
              spent={spent}
              onReset={apiKey ? () => void resetBudget() : undefined}
            />
          )}

          <ErrorBanner message={error} />
        </form>
      )}
    </Sheet>
  );
}

/** Two steps, so it says where you are and nothing more. */
function Steps({ steps, active }: { steps: string[]; active: number }) {
  return (
    <div className="flex items-center gap-2 text-xs text-muted-foreground">
      {steps.map((label, i) => (
        <React.Fragment key={label}>
          {i > 0 && <span className="h-px flex-1 bg-border" />}
          <span className={cn("flex items-center gap-1.5", i === active && "font-medium text-foreground")}>
            <span
              className={cn(
                "flex size-5 items-center justify-center rounded-full border text-[11px] tabular-nums",
                i < active
                  ? "border-transparent bg-[--color-success]/15 text-[--color-success]"
                  : i === active
                    ? "border-transparent bg-primary text-primary-foreground"
                    : "border-border",
              )}
            >
              {i < active ? <Check className="size-3" /> : i + 1}
            </span>
            {label}
          </span>
        </React.Fragment>
      ))}
    </div>
  );
}

function PresetCard({
  preset,
  selected,
  customised,
  onSelect,
}: {
  preset: Preset;
  selected: boolean;
  customised: boolean;
  onSelect: () => void;
}) {
  const t = useT();
  const summary = [
    preset.expiry === "never"
      ? t("keys.neverExpires")
      : t("keys.expiresInDays", { days: String(EXPIRY_DAYS[preset.expiry]) }),
    preset.restrictModels ? t("keys.modelsPickedShort") : t("keys.modelsAll"),
    limitSummary(applyPreset(preset), t) || t("keys.unlimited"),
  ].join(" · ");

  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "rounded-lg border px-3 py-2.5 text-left transition-colors",
        selected ? "border-primary bg-accent/60" : "border-border hover:bg-muted/50",
      )}
    >
      <span className="flex items-center gap-1.5 text-sm font-medium">
        {presetLabel(preset.id, t)}
        {customised && <Badge variant="accent">{t("keys.presetCustomised")}</Badge>}
      </span>
      <span className="mt-1 block text-xs text-muted-foreground">{summary}</span>
    </button>
  );
}

/** What the key will allow, in three lines, before it exists. */
function PolicyReview({ policy, label, name }: { policy: PolicyForm; label: string; name?: string }) {
  const t = useT();
  const rows: [string, string][] = [
    ...(name === undefined ? [] : ([[t("common.name"), name]] as [string, string][])),
    [t("keys.expiration"), expiryText(policy, t)],
    [t("keys.allowedModels"), modelsText(policy, t)],
    [t("keys.rateSection"), limitSummary(policy, t) || t("keys.unlimited")],
    [t("keys.budget"), budgetText(policy, t)],
  ];
  return (
    <div>
      <p className="mb-1.5 text-xs font-medium text-muted-foreground">{label}</p>
      <dl className="rounded-md border border-border bg-muted/40 px-3 py-2 text-sm">
        {rows.map(([term, value]) => (
          <div key={term} className="flex items-baseline justify-between gap-3 py-1">
            <dt className="text-muted-foreground">{term}</dt>
            <dd className="min-w-0 truncate text-right">{value}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

/** The three settings, each folded to its current value. This is the block the
 *  edit sheet shows on its own — one form, two shells. */
function PolicyFields({
  policy,
  onChange,
  models,
  modelsLoading,
  modelsError,
  disabled,
  apiKey,
  spent,
  onReset,
}: {
  policy: PolicyForm;
  onChange: (next: PolicyForm) => void;
  models: OfferedModel[];
  modelsLoading: boolean;
  modelsError: string;
  disabled?: boolean;
  /** The key being edited, or null while one is being created. Only an
      existing key has spending to report. */
  apiKey?: APIKey | null;
  spent: number;
  onReset?: () => void;
}) {
  const t = useT();
  const set = (patch: Partial<PolicyForm>) => onChange({ ...policy, ...patch });

  return (
    <div className="space-y-2">
      <Disclosure
        title={t("keys.expiration")}
        value={expiryText(policy, t)}
        defaultOpen={policy.expiresAt !== ""}
      >
        <ChoiceChips
          value={policy.expiryChoice}
          disabled={disabled}
          onChange={(choice) => set({ expiryChoice: choice, expiresAt: expiryFromChoice(choice, policy.expiresAt) })}
          options={[
            { value: "never", label: t("keys.neverExpires") },
            { value: "7", label: t("keys.expiryDays", { days: "7" }) },
            { value: "30", label: t("keys.expiryDays", { days: "30" }) },
            { value: "90", label: t("keys.expiryDays", { days: "90" }) },
            { value: "custom", label: t("keys.expiryCustom") },
          ]}
        />
        {policy.expiryChoice !== "never" && (
          <Input
            type="datetime-local"
            value={policy.expiresAt}
            onChange={(e) => set({ expiryChoice: "custom", expiresAt: e.target.value })}
            className="h-8 sm:w-56"
            disabled={disabled}
          />
        )}
        <p className="text-xs text-muted-foreground">{t("keys.expirationHint")}</p>
      </Disclosure>

      <Disclosure
        title={t("keys.allowedModels")}
        value={modelsText(policy, t)}
        defaultOpen={policy.restrictModels}
      >
        <Tabs
          value={policy.restrictModels ? "picked" : "all"}
          onValueChange={(v) => set({ restrictModels: v === "picked" })}
        >
          <TabsList className="w-full">
            <TabsTrigger value="all" className="flex-1">{t("keys.modelsAll")}</TabsTrigger>
            <TabsTrigger value="picked" className="flex-1">{t("keys.modelsPicked")}</TabsTrigger>
          </TabsList>
        </Tabs>
        {!policy.restrictModels ? (
          <p className="text-xs text-muted-foreground">{t("keys.modelsAllHint")}</p>
        ) : modelsLoading ? (
          <div className="flex min-h-28 items-center justify-center rounded-md border border-border">
            <Spinner className="text-muted-foreground" />
          </div>
        ) : modelsError ? (
          <ErrorBanner message={modelsError} />
        ) : (
          <ModelPicker
            models={withMissingSelections(models, policy.allowedModels, t("keys.missingModel"))}
            selected={new Set(policy.allowedModels)}
            onChange={(selected) => set({ allowedModels: [...selected] })}
            disabled={disabled}
            listClassName="max-h-64"
          />
        )}
      </Disclosure>

      <Disclosure
        title={t("keys.budget")}
        value={budgetText(policy, t)}
        defaultOpen={policy.budget !== ""}
      >
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <span className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-sm text-muted-foreground">
              $
            </span>
            <Input
              type="number"
              min="0"
              step="0.01"
              inputMode="decimal"
              value={policy.budget}
              onChange={(e) => set({ budget: e.target.value })}
              placeholder={t("keys.noBudget")}
              className="h-8 w-36 pl-6 tabular-nums"
              disabled={disabled}
            />
          </div>
          <ChoiceChips
            value={policy.budgetPeriod}
            disabled={disabled || budgetValue(policy) === null}
            onChange={(period) => set({ budgetPeriod: period })}
            options={[
              { value: "total", label: t("keys.budgetTotal") },
              { value: "daily", label: t("keys.budgetDaily") },
              { value: "weekly", label: t("keys.budgetWeekly") },
              { value: "monthly", label: t("keys.budgetMonthly") },
            ]}
          />
        </div>

        {/* What has been spent so far exists only for a key that exists. */}
        {apiKey?.budget_usd != null && (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-muted/40 px-3 py-2 text-xs">
            <span className="text-muted-foreground">
              {t("keys.spentOf", { spent: formatUSD(spent), budget: formatUSD(apiKey.budget_usd) })}
              {" · "}
              {apiKey.budget_resets_at
                ? t("keys.budgetResetsAt", { when: formatTime(apiKey.budget_resets_at) })
                : t("keys.budgetManualReset")}
              {(apiKey.unpriced_requests ?? 0) > 0 &&
                ` · ${t("keys.unpricedCount", { count: String(apiKey.unpriced_requests) })}`}
            </span>
            {apiKey.budget_period === "total" && onReset && (
              <Button type="button" variant="outline" size="sm" onClick={onReset} disabled={disabled}>
                <RotateCcw /> {t("keys.resetBudget")}
              </Button>
            )}
          </div>
        )}

        <p className="text-xs text-muted-foreground">{t("keys.budgetHint")}</p>
        {budgetValue(policy) !== null && (
          <p className="text-xs text-muted-foreground">{t("keys.budgetApproximate")}</p>
        )}
      </Disclosure>

      <Disclosure
        title={t("keys.rateSection")}
        value={limitSummary(policy, t) || t("keys.unlimited")}
        defaultOpen={policy.rateChoice !== "unlimited"}
      >
        <ChoiceChips
          value={policy.rateChoice}
          disabled={disabled}
          onChange={(choice) => set({ rateChoice: choice, ...rateValues(choice, policy) })}
          options={[
            { value: "unlimited", label: t("keys.unlimited") },
            { value: "relaxed", label: t("keys.rateRelaxed", { rpm: RELAXED_RPM }) },
            { value: "strict", label: t("keys.rateStrict", { rpm: STRICT_RPM }) },
            { value: "custom", label: t("keys.rateCustom") },
          ]}
        />
        {/* The seven fields exist for the operator who wants them and stay out
            of the way of everyone else, who gets the same numbers from a chip. */}
        {policy.rateChoice === "custom" ? (
          <div className="-mx-3 -mb-3">
            <LimitGroup title={t("keys.requestLimits")} hint={t("keys.unlimitedHint")}>
              <LimitRow label={t("keys.rpmLabel")} value={policy.rpm} onChange={(v) => set({ rpm: v })} />
              <LimitRow label={t("keys.rphLabel")} value={policy.rph} onChange={(v) => set({ rph: v })} />
              <LimitRow label={t("keys.rpdLabel")} value={policy.rpd} onChange={(v) => set({ rpd: v })} />
            </LimitGroup>

            <LimitGroup title={t("keys.tokenLimits")} hint={t("keys.tokenAccountingHint")}>
              <LimitRow label={t("keys.tpmLabel")} value={policy.tpm} onChange={(v) => set({ tpm: v })} />
              <LimitRow label={t("keys.tpdLabel")} value={policy.tpd} onChange={(v) => set({ tpd: v })} />
            </LimitGroup>

            <LimitGroup title={t("keys.safetyLimits")} hint={t("keys.safetyHint")}>
              <LimitRow label={t("keys.maxConcurrent")} value={policy.maxConcurrent} onChange={(v) => set({ maxConcurrent: v })} />
              <LimitRow label={t("keys.maxOutputTokens")} value={policy.maxOutputTokens} onChange={(v) => set({ maxOutputTokens: v })} />
            </LimitGroup>
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">{t("keys.unlimitedHint")}</p>
        )}
      </Disclosure>
    </div>
  );
}

/** A folded setting that says its current value while it is folded. Its own
 *  state, not a prop: a re-render on every keystroke would otherwise snap a
 *  `<details open={…}>` shut under the cursor. */
function Disclosure({
  title,
  value,
  defaultOpen = false,
  children,
}: {
  title: string;
  value: string;
  defaultOpen?: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = React.useState(defaultOpen);
  return (
    <details
      className="group rounded-md border border-border"
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
    >
      {/* list-none drops the browser's own triangle, which would otherwise sit
          next to the chevron. */}
      <summary className="flex cursor-pointer list-none select-none items-center gap-2 px-3 py-2.5 text-sm [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
        <span className="font-medium">{title}</span>
        {/* Only worth saying while the value itself is hidden. */}
        <span className="ml-auto truncate text-xs text-muted-foreground group-open:hidden">{value}</span>
      </summary>
      <div className="space-y-2.5 border-t border-border/70 px-3 py-3">{children}</div>
    </details>
  );
}

function ChoiceChips<T extends string>({
  value,
  options,
  onChange,
  disabled,
}: {
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          disabled={disabled}
          onClick={() => onChange(o.value)}
          className={cn(
            "h-7 rounded-full border px-3 text-xs transition-colors disabled:opacity-50",
            o.value === value
              ? "border-transparent bg-accent font-medium text-accent-foreground"
              : "border-border text-muted-foreground hover:bg-muted/60",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

// One heading and one explanation per group, not per field: the three request
// limits differ by window length and nothing else, so a hint under each one
// said the same sentence three times.
function LimitGroup({ title, hint, children }: { title: string; hint: string; children: React.ReactNode }) {
  return (
    <section className="border-t border-border/70 px-3 py-2.5">
      <h3 className="text-xs font-medium text-muted-foreground">{title}</h3>
      <div className="mt-1">{children}</div>
      <p className="mt-1.5 text-xs text-muted-foreground">{hint}</p>
    </section>
  );
}

function LimitRow({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  const t = useT();
  const id = React.useId();
  return (
    <div className="flex items-center justify-between gap-4 py-1">
      <label htmlFor={id} className="min-w-0 select-none text-sm">{label}</label>
      <Input
        id={id}
        type="number"
        min="0"
        step="1"
        inputMode="numeric"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={t("keys.unlimited")}
        className="h-8 w-32 shrink-0 px-2 text-center tabular-nums"
      />
    </div>
  );
}

/** The seven numeric limits, in the order the custom section lists them. */
function limitFields(policy: PolicyForm, t: TFunction) {
  return [
    { label: "RPM", value: policy.rpm },
    { label: "RPH", value: policy.rph },
    { label: "RPD", value: policy.rpd },
    { label: "TPM", value: policy.tpm },
    { label: "TPD", value: policy.tpd },
    { label: t("keys.concurrentShort"), value: policy.maxConcurrent },
    { label: t("keys.outputShort"), value: policy.maxOutputTokens },
  ];
}

/** What a folded rate row says: the values that are actually set. */
function limitSummary(policy: PolicyForm, t: TFunction): string {
  return limitFields(policy, t)
    .filter(({ value }) => value.trim() !== "")
    .map(({ label, value }) => `${label} ${value}`)
    .join(" · ");
}

function policyInput(form: PolicyForm): APIKeyPolicyInput {
  const number = (value: string) => value.trim() === "" ? 0 : Number.parseInt(value, 10);
  return {
    rpm: number(form.rpm), rph: number(form.rph), rpd: number(form.rpd),
    tpm: number(form.tpm), tpd: number(form.tpd),
    max_concurrent: number(form.maxConcurrent), max_output_tokens: number(form.maxOutputTokens),
    expires_at: form.expiresAt ? new Date(form.expiresAt).toISOString() : "",
    allowed_models: form.restrictModels ? [...new Set(form.allowedModels)] : [],
    budget_usd: budgetValue(form) ?? 0,
    budget_period: form.budgetPeriod,
  };
}

function policyFromKey(k: APIKey): PolicyForm {
  const value = (v: number | null) => v === null ? "" : String(v);
  const form: PolicyForm = {
    ...emptyPolicy,
    rpm: value(k.rpm), rph: value(k.rph), rpd: value(k.rpd),
    tpm: value(k.tpm), tpd: value(k.tpd),
    maxConcurrent: value(k.max_concurrent), maxOutputTokens: value(k.max_output_tokens),
    // An existing key keeps the exact time it was given, so editing something
    // else does not quietly move its expiry to a round number of days.
    expiresAt: k.expires_at ? localDateTime(k.expires_at) : "",
    expiryChoice: k.expires_at ? "custom" : "never",
    restrictModels: k.allowed_models.length > 0,
    allowedModels: [...k.allowed_models],
    budget: k.budget_usd === null ? "" : String(k.budget_usd),
    budgetPeriod: k.budget_period,
  };
  return { ...form, rateChoice: rateChoiceOf(form) };
}

function withMissingSelections(models: OfferedModel[], selected: string[], missingLabel: string) {
  const ids = new Set(models.map((model) => model.id));
  const missing = selected
    .filter((id) => !ids.has(id))
    .map((id) => ({ id, display_name: missingLabel, registered: false }));
  return [...models, ...missing];
}

function localDateTime(value: string) {
  const d = new Date(value);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function LimitSummary({ apiKey: k }: { apiKey: APIKey }) {
  const t = useT();
  const values = [
    ["RPM", k.rpm], ["RPH", k.rph], ["RPD", k.rpd], ["TPM", k.tpm], ["TPD", k.tpd],
    [t("keys.concurrentShort"), k.max_concurrent], [t("keys.outputShort"), k.max_output_tokens],
  ] as const;
  const active = values.filter(([, value]) => value !== null);
  // A budget is not summarised here: it has a column of its own, because what
  // matters about it is how much is left and when that changes.
  if (active.length === 0 && k.allowed_models.length === 0) {
    return <span className="text-xs text-muted-foreground">{t("keys.unlimited")}</span>;
  }
  return (
    <div className="flex max-w-72 flex-wrap gap-1">
      {active.slice(0, 3).map(([label, value]) => <Badge key={label} variant="outline">{label} {value}</Badge>)}
      {k.allowed_models.length > 0 && <Badge variant="accent">{t("keys.modelCount", { count: String(k.allowed_models.length) })}</Badge>}
      {active.length > 3 && <Badge variant="outline">+{active.length - 3}</Badge>}
    </div>
  );
}

/**
 * What is left of the budget, and when that number goes back up.
 *
 * Remaining rather than spent: the question a key with a cap raises is how
 * much further it can go. The reset instant comes from the server, which is
 * the same one the limiter counts from — recomputing "start of the month"
 * here would eventually disagree with the thing doing the refusing.
 */
function BudgetCell({ apiKey: k }: { apiKey: APIKey }) {
  const t = useT();
  if (k.budget_usd === null) {
    return <span className="text-xs text-muted-foreground">{t("keys.noBudget")}</span>;
  }
  const spent = k.spent_usd ?? 0;
  const left = Math.max(0, k.budget_usd - spent);
  const exhausted = spent >= k.budget_usd;
  const unpriced = k.unpriced_requests ?? 0;
  const detail = [
    t("keys.spentOf", { spent: formatUSD(spent), budget: formatUSD(k.budget_usd) }),
    unpriced > 0 ? t("keys.unpricedCount", { count: String(unpriced) }) : "",
    // The badge says the money is gone; the consequence of that is worth
    // spelling out somewhere.
    exhausted ? t("keys.budgetExhausted") : "",
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <div className="min-w-0" title={detail}>
      {exhausted ? (
        <Badge variant="destructive">{t("keys.budgetSpentUp")}</Badge>
      ) : (
        <span className="text-sm tabular-nums">
          {t("keys.budgetLeft", { amount: formatUSD(left) })}
        </span>
      )}
      <p className="mt-0.5 text-xs text-muted-foreground">
        {k.budget_resets_at
          ? t("keys.budgetResetsAt", { when: formatTime(k.budget_resets_at) })
          : t("keys.budgetManualReset")}
      </p>
    </div>
  );
}

function Expiration({ apiKey: k }: { apiKey: APIKey }) {
  const t = useT();
  if (!k.expires_at) return <span className="text-xs text-muted-foreground">{t("keys.neverExpires")}</span>;
  const expired = new Date(k.expires_at).getTime() <= Date.now();
  return expired
    ? <Badge variant="destructive">{t("keys.expired")}</Badge>
    : <span className="text-xs text-muted-foreground">{formatTime(k.expires_at)}</span>;
}

/**
 * Step two: the secret, once. It lives inside the same sheet the key was
 * created in — as a second dialog handing off from the first, the one moment
 * that cannot be repeated was also the one moment the operator was clicking
 * through.
 */
function IssuedStep({ secret, name, policy }: { secret: string; name: string; policy: PolicyForm }) {
  const t = useT();
  const [copiedKey, setCopiedKey] = React.useState(false);
  const [copiedExample, setCopiedExample] = React.useState(false);
  const { toast } = useToast();

  // The example must name a model this key may actually call, so a restricted
  // key uses one of its own rather than whatever the install lists first.
  const fallback = useAsync(
    () => (policy.restrictModels ? Promise.resolve(null) : api.models({ limit: 1 })),
    [policy.restrictModels],
  );
  const model = policy.restrictModels
    ? policy.allowedModels[0] ?? ""
    : fallback.data?.models.find((m) => m.enabled)?.upstream_model_id ?? "";

  const example = [
    `curl ${window.location.origin}/v1/chat/completions \\`,
    `  -H "Authorization: Bearer ${secret}" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -d '{`,
    `    "model": "${model || "your-model"}",`,
    `    "messages": [{"role": "user", "content": "Hello"}]`,
    `  }'`,
  ].join("\n");

  async function copy(text: string, mark: (v: boolean) => void) {
    if (await copyToClipboard(text)) {
      mark(true);
      setTimeout(() => mark(false), 1800);
    } else {
      toast(t("keys.copyFailed"), "error");
    }
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center gap-2 rounded-md border border-border bg-muted/50 p-3">
        <code className="min-w-0 flex-1 break-all font-mono text-xs">{secret}</code>
        <Button
          variant="outline"
          size="icon-sm"
          onClick={() => void copy(secret, setCopiedKey)}
          title={t("common.copy")}
        >
          {copiedKey ? <Check className="text-[--color-success]" /> : <Copy />}
        </Button>
      </div>

      <div>
        <div className="mb-1.5 flex items-center justify-between gap-2">
          <p className="text-sm font-medium">{t("keys.tryItNow")}</p>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => void copy(example, setCopiedExample)}
            title={t("common.copy")}
          >
            {copiedExample ? <Check className="text-[--color-success]" /> : <Copy />}
          </Button>
        </div>
        <pre className="overflow-x-auto rounded-md border border-border bg-muted/40 p-3 font-mono text-xs leading-relaxed">
          {example}
        </pre>
        <p className="mt-1.5 text-xs text-muted-foreground">
          {model ? t("keys.tryItHint") : t("keys.tryItNoModel")}
        </p>
      </div>

      {/* The last chance to notice the key is not what was meant, while it is
          still one delete away from harmless. */}
      <PolicyReview policy={policy} label={t("keys.issuedSummary")} name={name} />
    </div>
  );
}

// OriginsDialog answers one question: is this key being used from somewhere it
// should not be? Individual log rows cannot answer it — one unfamiliar address
// among thousands is invisible. Grouped and counted, it is obvious.
function OriginsDialog({ apiKey, onClose }: { apiKey: APIKey | null; onClose: () => void }) {
  const t = useT();
  const [origins, setOrigins] = React.useState<KeyOrigin[] | null>(null);
  const [error, setError] = React.useState("");
  const days = 30;

  React.useEffect(() => {
    if (!apiKey) {
      setOrigins(null);
      setError("");
      return;
    }
    let cancelled = false;
    api
      .keyOrigins(apiKey.id, days)
      .then((r) => !cancelled && setOrigins(r.origins))
      .catch((e) => !cancelled && setError(errorMessage(e)));
    return () => {
      cancelled = true;
    };
  }, [apiKey]);

  return (
    <Dialog
      open={apiKey !== null}
      onOpenChange={(o) => !o && onClose()}
      title={t("keys.origins")}
      className="max-w-lg"
    >
      <p className="mb-4 text-sm text-muted-foreground">
        {t("keys.originsHint", { days: String(days) })}
      </p>
      <ErrorBanner message={error} />
      {!origins ? (
        <div className="flex justify-center py-8">
          <Spinner className="text-muted-foreground" />
        </div>
      ) : origins.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{t("keys.originsEmpty")}</p>
      ) : (
        <ul className="space-y-2">
          {origins.map((o) => (
            <li
              key={o.client_ip}
              className="flex items-baseline justify-between gap-3 rounded-md border border-border/70 bg-muted/30 px-3 py-2"
            >
              <code className="font-mono text-sm">{o.client_ip}</code>
              <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                {t("keys.originRequests", { count: String(o.requests) })} ·{" "}
                {formatRelative(o.last_seen, {
                  never: t("common.never"),
                  justNow: t("common.justNow"),
                })}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}
