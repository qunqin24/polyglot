import * as React from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useDelayedFlag } from "@/lib/hooks";
import { cn } from "@/lib/utils";

const reducedMotion = () =>
  typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

/**
 * An icon that is itself the action (refresh, sync). It starts turning the
 * moment the work starts, keeps going until the result is back, and then
 * finishes the lap it is on, so it never freezes at an odd angle.
 */
export function TurnIcon({
  busy,
  children,
  className,
}: {
  busy: boolean;
  children: React.ReactElement<{ className?: string; onAnimationIteration?: () => void }>;
  className?: string;
}) {
  const [turning, setTurning] = React.useState(false);
  const still = reducedMotion();

  React.useEffect(() => {
    if (busy) setTurning(true);
    else if (still) setTurning(false);
  }, [busy, still]);

  return React.cloneElement(children, {
    className: cn(children.props.className, className, turning && !still && "animate-lap"),
    onAnimationIteration: () => {
      if (!busy) setTurning(false);
    },
  });
}

type ButtonProps = React.ComponentProps<typeof Button>;

/**
 * A button that waits without changing size.
 *
 * - `busy` blocks a second click but leaves the button focusable: it is not
 *   `disabled`, and says `aria-busy` to a screen reader.
 * - With `icon`, the icon is swapped for a spinner in the same slot. With
 *   `iconIsAction`, the icon turns instead (see TurnIcon).
 * - Without an icon, the spinner hangs in the button's left padding, so the
 *   label and the button width stay where they were.
 * - The spinner appears after 300 ms and stays at least 500 ms; a faster
 *   request shows nothing.
 */
export function LoadingButton({
  busy = false,
  icon,
  iconIsAction = false,
  onClick,
  children,
  className,
  ...props
}: ButtonProps & { busy?: boolean; icon?: React.ReactElement<{ className?: string }>; iconIsAction?: boolean }) {
  const shown = useDelayedFlag(busy);
  const spinner = <Loader2 className="size-4 animate-spin" aria-hidden="true" />;

  let lead: React.ReactNode = null;
  if (icon && iconIsAction) lead = <TurnIcon busy={busy}>{icon}</TurnIcon>;
  else if (icon) lead = shown ? spinner : icon;
  else if (shown) {
    lead = (
      <span aria-hidden="true" className="relative -me-2 size-0">
        <Loader2 className="absolute -left-4 top-1/2 size-3 -translate-y-1/2 animate-spin" />
      </span>
    );
  }

  return (
    <Button
      {...props}
      aria-busy={busy || undefined}
      className={cn(busy && "cursor-progress", className)}
      onClick={(e) => {
        if (busy) {
          e.preventDefault();
          return;
        }
        onClick?.(e);
      }}
    >
      {lead}
      {children}
    </Button>
  );
}
