import * as React from "react";
import { Link } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import type { APIKey, SpendStats } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { spendColor, spendLabel, usd } from "./spend-format";
import { cn, formatNumber, formatTime } from "@/lib/utils";

/**
 * The spend half of the Overview: how much this period, where it is heading,
 * what the cache saved, and where the money went. Every figure is an estimate
 * from published prices, and a request nobody could price is counted as such,
 * never added as zero.
 */

// --- the ruler ----------------------------------------------------------------

/**
 * Spent so far, where it is heading and where the last period ended, on one
 * scale. A month in progress is compared by its projection: its partial total
 * would always look cheaper than a whole month.
 */
export function SpendRuler({ stats }: { stats: SpendStats }) {
  const t = useT();
  const nowSec = stats.to;
  const elapsed = Math.max(1, nowSec - stats.from);
  const isMonth = stats.range === "month";
  const span = isMonth ? stats.period_end - stats.from : 30 * 86400;
  // Under an hour of a month is too little to project from; show the spend
  // and leave the projection out rather than multiply noise by 700.
  const projected = elapsed >= 3600 ? (stats.cost_usd / elapsed) * span : null;
  const compareWith = isMonth ? projected : stats.cost_usd;
  // A previous period cut short by log retention is a partial total; a
  // percentage drawn from it would be confidently wrong, so none is drawn.
  const prevPartial = stats.retained_since !== undefined;
  const delta =
    compareWith !== null && stats.prev_cost_usd > 0 && !prevPartial
      ? (compareWith / stats.prev_cost_usd - 1) * 100
      : null;

  const scale = Math.max(projected ?? 0, stats.prev_cost_usd, stats.cost_usd, 0.0001) * 1.08;
  const pct = (v: number) => `${Math.min(100, (v / scale) * 100)}%`;
  const days = Math.max(1, Math.ceil(elapsed / 86400));

  const spentLabel = {
    "24h": t("spend.spent24h"),
    "7d": t("spend.spent7d"),
    "30d": t("spend.spent30d"),
    month: t("spend.spentMonth", { days: String(days) }),
  }[stats.range];
  const projLabel = isMonth ? t("spend.projectMonth") : t("spend.project30d");
  const prevLabel = {
    "24h": t("spend.prev24h"),
    "7d": t("spend.prev7d"),
    "30d": t("spend.prev30d"),
    month: t("spend.prevMonth"),
  }[stats.range];

  const whole = Math.floor(stats.cost_usd);
  const cents = usd(stats.cost_usd, 2).split(".")[1];

  return (
    <section className="mb-7" aria-live="polite">
      <div className="mb-3.5 flex flex-wrap items-end gap-x-7 gap-y-3">
        <div>
          <div className="mb-1.5 text-xs text-muted-foreground">{spentLabel}</div>
          <div className="text-[44px] leading-none font-semibold tracking-tight tabular-nums">
            ${whole.toLocaleString("en-US")}
            <span className="ml-0.5 text-xl font-medium text-muted-foreground">.{cents}</span>
          </div>
        </div>
        {projected !== null && (
          <div>
            <div className="mb-1.5 text-xs text-muted-foreground">{projLabel}</div>
            <div className="text-[22px] font-semibold tabular-nums">{usd(projected, 2)}</div>
          </div>
        )}
        <div>
          <div className="mb-1.5 text-xs text-muted-foreground">{prevLabel}</div>
          <div className="text-[22px] font-medium text-muted-foreground tabular-nums">
            {usd(stats.prev_cost_usd, 2)}
          </div>
          {prevPartial && (
            <div className="mt-0.5 text-[11px] text-muted-foreground">
              {t("spend.prevPartial", { date: shortDate(stats.retained_since!) })}
            </div>
          )}
        </div>
        {delta !== null && Math.abs(delta) >= 0.5 && (
          <span
            className={cn(
              "rounded-md px-2 py-0.5 text-[13px] font-medium",
              delta > 0
                ? "bg-[color-mix(in_oklch,var(--warning)_18%,transparent)] text-[color-mix(in_oklch,var(--warning)_60%,var(--foreground))]"
                : "bg-[color-mix(in_oklch,var(--success)_16%,transparent)] text-success",
            )}
          >
            {isMonth
              ? t(delta > 0 ? "spend.projectedMore" : "spend.projectedLess", { pct: Math.abs(delta).toFixed(0) })
              : t(delta > 0 ? "spend.more" : "spend.less", { pct: Math.abs(delta).toFixed(0) })}
          </span>
        )}
      </div>

      <div className="relative mt-1 h-9" role="img" aria-label={`${spentLabel} ${usd(stats.cost_usd)}`}>
        <div className="absolute inset-x-0 top-3.5 h-2 rounded bg-muted" />
        {isMonth && projected !== null && (
          <div
            className="absolute top-3.5 h-2 rounded transition-[width] duration-300 ease-out"
            style={{
              width: pct(projected),
              background:
                "repeating-linear-gradient(135deg, color-mix(in oklch, var(--brand) 40%, transparent) 0 4px, transparent 4px 8px)",
            }}
          />
        )}
        <div
          className="absolute top-3.5 left-0 h-2 rounded bg-brand transition-[width] duration-300 ease-out"
          style={{ width: pct(stats.cost_usd) }}
        />
        {stats.prev_cost_usd > 0 && !prevPartial && (
          <div
            className="absolute top-1.5 h-6 w-0.5 rounded-sm bg-muted-foreground transition-[left] duration-300 ease-out"
            style={{ left: pct(stats.prev_cost_usd) }}
          >
            <span className="absolute -top-5 -translate-x-1/2 text-[11px] whitespace-nowrap text-muted-foreground">
              {prevLabel} {usd(stats.prev_cost_usd, 0)}
            </span>
          </div>
        )}
      </div>
      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span className="flex items-center gap-1.5">
          <i className="size-2.5 rounded-[3px] bg-brand" />
          {t("spend.legendSpent")}
        </span>
        {isMonth && projected !== null && (
          <span className="flex items-center gap-1.5">
            <i
              className="size-2.5 rounded-[3px]"
              style={{
                background:
                  "repeating-linear-gradient(135deg, color-mix(in oklch, var(--brand) 45%, transparent) 0 3px, transparent 3px 6px)",
              }}
            />
            {t("spend.legendProjected")}
          </span>
        )}
        <span className="flex items-center gap-1.5">
          <i className="h-2.5 w-0.5 bg-muted-foreground" />
          {t("spend.legendPrev")}
        </span>
      </div>
    </section>
  );
}

