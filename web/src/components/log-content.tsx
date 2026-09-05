import * as React from "react";
import { Download } from "lucide-react";
import { api, type ContentStage, type RequestLogDetail } from "@/lib/api";
import { errorMessage, useAsync } from "@/lib/hooks";
import { useT, type TFunction } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/input";
import { ErrorBanner, Select, Spinner } from "@/components/ui/misc";

function stageName(stage: ContentStage, t: TFunction) {
  const names: Record<string, string> = {
    client_request: t("contentLogs.clientRequest"), client_response: t("contentLogs.clientResponse"),
    canonical_request: t("contentLogs.canonicalRequest"), canonical_response: t("contentLogs.canonicalResponse"),
    canonical_stream: t("contentLogs.canonicalStream"), upstream_request: t("contentLogs.upstreamRequest"),
    upstream_response: t("contentLogs.upstreamResponse"), attempt_error: t("contentLogs.attemptError"),
  };
  const name = names[stage.kind] ?? stage.kind;
  return stage.attempt ? t("contentLogs.attemptStage", { attempt: stage.attempt, stage: name }) : name;
}

export function LogContent({ log }: { log: RequestLogDetail }) {
  const t = useT();
  const [opened, setOpened] = React.useState(false);
  return <section className="flex flex-col gap-3">
    <h3 className="text-sm font-medium">{t("contentLogs.payloads")}</h3>
    {log.content_error && <ErrorBanner message={log.content_error} />}
    {!log.content_id ? <p className="text-sm text-muted-foreground">{t("contentLogs.notRecorded")}</p> : <>
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" onClick={() => setOpened(!opened)}>{opened ? t("contentLogs.hide") : t("contentLogs.view")}</Button>
        <Button variant="outline" onClick={() => window.location.assign(`/api/logs/${log.id}/export`)}><Download />{t("contentLogs.download")}</Button>
      </div>
      {opened && <ContentStages id={log.id} />}
    </>}
  </section>;
}

function ContentStages({ id }: { id: number }) {
  const t = useT();
  const { data, error } = useAsync(() => api.logManifest(id), ["log-manifest", id]);
  const [selected, setSelected] = React.useState("");
  const stage = data?.stages.find((s) => s.id === selected) ?? data?.stages[0];
  if (error) return <ErrorBanner message={t("contentLogs.unavailable", { message: error })} />;
  if (!data) return <Spinner />;
  return <div className="flex flex-col gap-3">
    {!data.complete && <ErrorBanner message={t("contentLogs.incomplete")} />}
    {stage && <>
      <Label htmlFor="capture-stage">{t("contentLogs.stage")}</Label>
      <Select id="capture-stage" value={stage.id} onValueChange={setSelected} options={data.stages.map((s) => ({ value: s.id, label: stageName(s, t) }))} />
      <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
        {stage.provider && <span>{stage.provider}</span>}{stage.protocol && <span>{stage.protocol}</span>}
        {stage.status && <span>{t("contentLogs.httpStatus", { status: stage.status })}</span>}
        <span>{t("contentLogs.bytes", { bytes: stage.bytes })}</span>
      </div>
      {stage.url && <code className="break-all text-xs">{stage.method} {stage.url}</code>}
      {stage.headers && Object.keys(stage.headers).length > 0 && <details className="text-xs"><summary className="cursor-pointer">{t("contentLogs.headers")}</summary><pre className="mt-2 whitespace-pre-wrap break-all">{JSON.stringify(stage.headers, null, 2)}</pre></details>}
      {!stage.complete && <p className="text-xs text-muted-foreground">{t("contentLogs.stageIncomplete")}</p>}
      <StageBody key={`${id}:${stage.id}`} id={id} stage={stage.id} />
    </>}
  </div>;
}

function StageBody({ id, stage }: { id: number; stage: string }) {
  const t = useT();
  const [text, setText] = React.useState("");
  const [offset, setOffset] = React.useState(0);
  const [more, setMore] = React.useState(false);
  const [busy, setBusy] = React.useState(true);
  const [error, setError] = React.useState("");
  const decoder = React.useRef(new TextDecoder());
  React.useEffect(() => {
    let active = true;
    const initialDecoder = new TextDecoder();
    api.logContent(id, stage).then((page) => {
      if (!active) return;
      decoder.current = initialDecoder;
      const bytes = Uint8Array.from(atob(page.data), (c) => c.charCodeAt(0));
      setText(initialDecoder.decode(bytes, { stream: page.has_more }));
      setOffset(page.next_offset); setMore(page.has_more);
    }).catch((e) => active && setError(errorMessage(e))).finally(() => active && setBusy(false));
    return () => { active = false; };
  }, [id, stage]);
  async function loadMore() {
    setBusy(true); setError("");
    try {
      const page = await api.logContent(id, stage, offset);
      const bytes = Uint8Array.from(atob(page.data), (c) => c.charCodeAt(0));
      const next = decoder.current.decode(bytes, { stream: page.has_more });
      setText((v) => v + next); setOffset(page.next_offset); setMore(page.has_more);
    } catch (e) { setError(errorMessage(e)); }
    finally { setBusy(false); }
  }
  let display = text;
  if (!more && text) { try { display = JSON.stringify(JSON.parse(text), null, 2); } catch { /* SSE and plain text are shown verbatim. */ } }
  return <div className="flex flex-col gap-2">
    {error && <ErrorBanner message={error} />}
    <pre tabIndex={0} aria-label={t("contentLogs.payloads")} className="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border bg-muted/30 p-3 font-mono text-xs leading-relaxed">{display || (busy ? t("contentLogs.loading") : t("contentLogs.empty"))}</pre>
    {more && offset < 1_048_576 && <Button variant="outline" disabled={busy} onClick={() => void loadMore()}>{busy && <Spinner />}{t("contentLogs.loadMore")}</Button>}
    {more && offset >= 1_048_576 && <p className="text-xs text-muted-foreground">{t("contentLogs.previewLimit")}</p>}
  </div>;
}
