import * as React from "react";
import { Check, Copy, RefreshCw, RotateCw } from "lucide-react";
import { api, type RuntimeSettings, type RuntimeSettingsResponse } from "@/lib/api";
import { copyToClipboard, errorMessage } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input, Textarea } from "@/components/kit/input";
import { ErrorBanner, Switch } from "@/components/kit/misc";
import { LoadingButton } from "@/components/kit/loading-button";
import { FormSkeleton, LoadingGate } from "@/components/kit/skeletons";
import { useToast } from "@/components/kit/toast";

// The form keeps numbers as the text the operator typed, so a half-typed
// value is not snapped to a number mid-edit; they are parsed on save.
type Draft = Omit<
  RuntimeSettings,
  | "log_retention_days"
  | "provider_cooldown_secs"
  | "upstream_timeout_secs"
  | "max_request_mb"
  | "max_media_mb"
  | "trace_sample_ratio"
  | "otlp_headers"
> & {
  log_retention_days: string;
  provider_cooldown_secs: string;
  upstream_timeout_secs: string;
  max_request_mb: string;
  max_media_mb: string;
  trace_sample_ratio: string;
  otlp_headers: string;
};

function toDraft(s: RuntimeSettings): Draft {
  return {
    ...s,
    log_retention_days: String(s.log_retention_days),
    provider_cooldown_secs: String(s.provider_cooldown_secs),
    upstream_timeout_secs: String(s.upstream_timeout_secs),
    max_request_mb: String(s.max_request_mb),
    max_media_mb: String(s.max_media_mb),
    trace_sample_ratio: String(s.trace_sample_ratio),
    otlp_headers: Object.entries(s.otlp_headers ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
  };
}

// One "name=value" per line, the same pairs the OpenTelemetry SDKs take.
function parseHeaders(text: string): Record<string, string> | null {
  const out: Record<string, string> = {};
  for (const line of text.split(/[\n,]/)) {
    const i = line.indexOf("=");
    if (i <= 0) continue;
    const k = line.slice(0, i).trim();
    if (k) out[k] = line.slice(i + 1).trim();
  }
  return Object.keys(out).length > 0 ? out : null;
}

function fromDraft(d: Draft): RuntimeSettings {
  return {
    ...d,
    log_retention_days: Number(d.log_retention_days),
    provider_cooldown_secs: Number(d.provider_cooldown_secs),
    upstream_timeout_secs: Number(d.upstream_timeout_secs),
    max_request_mb: Number(d.max_request_mb),
    max_media_mb: Number(d.max_media_mb),
    trace_sample_ratio: Number(d.trace_sample_ratio),
    otlp_headers: parseHeaders(d.otlp_headers),
  };
}

/**
 * The settings that used to be environment variables. Everything here applies
 * to the next request when saved, except metrics and tracing, which are built
 * at start and say so.
 */
export function RuntimeSettingsForm() {
  const t = useT();
  const { toast } = useToast();
  const [loaded, setLoaded] = React.useState<RuntimeSettingsResponse | null>(null);
  const [draft, setDraft] = React.useState<Draft | null>(null);
  const [error, setError] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [attempt, setAttempt] = React.useState(0);

  const apply = React.useCallback((res: RuntimeSettingsResponse) => {
    setLoaded(res);
    setDraft(toDraft(res.settings));
  }, []);

  React.useEffect(() => {
    setError("");
    api.runtimeSettings().then(apply, (e) => setError(errorMessage(e)));
  }, [apply, attempt]);

  if (!draft || !loaded) {
    return error ? (
      <ErrorBanner message={error} onRetry={() => setAttempt((n) => n + 1)} />
    ) : (
      <Card>
        <CardContent className="pt-6">
          <LoadingGate loading skeleton={<FormSkeleton fields={6} />}>
            {null}
          </LoadingGate>
        </CardContent>
      </Card>
    );
  }

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => (d ? { ...d, [k]: v } : d));
  const dirty = JSON.stringify(fromDraft(draft)) !== JSON.stringify(fromDraft(toDraft(loaded.settings)));

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!draft) return;
    setSaving(true);
    setError("");
    try {
      apply(await api.updateRuntimeSettings(fromDraft(draft)));
      toast(t("runtime.saved"), "success");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setSaving(false);
    }
  }

  const num = (k: keyof Draft, props?: React.ComponentProps<typeof Input>) => (
    <Input
      inputMode="numeric"
      value={draft[k] as string}
      onChange={(e) => set(k, e.target.value as never)}
      className="font-mono"
      {...props}
    />
  );

  return (
    <form onSubmit={(e) => void save(e)} className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>{t("runtime.requests")}</CardTitle>
          <CardDescription>{t("runtime.appliesNow")}</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <Field label={t("runtime.upstreamTimeout")} hint={t("runtime.upstreamTimeoutHint")}>
            {num("upstream_timeout_secs")}
          </Field>
          <Field label={t("runtime.providerCooldown")} hint={t("runtime.providerCooldownHint")}>
            {num("provider_cooldown_secs")}
          </Field>
          <Field label={t("runtime.maxRequest")} hint={t("runtime.maxRequestHint")}>
            {num("max_request_mb")}
          </Field>
          <Field label={t("runtime.logRetention")} hint={t("runtime.logRetentionHint")}>
            {num("log_retention_days")}
          </Field>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("runtime.network")}</CardTitle>
          <CardDescription>{t("runtime.appliesNow")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Toggle
            label={t("runtime.trustProxy")}
            hint={t("runtime.trustProxyHint")}
            checked={draft.trust_proxy_headers}
            onChange={(v) => set("trust_proxy_headers", v)}
          />
          <Toggle
            label={t("runtime.blockPrivate")}
            hint={t("runtime.blockPrivateHint")}
            checked={draft.block_private_upstream}
            onChange={(v) => set("block_private_upstream", v)}
          />
          <Toggle
            label={t("runtime.fetchMedia")}
            hint={t("runtime.fetchMediaHint")}
            checked={draft.fetch_remote_media}
            onChange={(v) => set("fetch_remote_media", v)}
          />
          {draft.fetch_remote_media && (
            <Field label={t("runtime.maxMedia")} className="sm:max-w-[50%]">
              {num("max_media_mb")}
            </Field>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("runtime.updates")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <Toggle
            label={t("runtime.updateCheck")}
            hint={t("runtime.updateCheckHint")}
            checked={draft.update_check_enabled}
            onChange={(v) => set("update_check_enabled", v)}
          />
          <Field label={t("runtime.updateRepository")} className="sm:max-w-[50%]">
            <Input
              value={draft.update_repository}
              onChange={(e) => set("update_repository", e.target.value)}
              className="font-mono"
              spellCheck={false}
            />
          </Field>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("runtime.observability")}</CardTitle>
          <CardDescription>{t("runtime.appliesOnRestart")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {loaded.restart_pending && (
            <div className="flex items-start gap-2 rounded-md border border-warning/30 bg-warning/8 px-3 py-2 text-sm">
              <RotateCw className="mt-0.5 size-4 shrink-0" />
              {t("runtime.restartPending")}
            </div>
          )}
          <Toggle
            label={t("runtime.metrics")}
            hint={t("runtime.metricsHint")}
            checked={draft.metrics_enabled}
            onChange={(v) => set("metrics_enabled", v)}
          />
          {draft.metrics_enabled && (
            <MetricsToken token={loaded.settings.metrics_token} onRegenerated={apply} />
          )}
          <Toggle
            label={t("runtime.tracing")}
            hint={t("runtime.tracingHint")}
            checked={draft.tracing_enabled}
            onChange={(v) => set("tracing_enabled", v)}
          />
          {draft.tracing_enabled && (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t("runtime.otlpEndpoint")} className="sm:col-span-2">
                <Input
                  value={draft.otlp_endpoint}
                  onChange={(e) => set("otlp_endpoint", e.target.value)}
                  placeholder="http://collector:4318"
                  className="font-mono"
                  spellCheck={false}
                />
              </Field>
              <Field label={t("runtime.otlpHeaders")} hint={t("runtime.otlpHeadersHint")} className="sm:col-span-2">
                <Textarea
                  value={draft.otlp_headers}
                  onChange={(e) => set("otlp_headers", e.target.value)}
                  placeholder="x-api-key=…"
                  rows={3}
                  className="font-mono"
                  spellCheck={false}
                />
              </Field>
              <Field label={t("runtime.sampleRatio")} hint={t("runtime.sampleRatioHint")}>
                {num("trace_sample_ratio", { inputMode: "decimal" })}
              </Field>
              <Field label={t("runtime.serviceName")}>
                <Input
                  value={draft.service_name}
                  onChange={(e) => set("service_name", e.target.value)}
                  className="font-mono"
                  spellCheck={false}
                />
              </Field>
            </div>
          )}
        </CardContent>
      </Card>

      <ErrorBanner message={error} />
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" disabled={!dirty || saving} onClick={() => setDraft(toDraft(loaded.settings))}>
          {t("common.cancel")}
        </Button>
        <LoadingButton type="submit" busy={saving} disabled={!dirty}>
          {t("common.save")}
        </LoadingButton>
      </div>
    </form>
  );
}