function shortDate(unix: number): string {
  const d = new Date(unix * 1000);
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

// --- the three facts ---------------------------------------------------------

export function SpendFacts({ stats }: { stats: SpendStats }) {
  const t = useT();
  const priced = stats.requests - stats.unpriced_requests;
  const hit = stats.input_tokens > 0 ? (stats.cached_input_tokens / stats.input_tokens) * 100 : null;
  return (
    <section className="mb-7 grid gap-3 md:grid-cols-3">
      <Fact label={t("spend.saved")} value={usd(stats.cache_savings_usd)}>
        {hit === null
          ? t("spend.savedNone")
          : t("spend.savedHit", { pct: hit.toFixed(0) }) +
            (stats.cache_savings_partial ? t("spend.savedPartial") : "")}
      </Fact>
      <Fact
        label={t("spend.avg")}
        value={priced > 0 ? usd(stats.cost_usd / priced, 4) : "—"}
      >
        {t("spend.avgNote", { count: priced.toLocaleString() })}
      </Fact>
      {stats.unpriced_requests > 0 ? (
        <Fact
          label={t("spend.unpriced")}
          value={t("spend.times", { count: stats.unpriced_requests.toLocaleString() })}
          warn
        >
          {t("spend.unpricedNote")}{" "}
          <Link to="/pricing" className="font-medium text-brand hover:underline">
            {t("spend.unpricedFix")} <ArrowRight className="inline size-3" />
          </Link>
        </Fact>
      ) : (
        <Fact label={t("spend.unpriced")} value={t("spend.times", { count: "0" })}>
          {t("spend.allPriced")}
        </Fact>
      )}
    </section>
  );
}

function Fact({
  label,
  value,
  warn,
  children,
}: {
  label: string;
  value: string;
  warn?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        "rounded-xl border bg-card px-3.5 py-3",
        warn && "border-[color-mix(in_oklch,var(--warning)_45%,var(--border))]",
      )}
    >
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="text-xl font-semibold tracking-tight tabular-nums">{value}</div>
      <p className="mt-0.5 text-xs text-muted-foreground">{children}</p>
    </div>
  );
}

