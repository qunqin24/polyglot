import * as React from "react";
import {
  type ColumnDef,
  type ColumnFiltersState,
  type FilterFn,
  type RowSelectionState,
  type SortingState,
  type VisibilityState,
  flexRender,
  getCoreRowModel,
  getFacetedRowModel,
  getFacetedUniqueValues,
  getFilteredRowModel,
  getPaginationRowModel,
  getSortedRowModel,
  useReactTable,
} from "@tanstack/react-table";
import {
  Check,
  CircleCheck,
  CircleOff,
  Copy,
  MoreHorizontal,
  Pencil,
  Plug,
  Plus,
  Server,
  Trash2,
} from "lucide-react";
import {
  api,
  type Provider,
  type ProviderInput,
} from "@/lib/api";
import { copyToClipboard, errorMessage, isAbort, useAsync, useDelayedFlag } from "@/lib/hooks";
import { useOptimisticFlags } from "@/lib/optimistic";
import { useT } from "@/lib/i18n";
import { cn, formatDuration, formatRelative } from "@/lib/utils";
import {
  DataTableBulkActions,
  DataTableColumnHeader,
  DataTablePagination,
  DataTableToolbar,
} from "@/components/data-table";
import { PageHeader } from "@/components/layout/page-header";
import { ProviderSheet } from "@/components/provider-create";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ConfirmDialog } from "@/components/kit/dialog";
import { SaveStatus } from "@/components/kit/save-status";
import { LoadingGate, TableRowsSkeleton } from "@/components/kit/skeletons";
import { Badge, EmptyState, ErrorBanner, Spinner, Switch } from "@/components/kit/misc";
import { useToast } from "@/components/kit/toast";


type StatusValue = "enabled" | "disabled";

// Faceted filters hold the selected values as an array; a row passes when its
// value is one of them.
const facetFilter: FilterFn<Provider> = (row, id, value: unknown) =>
  Array.isArray(value) && value.includes(row.getValue(id));

// Search covers what an operator remembers a provider by, not every column.
const providerSearch: FilterFn<Provider> = (row, _id, value: unknown) => {
  const q = typeof value === "string" ? value.trim().toLowerCase() : "";
  if (!q) return true;
  const p = row.original;
  return [p.name, p.base_url, p.note].some((s) => s.toLowerCase().includes(q));
};

// The update writes every field, so the row's own values travel back with
// the flag being changed — anything left out would be erased.
const MULTI_PROTOCOL = "multi";

function withEnabled(p: Provider, enabled: boolean): ProviderInput {
  return {
    name: p.name,
    protocol: p.protocol,
    base_url: p.base_url,
    vendor: p.vendor,
    note: p.note,
    api_key: null,
    headers: p.headers,
    timeout_secs: p.timeout_secs,
    priority: p.priority,
    strict_fields: p.strict_fields,
    auto_disable_on_auth_error: p.auto_disable_on_auth_error,
    enabled,
  };
}

