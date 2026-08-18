import * as React from "react";
import { Check, Search } from "lucide-react";
import type { OfferedModel } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

/**
 * ModelPicker lists what an upstream offers and lets the operator choose.
 *
 * Discovery proposes; the operator disposes. Nothing here writes anything —
 * the caller owns both the fetch and what happens to the selection, because
 * one caller is creating a provider that does not exist yet and the other is
 * adding models to one that does.
 *
 * Models already in the registry are shown but not selectable: offering to add
 * them again would only invite confusion about what a second tick would mean.
 */
export function ModelPicker({
  models,
  selected,
  onChange,
  disabled,
  listClassName,
}: {
  models: OfferedModel[];
  selected: Set<string>;
  onChange: (next: Set<string>) => void;
  disabled?: boolean;
  /** Height of the scrolling list. Shorter where the picker is one field
      among several rather than the point of the dialog. */
  listClassName?: string;
}) {
  const t = useT();
  const [query, setQuery] = React.useState("");

  const shown = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return models;
    return models.filter(
      (m) =>
        m.id.toLowerCase().includes(q) || (m.display_name ?? "").toLowerCase().includes(q),
    );
  }, [models, query]);

  const selectable = shown.filter((m) => !m.registered);
  const allShownSelected = selectable.length > 0 && selectable.every((m) => selected.has(m.id));

  function toggle(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    onChange(next);
  }

  // Select-all acts on what the search is showing, not on the whole list:
  // ticking 300 models because a filter was cleared is exactly the surprise
  // this picker exists to prevent.
  function toggleShown() {
    const next = new Set(selected);
    for (const m of selectable) {
      if (allShownSelected) next.delete(m.id);
      else next.add(m.id);
    }
    onChange(next);
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("models.pickerSearch")}
            className="h-8 pl-8 text-sm"
            disabled={disabled}
          />
        </div>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={toggleShown}
          disabled={disabled || selectable.length === 0}
        >
          {allShownSelected ? t("models.pickerNone") : t("models.pickerAll")}
        </Button>
      </div>

      <div
        className={cn(
          "overflow-y-auto rounded-md border border-border",
          listClassName ?? "max-h-[22rem]",
        )}
      >
        {shown.length === 0 ? (
          <p className="p-4 text-center text-sm text-muted-foreground">
            {t("models.pickerNoMatch")}
          </p>
        ) : (
          <ul className="divide-y divide-border">
            {shown.map((m) => {
              const checked = selected.has(m.id);
              return (
                <li key={m.id}>
                  <button
                    type="button"
                    disabled={disabled || m.registered}
                    onClick={() => toggle(m.id)}
                    className={cn(
                      "flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm transition-colors",
                      m.registered
                        ? "cursor-default text-muted-foreground"
                        : "hover:bg-muted/60",
                    )}
                  >
                    <span
                      className={cn(
                        "flex size-4 shrink-0 items-center justify-center rounded border",
                        m.registered
                          ? "border-border bg-muted"
                          : checked
                            ? "border-primary bg-primary text-primary-foreground"
                            : "border-input",
                      )}
                    >
                      {(checked || m.registered) && <Check className="size-3" />}
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block truncate font-mono text-xs">{m.id}</span>
                      {m.display_name && m.display_name !== m.id && (
                        <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                          {m.display_name}
                        </span>
                      )}
                    </span>
                    {m.registered && (
                      <span className="shrink-0 text-xs">{t("models.pickerAlready")}</span>
                    )}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      <p className="text-xs text-muted-foreground">
        {t("models.pickerCount", { selected: selected.size, total: models.length })}
      </p>
    </div>
  );
}
