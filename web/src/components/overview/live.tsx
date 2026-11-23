import * as React from "react";
import { Pause, Play } from "lucide-react";
import type { LiveEvent, LiveRequest } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { modelLabel } from "@/lib/model-name";
import { cn, formatDuration } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { usd } from "./spend-format";

/**
 * "What is happening": the last minute of requests as capsules in lanes,
 * coloured like the spend chart below, and the ones still writing.
 *
 * It reads /api/live, a server-sent event stream. The connection is opened
 * when the Overview mounts and closed when it unmounts, so no other page pays
 * for it; EventSource reconnects by itself and the snapshot sent on every
 * (re)connect rebuilds the picture.
 */

const WINDOW_MS = 60_000;
const LANES = 7;
const LANE_H = 19;
const GAP_MS = 900;

type Tracked = LiveRequest & { lane: number };

export function LivePanel({
  colorFor,
  names,
}: {
  /** The colour a model has in the spend chart, so both read as one picture. */
  colorFor: (model: string) => string;
  /** Catalog display names by model id. */
  names: Record<string, string>;
}) {
  const t = useT();
  const [paused, setPaused] = React.useState(false);
  const [connected, setConnected] = React.useState(false);
  const requests = React.useRef(new Map<string, Tracked>());
  // The server's clock and this browser's can disagree; positions are drawn
  // against the server's. The smallest gap seen between "the server says this
  // started" and "it arrived here" is the skew plus the shortest delivery.
  const skew = React.useRef<number | null>(null);
  const [, setFrame] = React.useState(0);
  const pausedAt = React.useRef(0);

  const serverNow = React.useCallback(
    () => (paused ? pausedAt.current : Date.now() - (skew.current ?? 0)),
    [paused],
  );

  React.useEffect(() => {
    const es = new EventSource("/api/live");
    const lanes = () => {
      const busy = new Array<number>(LANES).fill(-Infinity);
      for (const r of requests.current.values()) {
        if (r.lane >= 0) busy[r.lane] = Math.max(busy[r.lane], r.ended_at ?? Infinity);
      }
      return busy;
    };
    const place = (r: LiveRequest): Tracked => {
      const existing = requests.current.get(r.id);
      if (existing) return { ...existing, ...r };
      const busy = lanes();
      // The first lane free by the time this one started; none free means it
      // is left out of the picture rather than drawn on top of another.
      const lane = busy.findIndex((end) => end + GAP_MS <= r.started_at);
      return { ...r, lane };
    };
    const learn = (r: LiveRequest) => {
      const d = Date.now() - r.started_at;
      if (skew.current === null || d < skew.current) skew.current = d;
    };
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);
    es.onmessage = (msg) => {
      let ev: LiveEvent;
      try {
        ev = JSON.parse(String(msg.data)) as LiveEvent;
      } catch {
        return;
      }
      const map = requests.current;
      switch (ev.type) {
        case "snapshot":
          map.clear();
          for (const r of [...ev.requests].sort((a, b) => a.started_at - b.started_at)) {
            learn(r);
            map.set(r.id, place(r));
          }
          break;
        case "start":
        case "end":
          if (ev.type === "start") learn(ev.request);
          map.set(ev.request.id, place(ev.request));
          break;
        case "first_token": {
          const r = map.get(ev.id);
          if (r) map.set(ev.id, { ...r, first_token_at: ev.at });
          break;
        }
        case "progress": {
          const r = map.get(ev.id);
          if (r) map.set(ev.id, { ...r, output_tokens: ev.output_tokens });
          break;
        }
      }
    };
    return () => es.close();
  }, []);

  // Redraw every frame while running; once a second for reduced motion.
  React.useEffect(() => {
    if (paused) return;
    const reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    let raf = 0;
    let last = 0;
    const tick = (ts: number) => {
      if (!reduce || ts - last > 1000) {
        last = ts;
        const cutoff = Date.now() - (skew.current ?? 0) - WINDOW_MS;
        for (const [id, r] of requests.current) {
          if (r.ended_at !== null && r.ended_at < cutoff) requests.current.delete(id);
        }
        setFrame((f) => f + 1);
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [paused]);

  const now = serverNow();
  const all = [...requests.current.values()];
  const running = all.filter((r) => r.ended_at === null);
  const writing = running.filter((r) => r.first_token_at !== null).sort((a, b) => a.started_at - b.started_at);
  const lastMinute = all.filter((r) => r.ended_at !== null && r.ended_at > now - 60_000);
  const minuteCost = lastMinute.reduce((s, r) => s + (r.cost_usd ?? 0), 0);
  const rps = all.filter((r) => r.started_at > now - 10_000).length / 10;
  const recent = all.filter((r) => r.ended_at !== null && r.ended_at > now - 10_000);
  const tps = recent.reduce((s, r) => s + r.output_tokens, 0) / 10;

  const label = (m: string) => modelLabel(m, names[m]);

  return (
    <section className="mb-7" aria-label={t("live.title")}>
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <h2 className="m-0 text-sm font-semibold">{t("live.title")}</h2>
        <span className="flex items-center gap-1.5 text-xs text-muted-foreground" aria-live="polite">
          <i
            className={cn(
              "size-[7px] rounded-full",
              paused || !connected ? "bg-muted-foreground" : "bg-success live-ring",
            )}
          />
          {paused ? t("live.paused") : connected ? t("live.state") : t("live.connecting")}
        </span>
        <span className="flex-1" />
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            pausedAt.current = Date.now() - (skew.current ?? 0);
            setPaused((p) => !p);
          }}
        >
          {paused ? <Play /> : <Pause />} {paused ? t("live.resume") : t("live.pause")}
        </Button>
      </div>

      <div className="grid overflow-hidden rounded-xl border bg-card lg:grid-cols-[1fr_300px]">
        <div className="min-w-0 px-[18px] pt-4 pb-3">
          <div className="mb-3.5 grid grid-cols-2 gap-x-9 gap-y-2 sm:grid-cols-4 sm:justify-start">
            <Meter label={t("live.minuteCost")} value={usd(minuteCost, 2)} brand />
            <Meter label={t("live.rps")} value={rps.toFixed(1)} />
            <Meter label={t("live.writing")} value={String(writing.length)} />
            <Meter label={t("live.tps")} value={Math.round(tps).toLocaleString()} unit="tok/s" title={t("live.tpsHint")} />
          </div>

          <div
            className="relative h-[150px] overflow-hidden rounded-lg"
            role="img"
            aria-label={t("live.riverLabel")}
            style={{
              background:
                "linear-gradient(90deg, var(--border) 1px, transparent 1px) 0 0 / calc(100% / 6) 100%, color-mix(in oklch, var(--muted) 55%, transparent)",
            }}
          >
            {all.length === 0 && (
              <p className="absolute inset-0 m-0 flex items-center justify-center text-xs text-muted-foreground">
                {connected ? t("live.quiet") : t("live.connecting")}
              </p>
            )}
            {all.map((r) =>
              r.lane < 0 ? null : (
                <Capsule key={r.id} r={r} now={now} color={colorFor(r.model)} label={label(r.model)} />
              ),
            )}
            <div className="pointer-events-none absolute inset-y-0 left-0 w-12 bg-gradient-to-r from-card to-transparent" />
            <div className="absolute inset-y-0 right-0 w-0.5 bg-brand opacity-50" />
          </div>
          <div className="mt-1.5 flex justify-between text-[11px] text-muted-foreground">
            <span>{t("live.ago60")}</span>
            <span>40</span>
            <span>20</span>
            <span>{t("live.now")}</span>
          </div>
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
            <span>{t("live.keyColor")}</span>
            <span className="flex items-center gap-1.5">
              <i className="h-2 w-[18px] rounded bg-[var(--spend-2)] opacity-45" />
              {t("live.keyWaiting")}
            </span>
            <span className="flex items-center gap-1.5">
              <i className="h-2 w-[18px] rounded bg-[var(--spend-2)] shadow-[inset_-3px_0_0_color-mix(in_oklch,var(--background)_70%,transparent)]" />
              {t("live.keyWriting")}
            </span>
            <span className="flex items-center gap-1.5">
              <i className="h-2 w-[18px] rounded shadow-[inset_0_0_0_1.5px_var(--destructive)]" />
              {t("live.keyFailed")}
            </span>
          </div>
        </div>

        <aside className="relative flex min-w-0 flex-col border-t pt-4 lg:border-t-0 lg:border-l">
          <h3 className="m-0 mb-2 flex justify-between px-4 text-xs font-medium text-muted-foreground">
            <span>{t("live.writingList")}</span>
            <span>{t("live.elapsed")}</span>
          </h3>
          <ScrollFade>
            {writing.length === 0 ? (
              <p className="m-0 py-6 text-center text-xs text-muted-foreground">{t("live.noneWriting")}</p>
            ) : (
              writing.map((r) => (
                <div
                  key={r.id}
                  className="grid grid-cols-[8px_1fr_auto] items-center gap-x-2.5 gap-y-0.5 border-t py-[7px] first:border-t-0"
                >
                  <span className="size-2 rounded-full" style={{ background: colorFor(r.model) }} />
                  <span className="truncate text-[13px] font-medium" title={r.model}>
                    {label(r.model)}
                  </span>
                  <span className="text-right text-xs tabular-nums">{formatDuration(Math.max(0, now - r.started_at))}</span>
                  <span className="col-span-2 col-start-2 flex justify-between text-[11px] text-muted-foreground tabular-nums">
                    <span className="truncate">{r.provider}</span>
                    <span>{r.output_tokens > 0 ? `${r.output_tokens.toLocaleString()} tok` : r.api_key}</span>
                  </span>
                </div>
              ))
            )}
          </ScrollFade>
        </aside>
      </div>
    </section>
  );
}

