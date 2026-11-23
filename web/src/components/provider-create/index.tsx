import * as React from "react";
import { CheckCircle2, Eye, EyeOff, Plug, Sparkles, XCircle } from "lucide-react";
import { api, type Provider, type TestResult } from "@/lib/api";
import { errorMessage } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import {
  agentPlatformBaseURL,
  customProtocolName,
  emptyForm,
  isAgentPlatformBaseURL,
  parseHeaders,
  presetFor,
  upstreamKey,
  type ProviderForm,
  type Upstream,
} from "@/lib/provider-form";
import { cn, formatDuration } from "@/lib/utils";
import { BrandIcon } from "@/components/brand-icon";
import { AdvancedFields } from "@/components/provider-advanced";
import { ProviderModels } from "@/components/provider-models";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Field, Input, noAutofill } from "@/components/kit/input";
import { ErrorBanner, Spinner, Switch } from "@/components/kit/misc";
import { LoadingButton } from "@/components/kit/loading-button";
import { isAbort, useCancellable, useSlowHint } from "@/lib/hooks";
import { useToast } from "@/components/kit/toast";
import { ModelChecklist } from "./model-checklist";
import { PanelSection } from "./panel-section";
import { UpstreamPicker } from "./upstream-picker";

type Props = {
  /** Absent when adding. */
  provider: Provider | null;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  protocols: { value: string; label: string }[];
  onSaved: () => void;
};

/**
 * Both halves of the provider form: adding one and editing one. The sheet
 * stays mounted so Radix can animate it out; the body mounts only while it is
 * open, so every opening starts clean.
 */
export function ProviderSheet({ provider, open, onOpenChange, protocols, onSaved }: Props) {
  const t = useT();
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col gap-0 sm:max-w-lg">
        <SheetHeader className="text-start">
          <SheetTitle>{provider ? t("providers.edit", { name: provider.name }) : t("providers.add")}</SheetTitle>
          <SheetDescription>{t("providers.form.description")}</SheetDescription>
        </SheetHeader>
        <ProviderFormBody
          key={provider?.id ?? "new"}
          provider={provider}
          protocols={protocols}
          onOpenChange={onOpenChange}
          onSaved={onSaved}
        />
      </SheetContent>
    </Sheet>
  );
}

// Fields whose change makes a fetched model list belong to a different
// endpoint than the one that will be saved.
const CONNECTION_FIELDS = new Set<keyof ProviderForm>(["protocol", "vendor", "base_url", "api_key", "headers", "timeout_secs"]);

function formOf(p: Provider): ProviderForm {
  return {
    name: p.name,
    protocol: p.protocol,
    base_url: p.base_url,
    vendor: p.vendor ?? "",
    note: p.note,
    // Write-only: the field starts empty and an untouched field keeps the
    // stored credential.
    api_key: "",
    headers: Object.keys(p.headers).length ? JSON.stringify(p.headers, null, 2) : "",
    timeout_secs: p.timeout_secs,
    priority: p.priority,
    strict_fields: p.strict_fields,
    auto_disable_on_auth_error: p.auto_disable_on_auth_error,
    enabled: p.enabled,
  };
}

/**
 * Adding starts on the upstream picker, because the upstream is the one choice
 * everything after it depends on; editing opens on the fields with the
 * provider's upstream already chosen, and the picker is one "Change" away.
 * The fields stay mounted, only hidden, while the picker is up, so going back
 * never costs what was typed. The protocol is never asked: a preset settles
 * it, and each "custom" card is a protocol.
 */
