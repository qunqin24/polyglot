import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
import { zonedDateParts } from "./timezone";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// The active BCP-47 tag for date and number formatting. The i18n provider sets
// it whenever the locale changes. Keeping it module-level avoids threading a
// locale argument through every call site for what is a single global setting.
let formatLocale: string | undefined;

export function setFormatLocale(tag: string) {
  formatLocale = tag;
}

// The instance timezone, set once the session loads. Every timestamp the API
// returns is UTC, so this is what decides the wall-clock time a reader sees.
// UTC is the safe value before the session loads: unlike the browser's local
// zone it cannot be changed by a timezone-spoofing extension.
let formatTimeZone = "UTC";

export function setFormatTimeZone(zone: string | undefined) {
  formatTimeZone = zone || "UTC";
}

export { browserTimeZone, isValidTimeZone, timeZoneNames, timeZoneOffsetLabel } from "./timezone";

// Compact number formatting delegates to Intl so Chinese gets 万/亿 rather
// than a transliterated k/M.
export function formatNumber(n: number): string {
  return new Intl.NumberFormat(formatLocale, {
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(n);
}

/**
 * Money, for reading rather than for accounting. Every cost Polyglot shows is
 * an estimate from a published price list, so the precision on offer is the
 * precision that helps you compare two models — not a figure to reconcile
 * against an invoice.
 *
 * A single request often costs a fraction of a cent, so small amounts keep
 * more digits than large ones, and anything that would round away to nothing
 * says so instead of showing $0.00 — that would read as free.
 */
export function formatUSD(usd: number): string {
  if (usd === 0) return "$0";
  if (usd < 0.0001) return "<$0.0001";
  const digits = usd >= 1 ? 2 : 4;
  return `$${usd.toFixed(digits)}`;
}

/** A price per million tokens, as models.dev publishes them. */
export function formatPrice(usdPerMillion: number): string {
  if (usdPerMillion === 0) return "0";
  const digits = usdPerMillion >= 1 ? 2 : 3;
  return usdPerMillion.toFixed(digits).replace(/\.?0+$/, "");
}

// Durations keep their units in symbols, which read the same in both locales.
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)} s`;
  return `${(ms / 60_000).toFixed(1)} min`;
}

const ENGLISH_MONTHS = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];

const pad2 = (n: number) => String(n).padStart(2, "0");

export function formatTime(iso: string): string {
  const d = new Date(iso);
  const p = zonedDateParts(d, formatTimeZone);
  if (!p) return "—";
  const clock = `${pad2(p.hour)}:${pad2(p.minute)}:${pad2(p.second)}`;
  return formatLocale?.startsWith("zh")
    ? `${p.month}月${p.day}日 ${clock}`
    : `${ENGLISH_MONTHS[p.month - 1]} ${p.day}, ${clock}`;
}

function formatDate(d: Date): string {
  const p = zonedDateParts(d, formatTimeZone);
  if (!p) return "—";
  return formatLocale?.startsWith("zh")
    ? `${p.year}/${p.month}/${p.day}`
    : `${p.month}/${p.day}/${p.year}`;
}

/**
 * formatRelative renders a timestamp as "3 minutes ago" in the active locale.
 * `justNow` and `never` are passed in already translated, because this module
 * deliberately has no dependency on the catalog.
 */
export function formatRelative(
  iso: string | null | undefined,
  labels: { never: string; justNow: string },
): string {
  if (!iso) return labels.never;
  const d = new Date(iso);
  // A zero timestamp from the API means "unset", not 1970.
  if (Number.isNaN(d.getTime()) || d.getUTCFullYear() < 2000) return labels.never;

  const diff = Date.now() - d.getTime();
  if (diff < 60_000) return labels.justNow;

  const rtf = new Intl.RelativeTimeFormat(formatLocale, { numeric: "auto" });
  const steps: [number, number, Intl.RelativeTimeFormatUnit][] = [
    [3_600_000, 60_000, "minute"],
    [86_400_000, 3_600_000, "hour"],
    [2_592_000_000, 86_400_000, "day"],
  ];
  for (const [limit, divisor, unit] of steps) {
    if (diff < limit) return rtf.format(-Math.round(diff / divisor), unit);
  }
  return formatDate(d);
}