// --- the stacked chart -------------------------------------------------------

function useWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = React.useRef<T>(null);
  const [width, setWidth] = React.useState(0);
  React.useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.floor(e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, width];
}

/** The smallest round number at or above v whose thirds are round too, so the
 *  axis reads $0 / $1 / $2 / $3 rather than $0 / $0.53 / $1.07. */
function niceCeiling(v: number): number {
  if (v <= 0) return 3;
  const step = v / 3;
  const mag = 10 ** Math.floor(Math.log10(step));
  const nice = [1, 2, 2.5, 5, 10].find((m) => m * mag >= step) ?? 10;
  return nice * mag * 3;
}

/** Spend per hour or day, stacked by the period's biggest rows. */
export function SpendChart({ stats }: { stats: SpendStats }) {
  const t = useT();
  const [ref, W] = useWidth<HTMLDivElement>();
  const [hover, setHover] = React.useState<{ i: number; x: number; y: number } | null>(null);
  const H = 220;
  const padL = 48;
  const padB = 22;
  const padT = 8;
  const n = stats.starts.length;
  const totals = stats.starts.map((_, i) => stats.stacks.reduce((s, b) => s + (b.points[i] ?? 0), 0));
  const max = niceCeiling(Math.max(...totals, 0));
  const bw = n > 0 && W > padL ? (W - padL) / n : 0;
  const hourly = stats.bucket_seconds < 86400;
  const othersCount = Math.max(0, stats.rows.length - (stats.stacks.length - 1));

  const label = (start: number) => {
    const d = new Date(start * 1000);
    return hourly
      ? `${String(d.getHours()).padStart(2, "0")}:00`
      : `${d.getMonth() + 1}/${d.getDate()}`;
  };
  const step = Math.max(1, Math.ceil(n / 8));

  return (
    <div ref={ref} className="relative">
      {W > 0 && (
        <svg width={W} height={H} role="img" aria-label={t("spend.chartLabel")} onMouseLeave={() => setHover(null)}>
          {[0, 1, 2, 3].map((k) => {
            const y = padT + (H - padT - padB) * (1 - k / 3);
            return (
              <g key={k}>
                <line x1={padL} x2={W} y1={y} y2={y} stroke="var(--border)" />
                <text x={padL - 8} y={y + 4} textAnchor="end" fontSize={11} fill="var(--muted-foreground)">
                  {usd((max * k) / 3, Number.isInteger(max / 3) ? 0 : max >= 3 ? 1 : 2)}
                </text>
              </g>
            );
          })}
          {stats.starts.map((start, i) => {
            let y = H - padB;
            const x = padL + i * bw + bw * 0.18;
            const w = Math.max(1, bw * 0.64);
            return (
              <g key={start}>
                {stats.stacks.map((b, si) => {
                  const v = b.points[i] ?? 0;
                  const h = (v / max) * (H - padT - padB);
                  y -= h;
                  return h > 0 ? (
                    <rect
                      key={si}
                      x={x}
                      y={y}
                      width={w}
                      height={Math.max(h - 1, 0.5)}
                      rx={2}
                      fill={spendColor(si, b.key === "")}
                    />
                  ) : null;
                })}
                <rect
                  x={padL + i * bw}
                  y={padT}
                  width={bw}
                  height={H - padT - padB}
                  fill="transparent"
                  onMouseMove={(e) => setHover({ i, x: e.clientX, y: e.clientY })}
                />
                {i % step === 0 && (
                  <text
                    x={padL + i * bw + bw / 2}
                    y={H - 6}
                    textAnchor="middle"
                    fontSize={11}
                    fill="var(--muted-foreground)"
                  >
                    {label(start)}
                  </text>
                )}
              </g>
            );
          })}
        </svg>
      )}
      {hover && (
        <div
          className="pointer-events-none fixed z-50 min-w-44 rounded-lg bg-primary px-2.5 py-2 text-xs text-primary-foreground shadow-md"
          style={{ left: Math.min(hover.x + 14, window.innerWidth - 220), top: hover.y - 20 }}
        >
          <div className="mb-1 opacity-75">{label(stats.starts[hover.i])}</div>
          {stats.stacks.map((b, si) => (
            <div key={si} className="flex justify-between gap-3">
              <span className="flex items-center gap-1.5">
                <i className="size-2 rounded-sm" style={{ background: spendColor(si, b.key === "") }} />
                {spendLabel(stats, b.key, t, othersCount)}
              </span>
              <b className="font-medium tabular-nums">{usd(b.points[hover.i] ?? 0)}</b>
            </div>
          ))}
          <div className="mt-1 flex justify-between gap-3 border-t border-primary-foreground/25 pt-1">
            <span>{t("spend.total")}</span>
            <b className="font-medium tabular-nums">{usd(totals[hover.i])}</b>
          </div>
        </div>
      )}
      <div className="mt-2.5 flex flex-wrap gap-x-3.5 gap-y-1 text-xs text-muted-foreground">
        {stats.stacks.map((b, si) => (
          <span key={si} className="flex items-center gap-1.5">
            <i className="size-2.5 rounded-[3px]" style={{ background: spendColor(si, b.key === "") }} />
            {spendLabel(stats, b.key, t, othersCount)}
          </span>
        ))}
      </div>
    </div>
  );
}

