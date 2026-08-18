// The Polyglot mark: one body, two shapes. The left half is squared, the right
// half is round, and the channel between them is where conversion happens —
// the same payload, written in two dialects. Drawn on the same 64-unit grid as
// public/favicon.svg, so the tab icon and the UI never drift apart.
export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 64 64" className={className} aria-hidden="true">
      <path d="M28 16H18a6 6 0 0 0-6 6v20a6 6 0 0 0 6 6h10z" fill="currentColor" />
      <path d="M35 15a17 17 0 0 1 0 34z" fill="currentColor" />
    </svg>
  );
}
