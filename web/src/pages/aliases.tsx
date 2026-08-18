import * as React from "react";
import { ArrowRight, Pencil, Plus, Tags, Trash2 } from "lucide-react";
import { api, type AliasInput, type ModelAlias, type Provider } from "@/lib/api";
import { errorMessage, useAsync } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ConfirmDialog, Dialog } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/input";
import {
  Badge,
  EmptyState,
  ErrorBanner,
  Select,
  Spinner,
  Switch,
  Table,
  Td,
  Th,
  Tr,
} from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";

// An alias is a logical model name. Nothing requires one: a discovered or
// manually added model is already callable by its own id. Aliases exist so a
// client can hold a short, stable name whose target an operator can change.
export function Aliases({ providers }: { providers: Provider[] }) {
  const t = useT();
  const { toast } = useToast();
  const { data, loading, error, reload } = useAsync(() => api.aliases(), []);

  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<ModelAlias | null>(null);
  const [deleting, setDeleting] = React.useState<ModelAlias | null>(null);

  async function remove() {
    if (!deleting) return;
    try {
      await api.deleteAlias(deleting.id);
      toast(t("aliases.deleted", { alias: deleting.alias }));
      reload();
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setDeleting(null);
    }
  }

  async function toggle(a: ModelAlias, enabled: boolean) {
    try {
      await api.updateAlias(a.id, {
        alias: a.alias,
        provider_id: a.provider_id,
        upstream_model: a.upstream_model,
        priority: a.priority,
        enabled,
      });
      reload();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  }

  const hasProviders = providers.length > 0;

  return (
    <>
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-2xl text-sm text-muted-foreground">{t("aliases.description")}</p>
        <Button
          onClick={() => {
            setEditing(null);
            setOpen(true);
          }}
          disabled={!hasProviders}
        >
          <Plus /> {t("aliases.add")}
        </Button>
      </div>

      <ErrorBanner message={error} />

      <Card>
        <CardContent className="p-0">
          {loading && !data ? (
            <div className="flex justify-center py-12">
              <Spinner className="text-muted-foreground" />
            </div>
          ) : (data?.length ?? 0) === 0 ? (
            <EmptyState
              icon={Tags}
              title={t("aliases.empty")}
              description={hasProviders ? t("aliases.emptyHint") : t("aliases.emptyNoProvider")}
              action={
                hasProviders ? (
                  <Button
                    onClick={() => {
                      setEditing(null);
                      setOpen(true);
                    }}
                  >
                    <Plus /> {t("aliases.add")}
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <Table>
              <thead>
                <Tr>
                  <Th className="pl-5">{t("aliases.alias")}</Th>
                  <Th />
                  <Th>{t("common.provider")}</Th>
                  <Th>{t("aliases.target")}</Th>
                  <Th>{t("aliases.priority")}</Th>
                  <Th>{t("common.enabled")}</Th>
                  <Th className="pr-5 text-right">{t("common.actions")}</Th>
                </Tr>
              </thead>
              <tbody>
                {data!.map((a) => (
                  <Tr key={a.id}>
                    <Td className="pl-5">
                      <span className="font-mono text-xs font-medium">{a.alias}</span>
                    </Td>
                    <Td className="w-4 text-muted-foreground/50">
                      <ArrowRight className="size-3.5" />
                    </Td>
                    <Td>
                      <div className="flex items-center gap-1.5">
                        <span className="text-sm">{a.provider_name}</span>
                        <Badge variant="accent">{a.protocol}</Badge>
                      </div>
                    </Td>
                    <Td className="font-mono text-xs text-muted-foreground">
                      {a.upstream_model || <span className="italic">{t("aliases.asRequested")}</span>}
                    </Td>
                    <Td className="tabular-nums text-muted-foreground">{a.priority}</Td>
                    <Td>
                      <Switch checked={a.enabled} onCheckedChange={(v) => void toggle(a, v)} />
                    </Td>
                    <Td className="pr-5">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          title={t("common.edit")}
                          onClick={() => {
                            setEditing(a);
                            setOpen(true);
                          }}
                        >
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          title={t("common.delete")}
                          onClick={() => setDeleting(a)}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                    </Td>
                  </Tr>
                ))}
              </tbody>
            </Table>
          )}
        </CardContent>
      </Card>

      <p className="mt-3 text-xs text-muted-foreground">{t("aliases.footnote")}</p>

      <AliasDialog
        key={editing?.id ?? "new"}
        open={open}
        onOpenChange={setOpen}
        alias={editing}
        providers={providers}
        onSaved={() => {
          setOpen(false);
          reload();
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("aliases.deleteTitle", { alias: deleting?.alias ?? "" })}
        description={t("aliases.deleteDescription")}
        onConfirm={() => void remove()}
      />
    </>
  );
}

function AliasDialog({
  open,
  onOpenChange,
  alias,
  providers,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  alias: ModelAlias | null;
  providers: Provider[];
  onSaved: () => void;
}) {
  const t = useT();
  const { toast } = useToast();
  const [name, setName] = React.useState(alias?.alias ?? "");
  const [providerID, setProviderID] = React.useState(
    String(alias?.provider_id ?? providers[0]?.id ?? ""),
  );
  const [target, setTarget] = React.useState(alias?.upstream_model ?? "");
  const [priority, setPriority] = React.useState(alias?.priority ?? 0);
  const [enabled, setEnabled] = React.useState(alias?.enabled ?? true);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  // Offer the models Polyglot already knows about for this provider, so the
  // target is picked rather than typed from memory.
  const models = useAsync(
    () => (providerID ? api.models({ provider_id: providerID, limit: 500 }) : Promise.resolve(null)),
    [providerID],
  );

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    const payload: AliasInput = {
      alias: name.trim(),
      provider_id: Number(providerID),
      upstream_model: target.trim(),
      priority,
      enabled,
    };
    setBusy(true);
    try {
      if (alias) await api.updateAlias(alias.id, payload);
      else await api.createAlias(payload);
      toast(alias ? t("aliases.updated") : t("aliases.created", { alias: payload.alias }));
      onSaved();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={alias ? t("aliases.editTitle") : t("aliases.addTitle")}
      description={t("aliases.dialogDescription")}
      footer={
        <Button type="submit" form="alias-form" disabled={busy}>
          {busy && <Spinner />} {t("common.save")}
        </Button>
      }
    >
      <form id="alias-form" autoComplete="off" onSubmit={(e) => void save(e)} className="space-y-4">
        <Field label={t("aliases.alias")} hint={t("aliases.aliasHint")}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="coding"
            required
            spellCheck={false}
            className="font-mono"
          />
        </Field>

        <Field label={t("common.provider")}>
          <Select
            value={providerID}
            onValueChange={setProviderID}
            placeholder={t("aliases.selectProvider")}
            options={providers.map((p) => ({
              value: String(p.id),
              label: p.name,
              hint: p.protocol,
            }))}
          />
        </Field>

        <Field label={t("aliases.target")} hint={t("aliases.targetHint")}>
          <Input
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            placeholder="anthropic/claude-sonnet-4"
            list="alias-target-models"
            spellCheck={false}
            className="font-mono"
          />
          <datalist id="alias-target-models">
            {models.data?.models.map((m) => (
              <option key={m.id} value={m.upstream_model_id} />
            ))}
          </datalist>
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("aliases.priority")} hint={t("aliases.priorityHint")}>
            <Input
              type="number"
              value={priority}
              onChange={(e) => setPriority(Number(e.target.value))}
            />
          </Field>
          <div className="flex items-end gap-2 pb-2">
            <Switch checked={enabled} onCheckedChange={setEnabled} id="alias-enabled" />
            <label htmlFor="alias-enabled" className="text-sm">
              {t("common.enabled")}
            </label>
          </div>
        </div>

        <ErrorBanner message={error} />
      </form>
    </Dialog>
  );
}
