import * as React from "react";
import { Copy, Plus, Trash2 } from "lucide-react";
import { api, type ContentLogSettings as Settings, type LogKey } from "@/lib/api";
import { errorMessage, useAsync } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { formatTime } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/input";
import { ErrorBanner, Select, Spinner, Switch } from "@/components/ui/misc";
import { ConfirmDialog } from "@/components/ui/dialog";
import { useToast } from "@/components/ui/toast";

export function ContentLogSettings() {
  const t = useT();
  const { toast } = useToast();
  const { data, error: loadError, reload } = useAsync(async () => {
    const [settings, keys] = await Promise.all([api.contentLogSettings(), api.logKeys()]);
    return { settings, keys };
  }, ["content-log-settings"]);
  const [settings, setSettings] = React.useState<Settings | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");
  const [name, setName] = React.useState("");
  const [secret, setSecret] = React.useState("");
  const [deleting, setDeleting] = React.useState<LogKey | null>(null);
  React.useEffect(() => { if (data) setSettings(data.settings); }, [data]);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!settings) return;
    setBusy(true); setError("");
    try { await api.saveContentLogSettings(settings); toast(t("contentLogs.saved")); reload(); }
    catch (e) { setError(errorMessage(e)); }
    finally { setBusy(false); }
  }
  async function create(e: React.FormEvent) {
    e.preventDefault(); setBusy(true); setError("");
    try { const result = await api.createLogKey(name.trim()); setSecret(result.secret); setName(""); reload(); }
    catch (e) { setError(errorMessage(e)); }
    finally { setBusy(false); }
  }
  async function remove(key: LogKey) {
    setDeleting(null); setBusy(true); setError("");
    try { await api.deleteLogKey(key.id); reload(); }
    catch (e) { setError(errorMessage(e)); }
    finally { setBusy(false); }
  }
  async function copy(value: string) {
    try { await navigator.clipboard.writeText(value); toast(t("contentLogs.copied")); }
    catch { setError(t("contentLogs.copyFailed")); }
  }

  if (loadError) return <ErrorBanner message={loadError} />;
  if (!settings || !data) return <Spinner />;
  return (
    <div className="flex flex-col gap-4">
      {error && <ErrorBanner message={error} />}
      <Card>
        <CardHeader><CardTitle>{t("contentLogs.title")}</CardTitle><CardDescription>{t("contentLogs.description")}</CardDescription></CardHeader>
        <CardContent>
          <form onSubmit={(e) => void save(e)} className="flex flex-col gap-5">
            <div className="flex items-center justify-between gap-4">
              <div className="flex flex-col gap-1"><Label htmlFor="full-content-enabled">{t("contentLogs.enabled")}</Label><p className="text-sm text-muted-foreground">{t("contentLogs.sensitive")}</p></div>
              <Switch id="full-content-enabled" checked={settings.enabled} disabled={busy} onCheckedChange={(enabled) => setSettings({ ...settings, enabled })} />
            </div>
            <div className="flex flex-col gap-2 sm:max-w-xs">
              <Label htmlFor="content-retention">{t("contentLogs.retention")}</Label>
              <Select id="content-retention" value={String(settings.retention_days)} disabled={busy} onValueChange={(v) => { if (v === "3" || v === "7" || v === "30") setSettings({ ...settings, retention_days: Number(v) as 3 | 7 | 30 }); }} options={[3, 7, 30].map((days) => ({ value: String(days), label: t("settings.retentionDays", { days }) }))} />
            </div>
            <p className="text-xs text-muted-foreground">{t("contentLogs.retentionHint")}</p>
            <div><Button type="submit" disabled={busy}>{busy && <Spinner />}{t("common.save")}</Button></div>
          </form>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle>{t("contentLogs.keys")}</CardTitle><CardDescription>{t("contentLogs.keysDescription")}</CardDescription></CardHeader>
        <CardContent className="flex flex-col gap-4">
          <form onSubmit={(e) => void create(e)} className="flex flex-col items-start gap-3 sm:flex-row sm:items-end">
            <div className="flex w-full flex-col gap-2"><Label htmlFor="log-key-name">{t("common.name")}</Label><Input id="log-key-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t("contentLogs.keyPlaceholder")} maxLength={100} required /></div>
            <Button type="submit" disabled={busy || !name.trim()}><Plus />{t("common.create")}</Button>
          </form>
          {secret && <div className="flex flex-col gap-2 rounded-md border border-border p-3" role="status">
            <p className="text-sm">{t("contentLogs.secretOnce")}</p>
            <code className="break-all font-mono text-xs select-all">{secret}</code>
            <div className="flex gap-2"><Button variant="outline" onClick={() => void copy(secret)}><Copy />{t("common.copy")}</Button><Button variant="ghost" onClick={() => setSecret("")}>{t("common.done")}</Button></div>
          </div>}
          {data.keys.length === 0 ? <p className="text-sm text-muted-foreground">{t("contentLogs.noKeys")}</p> : data.keys.map((key) => (
            <div key={key.id} className="flex items-center justify-between gap-3 rounded-md border border-border p-3">
              <div className="min-w-0"><p className="break-words text-sm font-medium">{key.name}</p><p className="text-xs text-muted-foreground"><code>{key.prefix}…</code> · {t("contentLogs.lastUsed", { time: key.last_used_at ? formatTime(key.last_used_at) : t("common.none") })}</p></div>
              <Button variant="ghost" size="icon-sm" disabled={busy} aria-label={t("contentLogs.revokeNamed", { name: key.name })} onClick={() => setDeleting(key)}><Trash2 /></Button>
            </div>
          ))}
          <div className="flex flex-col gap-2 text-sm"><p>{t("contentLogs.agentHint")}</p><code className="break-all text-xs">GET {window.location.origin}/api/logs/v1/</code><code className="break-all text-xs">Authorization: Bearer &lt;LOG_KEY&gt;</code></div>
        </CardContent>
      </Card>
      <ConfirmDialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)} title={t("contentLogs.revokeTitle")} description={t("contentLogs.revokeDescription")} onConfirm={() => deleting && void remove(deleting)} />
    </div>
  );
}