function Toggle({
  label,
  hint,
  checked,
  onChange,
}: {
  label: string;
  hint: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  const id = React.useId();
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="space-y-1">
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        <p className="text-xs text-muted-foreground">{hint}</p>
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  );
}

// The scrape token is generated on first start. It is shown so it can be put
// into Prometheus, and replaced rather than edited.
function MetricsToken({
  token,
  onRegenerated,
}: {
  token: string;
  onRegenerated: (res: RuntimeSettingsResponse) => void;
}) {
  const t = useT();
  const { toast } = useToast();
  const [copied, setCopied] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  async function copy() {
    if (await copyToClipboard(token)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } else {
      toast(t("keys.copyFailed"), "error");
    }
  }

  async function regenerate() {
    if (!window.confirm(t("runtime.regenerateConfirm"))) return;
    setBusy(true);
    try {
      onRegenerated(await api.regenerateMetricsToken());
      toast(t("runtime.tokenRegenerated"), "success");
    } catch (err) {
      toast(errorMessage(err), "error");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Field label={t("runtime.metricsToken")} hint={t("runtime.metricsTokenHint")}>
      <div className="flex gap-2">
        <Input value={token} readOnly className="font-mono" onFocus={(e) => e.currentTarget.select()} />
        <Button type="button" variant="outline" size="icon" onClick={() => void copy()} title={t("keys.copyKey")}>
          {copied ? <Check className="text-success" /> : <Copy />}
        </Button>
        <LoadingButton
          type="button"
          variant="outline"
          size="icon"
          onClick={() => void regenerate()}
          busy={busy}
          icon={<RefreshCw />}
          iconIsAction
          title={t("runtime.regenerate")}
          aria-label={t("runtime.regenerate")}
        />
      </div>
    </Field>
  );
}
