import * as React from "react";
import { cn } from "@/lib/utils";
import { useT } from "@/lib/i18n";
import { LoadingButton } from "@/components/kit/loading-button";
import {
  Dialog as DialogRoot,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Sheet as SheetRoot,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";

// Polyglot's dialog API on top of the shadcn-admin primitives: a call site
// passes title, body and footer instead of composing the parts every time.

export function Dialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  className,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: React.ReactNode;
  footer?: React.ReactNode;
  className?: string;
}) {
  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogContent className={cn("max-h-[calc(100vh-4rem)] overflow-y-auto sm:max-w-lg", className)}>
        <DialogHeader className="text-start">
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription className={description ? undefined : "sr-only"}>
            {description ?? title}
          </DialogDescription>
        </DialogHeader>
        {children}
        {footer && <DialogFooter>{footer}</DialogFooter>}
      </DialogContent>
    </DialogRoot>
  );
}

/** A dialog docked to the right edge, for long forms: header and footer stay
    put and only the fields between them scroll. */
export function Sheet({
  open,
  onOpenChange,
  title,
  description,
  header,
  children,
  footer,
  className,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  header?: React.ReactNode;
  children: React.ReactNode;
  footer?: React.ReactNode;
  className?: string;
}) {
  return (
    <SheetRoot open={open} onOpenChange={onOpenChange}>
      <SheetContent className={cn("flex w-full flex-col gap-0 sm:max-w-xl", className)}>
        <SheetHeader className="border-b text-start">
          <SheetTitle>{title}</SheetTitle>
          <SheetDescription className={description ? undefined : "sr-only"}>
            {description ?? title}
          </SheetDescription>
          {header && <div className="pt-2">{header}</div>}
        </SheetHeader>
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-5">{children}</div>
        {footer && <SheetFooter className="flex-row justify-end border-t">{footer}</SheetFooter>}
      </SheetContent>
    </SheetRoot>
  );
}

/**
 * A confirmation that waits for its action. `onConfirm` may return a promise:
 * the dialog stays open with the button busy until it settles, then closes.
 * The action reports its own failure (a toast or a banner), so a rejected
 * promise still closes the dialog.
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  onConfirm,
  destructive = true,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel?: string;
  onConfirm: () => void | Promise<unknown>;
  destructive?: boolean;
}) {
  const t = useT();
  const [busy, setBusy] = React.useState(false);

  async function run() {
    setBusy(true);
    try {
      await onConfirm();
    } catch {
      // The caller has already said what failed.
    } finally {
      setBusy(false);
      onOpenChange(false);
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={(o) => (busy ? undefined : onOpenChange(o))}>
      <AlertDialogContent className="sm:max-w-md" aria-busy={busy || undefined}>
        <AlertDialogHeader className="text-start">
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{t("common.cancel")}</AlertDialogCancel>
          <LoadingButton
            variant={destructive ? "destructive" : "default"}
            busy={busy}
            onClick={() => void run()}
          >
            {confirmLabel ?? t("common.delete")}
          </LoadingButton>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
