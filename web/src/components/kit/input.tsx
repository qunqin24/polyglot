import * as React from "react";
import { cn } from "@/lib/utils";
import { Input as BaseInput } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export { Textarea } from "@/components/ui/textarea";
export { Label };

/**
 * noAutofill marks a field that browsers and password managers must leave
 * alone, even though it looks like a credential. Chrome ignores
 * autocomplete="off" on password inputs, so a provider API key has to claim
 * to be a new password; the data attributes are the password managers' opt-outs.
 */
export const noAutofill = {
  autoComplete: "new-password",
  "data-1p-ignore": true,
  "data-lpignore": "true",
  "data-bwignore": true,
  "data-form-type": "other",
} as const;

/** Configuration fields outnumber real credential fields, so not autofilling
    is the default; a genuine login field opts back in by passing autoComplete. */
export function Input({ autoComplete, ...props }: React.ComponentProps<"input">) {
  const managed = autoComplete !== undefined;
  return (
    <BaseInput
      autoComplete={autoComplete ?? "off"}
      {...(managed ? {} : { "data-1p-ignore": true, "data-lpignore": "true", "data-bwignore": true })}
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
    <div className={cn("space-y-2", className)}>
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
