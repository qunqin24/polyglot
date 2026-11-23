import type { SpendDimension, SpendRange, SpendStats } from "@/lib/api";
import type { TFunction } from "@/lib/i18n";
import { modelLabel } from "@/lib/model-name";

// Shared by the spend components and the live panel: colours, money and the
// name of a row.

export const SPEND_COLORS = [
  "var(--spend-1)",
  "var(--spend-2)",
  "var(--spend-3)",
  "var(--spend-4)",
  "var(--spend-5)",
  "var(--spend-6)",
] as const;

/** A band's colour: the five named bands in order, grey for the rest. */
export function spendColor(i: number, isOther = false): string {
  return isOther ? SPEND_COLORS[5] : SPEND_COLORS[Math.min(i, 4)];
}

/** Money for reading: two decimals from a dollar up, four below it. */
export function usd(v: number, digits?: number): string {
  const d = digits ?? (Math.abs(v) >= 1 || v === 0 ? 2 : 4);
  return "$" + v.toLocaleString("en-US", { minimumFractionDigits: d, maximumFractionDigits: d });
}

/** What a row or band is called: a model by its display name, anything else
 *  by its own name. The folded remainder has no key. */
export function spendLabel(stats: SpendStats, key: string, t: TFunction, othersCount = 0): string {
  if (key === "") return t("spend.others", { count: String(othersCount) });
  return stats.dimension === "model" ? modelLabel(key, stats.names[key]) : key;
}

export const SPEND_RANGES: SpendRange[] = ["24h", "7d", "30d", "month"];
export const SPEND_DIMENSIONS: SpendDimension[] = ["model", "provider", "key"];
