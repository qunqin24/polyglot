import * as React from "react";
import { Label as LabelPrimitive } from "radix-ui";
import { cn } from "@/lib/utils";

/**
 * noAutofill marks a field that browsers and password managers must leave
 * alone, even though it looks like a credential.
 *
 * Chrome ignores `autocomplete="off"` on password inputs — it is the one value
 * it treats as advisory — so a provider's API key has to claim to be a *new*
 * password to stop saved credentials being poured into it. The data attributes
 * are the opt-outs 1Password, LastPass, Bitwarden and Dashlane each honour.
 */
export const noAutofill = {
  autoComplete: "new-password",
  "data-1p-ignore": true,
  "data-lpignore": "true",
  "data-bwignore": true,
  "data-form-type": "other",
} as const;

export function Input({ className, type, autoComplete, ...props }: React.ComponentProps<"input">) {
  // Admin configuration fields vastly outnumber real credential fields here, so
  // not autofilling is the default and a genuine login field opts back in by
  // passing autoComplete. The old default filled a username into "Base URL".
  const managed = autoComplete !== undefined;
  return (
    <input
      type={type}
      autoComplete={autoComplete ?? "off"}
      {...(managed ? {} : { "data-1p-ignore": true, "data-lpignore": "true", "data-bwignore": true })}
      className={cn(
        "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-xs transition-colors",
        "placeholder:text-muted-foreground/70",
        "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/25 outline-none",
        "disabled:cursor-not-allowed disabled:opacity-50",
        "file:border-0 file:bg-transparent file:text-sm file:font-medium",
        className,
      )}
      {...props}
    />
  );
}

export function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      className={cn(
        "flex min-h-20 w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs transition-colors",
        "placeholder:text-muted-foreground/70",
        "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/25 outline-none",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export function Label({
  className,
  ...props
}: React.ComponentProps<typeof LabelPrimitive.Root>) {
  return (
    <LabelPrimitive.Root
      className={cn(
        "block text-sm font-medium leading-none select-none peer-disabled:opacity-70",
        className,
      )}
      {...props}
    />
  );
}

export function Field({
  label,
  hint,
  error,
  children,
  className,
}: {
  label: string;
  hint?: React.ReactNode;
  error?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      <Label>{label}</Label>
      {children}
      {error ? (
        <p className="text-xs text-destructive">{error}</p>
      ) : hint ? (
        <p className="text-xs text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  );
}