function ProviderFormBody({ provider, protocols, onOpenChange, onSaved }: Omit<Props, "open">) {
  const t = useT();
  const { toast } = useToast();
  const isEdit = provider !== null;

  const customUpstream = React.useCallback(
    (protocol: Provider["protocol"]): Upstream => {
      const label = protocols.find((p) => p.value === protocol)?.label ?? protocol;
      return { kind: "custom", protocol, label: t("providers.picker.customLabel", { protocol: customProtocolName(label) }) };
    },
    [protocols, t],
  );

  // Read once: afterwards the operator owns these.
  const [initial] = React.useState((): { upstream: Upstream; project: string; location: string } | null => {
    if (!provider) return null;
    const found = presetFor(provider.protocol, provider.base_url, provider.vendor);
    return {
      upstream: found ? { kind: "preset", preset: found.preset } : customUpstream(provider.protocol),
      project: found?.project ?? "",
      location: found?.location ?? "",
    };
  });

  const [step, setStep] = React.useState<"picker" | "form">(isEdit ? "form" : "picker");
  const [upstream, setUpstream] = React.useState<Upstream | null>(initial?.upstream ?? null);
  const [form, setForm] = React.useState<ProviderForm>(provider ? formOf(provider) : emptyForm);
  const [project, setProject] = React.useState(initial?.project ?? "");
  const [location, setLocation] = React.useState(initial?.location ?? "");
  const [picked, setPicked] = React.useState<Set<string>>(new Set());
  const [revision, setRevision] = React.useState(0);
  const [fetching, setFetching] = React.useState(false);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const testCall = useCancellable();
  const testSlow = useSlowHint(testing);
  const [test, setTest] = React.useState<{ result: TestResult; connection: string } | null>(null);
  // The stored key, once the operator has asked to see it. Putting it in the
  // field is not an edit, so it is tracked apart from form.api_key.
  const [storedKey, setStoredKey] = React.useState<string | null>(null);
  const [showKey, setShowKey] = React.useState(false);
  const [revealing, setRevealing] = React.useState(false);

  const resetModels = React.useCallback(() => {
    setPicked(new Set());
    setFetching(false);
    setRevision((r) => r + 1);
  }, []);

  const set = <K extends keyof ProviderForm>(k: K, v: ProviderForm[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
    if (CONNECTION_FIELDS.has(k)) resetModels();
  };

  const parsed = parseHeaders(form.headers);
  const standard = upstream?.kind === "preset" && upstream.preset.agentPlatformStandard === true;
  const agentPlatform = form.protocol === "gemini" && isAgentPlatformBaseURL(form.base_url);
  const upstreamLabel = upstream ? (upstream.kind === "preset" ? upstream.preset.label : upstream.label) : "";
  // On edit an empty key field means "keep the stored credential". A key that
  // was only read back into the field is the stored one too, so it is not sent.
  const keyValue = isEdit && (form.api_key === "" || form.api_key === storedKey) ? null : form.api_key;
  // keyValue, not form.api_key: merely viewing the key must not void the test verdict.
  const connection = [form.protocol, form.vendor, form.base_url, keyValue ?? "", form.headers, form.timeout_secs].join(" ");
  // A verdict belongs to the connection it was measured on.
  const verdict = test && test.connection === connection ? test.result : null;

  async function toggleKey() {
    if (showKey) {
      setShowKey(false);
      return;
    }
    if (isEdit && provider?.has_api_key && form.api_key === "" && storedKey === null) {
      setRevealing(true);
      try {
        const res = await api.revealProviderKey(provider.id);
        setStoredKey(res.secret);
        setForm((f) => (f.api_key === "" ? { ...f, api_key: res.secret } : f));
      } catch (err) {
        toast(errorMessage(err), "error");
        return;
      } finally {
        setRevealing(false);
      }
    }
    setShowKey(true);
  }

  // The name follows the upstream until the operator types their own: a blank
  // field, or one still holding the previous upstream's label, is not theirs.
  function choose(next: Upstream) {
    const nextLabel = next.kind === "preset" ? next.preset.label : "";
    const name = form.name.trim();
    setForm((f) => ({
      ...f,
      name: name === "" || name === upstreamLabel ? nextLabel || f.name : f.name,
      protocol: next.kind === "preset" ? next.preset.protocol : next.protocol,
      // A custom upstream keeps whatever address is there; a preset brings its own.
      base_url: next.kind === "preset" ? next.preset.base_url : f.base_url,
      vendor: next.kind === "preset" ? (next.preset.vendor ?? "") : "",
    }));
    setProject("");
    setLocation("");
    if (!upstream || upstreamKey(upstream) !== upstreamKey(next)) resetModels();
    setUpstream(next);
    setStep("form");
  }

  function headersOrError(): Record<string, string> | null {
    if (parsed.ok) return parsed.headers;
    setError(parsed.reason === "invalid" ? t("providers.headersInvalid") : t("providers.headersNotObject"));
    return null;
  }

  async function runTest() {
    setError("");
    const headers = headersOrError();
    if (!headers) return;
    setTesting(true);
    try {
      const result = await api.testProvider(
        {
          id: provider?.id ?? 0,
          protocol: form.protocol,
          base_url: form.base_url,
          vendor: form.vendor,
          api_key: keyValue,
          headers,
          timeout_secs: form.timeout_secs,
        },
        testCall.start(),
      );
      setTest({ result, connection });
    } catch (e) {
      if (!isAbort(e)) setError(errorMessage(e));
    } finally {
      setTesting(false);
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy || fetching) return;
    setError("");
    const headers = headersOrError();
    if (!headers) return;
    const payload = {
      name: form.name.trim(),
      protocol: form.protocol,
      base_url: form.base_url,
      vendor: form.vendor,
      note: form.note,
      api_key: keyValue,
      headers,
      timeout_secs: form.timeout_secs,
      priority: form.priority,
      strict_fields: form.strict_fields,
      auto_disable_on_auth_error: form.auto_disable_on_auth_error,
      enabled: form.enabled,
    };
    setBusy(true);
    try {
      if (provider) {
        // Model membership is applied as it is edited, so the save only
        // carries the provider's own fields.
        await api.updateProvider(provider.id, payload);
        toast(t("providers.updated", { name: payload.name }));
      } else {
        const res = await api.createProvider({ ...payload, models: [...picked].map((id) => ({ id })) });
        toast(
          res.models_added > 0
            ? t("providers.addedWithModels", { name: payload.name, count: res.models_added })
            : t("providers.addedNoModels", { name: payload.name }),
        );
      }
      onSaved();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      {step === "picker" && (
        <UpstreamPicker
          protocols={protocols}
          current={upstream ? upstreamKey(upstream) : undefined}
          onSelect={choose}
          onBack={upstream ? () => setStep("form") : undefined}
        />
      )}

      {/* Hidden rather than unmounted, so the fields and the model selection
          survive a trip to the picker. The model list sits outside the <form>
          so an Enter in its inputs cannot submit the provider. */}
      <div className={cn("flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-4 py-1 pb-6", step === "picker" && "hidden")}>
        <form id="provider-form" autoComplete="off" onSubmit={(e) => void submit(e)} className="flex flex-col gap-6">
          <PanelSection title={t("providers.form.sectionConnection")}>
            <Field label={t("common.name")}>
              <Input
                value={form.name}
                onChange={(e) => set("name", e.target.value)}
                placeholder="OpenRouter"
                maxLength={80}
                required
              />
            </Field>

            <div className="space-y-2" role="group" aria-labelledby="provider-upstream-label">
              <div id="provider-upstream-label" className="text-sm leading-none font-medium">
                {t("providers.form.upstream")}
              </div>
              <div className="flex items-center gap-3 rounded-lg border p-3">
                <span className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-background">
                  {upstream?.kind === "preset" ? (
                    <BrandIcon name={upstream.preset.brand} className="size-5" />
                  ) : (
                    <Sparkles className="size-4 text-muted-foreground" />
                  )}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{upstreamLabel}</span>
                  <span className="block truncate font-mono text-xs text-muted-foreground">
                    {form.vendor ? t("providers.multiProtocol") : form.protocol}
                  </span>
                </span>
                <Button type="button" variant="outline" size="sm" className="shrink-0" onClick={() => setStep("picker")}>
                  {t("providers.form.change")}
                </Button>
              </div>
            </div>

            {standard && (
              <div className="grid grid-cols-2 gap-4">
                <Field label={t("providers.agentPlatformProject")} hint={t("providers.agentPlatformProjectHint")}>
                  <Input
                    value={project}
                    onChange={(e) => {
                      setProject(e.target.value);
                      set("base_url", agentPlatformBaseURL(e.target.value, location));
                    }}
                    placeholder="my-google-cloud-project"
                    required
                    spellCheck={false}
                  />
                </Field>
                <Field label={t("providers.agentPlatformLocation")} hint={t("providers.agentPlatformLocationHint")}>
                  <Input
                    value={location}
                    onChange={(e) => {
                      setLocation(e.target.value);
                      set("base_url", agentPlatformBaseURL(project, e.target.value));
                    }}
                    placeholder="global"
                    pattern="[A-Za-z0-9-]+"
                    spellCheck={false}
                  />
                </Field>
              </div>
            )}

            <Field label={t("providers.baseURL")} hint={form.vendor ? t("providers.vendorHint") : t("providers.baseURLHint")}>
              <Input
                value={form.base_url}
                onChange={(e) => set("base_url", e.target.value)}
                placeholder="https://api.example.com"
                inputMode="url"
                readOnly={standard || form.vendor !== ""}
                required
                spellCheck={false}
                className="font-mono text-sm"
              />
            </Field>

            <Field label={t("providers.note")} hint={t("providers.noteHint")}>
              <Input
                value={form.note}
                onChange={(e) => set("note", e.target.value)}
                placeholder={t("providers.notePlaceholder")}
                maxLength={500}
              />
            </Field>
          </PanelSection>

          <PanelSection
            title={t("providers.form.sectionCredential")}
            action={
              isEdit && !agentPlatform ? (
                <LoadingButton
                  type="button"
                  variant="outline"
                  size="sm"
                  busy={testing}
                  icon={<Plug />}
                  onClick={() => void runTest()}
                  disabled={!form.base_url}
                >
                  {t("providers.testConnection")}
                </LoadingButton>
              ) : undefined
            }
          >
            <Field
              label={t("providers.apiKey")}
              hint={isEdit ? t("providers.apiKeyHintEdit") : t("providers.apiKeyHintNew")}
            >
              <div className="relative">
                <Input
                  type={showKey ? "text" : "password"}
                  className="pr-9"
                  value={form.api_key}
                  onChange={(e) => set("api_key", e.target.value)}
                  placeholder={provider?.has_api_key ? "••••••••••••" : "sk-…"}
                  spellCheck={false}
                  {...noAutofill}
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  className="absolute right-0.5 top-1/2 -translate-y-1/2"
                  title={showKey ? t("providers.hideKey") : t("providers.showKey")}
                  aria-label={showKey ? t("providers.hideKey") : t("providers.showKey")}
                  disabled={revealing}
                  onClick={() => void toggleKey()}
                >
                  {revealing ? <Spinner decorative className="size-4" /> : showKey ? <EyeOff /> : <Eye />}
                </Button>
              </div>
            </Field>
            {testing && (
              <p role="status" className="flex flex-wrap items-center gap-x-3 text-xs text-muted-foreground">
                <span>{testSlow ? t("common.slowHint") : t("providers.testingForm")}</span>
                <button type="button" onClick={testCall.cancel} className="underline underline-offset-2 hover:text-foreground">
                  {t("common.cancel")}
                </button>
              </p>
            )}
            {verdict && (
              <div
                className={cn(
                  "flex items-start gap-2 rounded-md border px-3 py-2 text-sm",
                  verdict.ok ? "border-success/30 bg-success/8" : "border-destructive/30 bg-destructive/8 text-destructive",
                )}
              >
                {verdict.ok ? (
                  <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" />
                ) : (
                  <XCircle className="mt-0.5 size-4 shrink-0" />
                )}
                <p className="min-w-0 break-words">
                  {verdict.ok
                    ? t("providers.testOk", { latency: formatDuration(verdict.latency_ms), count: verdict.model_count ?? 0 })
                    : verdict.error}
                </p>
              </div>
            )}
            <div className="flex items-center justify-between gap-4">
              <label htmlFor="provider-enabled" className="text-sm">
                {t("common.enabled")}
              </label>
              <Switch id="provider-enabled" checked={form.enabled} onCheckedChange={(v) => set("enabled", v)} />
            </div>
          </PanelSection>

          <AdvancedFields form={form} set={set} parsed={parsed} />
          <ErrorBanner message={error} />
        </form>

        {provider ? (
          // A saved provider's models change as they are edited, one request
          // per change, so they are not part of the save.
          <PanelSection title={t("providers.models")}>
            <ProviderModels
              provider={provider}
              picked={picked}
              onPickedChange={setPicked}
              protocol={form.protocol}
              vendor={form.vendor}
              baseURL={form.base_url}
              apiKey={keyValue}
              headers={parsed.ok ? parsed.headers : {}}
              timeoutSecs={form.timeout_secs}
              showFetch={!agentPlatform}
              hint={agentPlatform ? t("providers.agentPlatformModelsHint") : undefined}
            />
          </PanelSection>
        ) : (
          <ModelChecklist
            key={revision}
            connection={{
              protocol: form.protocol,
              vendor: form.vendor,
              base_url: form.base_url,
              api_key: keyValue ?? "",
              headers: parsed.ok ? parsed.headers : null,
              timeout_secs: form.timeout_secs,
            }}
            canList={!agentPlatform}
            hint={agentPlatform ? t("providers.agentPlatformModelsHint") : undefined}
            selected={picked}
            onChange={setPicked}
            onFetchingChange={setFetching}
          />
        )}
      </div>

      {step === "form" && (
        <SheetFooter className="flex-row justify-end border-t">
          <Button type="button" variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <LoadingButton type="submit" form="provider-form" busy={busy} disabled={fetching || !form.name.trim() || !form.base_url.trim()}>
            {isEdit
              ? t("common.save")
              : picked.size > 0
                ? t("providers.wizard.createWithModels", { count: picked.size })
                : t("providers.wizard.create")}
          </LoadingButton>
        </SheetFooter>
      )}
    </>
  );
}
