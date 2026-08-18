import * as React from "react";
import { CheckCircle2, AlertCircle, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { useT } from "@/lib/i18n";

// A ~60 line toast instead of a dependency. It does what Polyglot needs and
// nothing else.

type ToastKind = "success" | "error";
interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

const ToastContext = React.createContext<{
  toast: (message: string, kind?: ToastKind) => void;
}>({ toast: () => {} });

export function useToast() {
  return React.useContext(ToastContext);
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const t = useT();
  const [toasts, setToasts] = React.useState<Toast[]>([]);
  const nextId = React.useRef(1);

  const dismiss = React.useCallback((id: number) => {
    setToasts((list) => list.filter((x) => x.id !== id));
  }, []);

  const toast = React.useCallback(
    (message: string, kind: ToastKind = "success") => {
      const id = nextId.current++;
      setToasts((list) => [...list, { id, kind, message }]);
      setTimeout(() => dismiss(id), kind === "error" ? 6000 : 3500);
    },
    [dismiss],
  );

  const value = React.useMemo(() => ({ toast }), [toast]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="pointer-events-none fixed bottom-4 right-4 z-100 flex w-full max-w-sm flex-col gap-2">
        {toasts.map((item) => (
          <div
            key={item.id}
            role="status"
            className={cn(
              "pointer-events-auto flex items-start gap-2.5 rounded-lg border bg-card px-3.5 py-3 text-sm shadow-lg animate-in-soft",
              item.kind === "error" ? "border-destructive/30" : "border-border",
            )}
          >
            {item.kind === "error" ? (
              <AlertCircle className="mt-0.5 size-4 shrink-0 text-destructive" />
            ) : (
              <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-[--color-success]" />
            )}
            <span className="flex-1 break-words">{item.message}</span>
            <button
              onClick={() => dismiss(item.id)}
              className="text-muted-foreground transition-colors hover:text-foreground"
              aria-label={t("common.dismiss")}
            >
              <X className="size-3.5" />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
