import * as React from "react";
import { ArrowRight, Play, Copy, Check, CircleAlert } from "lucide-react";
import { api, type InspectResult, type Model, type ModelAlias, type ProtocolName } from "@/lib/api";
import { copyToClipboard, errorMessage, useAsync } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { PageHeader } from "@/components/layout";
import { FidelityRow } from "@/pages/logs";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Textarea } from "@/components/ui/input";
import { Badge, ErrorBanner, Select, Spinner, Switch } from "@/components/ui/misc";

// The Protocol Inspector runs a request through decode -> canonical -> encode
// and shows all three stages. Nothing is sent upstream.

const SAMPLES: Record<ProtocolName, string> = {
  openai: JSON.stringify(
    {
      model: "my-model",
      messages: [
        { role: "system", content: "You are a concise assistant." },
        { role: "user", content: "What is the weather in Paris?" },
      ],
      tools: [
        {
          type: "function",
          function: {
            name: "get_weather",
            description: "Look up the current weather",
            parameters: {
              type: "object",
              properties: { city: { type: "string" } },
              required: ["city"],
            },
          },
        },
      ],
      temperature: 0.3,
      max_tokens: 512,
      stream: true,
    },
    null,
    2,
  ),
  anthropic: JSON.stringify(
    {
      model: "my-model",
      max_tokens: 512,
      system: "You are a concise assistant.",
      messages: [{ role: "user", content: "What is the weather in Paris?" }],
      tools: [
        {
          name: "get_weather",
          description: "Look up the current weather",
          input_schema: {
            type: "object",
            properties: { city: { type: "string" } },
            required: ["city"],
          },
        },
      ],
      thinking: { type: "enabled", budget_tokens: 2048 },
    },
    null,
    2,
  ),
  "openai-responses": JSON.stringify(
    {
      model: "my-model",
      instructions: "You are a concise assistant.",
      input: "What is the weather in Paris?",
      tools: [
        {
          type: "function",
          name: "get_weather",
          description: "Look up the current weather",
          parameters: {
            type: "object",
            properties: { city: { type: "string" } },
            required: ["city"],
          },
        },
      ],
      max_output_tokens: 512,
      reasoning: { effort: "medium", summary: "auto" },
    },
    null,
    2,
  ),
  gemini: JSON.stringify(
    {
      contents: [{ role: "user", parts: [{ text: "What is the weather in Paris?" }] }],
      systemInstruction: { parts: [{ text: "You are a concise assistant." }] },
      tools: [
        {
          functionDeclarations: [
            {
              name: "get_weather",
              description: "Look up the current weather",
              parameters: {
                type: "object",
                properties: { city: { type: "string" } },
                required: ["city"],
              },
            },
          ],
        },
      ],
      generationConfig: { temperature: 0.3, maxOutputTokens: 512 },
    },
    null,
    2,
  ),
};

