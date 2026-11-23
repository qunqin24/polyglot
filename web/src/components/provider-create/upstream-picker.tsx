import * as React from "react";
import { ArrowLeft, Search, Sparkles } from "lucide-react";
import type { ProtocolName } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { PRESETS, customProtocolName, upstreamKey, type Upstream } from "@/lib/provider-form";
import { cn } from "@/lib/utils";
import { BrandIcon } from "@/components/brand-icon";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

type Tab = "all" | "vendor" | "platform" | "custom";

const TABS = [
  { value: "all", label: "providers.picker.tabAll" },
  { value: "vendor", label: "providers.picker.tabVendor" },
  { value: "platform", label: "providers.picker.tabPlatform" },
  { value: "custom", label: "providers.picker.tabCustom" },
] as const;

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return "";
  }
}

/**
 * Where adding a provider starts: a searchable, tabbed grid of upstreams.
 * An operator arrives knowing a name, or for a relay only its address, so the
 * search covers the label, the protocol and the endpoint. The protocol is not
 * a separate question: a preset settles it, and "custom" is one card per
 * protocol.
 */
export function UpstreamPicker({
  protocols,
  current,
  onSelect,
  onBack,
}: {
  protocols: { value: string; label: string }[];
  current?: string;
  onSelect: (u: Upstream) => void;
  /** Present only when there is a form to return to without choosing. */
  onBack?: () => void;
}) {
  const t = useT();
  const [search, setSearch] = React.useState("");
  const [tab, setTab] = React.useState<Tab>("all");

  const all: Upstream[] = [
    ...PRESETS.map((preset): Upstream => ({ kind: "preset", preset })),
    ...protocols.map(
      (p): Upstream => ({
        kind: "custom",
        protocol: p.value as ProtocolName,
        label: t("providers.picker.customLabel", { protocol: customProtocolName(p.label) }),
      }),
    ),
  ];

  const q = search.trim().toLowerCase();
  const visible = all.filter((u) => {
    const group = u.kind === "preset" ? u.preset.group : "custom";
    if (tab !== "all" && group !== tab) return false;
    if (!q) return true;
    const text = u.kind === "preset" ? `${u.preset.label} ${u.preset.protocol} ${u.preset.base_url}` : `${u.label} ${u.protocol}`;
    return text.toLowerCase().includes(q);
  });

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 px-4 py-1">
      {onBack && (
        <Button type="button" variant="ghost" size="sm" className="-ms-2 self-start" onClick={onBack}>
          <ArrowLeft aria-hidden="true" />
          {t("providers.picker.back")}
        </Button>
      )}

      <div className="relative">
        <Search className="pointer-events-none absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          autoFocus
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t("providers.picker.search")}
          className="ps-8"
        />
      </div>

      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList className="w-full">
          {TABS.map(({ value, label }) => (
            <TabsTrigger key={value} value={value} className="px-1.5 text-xs sm:text-sm">
              {t(label)}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        {visible.length === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">{t("providers.picker.empty")}</p>
        ) : (
          <div className="grid grid-cols-1 gap-2 min-[420px]:grid-cols-2">
            {visible.map((u) => {
              const key = upstreamKey(u);
              const isCurrent = key === current;
              const label = u.kind === "preset" ? u.preset.label : u.label;
              const sub = u.kind === "preset" ? hostOf(u.preset.base_url) : t("providers.picker.customHost");
              return (
                <button
                  key={key}
                  type="button"
                  onClick={() => onSelect(u)}
                  className={cn(
                    "flex items-center gap-3 rounded-lg border p-3 text-start transition-colors hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none",
                    isCurrent && "border-primary",
                  )}
                >
                  <span className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-background">
                    {u.kind === "preset" ? (
                      <BrandIcon name={u.preset.brand} className="size-5" />
                    ) : (
                      <Sparkles className="size-4 text-muted-foreground" />
                    )}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium">{label}</span>
                      {isCurrent && (
                        <Badge variant="secondary" className="shrink-0">
                          {t("providers.picker.current")}
                        </Badge>
                      )}
                    </span>
                    <span className="block truncate text-xs text-muted-foreground">{sub}</span>
                  </span>
                </button>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
