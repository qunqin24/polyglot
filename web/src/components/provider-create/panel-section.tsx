import type { ReactNode } from "react";
import { Separator } from "@/components/ui/separator";

/** A labelled run of fields in the provider panel: a heading, an optional
 *  control beside it, and a rule. */
export function PanelSection({
  title,
  action,
  children,
}: {
  title: string;
  /** Sits on the heading row, for a control that acts on the whole section. */
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="space-y-4">
      <div className="space-y-2">
        <div className="flex min-h-8 items-center justify-between gap-2">
          <h3 className="text-sm leading-none font-medium">{title}</h3>
          {action}
        </div>
        <Separator />
      </div>
      {children}
    </section>
  );
}
