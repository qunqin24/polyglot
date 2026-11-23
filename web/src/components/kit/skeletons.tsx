import * as React from "react";
import { Skeleton } from "@/components/ui/skeleton";
import { useDelayedFlag } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

// Shapes for the places the layout is known before the data is. Line heights
// follow the text they stand in for (12px for 14px text) and widths vary a
// little so the block does not read as a ruler.

const widths = ["w-3/4", "w-1/2", "w-2/3", "w-5/6", "w-2/5", "w-3/5"];
const pick = (i: number) => widths[i % widths.length];

/** One line of text. */
export function SkeletonLine({ className }: { className?: string }) {
  return <Skeleton className={cn("h-3 rounded-sm", className)} />;
}

/** Rows of a table: a leading line, a few columns, a trailing pill. */
export function TableRowsSkeleton({ rows = 6, columns = 4 }: { rows?: number; columns?: number }) {
  return (
    <div className="divide-y">
      {Array.from({ length: rows }, (_, r) => (
        <div key={r} className="flex h-14 items-center gap-6 px-5">
          <SkeletonLine className="w-40 shrink-0" />
          {Array.from({ length: columns }, (_, c) => (
            <SkeletonLine key={c} className={cn("flex-1", pick(r + c))} />
          ))}
          <Skeleton className="h-5 w-16 shrink-0 rounded-md" />
        </div>
      ))}
    </div>
  );
}

/** One model or price card, the size of the real one in the card grids. */
export function CardSkeleton({ metrics = 6 }: { metrics?: number }) {
  return (
    <div className="flex flex-col rounded-xl border bg-card p-4 shadow-xs">
      <div className="flex items-start gap-3">
        <Skeleton className="size-8 shrink-0 rounded-full" />
        <div className="min-w-0 flex-1 space-y-2 pt-1">
          <SkeletonLine className="w-3/4" />
          <SkeletonLine className="w-1/3" />
        </div>
      </div>
      <div className="mt-3 flex gap-1.5">
        <Skeleton className="h-5 w-14 rounded-md" />
        <Skeleton className="h-5 w-10 rounded-md" />
      </div>
      <div className="mt-4 grid grid-cols-3 gap-x-3 gap-y-3 border-t pt-3">
        {Array.from({ length: metrics }, (_, i) => (
          <div key={i} className="space-y-2">
            <SkeletonLine className="h-2.5 w-3/4" />
            <SkeletonLine className="w-1/2" />
          </div>
        ))}
      </div>
    </div>
  );
}

export function CardGridSkeleton({ count = 8, metrics }: { count?: number; metrics?: number }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
      {Array.from({ length: count }, (_, i) => (
        <CardSkeleton key={i} metrics={metrics} />
      ))}
    </div>
  );
}

/** The value of a stat card (24px figure). */
export function StatValueSkeleton() {
  // Same height as the 24px figure's line box, so the card does not resize.
  return (
    <div className="mt-1.5 flex h-8 items-center">
      <Skeleton className="h-6 w-24" />
    </div>
  );
}

/** A block the size of a chart or timeline. */
export function BlockSkeleton({ className }: { className?: string }) {
  return <Skeleton className={cn("w-full", className)} />;
}

/** Settings form: a label and a field, repeated. */
export function FormSkeleton({ fields = 5 }: { fields?: number }) {
  return (
    <div className="space-y-5">
      {Array.from({ length: fields }, (_, i) => (
        <div key={i} className="space-y-2">
          <SkeletonLine className="h-3 w-28" />
          <Skeleton className="h-9 w-full rounded-md" />
        </div>
      ))}
    </div>
  );
}

/** Rows inside a dialog or a picker: a checkbox or avatar and one line. */
export function ListRowsSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <div className="space-y-3">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex items-center gap-3">
          <Skeleton className="size-4 shrink-0 rounded-sm" />
          <SkeletonLine className={cn("h-3.5", pick(i))} />
        </div>
      ))}
    </div>
  );
}

/**
 * Shows `skeleton` while `loading`, and the children once the data is in.
 *
 * - The skeleton's space is reserved at once but painted only after 300 ms,
 *   so a fast answer shows nothing and the layout never moves.
 * - Once painted it stays for at least 500 ms (useDelayedFlag).
 * - Rows then fade in from the top, 200 ms each, 40 ms apart, when a
 *   skeleton was on screen.
 * - The region says aria-busy and announces "loading" politely.
 */
export function LoadingGate({
  loading,
  skeleton,
  children,
  label,
}: {
  loading: boolean;
  skeleton: React.ReactNode;
  children: React.ReactNode;
  label?: string;
}) {
  const t = useT();
  const painted = useDelayedFlag(loading);
  const wasPainted = React.useRef(false);
  if (painted) wasPainted.current = true;
  if (loading || painted) {
    return (
      <div role="status" aria-busy="true" className={cn(!painted && "invisible")}>
        <span className="sr-only">{label ?? t("common.loading")}</span>
        {skeleton}
      </div>
    );
  }
  return wasPainted.current ? <div className="contents reveal-rows">{children}</div> : <>{children}</>;
}
