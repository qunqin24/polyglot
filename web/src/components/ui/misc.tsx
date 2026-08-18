import * as React from "react";
import { Switch as SwitchPrimitive, Select as SelectPrimitive, Tabs as TabsPrimitive } from "radix-ui";
import { Check, ChevronDown, Loader2 } from "lucide-react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

// --- badge ------------------------------------------------------------------

const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs font-medium whitespace-nowrap",
  {
    variants: {
      variant: {
        default: "border-transparent bg-secondary text-secondary-foreground",
        outline: "border-border text-foreground",
        success: "border-transparent bg-[--color-success]/12 text-[--color-success]",
        destructive: "border-transparent bg-destructive/12 text-destructive",
        warning: "border-transparent bg-[--color-warning]/15 text-[--color-warning]",
        accent: "border-transparent bg-accent text-accent-foreground",
      },
    },
    defaultVariants: { variant: "default" },
  },
);

export function Badge({
  className,
  variant,
  ...props
}: React.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}

// --- switch -----------------------------------------------------------------

export function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        "peer inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors",
        "focus-visible:ring-[3px] focus-visible:ring-ring/40 outline-none disabled:cursor-not-allowed disabled:opacity-50",
        "data-[state=checked]:bg-primary data-[state=unchecked]:bg-input",
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb className="pointer-events-none block size-4 rounded-full bg-background shadow-sm transition-transform data-[state=checked]:translate-x-4 data-[state=unchecked]:translate-x-0" />
    </SwitchPrimitive.Root>
  );
}

// --- select -----------------------------------------------------------------

export function Select({
  value,
  onValueChange,
  placeholder,
  options,
  className,
  disabled,
}: {
  value: string;
  onValueChange: (v: string) => void;
  placeholder?: string;
  /** icon is decorative and shows in the list only: the trigger renders the
      selected item's text, and an action select is reset to its placeholder. */
  options: { value: string; label: string; hint?: string; icon?: React.ReactNode }[];
  className?: string;
  disabled?: boolean;
}) {
  const icons = options.some((o) => o.icon !== undefined);
  return (
    <SelectPrimitive.Root value={value} onValueChange={onValueChange} disabled={disabled}>
      <SelectPrimitive.Trigger
        className={cn(
          "flex h-9 w-full items-center justify-between gap-2 rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs",
          "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/25 outline-none",
          "disabled:cursor-not-allowed disabled:opacity-50 data-[placeholder]:text-muted-foreground/70",
          className,
        )}
      >
        {/* The trigger is a fixed height, so a label longer than the control
            has to clip rather than wrap — wrapping pushes the second line out
            of the box instead of making room for it. */}
        <span className="min-w-0 truncate text-left">
          <SelectPrimitive.Value placeholder={placeholder} />
        </span>
        <SelectPrimitive.Icon className="shrink-0">
          <ChevronDown className="size-4 opacity-50" />
        </SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>
      <SelectPrimitive.Portal>
        <SelectPrimitive.Content
          position="popper"
          sideOffset={4}
          className="z-50 max-h-72 min-w-[var(--radix-select-trigger-width)] overflow-hidden rounded-md border border-border bg-popover text-popover-foreground shadow-md animate-in-soft"
        >
          <SelectPrimitive.Viewport className="p-1">
            {options.map((o) => (
              <SelectPrimitive.Item
                key={o.value}
                value={o.value}
                className={cn(
                  "relative flex cursor-default select-none items-center gap-2 rounded-sm py-1.5 pr-2 text-sm outline-none",
                  "data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground",
                  icons ? "pl-2" : "pl-8",
                )}
              >
                {/* One slot on the left, for whichever of the two identifies
                    the row. An icon wins: a list of brands is picked out by
                    its marks, and the trigger already says what is selected —
                    a tick column that is always empty is just a gap. */}
                {icons ? (
                  (o.icon ?? <span className="size-4 shrink-0" />)
                ) : (
                  <span className="absolute left-2 flex size-4 items-center justify-center">
                    <SelectPrimitive.ItemIndicator>
                      <Check className="size-3.5" />
                    </SelectPrimitive.ItemIndicator>
                  </span>
                )}
                <SelectPrimitive.ItemText>{o.label}</SelectPrimitive.ItemText>
                {o.hint && <span className="ml-auto text-xs text-muted-foreground">{o.hint}</span>}
              </SelectPrimitive.Item>
            ))}
          </SelectPrimitive.Viewport>
        </SelectPrimitive.Content>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  );
}

// --- tabs -------------------------------------------------------------------

export const Tabs = TabsPrimitive.Root;

export function TabsList({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn("inline-flex h-9 items-center gap-1 rounded-lg bg-muted p-1", className)}
      {...props}
    />
  );
}

export function TabsTrigger({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        "inline-flex items-center justify-center rounded-md px-3 py-1 text-sm font-medium transition-colors outline-none",
        "data-[state=active]:bg-background data-[state=active]:shadow-xs text-muted-foreground data-[state=active]:text-foreground",
        className,
      )}
      {...props}
    />
  );
}

export const TabsContent = TabsPrimitive.Content;

// --- table ------------------------------------------------------------------

export function Table({ className, ...props }: React.ComponentProps<"table">) {
  return (
    <div className="w-full overflow-x-auto">
      <table className={cn("w-full caption-bottom text-sm", className)} {...props} />
    </div>
  );
}

export function Th({ className, ...props }: React.ComponentProps<"th">) {
  return (
    <th
      className={cn(
        "h-9 px-3 text-left align-middle text-xs font-medium text-muted-foreground whitespace-nowrap",
        className,
      )}
      {...props}
    />
  );
}

export function Td({ className, ...props }: React.ComponentProps<"td">) {
  return <td className={cn("px-3 py-2.5 align-middle", className)} {...props} />;
}

export function Tr({ className, ...props }: React.ComponentProps<"tr">) {
  return <tr className={cn("border-b border-border/60 last:border-0", className)} {...props} />;
}

// --- states -----------------------------------------------------------------

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cn("size-4 animate-spin", className)} />;
}

export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
}: {
  icon: React.ComponentType<{ className?: string }>;
  title: string;
  description: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 px-6 py-14 text-center">
      <div className="rounded-full bg-muted p-3">
        <Icon className="size-5 text-muted-foreground" />
      </div>
      <div className="space-y-1">
        <p className="text-sm font-medium">{title}</p>
        <p className="max-w-sm text-sm text-muted-foreground">{description}</p>
      </div>
      {action}
    </div>
  );
}

export function ErrorBanner({ message }: { message: string }) {
  if (!message) return null;
  return (
    <div className="rounded-md border border-destructive/30 bg-destructive/8 px-3 py-2 text-sm text-destructive">
      {message}
    </div>
  );
}
