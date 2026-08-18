import * as React from "react";
import { api } from "@/lib/api";
import { errorMessage, useTheme, type Theme } from "@/lib/hooks";
import { useT, useI18n, LOCALES, type Locale } from "@/lib/i18n";
import {
  browserTimeZone,
  formatTime,
  isValidTimeZone,
  setFormatTimeZone,
  timeZoneNames,
  timeZoneOffsetLabel,
} from "@/lib/utils";
import { useSession } from "@/App";
import { PageHeader } from "@/components/layout";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner, Select, Spinner } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { UpdateSettings } from "@/components/update-notice";

export function Settings() {
  const t = useT();
  const { me, refresh, signOut } = useSession();
  const { theme, setTheme } = useTheme();
  const { locale, setLocale } = useI18n();

  return (
    <>
      <PageHeader title={t("settings.title")} description={t("settings.description")} />

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>{t("settings.instance")}</CardTitle>
            <CardDescription>{t("settings.instanceDescription")}</CardDescription>
          </CardHeader>
          <CardContent>
            <dl className="space-y-3 text-sm">
              <Row label={t("settings.version")} value={me.version} mono />
              <Row label={t("settings.dataDir")} value={me.data_dir} mono />
              <Row label={t("settings.upstreamTimeout")} value={me.upstream_timeout} mono />
              <Row
                label={t("settings.logRetention")}
                value={
                  me.log_retention > 0
                    ? t("settings.retentionDays", { days: me.log_retention })
                    : t("settings.retentionForever")
                }
              />
              <Row
                label={t("settings.droppedLogs")}
                value={me.dropped_logs > 0 ? String(me.dropped_logs) : t("common.none")}
              />
              <Row label={t("settings.administrator")} value={me.username} />
              <Row label={t("common.created")} value={formatTime(me.created_at)} />
            </dl>
            <div className="mt-5 border-t border-border pt-5">
              <UpdateSettings />
            </div>
          </CardContent>
        </Card>

        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>{t("settings.appearance")}</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={t("settings.theme")}>
                  <Select
                    value={theme}
                    onValueChange={(v) => setTheme(v as Theme)}
                    options={[
                      { value: "system", label: t("settings.themeSystem") },
                      { value: "light", label: t("settings.themeLight") },
                      { value: "dark", label: t("settings.themeDark") },
                    ]}
                  />
                </Field>
                <Field label={t("settings.language")}>
                  <Select
                    value={locale}
                    onValueChange={(v) => setLocale(v as Locale)}
                    options={LOCALES.map((l) => ({ value: l.value, label: l.label }))}
                  />
                </Field>
              </div>
              <div className="mt-4">
                <TimezoneField current={me.timezone} onSaved={refresh} />
              </div>
            </CardContent>
          </Card>

          <PasswordCard onChanged={() => void signOut()} />
        </div>
      </div>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>{t("settings.endpoints")}</CardTitle>
          <CardDescription>{t("settings.endpointsDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-2 text-sm">
          <Endpoint method="POST" path="/v1/chat/completions" label={t("settings.endpointOpenAIChat")} />
          <Endpoint method="POST" path="/v1/responses" label={t("settings.endpointResponses")} />
          <Endpoint method="GET" path="/v1/models" label={t("settings.endpointModels")} />
          <Endpoint method="POST" path="/v1/messages" label={t("settings.endpointAnthropic")} />
          <Endpoint
            method="POST"
            path="/v1beta/models/{model}:generateContent"
            label={t("settings.endpointGeminiGenerate")}
          />
          <Endpoint
            method="POST"
            path="/v1beta/models/{model}:streamGenerateContent"
            label={t("settings.endpointGeminiStream")}
          />
        </CardContent>
      </Card>
    </>
  );
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? "font-mono text-xs" : ""}>{value}</dd>
    </div>
  );
}

function Endpoint({ method, path, label }: { method: string; path: string; label: string }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="w-12 shrink-0 font-mono text-xs text-muted-foreground">{method}</span>
      <code className="font-mono text-xs">{path}</code>
      <span className="text-xs text-muted-foreground">— {label}</span>
    </div>
  );
}

function PasswordCard({ onChanged }: { onChanged: () => void }) {
  const t = useT();
  const { toast } = useToast();
  const [current, setCurrent] = React.useState("");
  const [next, setNext] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (next !== confirm) {
      setError(t("settings.passwordMismatch"));
      return;
    }
    setBusy(true);
    try {
      await api.changePassword(current, next);
      toast(t("settings.passwordChanged"));
      onChanged();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("settings.changePassword")}</CardTitle>
        <CardDescription>{t("settings.changePasswordDescription")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={(e) => void submit(e)} className="space-y-3">
          <Field label={t("settings.currentPassword")}>
            <Input
              type="password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              autoComplete="current-password"
              required
            />
          </Field>
          <Field label={t("settings.newPassword")} hint={t("auth.passwordHint")}>
            <Input
              type="password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              autoComplete="new-password"
              minLength={8}
              required
            />
          </Field>
          <Field label={t("settings.confirmNewPassword")}>
            <Input
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              autoComplete="new-password"
              required
            />
          </Field>
          <ErrorBanner message={error} />
          <Button type="submit" disabled={busy}>
            {busy && <Spinner />} {t("settings.changePassword")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

/**
 * timezoneOptions uses the IANA data bundled with the application. Browser
 * extensions can spoof Intl's timezone and offsets, but cannot rewrite this
 * list or its calculations.
 */
function timezoneOptions(current: string): { value: string; label: string }[] {
  let zones = timeZoneNames();
  // The stored zone must always be selectable, even if this browser does not
  // list it — otherwise the picker would silently show the wrong value.
  if (current && !zones.includes(current)) zones = [current, ...zones];

  return zones.map((zone) => {
    const offset = timeZoneOffsetLabel(zone);
    return { value: zone, label: offset ? `${zone} (${offset})` : zone };
  });
}

function TimezoneField({ current, onSaved }: { current: string; onSaved: () => void }) {
  const t = useT();
  const { toast } = useToast();
  const [saving, setSaving] = React.useState(false);
  const options = React.useMemo(() => timezoneOptions(current), [current]);
  const detected = React.useMemo(browserTimeZone, []);

  async function save(zone: string) {
    if (zone === current) return;
    if (!isValidTimeZone(zone)) {
      toast(t("settings.timezoneInvalid", { timezone: zone }), "error");
      return;
    }
    setSaving(true);
    try {
      await api.updateSettings({ timezone: zone });
      // Apply immediately so every timestamp on screen moves with the change,
      // then reload the session so a refresh keeps it.
      setFormatTimeZone(zone);
      onSaved();
      toast(t("settings.timezoneSaved", { timezone: zone }), "success");
    } catch (err) {
      toast(errorMessage(err), "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Field label={t("settings.timezone")} hint={t("settings.timezoneHint")}>
      <div className="flex items-center gap-2">
        <Select value={current} onValueChange={(v) => void save(v)} disabled={saving} options={options} />
        {saving && <Spinner className="size-4 text-muted-foreground" />}
      </div>
      {current !== detected && isValidTimeZone(detected) && (
        <Button
          type="button"
          variant="link"
          size="sm"
          className="h-auto justify-start p-0 text-xs text-muted-foreground"
          onClick={() => void save(detected)}
        >
          {t("settings.timezoneUseBrowser", { timezone: detected })}
        </Button>
      )}
    </Field>
  );
}
