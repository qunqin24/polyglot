import * as React from "react";
import { Plus, Server, Pencil, Trash2, Plug, CheckCircle2, XCircle, ChevronRight } from "lucide-react";
import {
  api,
  type Provider,
  type ProtocolName,
  type ProviderInput,
  type TestResult,
} from "@/lib/api";
import { errorMessage, useAsync } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { cn, formatDuration, formatRelative } from "@/lib/utils";
import { BrandIcon, type BrandName } from "@/components/brand-icon";
import { PageHeader } from "@/components/layout";
import { ProviderModels } from "@/components/provider-models";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ConfirmDialog, Sheet } from "@/components/ui/dialog";
import { Field, Input, Textarea, noAutofill } from "@/components/ui/input";
import {
  Badge,
  EmptyState,
  ErrorBanner,
  Select,
  Spinner,
  Switch,
  Table,
  Td,
  Th,
  Tr,
} from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";

// Presets cover the providers people actually add first. Each one is a Base
// URL plus a protocol — never a bespoke integration.
type ProviderPreset = {
  label: string;
  protocol: ProtocolName;
  base_url: string;
  /** The vendor's mark, so the list is scanned rather than read. Two presets
      may share one — the Responses API is still OpenAI. */
  brand: BrandName;
  agentPlatformStandard?: boolean;
};

const PRESETS: ProviderPreset[] = [
  { label: "OpenAI", protocol: "openai", base_url: "https://api.openai.com", brand: "openai" },
  { label: "OpenAI (Responses API)", protocol: "openai-responses", base_url: "https://api.openai.com", brand: "openai" },
  { label: "OpenRouter", protocol: "openai", base_url: "https://openrouter.ai", brand: "openrouter" },
  { label: "DeepSeek", protocol: "openai", base_url: "https://api.deepseek.com", brand: "deepseek" },
  { label: "SiliconFlow", protocol: "openai", base_url: "https://api.siliconflow.cn", brand: "siliconcloud" },
  { label: "Groq", protocol: "openai", base_url: "https://api.groq.com", brand: "groq" },
  { label: "Ollama (local)", protocol: "openai", base_url: "http://127.0.0.1:11434", brand: "ollama" },
  { label: "Anthropic", protocol: "anthropic", base_url: "https://api.anthropic.com", brand: "anthropic" },
  { label: "Google Gemini", protocol: "gemini", base_url: "https://generativelanguage.googleapis.com", brand: "gemini" },
  {
    label: "Gemini Enterprise Agent Platform (Express)",
    protocol: "gemini",
    base_url: "https://aiplatform.googleapis.com/v1/publishers/google",
    brand: "vertexai",
  },
  {
    label: "Gemini Enterprise Agent Platform (Standard)",
    protocol: "gemini",
    base_url: agentPlatformBaseURL("PROJECT_ID", ""),
    brand: "vertexai",
    agentPlatformStandard: true,
  },
];

function agentPlatformBaseURL(projectID: string, location: string): string {
  const region = location.trim().toLowerCase() || "global";
  const host =
    region === "global"
      ? "aiplatform.googleapis.com"
      : region === "us" || region === "eu"
        ? `aiplatform.${region}.rep.googleapis.com`
        : `${region}-aiplatform.googleapis.com`;
  const project = encodeURIComponent(projectID.trim() || "PROJECT_ID");
  return `https://${host}/v1/projects/${project}/locations/${region}/publishers/google`;
}

function isAgentPlatformBaseURL(baseURL: string): boolean {
  try {
    const url = new URL(baseURL);
    const agentPlatformHost =
      url.hostname === "aiplatform.googleapis.com" ||
      url.hostname.endsWith("-aiplatform.googleapis.com") ||
      /^aiplatform\.(?:us|eu)\.rep\.googleapis\.com$/.test(url.hostname);
    const path = url.pathname.replace(/\/+$/, "");
    return agentPlatformHost && path.endsWith("/publishers/google");
  } catch {
    return false;
  }
}

