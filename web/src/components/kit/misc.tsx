import * as React from "react";
import { Select as SelectPrimitive } from "radix-ui";
import { AlertCircle, CheckIcon, Loader2, RefreshCw } from "lucide-react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";
import { useT } from "@/lib/i18n";
import { LoadingButton } from "@/components/kit/loading-button";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Select as SelectRoot,
  SelectContent,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { TableCell, TableHead, TableRow } from "@/components/ui/table";

export { Switch } from "@/components/ui/switch";
export { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

// --- badge ------------------------------------------------------------------

// shadcn-admin's badge shape with the status colours Polyglot pages need.
const badgeVariants = cva(
  "inline-flex w-fit shrink-0 items-center justify-center gap-1 overflow-hidden rounded-md border px-2 py-0.5 text-xs font-medium whitespace-nowrap [&>svg]:pointer-events-none [&>svg]:size-3",
  {
    variants: {
      variant: {
        default: "border-transparent bg-secondary text-secondary-foreground",
        outline: "text-foreground",
        success: "border-transparent bg-success/12 text-success",
        destructive: "border-transparent bg-destructive/12 text-destructive",
        warning: "border-transparent bg-warning/15 text-warning",
        accent: "border-transparent bg-primary/10 text-primary",
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
  return <span data-slot="badge" className={cn(badgeVariants({ variant }), className)} {...props} />;
}

// --- select -----------------------------------------------------------------

export function Select({
  id,
  value,
  onValueChange,
  placeholder,
  options,
  className,
  disabled,
}: {
  id?: string;
  value: string;
  onValueChange: (v: string) => void;
  placeholder?: string;
  /** icon is decorative and shows in the list only. */
  options: { value: string; label: string; hint?: string; icon?: React.ReactNode }[];
  className?: string;
  disabled?: boolean;
}) {
  return (
    <SelectRoot value={value} onValueChange={onValueChange} disabled={disabled}>
      <SelectTrigger id={id} className={cn("w-full min-w-0 [&>span]:truncate", className)}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent position="popper" className="max-h-72">
        {options.map((o) => (
          // The trigger shows only the label: icon and hint sit outside
          // ItemText, which is what Radix copies into the trigger.
          <SelectPrimitive.Item
            key={o.value}
            value={o.value}
            className="relative flex w-full cursor-default items-center gap-2 rounded-sm py-1.5 ps-2 pe-8 text-sm outline-hidden select-none focus:bg-accent focus:text-accent-foreground data-disabled:pointer-events-none data-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4"
          >
            <span className="absolute inset-e-2 flex size-3.5 items-center justify-center">
              <SelectPrimitive.ItemIndicator>
                <CheckIcon className="size-4" />
              </SelectPrimitive.ItemIndicator>
            </span>
            {o.icon}
            <SelectPrimitive.ItemText>{o.label}</SelectPrimitive.ItemText>
            {o.hint && <span className="ms-auto text-xs text-muted-foreground">{o.hint}</span>}
          </SelectPrimitive.Item>
        ))}
      </SelectContent>
    </SelectRoot>
  );
}

// --- table ------------------------------------------------------------------

export function Table({ className, ...props }: React.ComponentProps<"table">) {
  return (
    <div className="relative w-full overflow-x-auto">
      <table data-slot="table" className={cn("w-full caption-bottom text-sm", className)} {...props} />
    </div>
  );
}

export function Th({ className, ...props }: React.ComponentProps<"th">) {
  return <TableHead className={cn("text-xs text-muted-foreground", className)} {...props} />;
}

export function Td({ className, ...props }: React.ComponentProps<"td">) {
  return <TableCell className={className} {...props} />;
}

export function Tr({ className, ...props }: React.ComponentProps<"tr">) {
  return <TableRow className={cn("last:border-0", className)} {...props} />;
}

// --- states -----------------------------------------------------------------

/** A spinner a screen reader can hear. Inside a control that already says what
 *  is happening, pass `decorative` so it is not announced twice. */
export function Spinner({
  className,
  label,
  decorative,
}: {
  className?: string;
  label?: string;
  decorative?: boolean;
}) {
  const t = useT();
  const icon = <Loader2 aria-hidden="true" className={cn("size-4 animate-spin", className)} />;
  if (decorative) return icon;
  return (
    <span role="status" className="inline-flex">
      {icon}
      <span className="sr-only">{label ?? t("common.loading")}</span>
    </span>
  );
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

export function ErrorBanner({
  message,
  onRetry,
  retrying,
}: {
  message: string;
  /** Adds a retry button inside the banner, where the failure is. */
  onRetry?: () => void;
  retrying?: boolean;
}) {
  const t = useT();
  if (!message) return null;
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertDescription>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span>{message}</span>
          {onRetry && (
            <LoadingButton
              type="button"
              variant="outline"
              size="sm"
              busy={retrying}
              icon={<RefreshCw />}
              iconIsAction
              onClick={onRetry}
              className="text-foreground"
            >
              {t("common.retry")}
            </LoadingButton>
          )}
        </div>
      </AlertDescription>
    </Alert>
  );
}
