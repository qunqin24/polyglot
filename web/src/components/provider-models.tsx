import * as React from "react";
import { ListPlus, Plus, X } from "lucide-react";
import { api, type Model, type ModelOffer, type Provider } from "@/lib/api";
import { errorMessage } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { Dialog } from "@/components/ui/dialog";
import { ModelPicker } from "@/components/model-picker";

/**
 * ProviderModels manages which models a provider exposes.
 *
 * Membership belongs to the provider, so adding and removing both live here
 * rather than on the Models page, which answers a different question: what does
 * this gateway serve right now.
 *
 * Three ways in, because upstreams differ: pick from the upstream's own list,
 * type an id for upstreams that cannot list, or remove one that is no longer
 * wanted. Nothing here happens without being asked for.
 */
export function ProviderModels({
  provider,
  picked,
  onPickedChange,
  protocol,
  baseURL,
  apiKey,
  headers,
  timeoutSecs,
  canList = true,
  showFetch = true,
  hint,
}: {
  /** null while the provider is being created and has no models yet. */
  provider: Provider | null;
  /** Models ticked in the picker, submitted with the form on create. */
  picked: Set<string>;
  onPickedChange: (next: Set<string>) => void;
  protocol: Provider["protocol"];
  baseURL: string;
  apiKey: string | null;
  headers: Record<string, string>;
  timeoutSecs: number;
  /** Listing is the same upstream call the connection test makes, so a
      credential that has not been proven yet has nothing to list from. The
      caller decides; typing an id by hand is never blocked. */
  canList?: boolean;
  /** Whether the upstream exposes a usable model-list endpoint. */
  showFetch?: boolean;
  /** Replaces the standard explanation — with why listing is unavailable, or
      with what the test already found. */
  hint?: string;
}) {
  const t = useT();
  const { toast } = useToast();

  const [current, setCurrent] = React.useState<Model[]>([]);
  const [offer, setOffer] = React.useState<ModelOffer | null>(null);
  const [listing, setListing] = React.useState(false);
  const [manual, setManual] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  // The picker opens in its own dialog. `draft` is what is ticked inside it and
  // is discarded on cancel; only confirming promotes it to a real choice.
  const [pickerOpen, setPickerOpen] = React.useState(false);
  const [draft, setDraft] = React.useState<Set<string>>(new Set());

  const providerID = provider?.id ?? 0;

  const loadCurrent = React.useCallback(async () => {
    if (!providerID) {
      setCurrent([]);
      return;
    }
    try {
      const res = await api.models({ provider_id: String(providerID), limit: 500 });
      setCurrent(res.models);
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  }, [providerID, toast]);

  React.useEffect(() => {
    void loadCurrent();
  }, [loadCurrent]);

  async function openPicker() {
    setPickerOpen(true);
    setOffer(null);
    setDraft(new Set());
    setListing(true);
    try {
      const result = await api.discoverModels({
        id: providerID,
        protocol,
        base_url: baseURL,
        api_key: apiKey,
        headers,
        timeout_secs: timeoutSecs,
      });
      setOffer(result);
    } catch (e) {
      toast(errorMessage(e), "error");
      setPickerOpen(false);
    } finally {
      setListing(false);
    }
  }

  // Confirming is the only thing that commits. On a saved provider the models
  // are registered now; while creating, they join the set the form submits.
  async function confirmPicker() {
    if (draft.size === 0) return;
    if (!providerID) {
      const next = new Set(picked);
      for (const id of draft) next.add(id);
      onPickedChange(next);
      setPickerOpen(false);
      return;
    }
    setBusy(true);
    try {
      await api.addProviderModels(providerID, [...draft].map((id) => ({ id })));
      await loadCurrent();
      setPickerOpen(false);
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  }

  async function addManual() {
    const id = manual.trim();
    if (!id) return;
    if (!providerID) {
      // Before the provider exists there is nothing to attach to, so the id
      // joins the same selection the picker feeds.
      onPickedChange(new Set(picked).add(id));
      setManual("");
      return;
    }
    setBusy(true);
    try {
      await api.createModel({
        provider_id: providerID,
        upstream_model_id: id,
        display_name: "",
        enabled: true,
      });
      setManual("");
      await loadCurrent();
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  }

  async function remove(m: Model) {
    setBusy(true);
    try {
      await api.deleteModel(m.id);
      await loadCurrent();
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  }

  // While creating, the chips show what will be registered on save; afterwards
  // they show what is registered. Same affordance, so the section reads the
  // same before and after the provider exists.
  const chips: { key: string; label: string; onRemove: () => void }[] = providerID
    ? current.map((m) => ({
        key: String(m.id),
        label: m.upstream_model_id,
        onRemove: () => void remove(m),
      }))
    : [...picked].map((id) => ({
        key: id,
        label: id,
        onRemove: () => {
          const next = new Set(picked);
          next.delete(id);
          onPickedChange(next);
        },
      }));

  return (
    <div className="space-y-3 rounded-lg border border-border p-3">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-sm font-medium">{t("providers.models")}</p>
          <p className="mt-0.5 text-xs text-muted-foreground">{hint ?? t("providers.modelsHint")}</p>
        </div>
        {showFetch && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => void openPicker()}
            disabled={listing || busy || !baseURL || !canList}
          >
            {listing ? <Spinner /> : <ListPlus />}
            {t("providers.fetchModels")}
          </Button>
        )}
      </div>

      {chips.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {chips.map((c) => (
            <span
              key={c.key}
              className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/50 py-1 pl-2 pr-1 font-mono text-xs"
            >
              {c.label}
              <button
                type="button"
                onClick={c.onRemove}
                disabled={busy}
                title={t("common.remove")}
                className="rounded p-0.5 text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive disabled:opacity-50"
              >
                <X className="size-3" />
                <span className="sr-only">{t("common.remove")}</span>
              </button>
            </span>
          ))}
        </div>
      )}

      <div className="flex items-center gap-2">
        <Input
          value={manual}
          onChange={(e) => setManual(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void addManual();
            }
          }}
          placeholder={t("providers.manualModelPlaceholder")}
          className="h-8 font-mono text-xs"
          disabled={busy}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => void addManual()}
          disabled={busy || manual.trim() === ""}
        >
          <Plus />
          {t("common.add")}
        </Button>
      </div>

      <Dialog
        open={pickerOpen}
        onOpenChange={(o) => !o && setPickerOpen(false)}
        className="max-w-2xl"
        title={t("models.pickerTitle")}
        description={t("models.pickerDescription")}
        footer={
          <>
            <Button type="button" variant="outline" onClick={() => setPickerOpen(false)} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button type="button" onClick={() => void confirmPicker()} disabled={busy || draft.size === 0}>
              {busy && <Spinner />}
              {t("models.pickerAdd", { count: draft.size })}
            </Button>
          </>
        }
      >
        {listing && (
          <div className="flex justify-center py-8">
            <Spinner className="text-muted-foreground" />
          </div>
        )}
        {offer && !offer.ok && (
          <p className="py-2 text-sm text-muted-foreground">
            {offer.supported
              ? t("providers.listFailed", { error: offer.error ?? "" })
              : t("providers.listUnsupported")}
          </p>
        )}
        {offer?.ok && <ModelPicker models={offer.models} selected={draft} onChange={setDraft} />}
      </Dialog>
    </div>
  );
}
