import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { MoreHorizontal, Pencil, Plus, Trash2 } from "lucide-react";
import { type ReactNode, useEffect, useMemo } from "react";
import { type IntlShape, useIntl } from "react-intl";
import { usePermission } from "@/app/modules";
import { DataTable } from "@/design/components/data-table/data-table";
import type { CellValue, Column } from "@/design/components/data-table/model";
import { Button } from "@/design/components/ui/button";
import { Alert } from "@/design/components/ui/display";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/design/components/ui/overlay";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { humanize } from "@/lib/forms/model";
import type { JSONSchema } from "@/lib/schema";
import { resourceMessages as m } from "../messages";
import { useResourceChanges } from "./actions";
import {
  actionTitle,
  fieldLabel,
  type Kind,
  kindDescription,
  kindNoun,
  kindNouns,
  kindPath,
  kindPlural,
  type Phase,
  phaseOf,
  type Resource,
  resourcePath,
  resourceSubtitle,
  resourceTitle,
  useKindSchemas,
  valueAt,
} from "./kinds";
import { useRouteKind } from "./layout";
import { listCap, useResourceList } from "./queries";
import { ColumnValue, PhaseBadge, When } from "./values";

const phases: Phase[] = ["pending", "applying", "ready", "degraded", "failed", "deleting"];

/** A column's heading: the field's label in the kind's catalog, its schema's title, or its name. */
function columnHeader(
  intl: IntlShape,
  kind: Kind,
  field: string,
  schemas: { spec?: unknown; status?: unknown } | undefined,
): string {
  const [part, ...rest] = field.split(".");
  const path = rest.join(".");
  if (part === "spec" || part === "status") {
    const label = fieldLabel(intl, kind.name, path, part === "spec" ? "fields" : "status");
    if (label) {
      return label;
    }
  }
  const schema = (part === "status" ? schemas?.status : schemas?.spec) as JSONSchema | undefined;
  let node: JSONSchema | undefined = schema;
  for (const key of rest) {
    node = node?.properties?.[key];
  }
  if (field === "status.phase") {
    return intl.formatMessage(m.columnState);
  }
  return node?.title ?? humanize(rest.at(-1) ?? field);
}

/** A cell's value for sorting and filtering: lists sort by their first item. */
function sortable(value: unknown): CellValue {
  if (Array.isArray(value)) {
    return value.map((v) => String(v)).join(", ");
  }
  if (value === null || value === undefined) {
    return undefined;
  }
  return typeof value === "object" ? JSON.stringify(value) : (value as CellValue);
}

/** The columns of a kind's list, from its UI hints. */
export function kindColumns(
  intl: IntlShape,
  kind: Kind,
  kinds: readonly Kind[],
  schemas: { spec?: unknown; status?: unknown } | undefined,
  menu: (resource: Resource) => ReactNode,
): Column<Resource>[] {
  const columns: Column<Resource>[] = [
    {
      id: "name",
      header: intl.formatMessage(m.columnName),
      value: (r) => resourceTitle(kind, r),
      sortable: true,
      width: "minmax(14rem, 2fr)",
      cell: (r) => {
        const subtitle = resourceSubtitle(kind, r);
        return (
          <div className="flex min-w-0 flex-col">
            <Link
              to={resourcePath(kind, r.id)}
              className="truncate rounded-sm font-medium text-primary underline-offset-4 hover:underline focus-visible:focus-ring"
            >
              {resourceTitle(kind, r)}
            </Link>
            {subtitle ? (
              <span className="truncate text-muted-foreground text-xs">{subtitle}</span>
            ) : null}
          </div>
        );
      },
    },
  ];
  const fields = (kind.ui.columns ?? []).filter(
    (c) => c.field !== `spec.${kind.ui.titleField ?? ""}`,
  );
  const hasPhase = fields.some((c) => c.field === "status.phase");
  for (const column of fields) {
    columns.push({
      id: column.field,
      header: columnHeader(intl, kind, column.field, schemas),
      value: (r) =>
        column.field === "status.phase" ? phaseOf(r) : sortable(valueAt(r, column.field)),
      sortable: true,
      ...(column.field === "status.phase"
        ? {
            filter: {
              options: phases.map((p) => ({
                value: p,
                label: intl.formatMessage(m.phase, { phase: p }),
              })),
            },
          }
        : column.format === "badge" || column.format === "link" || !column.format
          ? { filter: "text" as const }
          : {}),
      cell: (r) =>
        column.field === "status.phase" ? (
          <PhaseBadge phase={phaseOf(r)} />
        ) : (
          <ColumnValue value={valueAt(r, column.field)} format={column.format} kinds={kinds} />
        ),
    });
  }
  if (!hasPhase) {
    columns.push({
      id: "status.phase",
      header: intl.formatMessage(m.columnState),
      value: (r) => phaseOf(r),
      sortable: true,
      filter: {
        options: phases.map((p) => ({
          value: p,
          label: intl.formatMessage(m.phase, { phase: p }),
        })),
      },
      cell: (r) => <PhaseBadge phase={phaseOf(r)} />,
    });
  }
  if (kind.scope !== "server") {
    columns.push({
      id: "scope",
      header: intl.formatMessage(m.columnOwner),
      value: (r) => r.scope.id ?? "",
      sortable: true,
      filter: "text",
      cell: (r) => <code className="font-mono text-xs">{r.scope.id}</code>,
    });
  }
  columns.push(
    {
      id: "updatedAt",
      header: intl.formatMessage(m.columnChanged),
      value: (r) => new Date(r.updatedAt),
      sortable: true,
      cell: (r) => <When time={r.updatedAt} />,
    },
    {
      id: "actions",
      header: intl.formatMessage(m.columnActions),
      width: "4rem",
      align: "end",
      hideable: false,
      cell: menu,
    },
  );
  return columns;
}

