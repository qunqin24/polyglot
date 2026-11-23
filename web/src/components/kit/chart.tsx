import { cn } from "@/lib/utils";

/**
 * Every chart in the WebUI is drawn here, by hand, in SVG. There is no chart
 * library and there should not be one: these are a handful of shapes, and a
 * dependency that ships a chart for every occasion would weigh more than the
 * rest of the frontend put together.
 *
 * Two rules hold across all of them:
 *
 *  - Colour comes from the `--chart-N` tokens, so light and dark mode are one
 *    definition rather than two, and a series keeps its colour between charts.
 *  - A gap in the data is drawn as a gap. Nothing here interpolates across a
 *    bucket that carried no traffic, because a smooth line over a quiet hour
 *    claims requests nobody made.
 */

export const SERIES_COLORS = [
  "var(--color-chart-1)",
  "var(--color-chart-2)",
  "var(--color-chart-3)",
  "var(--color-chart-4)",
  "var(--color-chart-5)",
  "var(--color-chart-6)",
] as const;

export function seriesColor(i: number): string {
  return SERIES_COLORS[i % SERIES_COLORS.length];
}

function points(values: number[], w: number, h: number, max: number): string {
  if (values.length === 0) return "";
  const step = values.length > 1 ? w / (values.length - 1) : 0;
  const scale = max > 0 ? h / max : 0;
  return values.map((v, i) => `${(i * step).toFixed(2)},${(h - v * scale).toFixed(2)}`).join(" ");
}

/** The largest value across every series, never below 1 so an all-zero chart
 *  draws a flat line on the floor instead of dividing by nothing. */
function ceiling(...series: (number | null)[][]): number {
  let max = 0;
  for (const s of series) for (const v of s) if (v !== null && v > max) max = v;
  return max > 0 ? max : 1;
}

/**
 * Splits a series into the runs that actually have a value, one point-string
 * per run. A null is a bucket where the measurement does not exist — a quiet
 * minute has no median latency — and drawing it as zero would claim the
 * gateway got fast while nobody was calling it. So the line breaks instead.
 */
function segments(values: (number | null)[], w: number, h: number, max: number): string[] {
  const step = values.length > 1 ? w / (values.length - 1) : 0;
  const scale = max > 0 ? h / max : 0;
  const out: string[] = [];
  let run: string[] = [];
  values.forEach((v, i) => {
    if (v === null) {
      if (run.length > 0) out.push(run.join(" "));
      run = [];
      return;
    }
    run.push(`${(i * step).toFixed(2)},${(h - v * scale).toFixed(2)}`);
  });
  if (run.length > 0) out.push(run.join(" "));
  // A run of one point draws nothing as a polyline, so give it width.
  return out.map((r) => (r.includes(" ") ? r : `${r} ${r.split(",")[0]},${r.split(",")[1]}`));
}

// --- sparkline --------------------------------------------------------------

/** A line small enough to sit under a number, with no axes and no labels. It
 *  says which way the number has been moving and nothing else. */