export function Inspector() {
  const t = useT();
  const protocols = useAsync(() => api.protocols(), []);
  const registry = useAsync(() => api.models({ limit: 500 }), []);
  const aliases = useAsync(() => api.aliases(), []);

  const [inputProtocol, setInputProtocol] = React.useState<ProtocolName>("openai");
  const [outputProtocol, setOutputProtocol] = React.useState<ProtocolName>("anthropic");
  const [useRouting, setUseRouting] = React.useState(false);
  const [model, setModel] = React.useState("");
  const [source, setSource] = React.useState(SAMPLES.openai);
  const [result, setResult] = React.useState<InspectResult | null>(null);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  function loadSample(p: ProtocolName) {
    setInputProtocol(p);
    setSource(SAMPLES[p]);
    setResult(null);
    setError("");
  }

  async function run() {
    setError("");
    setBusy(true);
    try {
      let parsed: unknown;
      try {
        parsed = JSON.parse(source);
      } catch (cause) {
        throw new Error(t("inspector.invalidJSON", { message: errorMessage(cause) }), { cause });
      }
      const res = await api.inspect({
        input_protocol: inputProtocol,
        output_protocol: useRouting ? "" : outputProtocol,
        use_routing: useRouting,
        body: parsed,
        model: model.trim(),
      });
      setResult(res);
    } catch (e) {
      setResult(null);
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const protocolOptions =
    protocols.data?.map((p) => ({ value: p.name, label: p.label })) ?? [
      { value: "openai", label: "OpenAI" },
    ];

  return (
    <>
      <PageHeader
        title={t("inspector.title")}
        description={t("inspector.description")}
        action={
          <Button onClick={() => void run()} disabled={busy}>
            {busy ? <Spinner /> : <Play />} {t("inspector.convert")}
          </Button>
        }
      />

      <Card className="mb-4">
        <CardContent className="flex flex-wrap items-end gap-4 p-4">
          <Field label={t("inspector.inputProtocol")} className="w-44">
            <Select
              value={inputProtocol}
              onValueChange={(v) => loadSample(v as ProtocolName)}
              options={protocolOptions}
            />
          </Field>

          <ArrowRight className="mb-2.5 size-4 text-muted-foreground/50" />

          <Field
            label={t("inspector.outputProtocol")}
            className="w-44"
            hint={useRouting ? t("inspector.outputFromRouting") : undefined}
          >
            <Select
              value={outputProtocol}
              onValueChange={(v) => setOutputProtocol(v as ProtocolName)}
              options={protocolOptions}
              disabled={useRouting}
            />
          </Field>

          <div className="mb-2.5 flex items-center gap-2">
            <Switch id="use-routing" checked={useRouting} onCheckedChange={setUseRouting} />
            <label htmlFor="use-routing" className="text-sm">
              {t("inspector.useRouting")}
            </label>
          </div>

          {useRouting && (
            <Field label={t("inspector.modelAlias")} className="w-48">
              <Select
                value={model}
                onValueChange={setModel}
                placeholder={t("inspector.selectAlias")}
                options={modelOptions(aliases.data, registry.data?.models)}
              />
            </Field>
          )}
        </CardContent>
      </Card>

      <ErrorBanner message={error} />

      <div className="mt-4 grid gap-4 xl:grid-cols-3">
        <Pane
          title={t("inspector.incoming")}
          subtitle={inputProtocol}
          tone="input"
          editable
          value={source}
          onChange={setSource}
        />
        <Pane
          title={t("inspector.canonical")}
          subtitle={t("inspector.canonicalSubtitle")}
          tone="canonical"
          value={result ? JSON.stringify(result.canonical, null, 2) : ""}
          placeholder={t("inspector.canonicalPlaceholder")}
        />
        <Pane
          title={t("inspector.outgoing")}
          subtitle={result?.route?.protocol ?? outputProtocol}
          tone="output"
          value={result ? JSON.stringify(result.outgoing, null, 2) : ""}
          placeholder={t("inspector.outgoingPlaceholder")}
          badge={
            result?.route ? (
              <Badge variant="outline">
                {result.route.provider} · {result.route.upstream_model}
              </Badge>
            ) : undefined
          }
        />
      </div>

      {result && (
        <Card className="mt-4">
          <CardHeader className="flex-row items-center justify-between">
            <CardTitle>{t("inspector.notes")}</CardTitle>
            {result.lossy ? (
              <Badge variant="warning">
                <CircleAlert className="size-3" /> {t("inspector.lossy")}
              </Badge>
            ) : (
              <Badge variant="success">{t("inspector.noLoss")}</Badge>
            )}
          </CardHeader>
          <CardContent>
            {(result.notes?.length ?? 0) === 0 ? (
              <p className="text-sm text-muted-foreground">
                {t("inspector.noNotes", { protocol: result.route?.protocol ?? outputProtocol })}
              </p>
            ) : (
              <ul className="space-y-2">
                {result.notes?.map((n, i) => (
                  <FidelityRow key={i} note={n} />
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      )}
    </>
  );
}

function Pane({
  title,
  subtitle,
  tone,
  value,
  onChange,
  editable,
  placeholder,
  badge,
}: {
  title: string;
  subtitle: string;
  tone: "input" | "canonical" | "output";
  value: string;
  onChange?: (v: string) => void;
  editable?: boolean;
  placeholder?: string;
  badge?: React.ReactNode;
}) {
  const t = useT();
  const [copied, setCopied] = React.useState(false);

  async function copy() {
    if (!value) return;
    if (await copyToClipboard(value)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    }
  }

  return (
    <Card
      className={cn(
        "flex flex-col overflow-hidden",
        tone === "canonical" && "border-primary/30",
      )}
    >
      <CardHeader className="flex-row items-center justify-between gap-2 border-b border-border/60 pb-3">
        <div className="flex min-w-0 items-center gap-2">
          <CardTitle>{title}</CardTitle>
          <Badge variant={tone === "canonical" ? "accent" : "outline"}>{subtitle}</Badge>
          {badge}
        </div>
        {!editable && value && (
          <Button variant="ghost" size="icon-sm" onClick={() => void copy()} title={t("common.copy")}>
            {copied ? <Check className="text-[--color-success]" /> : <Copy />}
          </Button>
        )}
      </CardHeader>
      <CardContent className="flex-1 p-0">
        {editable ? (
          <Textarea
            value={value}
            onChange={(e) => onChange?.(e.target.value)}
            spellCheck={false}
            className="h-[28rem] resize-none rounded-none border-0 font-mono text-xs shadow-none focus-visible:ring-0"
          />
        ) : value ? (
          <pre className="h-[28rem] overflow-auto p-3 font-mono text-xs leading-relaxed">
            {value}
          </pre>
        ) : (
          <div className="flex h-[28rem] items-center justify-center p-6 text-center text-sm text-muted-foreground">
            {placeholder}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// modelOptions lists what a real request could resolve: aliases first, since
// they take precedence, then the real models each provider offers.
function modelOptions(
  aliases: ModelAlias[] | null,
  models: Model[] | null | undefined,
): { value: string; label: string; hint?: string }[] {
  const out: { value: string; label: string; hint?: string }[] = [];
  const seen = new Set<string>();
  for (const a of aliases ?? []) {
    if (!a.enabled || a.alias === "*" || seen.has(a.alias)) continue;
    seen.add(a.alias);
    out.push({ value: a.alias, label: a.alias, hint: a.provider_name });
  }
  for (const m of models ?? []) {
    if (!m.enabled || seen.has(m.upstream_model_id)) continue;
    seen.add(m.upstream_model_id);
    out.push({ value: m.upstream_model_id, label: m.upstream_model_id, hint: m.provider_name });
  }
  return out;
}