const emptyForm = {
  name: "",
  protocol: "openai" as ProtocolName,
  base_url: "",
  note: "",
  api_key: "",
  headers: "",
  timeout_secs: 0,
  priority: 0,
  enabled: true,
  strict_fields: false,
  auto_disable_on_auth_error: false,
};
type Form = typeof emptyForm;

type SortKey = "priority" | "name" | "models";

export function Providers() {
  const t = useT();
  const { data, loading, error, reload } = useAsync(() => api.providers(), []);
  const protocols = useAsync(() => api.protocols(), []);
  const { toast } = useToast();

  const [search, setSearch] = React.useState("");
  const [protocolFilter, setProtocolFilter] = React.useState("");
  const [statusFilter, setStatusFilter] = React.useState("");
  const [sort, setSort] = React.useState<SortKey>("priority");

  // Filtering and sorting happen here rather than on the server: a gateway has
  // a handful of providers and they all arrive in one response, so a round
  // trip per keystroke would buy nothing.
  const protocolsInUse = React.useMemo(
    () => [...new Set((data ?? []).map((p) => p.protocol))].sort(),
    [data],
  );

  const visible = React.useMemo(() => {
    const q = search.trim().toLowerCase();
    const rows = (data ?? []).filter((p) => {
      if (
        q &&
        !p.name.toLowerCase().includes(q) &&
        !p.base_url.toLowerCase().includes(q) &&
        !p.note.toLowerCase().includes(q)
      ) {
        return false;
      }
      if (protocolFilter && p.protocol !== protocolFilter) return false;
      if (statusFilter === "enabled" && !p.enabled) return false;
      if (statusFilter === "disabled" && p.enabled) return false;
      return true;
    });

    // Priority is the default because the row order then *is* the order the
    // router tries providers in. The other sorts are for finding something,
    // and say so in their labels.
    return rows.sort((a, b) => {
      switch (sort) {
        case "name":
          return a.name.localeCompare(b.name);
        case "models":
          return b.model_count - a.model_count || a.name.localeCompare(b.name);
        default:
          return b.priority - a.priority || a.name.localeCompare(b.name);
      }
    });
  }, [data, search, protocolFilter, statusFilter, sort]);

  const filtered = search !== "" || protocolFilter !== "" || statusFilter !== "";

  const [editing, setEditing] = React.useState<Provider | null>(null);
  const [open, setOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState<Provider | null>(null);

  function openNew() {
    setEditing(null);
    setOpen(true);
  }

  function openEdit(p: Provider) {
    setEditing(p);
    setOpen(true);
  }

  async function remove() {
    if (!deleting) return;
    try {
      await api.deleteProvider(deleting.id);
      toast(t("providers.deleted", { name: deleting.name }));
      reload();
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setDeleting(null);
    }
  }

  async function toggle(p: Provider, enabled: boolean) {
    try {
      // The update writes every field, so the row's own values have to travel
      // back with the flag being changed — anything left out is erased.
      await api.updateProvider(p.id, {
        name: p.name,
        protocol: p.protocol,
        base_url: p.base_url,
        note: p.note,
        api_key: null,
        headers: p.headers,
        timeout_secs: p.timeout_secs,
        priority: p.priority,
        strict_fields: p.strict_fields,
        auto_disable_on_auth_error: p.auto_disable_on_auth_error,
        enabled,
      });
      reload();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  }

  return (
    <>
      <PageHeader
        title={t("providers.title")}
        description={t("providers.description")}
        action={
          <Button onClick={openNew}>
            <Plus /> {t("providers.add")}
          </Button>
        }
      />

      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Input
          placeholder={t("providers.search")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select
          value={protocolFilter}
          onValueChange={setProtocolFilter}
          className="w-40"
          placeholder={t("common.anyProtocol")}
          options={[
            { value: "", label: t("common.anyProtocol") },
            ...protocolsInUse.map((p) => ({ value: p, label: p })),
          ]}
        />
        <Select
          value={statusFilter}
          onValueChange={setStatusFilter}
          className="w-32"
          placeholder={t("common.anyStatus")}
          options={[
            { value: "", label: t("common.anyStatus") },
            { value: "enabled", label: t("common.enabled") },
            { value: "disabled", label: t("common.disabled") },
          ]}
        />
        <Select
          value={sort}
          onValueChange={(v) => setSort(v as SortKey)}
          className="w-52"
          options={[
            { value: "priority", label: t("providers.sortPriority") },
            { value: "name", label: t("providers.sortName") },
            { value: "models", label: t("providers.sortModels") },
          ]}
        />
        {data && (
          <span className="ml-auto text-xs text-muted-foreground">
            {t("providers.count", { shown: visible.length, total: data.length })}
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
              icon={Server}
              title={filtered ? t("providers.emptyFiltered") : t("providers.empty")}
              description={filtered ? t("providers.emptyFilteredHint") : t("providers.emptyHint")}
              action={
                filtered ? undefined : (
                  <Button onClick={openNew}>
                    <Plus /> {t("providers.add")}
                  </Button>
                )
              }
            />
          ) : (
            <Table>
              <thead>
                <Tr>
                  <Th className="pl-5">{t("common.name")}</Th>
                  <Th
                    className="text-right"
                    title={sort === "priority" ? undefined : t("providers.notRouteOrder")}
                  >
                    {t("providers.priority")}
                    {sort !== "priority" && <span className="ml-1 text-muted-foreground/60">*</span>}
                  </Th>
                  <Th>{t("common.protocol")}</Th>
                  <Th>{t("providers.baseURL")}</Th>
                  <Th>{t("providers.credential")}</Th>
                  <Th>{t("providers.models")}</Th>
                  <Th>{t("common.enabled")}</Th>
                  <Th className="pr-5 text-right">{t("common.actions")}</Th>
                </Tr>
              </thead>
              <tbody>
                {visible.map((p) => (
                  <Tr key={p.id}>
                    <Td className="pl-5 font-medium">
                      <div className="flex flex-wrap items-center gap-1.5">
                        {p.name}
                        {p.cooling_until && (
                          <span title={t("providers.coolingHint", { seconds: secondsLeft(p.cooling_until) })}>
                            <Badge variant="warning">{t("providers.cooling")}</Badge>
                          </span>
                        )}
                        {!p.enabled && p.disabled_reason && (
                          <span title={p.disabled_reason}>
                            <Badge variant="destructive">{t("providers.autoDisabled")}</Badge>
                          </span>
                        )}
                      </div>
                      {/* Under the name, where the reason for a row belongs.
                          title carries the rest of a long one rather than
                          letting it set the column width. */}
                      {p.note && (
                        <p
                          title={p.note}
                          className="mt-0.5 max-w-[16rem] truncate text-xs font-normal text-muted-foreground"
                        >
                          {p.note}
                        </p>
                      )}
                    </Td>
                    {/* The list is ordered by this, so showing it is what makes
                        the row order legible as the routing order. */}
                    <Td className="text-right tabular-nums text-muted-foreground">{p.priority}</Td>
                    <Td>
                      <div className="flex flex-wrap items-center gap-1">
                        <Badge variant="accent">{p.protocol}</Badge>
                      </div>
                    </Td>
                    <Td className="max-w-[22rem] truncate font-mono text-xs text-muted-foreground">
                      {p.base_url}
                    </Td>
                    <Td>
                      {p.has_api_key ? (
                        <span className="font-mono text-xs text-muted-foreground">••••••••</span>
                      ) : (
                        <span className="text-xs text-muted-foreground">{t("common.none")}</span>
                      )}
                    </Td>
                    <Td>
                      <span className="text-sm tabular-nums">{p.model_count}</span>
                      <p className="text-xs text-muted-foreground">
                        {p.models_synced_at
                          ? t("models.syncedAt", {
                              when: formatRelative(p.models_synced_at, {
                                never: t("models.neverSynced"),
                                justNow: t("common.justNow"),
                              }),
                            })
                          : t("models.neverSynced")}
                      </p>
                    </Td>
                    <Td>
                      <Switch checked={p.enabled} onCheckedChange={(v) => void toggle(p, v)} />
                    </Td>
                    <Td className="pr-5">
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" size="icon-sm" onClick={() => openEdit(p)} title={t("common.edit")}>
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => setDeleting(p)}
                          title={t("common.delete")}
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

      <ProviderDialog
        key={editing?.id ?? "new"}
        open={open}
        onOpenChange={setOpen}
        provider={editing}
        protocols={protocols.data?.map((p) => ({ value: p.name, label: p.label })) ?? []}
        onSaved={() => {
          setOpen(false);
          reload();
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("providers.deleteTitle", { name: deleting?.name ?? "" })}
        description={t("providers.deleteDescription")}
        onConfirm={() => void remove()}
      />
    </>
  );
}

// ---------------------------------------------------------------------------
// The dialog follows the order the work actually happens in: connect, verify,
// then choose models. Agent Platform skips verification because its generation
// resource exposes neither a credential probe nor a compatible model list.
// ---------------------------------------------------------------------------

type HeaderParse =
  | { ok: true; headers: Record<string, string> }
  | { ok: false; reason: "invalid" | "notObject" };

// JSON.parse is typed `any`, so the result is taken as unknown and narrowed
// here rather than trusted. Non-string values are stringified: a header can
// only ever be a string on the wire.
//
// Pure on purpose — it runs during render to feed the models block, and a
// function that reported failure by setting state could not.
function parseHeaders(text: string): HeaderParse {
  if (!text.trim()) return { ok: true, headers: {} };
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { ok: false, reason: "invalid" };
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return { ok: false, reason: "notObject" };
  }
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(parsed)) {
    out[key] = typeof value === "string" ? value : String(value);
  }
  return { ok: true, headers: out };
}

function ProviderDialog({
  open,
  onOpenChange,
  provider,
  protocols,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  provider: Provider | null;
  protocols: { value: string; label: string }[];
  onSaved: () => void;
}) {
  const t = useT();
  const { toast } = useToast();
  const [form, setForm] = React.useState<Form>(() =>
    provider
      ? {
          name: provider.name,
          protocol: provider.protocol,
          base_url: provider.base_url,
          note: provider.note,
          api_key: "",
          headers: Object.keys(provider.headers).length
            ? JSON.stringify(provider.headers, null, 2)
            : "",
          timeout_secs: provider.timeout_secs,
          priority: provider.priority,
          strict_fields: provider.strict_fields,
          auto_disable_on_auth_error: provider.auto_disable_on_auth_error,
          enabled: provider.enabled,
        }
      : emptyForm,
  );
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [test, setTest] = React.useState<TestResult | null>(null);
  // Which connection the result above belongs to. Editing the URL after a
  // green tick must not leave the tick standing for the old one.
  const [testedConnection, setTestedConnection] = React.useState("");
  const [agentPlatformStandard, setAgentPlatformStandard] = React.useState(false);
  const [agentPlatformProject, setAgentPlatformProject] = React.useState("");
  const [agentPlatformLocation, setAgentPlatformLocation] = React.useState("");
  // What the operator picked for a provider that does not exist yet. On a
  // saved provider ProviderModels applies changes directly, so this stays
  // empty there.
  const [picked, setPicked] = React.useState<Set<string>>(new Set());
  // The name follows the preset until it is typed in. An existing provider
  // already has the name its operator chose.
  const [nameTouched, setNameTouched] = React.useState(provider !== null);

  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm((f) => ({ ...f, [k]: v }));

  const parsed = parseHeaders(form.headers);
  const connection = [form.protocol, form.base_url, form.api_key, form.headers, form.timeout_secs].join(" ");
  const verdict = test !== null && testedConnection === connection ? test : null;
  const agentPlatform = form.protocol === "gemini" && isAgentPlatformBaseURL(form.base_url);

  // A saved provider was already reachable once and its models are listed on
  // screen, so only a provider that does not exist yet has to prove itself
  // before the picker will open.
  const canList = provider !== null || verdict?.ok === true;
  const modelsHint = agentPlatform
    ? t("providers.agentPlatformModelsHint")
    : canList
      ? verdict?.ok
        ? t("providers.listFromTest", { count: verdict.model_count ?? 0 })
        : undefined
      : t("providers.listNeedsTest");

  // An empty key field on edit means "keep the stored credential".
  function keyValue(): string | null {
    if (!provider) return form.api_key;
    return form.api_key === "" ? null : form.api_key;
  }

  function headersOrError(): Record<string, string> | null {
    if (parsed.ok) return parsed.headers;
    setError(parsed.reason === "invalid" ? t("providers.headersInvalid") : t("providers.headersNotObject"));
    return null;
  }

  async function runTest() {
    setError("");
    setTest(null);
    const headers = headersOrError();
    if (!headers) return;
    setTesting(true);
    try {
      const result = await api.testProvider({
        id: provider?.id ?? 0,
        protocol: form.protocol,
        base_url: form.base_url,
        api_key: keyValue(),
        headers,
        timeout_secs: form.timeout_secs,
      });
      setTest(result);
      setTestedConnection(connection);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setTesting(false);
    }
  }

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    const headers = headersOrError();
    if (!headers) return;

    const payload: ProviderInput = {
      name: form.name,
      protocol: form.protocol,
      base_url: form.base_url,
      note: form.note,
      api_key: keyValue(),
      headers,
      timeout_secs: form.timeout_secs,
      priority: form.priority,
      strict_fields: form.strict_fields,
      auto_disable_on_auth_error: form.auto_disable_on_auth_error,
      enabled: form.enabled,
      models: [...picked].map((id) => ({ id })),
    };
    setBusy(true);
    try {
      if (provider) {
        // Model membership is applied as it is edited, so the save only
        // carries the provider's own fields.
        await api.updateProvider(provider.id, payload);
        toast(t("providers.updated", { name: form.name }));
      } else {
        const res = await api.createProvider(payload);
        toast(
          res.models_added > 0
            ? t("providers.addedWithModels", { name: form.name, count: res.models_added })
            : t("providers.addedNoModels", { name: form.name }),
        );
      }
      onSaved();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  function applyPreset(label: string) {
    const preset = PRESETS.find((p) => p.label === label);
    if (!preset) return;
    setAgentPlatformStandard(preset.agentPlatformStandard === true);
    setAgentPlatformProject("");
    setAgentPlatformLocation("");
    setForm((f) => ({
      ...f,
      // Clearing the field hands the name back to the preset, so trying a
      // few upstreams does not leave the first one's name on the last one.
      name: nameTouched && f.name.trim() !== "" ? f.name : preset.label,
      protocol: preset.protocol,
      base_url: preset.base_url,
    }));
  }

  function setAgentPlatform(projectID: string, location: string) {
    setAgentPlatformProject(projectID);
    setAgentPlatformLocation(location);
    set("base_url", agentPlatformBaseURL(projectID, location));
  }

  // Everything in Advanced is something most providers never need, so the
  // folded row says which of them are not at their default.
  const advanced = [
    form.priority !== 0 ? `${t("providers.priority")} ${form.priority}` : null,
    form.timeout_secs > 0 ? t("providers.timeoutSummary", { seconds: form.timeout_secs }) : null,
    parsed.ok && Object.keys(parsed.headers).length > 0
      ? t("providers.headerSummary", { count: Object.keys(parsed.headers).length })
      : null,
    form.strict_fields ? t("providers.strictFields") : null,
    form.auto_disable_on_auth_error ? t("providers.autoDisable") : null,
  ].filter((v): v is string => v !== null);

  return (
    <Sheet
      open={open}
      onOpenChange={onOpenChange}
      className="max-w-2xl"
      title={provider ? t("providers.edit", { name: provider.name }) : t("providers.add")}
      description={t("providers.dialogDescription")}
      footer={
        <>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" form="provider-form" disabled={busy}>
            {busy && <Spinner />} {t("common.save")}
          </Button>
        </>
      }
    >
      <form id="provider-form" autoComplete="off" onSubmit={(e) => void save(e)} className="space-y-6">
        <Step n={1} title={t("providers.stepConnect")}>
          {!provider && (
            <Field label={t("providers.preset")} hint={t("providers.presetHint")}>
              <Select
                value=""
                placeholder={t("providers.presetPlaceholder")}
                onValueChange={applyPreset}
                options={PRESETS.map((p) => ({
                  value: p.label,
                  label: p.label,
                  hint: p.protocol,
                  icon: <BrandIcon name={p.brand} />,
                }))}
              />
            </Field>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("common.name")}>
              <Input
                value={form.name}
                onChange={(e) => {
                  set("name", e.target.value);
                  setNameTouched(true);
                }}
                placeholder="OpenRouter"
                required
              />
            </Field>
            <Field label={t("common.protocol")}>
              <Select
                value={form.protocol}
                onValueChange={(v) => set("protocol", v as ProtocolName)}
                options={protocols}
                placeholder={t("providers.selectProtocol")}
              />
            </Field>
          </div>

          <Field label={t("providers.note")} hint={t("providers.noteHint")}>
            <Input
              value={form.note}
              onChange={(e) => set("note", e.target.value)}
              placeholder={t("providers.notePlaceholder")}
              maxLength={500}
            />
          </Field>

          {agentPlatformStandard && (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label={t("providers.agentPlatformProject")}
                hint={t("providers.agentPlatformProjectHint")}
              >
                <Input
                  value={agentPlatformProject}
                  onChange={(e) => setAgentPlatform(e.target.value, agentPlatformLocation)}
                  placeholder="my-google-cloud-project"
                  required
                  spellCheck={false}
                />
              </Field>
              <Field
                label={t("providers.agentPlatformLocation")}
                hint={t("providers.agentPlatformLocationHint")}
              >
                <Input
                  value={agentPlatformLocation}
                  onChange={(e) => setAgentPlatform(agentPlatformProject, e.target.value)}
                  placeholder="global"
                  pattern="[A-Za-z0-9-]+"
                  spellCheck={false}
                />
              </Field>
            </div>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("providers.baseURL")} hint={t("providers.baseURLHint")}>
              <Input
                value={form.base_url}
                onChange={(e) => set("base_url", e.target.value)}
                placeholder="https://api.example.com"
                readOnly={agentPlatformStandard}
                required
                spellCheck={false}
              />
            </Field>

            <Field
              label={t("providers.apiKey")}
              hint={provider ? t("providers.apiKeyHintEdit") : t("providers.apiKeyHintNew")}
            >
              <Input
                type="password"
                value={form.api_key}
                onChange={(e) => set("api_key", e.target.value)}
                placeholder={provider?.has_api_key ? "••••••••••••" : "sk-…"}
                spellCheck={false}
                {...noAutofill}
              />
            </Field>
          </div>
        </Step>

        {/* The button and its answer are one row. They used to be a sheet
            apart — the button pinned in the footer, the verdict at the end of
            the scrolling form — which took a scrollIntoView to paper over. */}
        {!agentPlatform && (
          <Step n={2} title={t("providers.stepVerify")}>
            <div className="flex flex-wrap items-center gap-3">
              <Button
                type="button"
                variant="outline"
                onClick={() => void runTest()}
                disabled={testing || !form.base_url}
              >
                {testing ? <Spinner /> : <Plug />} {t("providers.testConnection")}
              </Button>
              {verdict !== null && (
                <div
                  className={cn(
                    "flex min-w-0 flex-1 items-start gap-2 rounded-md border px-3 py-2 text-sm",
                    verdict.ok
                      ? "border-[--color-success]/30 bg-[--color-success]/8"
                      : "border-destructive/30 bg-destructive/8 text-destructive",
                  )}
                >
                  {verdict.ok ? (
                    <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-[--color-success]" />
                  ) : (
                    <XCircle className="mt-0.5 size-4 shrink-0" />
                  )}
                  <p className="min-w-0 break-words">
                    {verdict.ok
                      ? t("providers.testOk", {
                          latency: formatDuration(verdict.latency_ms),
                          count: verdict.model_count ?? 0,
                        })
                      : verdict.error}
                  </p>
                </div>
              )}
            </div>
          </Step>
        )}

        <Step n={agentPlatform ? 2 : 3} title={t("providers.models")}>
          <ProviderModels
            provider={provider}
            picked={picked}
            onPickedChange={setPicked}
            protocol={form.protocol}
            baseURL={form.base_url}
            apiKey={keyValue()}
            headers={parsed.ok ? parsed.headers : {}}
            timeoutSecs={form.timeout_secs}
            canList={canList}
            showFetch={!agentPlatform}
            hint={modelsHint}
          />
        </Step>

        <details className="group rounded-md border border-border">
          {/* list-none drops the browser's own triangle, which would otherwise
              sit next to the chevron. */}
          <summary className="flex cursor-pointer list-none select-none items-center gap-2 px-3 py-2.5 text-sm [&::-webkit-details-marker]:hidden">
            <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
            <span className="font-medium">{t("common.advanced")}</span>
            <span className="ml-auto truncate text-xs text-muted-foreground group-open:hidden">
              {advanced.length > 0 ? advanced.join(" · ") : t("providers.advancedDefaults")}
            </span>
          </summary>
          <div className="space-y-4 border-t border-border/70 px-3 py-3">
            {/* Priority answers a question most installs never ask — it does
                nothing at all until two providers offer the same model id —
                so it lives here rather than above the models. */}
            <Field label={t("providers.priority")} hint={t("providers.priorityHint")}>
              <Input
                type="number"
                value={form.priority}
                onChange={(e) => set("priority", Number(e.target.value))}
                placeholder="0"
                className="sm:w-40"
              />
            </Field>
            <Field label={t("providers.customHeaders")} hint={t("providers.customHeadersHint")}>
              <Textarea
                value={form.headers}
                onChange={(e) => set("headers", e.target.value)}
                placeholder="{}"
                className="font-mono text-xs"
                spellCheck={false}
              />
            </Field>
            <Field label={t("providers.timeout")} hint={t("providers.timeoutHint")}>
              <Input
                type="number"
                min={0}
                max={3600}
                value={form.timeout_secs}
                onChange={(e) => set("timeout_secs", Number(e.target.value))}
                className="sm:w-40"
              />
            </Field>
            {/* Field stacks a label above a full-width control, which is
                wrong for a switch: the two end up on one line with nothing
                between them. Switches follow the house pattern instead —
                control first, label beside it, hint underneath. */}
            <div className="space-y-1.5">
              <div className="flex items-center gap-2">
                <Switch
                  id="strict-fields"
                  checked={form.strict_fields}
                  onCheckedChange={(v) => set("strict_fields", v)}
                />
                <label htmlFor="strict-fields" className="text-sm font-medium select-none">
                  {t("providers.strictFields")}
                </label>
              </div>
              <p className="text-xs text-muted-foreground">{t("providers.strictFieldsHint")}</p>
            </div>

            <div className="space-y-1.5">
              <div className="flex items-center gap-2">
                <Switch
                  id="auto-disable"
                  checked={form.auto_disable_on_auth_error}
                  onCheckedChange={(v) => set("auto_disable_on_auth_error", v)}
                />
                <label htmlFor="auto-disable" className="text-sm font-medium select-none">
                  {t("providers.autoDisable")}
                </label>
              </div>
              <p className="text-xs text-muted-foreground">{t("providers.autoDisableHint")}</p>
            </div>
          </div>
        </details>

        <ErrorBanner message={error} />
      </form>
    </Sheet>
  );
}

/** A numbered section. The numbers are the point: they say these three things
 *  happen in this order, because each one needs the one before it. */
function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-4">
      <h3 className="flex items-center gap-2 border-b border-border pb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        <span className="flex size-4 items-center justify-center rounded-full border border-border text-[10px] tabular-nums">
          {n}
        </span>
        {title}
      </h3>
      {children}
    </section>
  );
}

// secondsLeft renders how much of a cooldown remains. It is recomputed on each
// render rather than ticked: the list already refreshes, and a timer per row
// would be a lot of machinery for a thirty-second badge.
function secondsLeft(until: string): string {
  const ms = new Date(until).getTime() - Date.now();
  return String(Math.max(0, Math.ceil(ms / 1000)));
}