export function Sparkline({
  values,
  color = "var(--color-chart-1)",
  className,
}: {
  values: number[];
  color?: string;
  className?: string;
}) {
  if (values.length < 2) return <div className={cn("h-4", className)} />;
  const max = ceiling(values);
  return (
    <svg
      viewBox="0 0 64 16"
      preserveAspectRatio="none"
      className={cn("h-4 w-full", className)}
      aria-hidden="true"
    >
      <polyline
        points={points(values, 64, 14, max)}
        fill="none"
        stroke={color}
        strokeWidth={1.25}
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}

// --- request timeline -------------------------------------------------------

/** Total requests as an area, failures filled underneath it. One chart rather
 *  than two, because the question is always what share of that traffic broke. */
export function TimelineChart({
  total,
  errors,
  height = 96,
  labels,
}: {
  total: number[];
  errors: number[];
  height?: number;
  labels?: [string, string];
}) {
  const w = 640;
  const h = height;
  const max = ceiling(total);
  const line = points(total, w, h, max);
  const errLine = points(errors, w, h, max);
  const hasErrors = errors.some((e) => e > 0);
  return (
    <svg
      viewBox={`0 0 ${w} ${h}`}
      preserveAspectRatio="none"
      className="w-full"
      style={{ height }}
      role="img"
      aria-label={labels ? `${labels[0]} · ${labels[1]}` : undefined}
    >
      <polygon points={`${line} ${w},${h} 0,${h}`} fill="var(--color-chart-1)" fillOpacity={0.14} />
      <polyline
        points={line}
        fill="none"
        stroke="var(--color-chart-1)"
        strokeWidth={1.5}
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
      {hasErrors && (
        <polygon points={`${errLine} ${w},${h} 0,${h}`} fill="var(--color-destructive)" fillOpacity={0.5} />
      )}
    </svg>
  );
}

// --- percentile bands -------------------------------------------------------

/** p50, p95 and p99 over time. The shaded band between the outer two is the
 *  spread, which is the part an average would have hidden. A bucket that
 *  carried no requests is a null, and the lines break across it rather than
 *  dipping to the floor. */
export function PercentileChart({
  p50,
  p95,
  p99,
  height = 110,
}: {
  p50: (number | null)[];
  p95: (number | null)[];
  p99: (number | null)[];
  height?: number;
}) {
  const w = 640;
  const h = height;
  const max = ceiling(p99, p95, p50);
  const bands = segments(p99, w, h, max).map((top, i) => {
    const floor = segments(p50, w, h, max)[i];
    return floor ? `${top} ${floor.split(" ").reverse().join(" ")}` : null;
  });
  const line = (values: (number | null)[], stroke: string, width: number, dash?: string) =>
    segments(values, w, h, max).map((pts, i) => (
      <polyline
        key={`${stroke}-${i}`}
        points={pts}
        fill="none"
        stroke={stroke}
        strokeWidth={width}
        strokeDasharray={dash}
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    ));
  return (
    <svg
      viewBox={`0 0 ${w} ${h}`}
      preserveAspectRatio="none"
      className="w-full"
      style={{ height }}
      aria-hidden="true"
    >
      {bands.map((pts, i) =>
        pts ? <polygon key={i} points={pts} fill="var(--color-chart-1)" fillOpacity={0.12} /> : null,
      )}
      {line(p50, "var(--color-muted-foreground)", 1.25)}
      {line(p95, "var(--color-chart-1)", 1.5)}
      {line(p99, "var(--color-warning)", 1.25, "4 3")}
    </svg>
  );
}

// --- histogram --------------------------------------------------------------

/**
 * Bars over buckets someone else decided. Two things it does that a plain bar
 * chart does not:
 *
 *  - An empty bucket still draws its slot, as a faint track. Drawing nothing
 *    there leaves the filled bars floating with no scale behind them, and a
 *    reader cannot tell whether the gap is one empty bucket or six.
 *  - The last bar has no upper bound and is coloured apart, because "slower
 *    than every threshold" is a different fact from "in the tail".
 *
 * `range` is the full span the bar covers and goes in the tooltip; `label` is
 * the short axis tick under it. They differ because only every other tick fits
 * across a card, so the tick alone cannot say what a bar contains.
 */
export function Histogram({
  bars,
  height = 140,
}: {
  bars: { label: string; range: string; count: number; overflow?: boolean }[];
  height?: number;
}) {
  const max = ceiling(bars.map((b) => b.count));
  // Ticks thin out only when they would collide, and the last one is always
  // kept: it is the bar with no upper bound, and it is the one worth reading.
  const showTick = (i: number) =>
    bars.length <= 7 || i % 2 === 0 || i === bars.length - 1;
  return (
    <div>
      <div className="flex items-end gap-1" style={{ height }}>
        {bars.map((b, i) => (
          <div
            key={i}
            className="flex h-full flex-1 flex-col justify-end rounded-[2px] bg-muted/50"
            title={`${b.range} · ${b.count}`}
          >
            <div
              className="rounded-[2px]"
              style={{
                height: b.count > 0 ? Math.max(3, (b.count / max) * height) : 0,
                background: b.overflow ? "var(--color-warning)" : "var(--color-chart-1)",
                opacity: b.overflow ? 0.9 : 0.4 + 0.6 * (b.count / max),
              }}
            />
          </div>
        ))}
      </div>
      <div className="mt-1.5 flex gap-1">
        {bars.map((b, i) => (
          <div
            key={i}
            className="flex-1 text-center text-[10px] whitespace-nowrap text-muted-foreground"
          >
            {showTick(i) ? b.label : ""}
          </div>
        ))}
      </div>
    </div>
  );
}

// --- stacked bars -----------------------------------------------------------

/** Bands stacked per bucket. The bands are decided once for the whole window,
 *  so a colour means the same thing at both ends of the chart. */
export function StackedBars({
  starts,
  stacks,
  height = 92,
  format,
}: {
  starts: number[];
  stacks: { label: string; points: number[] }[];
  height?: number;
  format: (v: number) => string;
}) {
  const totals = starts.map((_, i) => stacks.reduce((sum, s) => sum + (s.points[i] ?? 0), 0));
  const max = ceiling(totals);
  return (
    <div className="flex items-end gap-[2px]" style={{ height }}>
      {starts.map((start, i) => (
        <div
          key={start}
          className="flex flex-1 flex-col justify-end"
          title={format(totals[i])}
        >
          {stacks.map((s, si) => {
            const v = s.points[i] ?? 0;
            if (v <= 0) return null;
            return (
              <div
                key={si}
                style={{
                  height: Math.max(1, (v / max) * height),
                  background: seriesColor(si),
                }}
              />
            );
          })}
        </div>
      ))}
    </div>
  );
}

/** A single bar split into parts of a whole — token composition, and nothing
 *  where the parts do not belong to the same total. */
export function CompositionBar({
  parts,
  className,
}: {
  parts: { label: string; value: number; color: string }[];
  className?: string;
}) {
  const total = parts.reduce((s, p) => s + p.value, 0);
  if (total <= 0) {
    return <div className={cn("h-3 rounded-sm bg-muted", className)} />;
  }
  return (
    <div className={cn("flex h-3 overflow-hidden rounded-sm", className)}>
      {parts.map((p) => (
        <div
          key={p.label}
          style={{ width: `${(p.value / total) * 100}%`, background: p.color }}
          title={`${p.label} · ${p.value}`}
        />
      ))}
    </div>
  );
}

// --- conversion matrix ------------------------------------------------------

/** The client protocol down the side, the upstream protocol across the top,
 *  and how many requests took each route. The diagonal is the same protocol in
 *  and out, which still went through canonical. */
export function HeatMatrix({
  rows,
  columns,
  value,
  label,
  format,
}: {
  rows: string[];
  columns: string[];
  value: (row: string, column: string) => number;
  label: (protocol: string) => string;
  format: (n: number) => string;
}) {
  let max = 0;
  for (const r of rows) for (const c of columns) max = Math.max(max, value(r, c));
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-separate border-spacing-[2px] text-xs">
        <thead>
          <tr>
            <th />
            {columns.map((c) => (
              <th key={c} className="pb-1 text-center text-[10px] font-normal text-muted-foreground">
                {label(c)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r}>
              <th className="pr-2 text-right text-[10px] font-normal whitespace-nowrap text-muted-foreground">
                {label(r)}
              </th>
              {columns.map((c) => {
                const n = value(r, c);
                const share = max > 0 ? n / max : 0;
                return (
                  <td
                    key={c}
                    className="rounded-[3px] py-1.5 text-center tabular-nums"
                    style={{
                      background:
                        n > 0 ? "var(--color-chart-1)" : "color-mix(in oklab, var(--color-muted) 60%, transparent)",
                      opacity: n > 0 ? 0.18 + 0.82 * share : 1,
                      color: share > 0.55 ? "var(--color-primary-foreground)" : "var(--color-foreground)",
                    }}
                  >
                    {n > 0 ? format(n) : "·"}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// --- flow -------------------------------------------------------------------

/** Protocol on the left, provider on the right, a band per route with its
 *  width set by the request count. Not a general Sankey — one hop, no cycles,
 *  which is all the routing table can produce. */
export function FlowChart({
  links,
  height = 168,
}: {
  links: { from: string; to: string; count: number }[];
  height?: number;
}) {
  const w = 320;
  const pad = 6;
  const total = links.reduce((s, l) => s + l.count, 0);
  if (total === 0) return null;

  const sum = (names: string[], pick: (l: (typeof links)[number]) => string) =>
    names.map((n) => ({ name: n, count: links.filter((l) => pick(l) === n).reduce((s, l) => s + l.count, 0) }));
  const froms = sum([...new Set(links.map((l) => l.from))], (l) => l.from);
  const tos = sum([...new Set(links.map((l) => l.to))], (l) => l.to);

  const lay = (nodes: { name: string; count: number }[]) => {
    const gaps = Math.max(0, nodes.length - 1) * pad;
    const usable = height - gaps;
    let y = 0;
    return new Map(
      nodes.map((n) => {
        const h = Math.max(3, (n.count / total) * usable);
        const box = { y, h };
        y += h + pad;
        return [n.name, box];
      }),
    );
  };
  const left = lay(froms);
  const right = lay(tos);
  const leftCursor = new Map<string, number>();
  const rightCursor = new Map<string, number>();

  return (
    <svg viewBox={`0 0 ${w} ${height}`} className="w-full" style={{ height }} aria-hidden="true">
      {links.map((l, i) => {
        const a = left.get(l.from);
        const b = right.get(l.to);
        if (!a || !b) return null;
        const fromNode = froms.find((f) => f.name === l.from)!;
        const toNode = tos.find((f) => f.name === l.to)!;
        const ah = (l.count / fromNode.count) * a.h;
        const bh = (l.count / toNode.count) * b.h;
        const ay = a.y + (leftCursor.get(l.from) ?? 0);
        const by = b.y + (rightCursor.get(l.to) ?? 0);
        leftCursor.set(l.from, (leftCursor.get(l.from) ?? 0) + ah);
        rightCursor.set(l.to, (rightCursor.get(l.to) ?? 0) + bh);
        return (
          <path
            key={i}
            d={`M8,${ay} C${w / 2},${ay} ${w / 2},${by} ${w - 8},${by}
                L${w - 8},${by + bh} C${w / 2},${by + bh} ${w / 2},${ay + ah} 8,${ay + ah} Z`}
            fill={seriesColor(froms.findIndex((f) => f.name === l.from))}
            fillOpacity={0.25}
          />
        );
      })}
      {froms.map((f, i) => {
        const box = left.get(f.name)!;
        return <rect key={f.name} x={0} y={box.y} width={8} height={box.h} rx={2} fill={seriesColor(i)} />;
      })}
      {tos.map((tnode) => {
        const box = right.get(tnode.name)!;
        return (
          <rect
            key={tnode.name}
            x={w - 8}
            y={box.y}
            width={8}
            height={box.h}
            rx={2}
            fill="var(--color-muted-foreground)"
          />
        );
      })}
    </svg>
  );
}

// --- treemap ----------------------------------------------------------------

/** Rectangles in proportion to a value, laid out by slicing the remaining
 *  space along its longer side. Not the squarest possible layout, but stable:
 *  the same input always produces the same picture. */
export function Treemap({
  items,
  height = 148,
  label,
}: {
  items: { key: string; value: number; title: string; subtitle?: string }[];
  height?: number;
  label?: (item: { key: string; value: number }) => string;
}) {
  const total = items.reduce((s, it) => s + it.value, 0);
  if (total <= 0) return null;

  const boxes: { key: string; x: number; y: number; w: number; h: number; item: (typeof items)[number] }[] = [];
  let x = 0;
  let y = 0;
  let w = 100;
  let h = 100;
  let remaining = total;
  items.forEach((item, i) => {
    if (i === items.length - 1) {
      boxes.push({ key: item.key, x, y, w, h, item });
      return;
    }
    const share = item.value / remaining;
    if (w >= h) {
      const cut = w * share;
      boxes.push({ key: item.key, x, y, w: cut, h, item });
      x += cut;
      w -= cut;
    } else {
      const cut = h * share;
      boxes.push({ key: item.key, x, y, w, h: cut, item });
      y += cut;
      h -= cut;
    }
    remaining -= item.value;
  });

  return (
    <div className="relative w-full overflow-hidden rounded-md" style={{ height }}>
      {boxes.map((b, i) => (
        <div
          key={b.key}
          className="absolute overflow-hidden p-1.5"
          style={{
            left: `${b.x}%`,
            top: `${b.y}%`,
            width: `${b.w}%`,
            height: `${b.h}%`,
            background: seriesColor(i),
            opacity: 0.85 - i * 0.07,
            outline: "1px solid var(--color-background)",
          }}
          title={label ? label(b.item) : b.item.title}
        >
          <p className="truncate text-[10px] leading-tight font-medium text-white">{b.item.title}</p>
          {b.item.subtitle && b.h > 22 && (
            <p className="truncate text-[10px] leading-tight text-white/80">{b.item.subtitle}</p>
          )}
        </div>
      ))}
    </div>
  );
}

// --- scatter ----------------------------------------------------------------

/** One dot per model: how long it took to say anything against how fast it
 *  then spoke. Both axes are measurements that only exist for a streamed
 *  reply, so a model that never streamed has no dot rather than a dot at zero. */
export function Scatter({
  dots,
  height = 150,
  xLabel,
  yLabel,
}: {
  dots: { key: string; x: number; y: number; size: number; title: string }[];
  height?: number;
  xLabel: string;
  yLabel: string;
}) {
  const w = 320;
  const padL = 26;
  const padB = 18;
  const maxX = ceiling(dots.map((d) => d.x));
  const maxY = ceiling(dots.map((d) => d.y));
  const maxSize = ceiling(dots.map((d) => d.size));
  return (
    <svg viewBox={`0 0 ${w} ${height}`} className="w-full" style={{ height }} aria-hidden="true">
      <line x1={padL} y1={height - padB} x2={w - 4} y2={height - padB} stroke="var(--color-border)" />
      <line x1={padL} y1={6} x2={padL} y2={height - padB} stroke="var(--color-border)" />
      {dots.map((d, i) => (
        <circle
          key={d.key}
          cx={padL + (d.x / maxX) * (w - padL - 12)}
          cy={height - padB - (d.y / maxY) * (height - padB - 12)}
          r={4 + 7 * Math.sqrt(d.size / maxSize)}
          fill={seriesColor(i)}
          fillOpacity={0.55}
        >
          <title>{d.title}</title>
        </circle>
      ))}
      <text x={padL + 2} y={12} fontSize={9} fill="var(--color-muted-foreground)">
        {yLabel}
      </text>
      <text x={w - 4} y={height - 4} fontSize={9} textAnchor="end" fill="var(--color-muted-foreground)">
        {xLabel}
      </text>
    </svg>
  );
}

// --- ring -------------------------------------------------------------------

/** A budget against its ceiling. Warns before it stops anything, because the
 *  request that crosses the line has already been paid for. */
export function Ring({
  fraction,
  tone = "normal",
  size = 34,
}: {
  fraction: number | null;
  tone?: "normal" | "warn" | "over";
  size?: number;
}) {
  const r = 15;
  const circumference = 2 * Math.PI * r;
  const filled = fraction === null ? 0 : Math.min(1, Math.max(0, fraction)) * circumference;
  const stroke =
    tone === "over"
      ? "var(--color-destructive)"
      : tone === "warn"
        ? "var(--color-warning)"
        : "var(--color-success)";
  return (
    <svg viewBox="0 0 36 36" style={{ width: size, height: size }} className="shrink-0" aria-hidden="true">
      <circle cx={18} cy={18} r={r} fill="none" stroke="var(--color-border)" strokeWidth={4} />
      {fraction !== null && (
        <circle
          cx={18}
          cy={18}
          r={r}
          fill="none"
          stroke={stroke}
          strokeWidth={4}
          strokeLinecap="round"
          strokeDasharray={`${filled} ${circumference}`}
          transform="rotate(-90 18 18)"
        />
      )}
    </svg>
  );
}

// --- legend -----------------------------------------------------------------

export function Legend({ items }: { items: { label: string; color: string }[] }) {
  return (
    <div className="flex flex-wrap gap-x-3 gap-y-1">
      {items.map((it) => (
        <span key={it.label} className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <span className="size-2 rounded-[2px]" style={{ background: it.color }} />
          {it.label}
        </span>
      ))}
    </div>
  );
}