function Meter({
  label,
  value,
  unit,
  brand,
  title,
}: {
  label: string;
  value: string;
  unit?: string;
  brand?: boolean;
  title?: string;
}) {
  return (
    <div title={title}>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={cn("text-[22px] leading-tight font-semibold tracking-tight tabular-nums", brand && "text-brand")}>
        {value}
        {unit && <small className="ml-1 text-xs font-medium text-muted-foreground">{unit}</small>}
      </div>
    </div>
  );
}

function Capsule({ r, now, color, label }: { r: Tracked; now: number; color: string; label: string }) {
  const t = useT();
  const from = now - WINDOW_MS;
  const start = Math.max(r.started_at, from);
  const end = Math.min(r.ended_at ?? now, now);
  const left = ((start - from) / WINDOW_MS) * 100;
  const width = Math.max(0.6, ((end - start) / WINDOW_MS) * 100);
  const running = r.ended_at === null;
  // A client hanging up is not the upstream failing: drawn grey, not red.
  const cancelled = r.status === "error" && r.error_type === "client_disconnect";
  const failed = r.status === "error" && !cancelled;
  const waiting = running && r.first_token_at === null;
  const title = [
    label,
    r.model,
    r.provider,
    cancelled
      ? t("live.tipCancelled")
      : failed
      ? t("live.tipFailed", { code: String(r.status_code || r.error_type) })
      : r.first_token_at !== null
        ? t("live.tipTtft", { value: formatDuration(r.first_token_at - r.started_at) })
        : t("live.keyWaiting"),
    r.output_tokens > 0 ? `${r.output_tokens.toLocaleString()} tok` : "",
    r.cost_usd !== null ? usd(r.cost_usd) : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div
      className={cn("absolute h-[11px] min-w-1.5 rounded-full", running && !waiting && "live-cap-run")}
      title={title}
      style={{
        top: 9 + r.lane * LANE_H,
        left: `${left}%`,
        width: `${width}%`,
        background: failed || cancelled ? "transparent" : color,
        color,
        opacity: waiting ? 0.45 : 1,
        boxShadow: failed
          ? "inset 0 0 0 1.5px var(--destructive)"
          : cancelled
            ? "inset 0 0 0 1.5px var(--muted-foreground)"
            : undefined,
      }}
    />
  );
}

/** A list that scrolls inside its box with the scrollbar hidden; a fade at the
 *  bottom says there is more below. */
function ScrollFade({ children }: { children: React.ReactNode }) {
  const ref = React.useRef<HTMLDivElement>(null);
  const [more, setMore] = React.useState(false);
  const update = React.useCallback(() => {
    const el = ref.current;
    if (el) setMore(el.scrollTop + el.clientHeight < el.scrollHeight - 2);
  }, []);
  React.useEffect(update);
  return (
    <div className="relative min-h-[220px] flex-1 lg:min-h-0">
      <div
        ref={ref}
        onScroll={update}
        tabIndex={0}
        className="scrollbar-none absolute inset-0 overflow-y-auto overscroll-contain px-4 pb-3"
      >
        {children}
      </div>
      <div
        className={cn(
          "pointer-events-none absolute inset-x-0 bottom-0 h-7 bg-gradient-to-b from-transparent to-card transition-opacity duration-200",
          more ? "opacity-100" : "opacity-0",
        )}
      />
    </div>
  );
}