export function Providers() {
  const t = useT();
  const { data, initialLoading, error, reload } = useAsync(() => api.providers(), ["providers"]);
  const protocols = useAsync(() => api.protocols(), ["protocols"]);
  const { toast, progress, finish } = useToast();

  const [editing, setEditing] = React.useState<Provider | null>(null);
  const [open, setOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState<Provider[]>([]);
  const [testingId, setTestingId] = React.useState<number | null>(null);

  const [rowSelection, setRowSelection] = React.useState<RowSelectionState>({});
  const [sorting, setSorting] = React.useState<SortingState>([]);
  const [columnFilters, setColumnFilters] = React.useState<ColumnFiltersState>([]);
  const [columnVisibility, setColumnVisibility] = React.useState<VisibilityState>({});
  const [globalFilter, setGlobalFilter] = React.useState("");

  const openNew = React.useCallback(() => {
    setEditing(null);
    setOpen(true);
  }, []);

  const openEdit = React.useCallback((p: Provider) => {
    setEditing(p);
    setOpen(true);
  }, []);

  // One row's switch: shown at once, saved in the background, put back with a
  // retry on the row if the save fails.
  const flags = useOptimisticFlags<number>((id, enabled) => {
    const p = data?.find((x) => x.id === id);
    return p ? api.updateProvider(p.id, withEnabled(p, enabled)) : Promise.resolve();
  }, reload);

  // Several rows at once are counted, not guessed at: one toast follows the
  // work row by row and turns into the result.
  const setEnabledMany = React.useCallback(
    async (list: Provider[], enabled: boolean) => {
      const changed = list.filter((p) => p.enabled !== enabled);
      const id = "providers-bulk";
      let done = 0;
      try {
        for (const p of changed) {
          progress(id, t("providers.bulkUpdating", { done, total: changed.length }));
          await api.updateProvider(p.id, withEnabled(p, enabled));
          done++;
        }
        finish(id, t("providers.bulkUpdated", { count: changed.length }));
      } catch (e) {
        finish(id, errorMessage(e), "error");
      } finally {
        void reload();
      }
    },
    [reload, t, progress, finish],
  );

  // A connection test can take up to 20 s. It runs under one toast that names
  // it, offers cancel, says so after 10 s, and becomes the result in place.
  const tests = React.useRef(new Map<number, AbortController>());
  const testConnection = React.useCallback(
    async (p: Provider) => {
      const id = `provider-test-${p.id}`;
      const controller = new AbortController();
      tests.current.set(p.id, controller);
      setTestingId(p.id);
      const opts = { onCancel: () => controller.abort(), cancelLabel: t("common.cancel") };
      const name = t("providers.testing", { name: p.name });
      progress(id, name, opts);
      const slow = setTimeout(() => progress(id, `${name} ${t("common.slowHint")}`, opts), 10_000);
      try {
        // A null key tells the server to use the stored credential.
        const r = await api.testProvider(
          {
            id: p.id,
            protocol: p.protocol,
            base_url: p.base_url,
            vendor: p.vendor,
            api_key: null,
            headers: p.headers,
            timeout_secs: p.timeout_secs,
          },
          controller.signal,
        );
        if (r.ok) {
          finish(id, t("providers.testOk", { latency: formatDuration(r.latency_ms), count: r.model_count ?? 0 }));
        } else {
          finish(id, `${p.name}: ${r.error ?? ""}`, "error");
        }
      } catch (e) {
        if (isAbort(e)) finish(id, t("providers.testCancelled"));
        else finish(id, errorMessage(e), "error");
      } finally {
        clearTimeout(slow);
        tests.current.delete(p.id);
        setTestingId(null);
      }
    },
    [t, progress, finish],
  );

  async function remove() {
    const list = deleting;
    const id = "providers-bulk";
    let done = 0;
    try {
      for (const p of list) {
        if (list.length > 1) progress(id, t("providers.bulkDeleting", { done, total: list.length }));
        await api.deleteProvider(p.id);
        done++;
      }
      const message =
        list.length === 1
          ? t("providers.deleted", { name: list[0].name })
          : t("providers.bulkDeleted", { count: list.length });
      if (list.length > 1) finish(id, message);
      else toast(message);
      setRowSelection({});
    } catch (e) {
      if (list.length > 1) finish(id, errorMessage(e), "error");
      else toast(errorMessage(e), "error");
    } finally {
      void reload();
    }
  }

  // Rows arrive in routing order — priority, then name — so an unsorted
  // table reads top to bottom the way the router tries providers.
  const rows = React.useMemo(
    () => [...(data ?? [])].sort((a, b) => b.priority - a.priority || a.name.localeCompare(b.name)),
    [data],
  );

  const columns = React.useMemo<ColumnDef<Provider>[]>(
    () => [
      {
        id: "select",
        header: ({ table }) => (
          <Checkbox
            checked={table.getIsAllPageRowsSelected() || (table.getIsSomePageRowsSelected() && "indeterminate")}
            onCheckedChange={(v) => table.toggleAllPageRowsSelected(!!v)}
            aria-label={t("table.selectAll")}
            className="translate-y-0.5"
          />
        ),
        cell: ({ row }) => (
          <Checkbox
            checked={row.getIsSelected()}
            onCheckedChange={(v) => row.toggleSelected(!!v)}
            aria-label={t("table.selectRow")}
            className="translate-y-0.5"
          />
        ),
        enableSorting: false,
        enableHiding: false,
        meta: { className: "w-10" },
      },
      {
        accessorKey: "name",
        header: ({ column }) => <DataTableColumnHeader column={column} title={t("common.name")} />,
        cell: ({ row }) => {
          const p = row.original;
          return (
            <div className="min-w-40">
              <div className="flex flex-wrap items-center gap-1.5">
                <button
                  type="button"
                  className="font-medium hover:underline"
                  onClick={() => openEdit(p)}
                >
                  {p.name}
                </button>
                {p.cooling_until && (
                  <span title={t("providers.coolingHint", { seconds: secondsLeft(p.cooling_until) })}>
                    <Badge variant="warning">{t("providers.cooling")}</Badge>
                  </span>
                )}
                {!p.enabled && p.disabled_reason && (
                  <span title={p.disabled_reason}>
                    <Badge variant="destructive">{t("providers.autoDisabled")}</Badge>
                  </span>
                )}
              </div>
              {p.note && (
                <p title={p.note} className="mt-0.5 max-w-[16rem] truncate text-xs text-muted-foreground">
                  {p.note}
                </p>
              )}
            </div>
          );
        },
        enableHiding: false,
        meta: { title: t("common.name") },
      },
      {
        id: "protocol",
        // A multi-protocol provider has no single protocol; its models do.
        accessorFn: (p) => (p.vendor ? MULTI_PROTOCOL : p.protocol),
        header: ({ column }) => <DataTableColumnHeader column={column} title={t("common.protocol")} />,
        cell: ({ row }) => (
          <Badge variant="accent">{row.original.vendor ? t("providers.multiProtocol") : row.original.protocol}</Badge>
        ),
        filterFn: facetFilter,
        meta: { title: t("common.protocol") },
      },
      {
        accessorKey: "priority",
        header: ({ column }) => <DataTableColumnHeader column={column} title={t("providers.priority")} />,
        cell: ({ row }) => <span className="tabular-nums text-muted-foreground">{row.original.priority}</span>,
        sortDescFirst: true,
        meta: { title: t("providers.priority") },
      },
      {
        accessorKey: "base_url",
        header: ({ column }) => <DataTableColumnHeader column={column} title={t("providers.baseURL")} />,
        cell: ({ row }) => (
          <span title={row.original.base_url} className="block max-w-[20rem] truncate font-mono text-xs text-muted-foreground">
            {row.original.base_url}
          </span>
        ),
        meta: { title: t("providers.baseURL") },
      },
      {
        id: "credential",
        accessorFn: (p) => (p.has_api_key ? 1 : 0),
        header: t("providers.credential"),
        cell: ({ row }) =>
          row.original.has_api_key ? (
            <CredentialCell provider={row.original} />
          ) : (
            <span className="text-xs text-muted-foreground">{t("common.none")}</span>
          ),
        enableSorting: false,
        meta: { title: t("providers.credential") },
      },
      {
        accessorKey: "model_count",
        header: ({ column }) => <DataTableColumnHeader column={column} title={t("providers.models")} />,
        cell: ({ row }) => {
          const p = row.original;
          return (
            <div>
              <span className="tabular-nums">{p.model_count}</span>
              <p className="text-xs text-muted-foreground">
                {p.models_synced_at
                  ? t("models.syncedAt", {
                      when: formatRelative(p.models_synced_at, {
                        never: t("models.neverSynced"),
                        justNow: t("common.justNow"),
                      }),
                    })
                  : t("models.neverSynced")}
              </p>
            </div>
          );
        },
        sortDescFirst: true,
        meta: { title: t("providers.models") },
      },
      {
        id: "status",
        accessorFn: (p): StatusValue => (p.enabled ? "enabled" : "disabled"),
        header: t("common.enabled"),
        cell: ({ row }) => (
          <div className="flex items-center gap-2">
            <Switch
              checked={flags.value(row.original.id, row.original.enabled)}
              onCheckedChange={(v) => void flags.toggle(row.original.id, v)}
              aria-label={t("common.enabled")}
            />
            <SaveStatus state={flags.status(row.original.id)} onRetry={() => flags.retry(row.original.id)} />
          </div>
        ),
        filterFn: facetFilter,
        enableSorting: false,
        meta: { title: t("common.status") },
      },
      {
        id: "actions",
        cell: ({ row }) => (
          <RowActions
            provider={row.original}
            testing={testingId === row.original.id}
            onEdit={openEdit}
            onTest={(p) => void testConnection(p)}
            onToggle={(p) => void flags.toggle(p.id, !flags.value(p.id, p.enabled))}
            onDelete={(p) => setDeleting([p])}
          />
        ),
        enableSorting: false,
        enableHiding: false,
        meta: { className: "w-10 text-end" },
      },
    ],
    [t, openEdit, flags, testConnection, testingId],
  );

  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting, columnFilters, columnVisibility, rowSelection, globalFilter },
    getRowId: (p) => String(p.id),
    enableRowSelection: true,
    onSortingChange: setSorting,
    onColumnFiltersChange: setColumnFilters,
    onColumnVisibilityChange: setColumnVisibility,
    onRowSelectionChange: setRowSelection,
    onGlobalFilterChange: setGlobalFilter,
    globalFilterFn: providerSearch,
    getCoreRowModel: getCoreRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    getFacetedRowModel: getFacetedRowModel(),
    getFacetedUniqueValues: getFacetedUniqueValues(),
  });

  const protocolsInUse = [...new Set(rows.map((p) => (p.vendor ? MULTI_PROTOCOL : p.protocol)))].sort();
  const protocolOptions = protocols.data?.map((p) => ({ value: p.name, label: p.label })) ?? [];
  const selected = table.getFilteredSelectedRowModel().rows.map((r) => r.original);
  const shown = table.getFilteredRowModel().rows.length;
  // Any explicit sort other than priority-first means the rows no longer
  // read as the routing order, so the page says so.
  const notRouteOrder = sorting.length > 0 && !(sorting[0].id === "priority" && sorting[0].desc);

  const addButton = (
    <Button onClick={openNew}>
      <Plus /> {t("providers.add")}
    </Button>
  );

  return (
    <>
      <PageHeader title={t("providers.title")} description={t("providers.description")} action={addButton} />
      <ErrorBanner message={error} onRetry={() => void reload()} />

      <LoadingGate
        loading={initialLoading}
        skeleton={
          <div className="rounded-md border">
            <TableRowsSkeleton rows={4} columns={4} />
          </div>
        }
      >
      {rows.length === 0 ? (
        <div className="rounded-md border">
          <EmptyState
            icon={Server}
            title={t("providers.empty")}
            description={t("providers.emptyHint")}
            action={addButton}
          />
        </div>
      ) : (
        <div className="flex flex-1 flex-col gap-4 max-sm:has-[div[role=toolbar]]:mb-16">
          <DataTableToolbar
            table={table}
            searchPlaceholder={t("providers.search")}
            filters={[
              {
                columnId: "protocol",
                title: t("common.protocol"),
                options: protocolsInUse.map((p) => ({ value: p, label: p === MULTI_PROTOCOL ? t("providers.multiProtocol") : p })),
              },
              {
                columnId: "status",
                title: t("common.status"),
                options: [
                  { value: "enabled", label: t("common.enabled"), icon: CircleCheck },
                  { value: "disabled", label: t("common.disabled"), icon: CircleOff },
                ],
              },
            ]}
          >
            <span
              className="text-xs text-muted-foreground"
              title={notRouteOrder ? t("providers.notRouteOrder") : undefined}
            >
              {t("providers.count", { shown, total: rows.length })}
              {notRouteOrder && " *"}
            </span>
          </DataTableToolbar>

          <div className="overflow-hidden rounded-md border">
            <Table>
              <TableHeader>
                {table.getHeaderGroups().map((group) => (
                  <TableRow key={group.id} className="group/row">
                    {group.headers.map((header) => (
                      <TableHead
                        key={header.id}
                        colSpan={header.colSpan}
                        className={cn(
                          header.column.columnDef.meta?.className,
                          header.column.columnDef.meta?.thClassName,
                        )}
                      >
                        {header.isPlaceholder ? null : flexRender(header.column.columnDef.header, header.getContext())}
                      </TableHead>
                    ))}
                  </TableRow>
                ))}
              </TableHeader>
              <TableBody>
                {table.getRowModel().rows.length ? (
                  table.getRowModel().rows.map((row) => (
                    <TableRow key={row.id} data-state={row.getIsSelected() && "selected"}>
                      {row.getVisibleCells().map((cell) => (
                        <TableCell
                          key={cell.id}
                          className={cn(
                            cell.column.columnDef.meta?.className,
                            cell.column.columnDef.meta?.tdClassName,
                          )}
                        >
                          {flexRender(cell.column.columnDef.cell, cell.getContext())}
                        </TableCell>
                      ))}
                    </TableRow>
                  ))
                ) : (
                  <TableRow>
                    <TableCell colSpan={columns.length} className="h-24 text-center text-muted-foreground">
                      {t("providers.emptyFiltered")}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>

          <DataTablePagination table={table} />

          <DataTableBulkActions table={table}>
            <Button variant="outline" size="sm" onClick={() => void setEnabledMany(selected, true)}>
              <CircleCheck /> {t("providers.enableSelected")}
            </Button>
            <Button variant="outline" size="sm" onClick={() => void setEnabledMany(selected, false)}>
              <CircleOff /> {t("providers.disableSelected")}
            </Button>
            <Button variant="destructive" size="sm" onClick={() => setDeleting(selected)}>
              <Trash2 /> {t("providers.deleteSelected")}
            </Button>
          </DataTableBulkActions>
        </div>
      )}
      </LoadingGate>

      <ProviderSheet
        provider={editing}
        open={open}
        onOpenChange={setOpen}
        protocols={protocolOptions}
        onSaved={() => {
          setOpen(false);
          void reload();
        }}
      />

      <ConfirmDialog
        open={deleting.length > 0}
        onOpenChange={(o) => !o && setDeleting([])}
        title={
          deleting.length > 1
            ? t("providers.bulkDeleteTitle", { count: deleting.length })
            : t("providers.deleteTitle", { name: deleting[0]?.name ?? "" })
        }
        description={t("providers.deleteDescription")}
        onConfirm={remove}
      />
    </>
  );
}

/** Copies the stored key straight to the clipboard; it is never rendered. */
function CredentialCell({ provider }: { provider: Provider }) {
  const t = useT();
  const { toast } = useToast();
  const [busy, setBusy] = React.useState(false);
  const [copied, setCopied] = React.useState(false);
  const showSpinner = useDelayedFlag(busy);

  async function copy() {
    setBusy(true);
    try {
      const res = await api.revealProviderKey(provider.id);
      if (await copyToClipboard(res.secret)) {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      } else {
        toast(t("keys.copyFailed"), "error");
      }
    } catch (err) {
      toast(errorMessage(err), "error");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex items-center gap-1">
      <span className="font-mono text-xs text-muted-foreground">••••••••</span>
      <Button
        variant="ghost"
        size="icon-sm"
        title={t("keys.copyKey")}
        aria-label={t("keys.copyKey")}
        disabled={busy}
        onClick={() => void copy()}
      >
        {showSpinner ? <Spinner decorative className="size-4" /> : copied ? <Check className="text-[--color-success]" /> : <Copy />}
      </Button>
    </div>
  );
}

function RowActions({
  provider,
  testing,
  onEdit,
  onTest,
  onToggle,
  onDelete,
}: {
  provider: Provider;
  testing: boolean;
  onEdit: (p: Provider) => void;
  onTest: (p: Provider) => void;
  onToggle: (p: Provider) => void;
  onDelete: (p: Provider) => void;
}) {
  const t = useT();
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" className="flex size-8 p-0 data-[state=open]:bg-muted">
          <MoreHorizontal className="size-4" />
          <span className="sr-only">{t("table.rowActions")}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        <DropdownMenuItem onClick={() => onEdit(provider)}>
          <Pencil /> {t("common.edit")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onTest(provider)} disabled={testing}>
          <Plug /> {t("providers.testConnection")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onToggle(provider)}>
          {provider.enabled ? <CircleOff /> : <CircleCheck />}
          {provider.enabled ? t("providers.disableSelected") : t("providers.enableSelected")}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onClick={() => onDelete(provider)}>
          <Trash2 /> {t("common.delete")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// secondsLeft renders how much of a cooldown remains. It is recomputed on each
// render rather than ticked: the list already refreshes, and a timer per row
// would be a lot of machinery for a thirty-second badge.
function secondsLeft(until: string): string {
  const ms = new Date(until).getTime() - Date.now();
  return String(Math.max(0, Math.ceil(ms / 1000)));
}