/**
 * A kind's resources, until its module has a screen of its own: a table
 * to search, filter, sort and save views of, kept current as resources
 * change, with each one's actions and actions for several at once, which
 * go through the change preview as one change.
 */
export default function KindList() {
  const intl = useIntl();
  const navigate = useNavigate();
  const allowed = usePermission();
  const search = useSearch({ strict: false }) as { new?: unknown };
  const { kind, kinds } = useRouteKind();
  const list = useResourceList(kind);
  const schemas = useKindSchemas(kind?.name);
  const changes = useResourceChanges();
  const newPath = kind ? `${kindPath(kind)}/new` : "";

  // Commands link to a kind's list with ?new=1 to create one.
  useEffect(() => {
    if (kind && search.new) {
      void navigate({ to: newPath, replace: true });
    }
  }, [kind, search.new, navigate, newPath]);

  const columns = useMemo(() => {
    if (!kind) {
      return [];
    }
    const may = (operation: string) => allowed(`${kind.name}.${operation}`);
    return kindColumns(intl, kind, kinds, schemas.data, (r) => (
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={intl.formatMessage(m.rowActions, { name: resourceTitle(kind, r) })}
          >
            <MoreHorizontal aria-hidden="true" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem asChild>
            <Link to={resourcePath(kind, r.id)}>{intl.formatMessage(m.open)}</Link>
          </DropdownMenuItem>
          {may("update") && !r.deletedAt ? (
            <DropdownMenuItem asChild>
              <Link to={`${resourcePath(kind, r.id)}/edit`}>
                <Pencil aria-hidden="true" />
                {intl.formatMessage(m.edit)}
              </Link>
            </DropdownMenuItem>
          ) : null}
          {(kind.actions ?? [])
            .filter((a) => may(a.name) && !r.deletedAt)
            .map((a) => (
              <DropdownMenuItem
                key={a.name}
                onSelect={() => changes.launch({ kind, resources: [r], action: a })}
              >
                {actionTitle(intl, kind, a)}
              </DropdownMenuItem>
            ))}
          {may("delete") && !r.deletedAt ? (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                destructive
                onSelect={() => changes.launch({ kind, resources: [r] })}
              >
                <Trash2 aria-hidden="true" />
                {intl.formatMessage(m.delete)}
              </DropdownMenuItem>
            </>
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>
    ));
  }, [intl, kind, kinds, schemas.data, allowed, changes]);

  if (!kind) {
    return null;
  }
  const may = (operation: string) => allowed(`${kind.name}.${operation}`);
  const plural = kindPlural(intl, kind);
  const items = list.data?.items ?? [];
  const canCreate = may("create") && !(kind.singleton && items.length > 0);
  const create = canCreate ? (
    <Button asChild>
      <Link to={newPath}>
        <Plus aria-hidden="true" />
        {intl.formatMessage(m.create, { kind: kindNoun(intl, kind) })}
      </Link>
    </Button>
  ) : null;
  // Every action the person may run works on several at once, those that
  // take input with the same input for each.
  const bulk = (kind.actions ?? []).filter((a) => may(a.name));
  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <h1 className="font-semibold text-2xl">{plural}</h1>
          <p className="max-w-prose text-muted-foreground">{kindDescription(intl, kind)}</p>
        </div>
        {create}
      </header>
      {list.data?.truncated ? (
        <Alert tone="info">{intl.formatMessage(m.truncated, { count: listCap })}</Alert>
      ) : null}
      {list.isError ? (
        <ErrorState
          title={intl.formatMessage(m.listFailed, { kinds: kindNouns(intl, kind) })}
          error={list.error}
          onRetry={() => void list.refetch()}
        />
      ) : (
        <DataTable
          id={`kind.${kind.name}`}
          label={plural}
          rows={items}
          rowKey={(r) => r.id}
          columns={columns}
          loading={list.isPending}
          bulkActions={(selected, clear) => {
            // What is being removed already takes no more changes.
            const live = selected.filter((r) => !r.deletedAt);
            return (
              <>
                {bulk.map((a) => (
                  <Button
                    key={a.name}
                    variant="outline"
                    size="sm"
                    disabled={live.length === 0}
                    onClick={() =>
                      changes.launch({ kind, resources: live, action: a, onApplied: clear })
                    }
                  >
                    {actionTitle(intl, kind, a)}
                  </Button>
                ))}
                {may("delete") ? (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={live.length === 0}
                    onClick={() => changes.launch({ kind, resources: live, onApplied: clear })}
                  >
                    <Trash2 aria-hidden="true" />
                    {intl.formatMessage(m.delete)}
                  </Button>
                ) : null}
              </>
            );
          }}
          empty={
            <EmptyState
              title={intl.formatMessage(m.empty, { kinds: kindNouns(intl, kind) })}
              description={intl.formatMessage(m.emptyHelp, { kind: kindNoun(intl, kind) })}
              action={create}
            />
          }
        />
      )}
      {changes.dialog}
    </div>
  );
}
