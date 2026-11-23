import { ChevronRight } from "lucide-react";
import { useT } from "@/lib/i18n";
import type { HeaderParse, ProviderForm } from "@/lib/provider-form";
import { Field, Input, Textarea } from "@/components/kit/input";
import { Switch } from "@/components/kit/misc";

/** The provider settings most upstreams never need, folded behind one row
 *  that says which of them differ from the default. */
export function AdvancedFields({
  form,
  set,
  parsed,
}: {
  form: ProviderForm;
  set: <K extends keyof ProviderForm>(k: K, v: ProviderForm[K]) => void;
  parsed: HeaderParse;
}) {
  const t = useT();
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
  );
}