// --- the ranking ---------------------------------------------------------------

export function SpendTable({ stats }: { stats: SpendStats }) {
  const t = useT();
  const top = stats.rows[0]?.cost_usd ?? 0;
  const head = { model: t("spend.colModel"), provider: t("spend.colProvider"), key: t("spend.colKey") }[stats.dimension];
  if (stats.rows.length === 0) {
    return <p className="py-6 text-center text-sm text-muted-foreground">{t("spend.empty")}</p>;
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[13px]">
        <thead>
          <tr className="text-left text-xs text-muted-foreground">
            <th className="w-6 pb-2 font-normal" />
            <th className="pb-2 font-normal">{head}</th>
            {stats.dimension === "model" && <th className="pb-2 font-normal">{t("spend.colProvider")}</th>}
            <th className="pb-2 font-normal">{t("spend.colShare")}</th>
            <th className="pb-2 text-right font-normal">{t("spend.colCost")}</th>
            <th className="pb-2 text-right font-normal">{t("spend.colRequests")}</th>
            <th className="pb-2 text-right font-normal">{t("spend.colPer")}</th>
          </tr>
        </thead>
        <tbody>
          {stats.rows.map((r, i) => {
            const share = stats.cost_usd > 0 ? (r.cost_usd / stats.cost_usd) * 100 : 0;
            const priced = r.requests - r.unpriced;
            return (
              <tr key={`${r.provider_name ?? ""}\u0000${r.key}`} className="border-t">
                <td className="py-2.5 pr-2 text-muted-foreground tabular-nums">{i + 1}</td>
                <td className="py-2.5 pr-3 whitespace-nowrap" title={r.key}>
                  {spendLabel(stats, r.key, t)}
                </td>
                {stats.dimension === "model" && (
                  <td className="py-2.5 pr-3 whitespace-nowrap text-muted-foreground">{r.provider_name}</td>
                )}
                <td className="py-2.5 pr-3">
                  <div className="flex min-w-36 items-center gap-2">
                    <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
                      <div
                        className="h-full rounded-full"
                        style={{ width: `${top > 0 ? (r.cost_usd / top) * 100 : 0}%`, background: spendColor(i) }}
                      />
                    </div>
                    <em className="w-9 text-right text-xs text-muted-foreground not-italic tabular-nums">
                      {share.toFixed(0)}%
                    </em>
                  </div>
                </td>
                <td className="py-2.5 pr-3 text-right tabular-nums">
                  {usd(r.cost_usd)}
                  {r.unpriced > 0 && (
                    <span
                      className="ml-1 text-[11px] text-[color-mix(in_oklch,var(--warning)_70%,var(--foreground))]"
                      title={t("spend.rowUnpriced", { count: String(r.unpriced) })}
                    >
                      +?
                    </span>
                  )}
                </td>
                <td className="py-2.5 pr-3 text-right tabular-nums">{r.requests.toLocaleString()}</td>
                <td className="py-2.5 text-right text-muted-foreground tabular-nums">
                  {priced > 0 ? usd(r.cost_usd / priced, 4) : "—"}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// --- tokens -----------------------------------------------------------------

export function TokenSplit({ stats }: { stats: SpendStats }) {
  const t = useT();
  // Cached input and cache writes are parts of the input count, not additions
  // to it; reasoning is part of the output.
  const uncached = Math.max(0, stats.input_tokens - stats.cached_input_tokens - stats.cache_write_tokens);
  const parts = [
    { label: t("spend.tokCached"), value: stats.cached_input_tokens, color: "var(--spend-2)" },
    { label: t("spend.tokUncached"), value: uncached, color: "var(--spend-4)" },
    { label: t("spend.tokWrite"), value: stats.cache_write_tokens, color: "var(--spend-3)" },
    { label: t("spend.tokOutput"), value: stats.output_tokens, color: "var(--spend-1)" },
  ];
  const total = parts.reduce((s, p) => s + p.value, 0);
  return (
    <>
      <div className="mt-1.5 mb-3 flex h-3 gap-0.5 overflow-hidden rounded">
        {total > 0 ? (
          parts.map((p) =>
            p.value > 0 ? (
              <span key={p.label} style={{ width: `${(p.value / total) * 100}%`, background: p.color }} title={p.label} />
            ) : null,
          )
        ) : (
          <span className="w-full bg-muted" />
        )}
      </div>
      <div className="grid grid-cols-[auto_1fr_auto] items-center gap-x-2.5 gap-y-1.5 text-[13px]">
        {parts.map((p) => (
          <React.Fragment key={p.label}>
            <span className="flex items-center gap-1.5">
              <i className="size-2.5 rounded-[3px]" style={{ background: p.color }} />
              {p.label}
            </span>
            <span className="text-xs text-muted-foreground tabular-nums">
              {total > 0 ? `${((p.value / total) * 100).toFixed(0)}%` : ""}
            </span>
            <span className="text-right tabular-nums">{formatNumber(p.value)}</span>
          </React.Fragment>
        ))}
        {stats.reasoning_tokens > 0 && (
          <span className="col-span-3 text-xs text-muted-foreground">
            {t("spend.reasoningAside", { count: formatNumber(stats.reasoning_tokens) })}
          </span>
        )}
      </div>
    </>
  );
}

// --- budgets ----------------------------------------------------------------

export function Budgets({ keys }: { keys: APIKey[] }) {
  const t = useT();
  const capped = keys.filter((k) => k.budget_usd !== null && k.budget_usd > 0);
  if (capped.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("spend.noBudgets")}</p>;
  }
  return (
    <div className="divide-y">
      {capped.map((k) => {
        const spent = k.spent_usd ?? 0;
        const share = Math.min(1, spent / (k.budget_usd ?? 1));
        return (
          <div key={k.id} className="py-2.5 first:pt-0 last:pb-0">
            <div className="flex justify-between gap-2 text-[13px]">
              <span className="truncate">{k.name}</span>
              <span className="tabular-nums">
                {usd(spent)} / {usd(k.budget_usd ?? 0)}
              </span>
            </div>
            <div className="my-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
              <div
                className={cn("h-full rounded-full", share >= 0.8 ? "bg-warning" : "bg-brand")}
                style={{ width: `${share * 100}%` }}
              />
            </div>
            <p className="text-xs text-muted-foreground">
              {k.budget_resets_at
                ? t("spend.resetsAt", { when: formatTime(k.budget_resets_at) })
                : t("spend.manualReset")}
            </p>
          </div>
        );
      })}
    </div>
  );
}
