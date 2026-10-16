// biome-ignore-all lint/a11y/useSemanticElements: only rows in view are rendered, so the table is drawn with ARIA table roles on positioned elements
// biome-ignore-all lint/a11y/useFocusableInteractive: rows and cells are not interactive themselves; the controls inside them are
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Bookmark,
  Columns3,
  ListFilter,
  Search,
  X,
} from "lucide-react";
import { type ReactNode, useEffect, useId, useMemo, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { safeStorage } from "@/design/theme/prefs";
import { cn } from "@/lib/utils";
import { designMessages as m } from "../../messages";
import { Button } from "../ui/button";
import { Checkbox } from "../ui/choice";
import { Badge } from "../ui/display";
import { Field } from "../ui/field";
import { Input } from "../ui/input";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../ui/overlay";
import { Popover, PopoverAnchor, PopoverContent, PopoverTrigger } from "../ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Skeleton } from "../ui/skeleton";
import {
  type Column,
  filterRows,
  emptyState as initialState,
  loadViews,
  nextSort,
  type SavedView,
  saveViews,
  sortRows,
  type TableState,
} from "./model";

export interface DataTableProps<Row> {
  /** Names the table for its saved views; unique across the panel. */
  id: string;
  /** What the table lists, for screen readers: "Sites". */
  label: string;
  rows: readonly Row[];
  rowKey: (row: Row) => string;
  columns: readonly Column<Row>[];
  loading?: boolean;
  /** Actions for the selected rows; rows can only be selected when there are some. */
  bulkActions?: (selected: Row[], clearSelection: () => void) => ReactNode;
  /** What the table shows when there are no rows at all. */
  empty?: ReactNode;
  /** The most the table grows before it scrolls. */
  maxHeight?: string;
  className?: string;
}

const anyValue = "__any";

function rowHeightPx(): number {
  const root = document.documentElement;
  const style = getComputedStyle(root);
  const value = style.getPropertyValue("--rs-row").trim();
  const rem = Number.parseFloat(value);
  const base = Number.parseFloat(style.fontSize) || 16;
  return value.endsWith("rem") && rem > 0 ? rem * base : 44;
}

/**
 * A long list of resources: only the rows in view are rendered, columns
 * sort, rows can be searched, filtered and selected for bulk actions, and
 * the current sort, search, filters and columns can be saved as a view.
 */
export function DataTable<Row>({
  id,
  label,
  rows,
  rowKey,
  columns,
  loading,
  bulkActions,
  empty,
  maxHeight = "70vh",
  className,
}: DataTableProps<Row>) {
  const intl = useIntl();
  const columnIds = useMemo(() => columns.map((c) => c.id), [columns]);
  const [state, setState] = useState<TableState>(initialState);
  const [views, setViews] = useState<SavedView[]>(() => loadViews(id, columnIds, safeStorage()));
  const [activeView, setActiveView] = useState("");
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const scrollRef = useRef<HTMLDivElement>(null);
  const captionId = useId();

  const visible = columns.filter((c, i) => i === 0 || !state.hidden.includes(c.id));
  const shown = useMemo(
    () => sortRows(filterRows(rows, columns, state, intl.locale), columns, state.sort, intl.locale),
    [rows, columns, state, intl.locale],
  );

  // Rows that went away can't stay selected.
  useEffect(() => {
    setSelected((current) => {
      const keys = new Set(rows.map(rowKey));
      const kept = [...current].filter((k) => keys.has(k));
      return kept.length === current.size ? current : new Set(kept);
    });
  }, [rows, rowKey]);

  const virtualizer = useVirtualizer({
    count: shown.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: rowHeightPx,
    overscan: 8,
    initialRect: { width: 1024, height: 640 },
  });

  const selectable = bulkActions !== undefined;
  const template = [
    selectable ? "2.75rem" : null,
    ...visible.map((c) => c.width ?? "minmax(8rem, 1fr)"),
  ]
    .filter(Boolean)
    .join(" ");
  const shownKeys = shown.map(rowKey);
  const selectedShown = shownKeys.filter((k) => selected.has(k)).length;
  const allState: boolean | "indeterminate" =
    selectedShown === 0 ? false : selectedShown === shownKeys.length ? true : "indeterminate";
  const filterColumns = columns.filter((c) => c.filter && c.value);
  const activeFilters = Object.values(state.filters).filter(Boolean).length;
  const update = (change: Partial<TableState>) => {
    setState((s) => ({ ...s, ...change }));
    setActiveView("");
  };
  const clearSelection = () => setSelected(new Set());

  const applyView = (viewId: string) => {
    setActiveView(viewId);
    const view = views.find((v) => v.id === viewId);
    setState(view ? structuredClone(view.state) : initialState);
  };
  const storeViews = (next: SavedView[]) => {
    setViews(next);
    saveViews(id, next, safeStorage());
  };

  const toggleRow = (key: string, on: boolean) => {
    setSelected((current) => {
      const next = new Set(current);
      if (on) {
        next.add(key);
      } else {
        next.delete(key);
      }
      return next;
    });
  };

  const headerCell = (column: Column<Row>) => {
    const sorted = state.sort?.column === column.id ? state.sort.direction : null;
    const SortIcon = sorted === "asc" ? ArrowUp : sorted === "desc" ? ArrowDown : ArrowUpDown;
    return (
      <div
        key={column.id}
        role="columnheader"
        aria-sort={sorted === "asc" ? "ascending" : sorted === "desc" ? "descending" : undefined}
        className={cn(
          "flex min-w-0 items-center px-cell-x font-medium text-muted-foreground text-xs",
          column.align === "end" && "justify-end",
        )}
      >
        {column.sortable && column.value ? (
          <button
            type="button"
            className="-mx-1 inline-flex min-w-0 items-center gap-1 rounded-sm px-1 py-0.5 hover:text-foreground focus-visible:focus-ring"
            onClick={() => update({ sort: nextSort(state.sort, column.id) })}
          >
            <span className="truncate">{column.header}</span>
            <SortIcon
              className={cn("size-3.5 shrink-0", !sorted && "opacity-50")}
              aria-hidden="true"
            />
            <span className="sr-only">
              {intl.formatMessage(
                sorted === "asc"
                  ? m.sortedAscending
                  : sorted === "desc"
                    ? m.sortedDescending
                    : m.sortable,
              )}
            </span>
          </button>
        ) : (
          <span className="truncate">{column.header}</span>
        )}
      </div>
    );
  };

  const selectedRows = shown.filter((row) => selected.has(rowKey(row)));

  return (
    <div data-slot="data-table" className={cn("flex min-w-0 flex-col gap-3", className)}>
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-48 flex-1 sm:max-w-xs">
          <Search
            className="pointer-events-none absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <Input
            type="search"
            value={state.search}
            onChange={(e) => update({ search: e.target.value })}
            placeholder={intl.formatMessage(m.tableSearch)}
            aria-label={intl.formatMessage(m.tableSearch)}
            className="ps-8"
          />
        </div>
        {filterColumns.length > 0 ? (
          <Popover>
            <PopoverTrigger asChild>
              <Button variant="outline">
                <ListFilter aria-hidden="true" />
                {intl.formatMessage(m.tableFilters)}
                {activeFilters > 0 ? (
                  <Badge variant="accent">{intl.formatNumber(activeFilters)}</Badge>
                ) : null}
              </Button>
            </PopoverTrigger>
            <PopoverContent align="start" className="flex w-80 flex-col gap-3">
              {filterColumns.map((column) =>
                column.filter === "text" ? (
                  <Field key={column.id} label={column.header}>
                    <Input
                      value={state.filters[column.id] ?? ""}
                      onChange={(e) =>
                        update({ filters: { ...state.filters, [column.id]: e.target.value } })
                      }
                    />
                  </Field>
                ) : (
                  <Field key={column.id} label={column.header}>
                    {(control) => (
                      <Select
                        value={state.filters[column.id] || anyValue}
                        onValueChange={(v) =>
                          update({
                            filters: { ...state.filters, [column.id]: v === anyValue ? "" : v },
                          })
                        }
                      >
                        <SelectTrigger {...control}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value={anyValue}>{intl.formatMessage(m.tableAny)}</SelectItem>
                          {typeof column.filter === "object"
                            ? column.filter.options.map((o) => (
                                <SelectItem key={o.value} value={o.value}>
                                  {o.label}
                                </SelectItem>
                              ))
                            : null}
                        </SelectContent>
                      </Select>
                    )}
                  </Field>
                ),
              )}
              <Button
                variant="ghost"
                size="sm"
                className="self-start"
                onClick={() => update({ filters: {} })}
              >
                {intl.formatMessage(m.tableClearFilters)}
              </Button>
            </PopoverContent>
          </Popover>
        ) : null}
        <ViewsMenu
          views={views}
          activeView={activeView}
          onApply={applyView}
          onSave={(name) => {
            const view: SavedView = {
              id: crypto.randomUUID(),
              name,
              state: structuredClone(state),
            };
            storeViews([...views, view]);
            setActiveView(view.id);
          }}
          onDelete={(viewId) => {
            storeViews(views.filter((v) => v.id !== viewId));
            setActiveView("");
          }}
        />
        {columns.some((c, i) => i > 0 && c.hideable !== false) ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline">
                <Columns3 aria-hidden="true" />
                {intl.formatMessage(m.tableColumns)}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {columns.slice(1).map((c) =>
                c.hideable === false ? null : (
                  <DropdownMenuCheckboxItem
                    key={c.id}
                    checked={!state.hidden.includes(c.id)}
                    onCheckedChange={(on) =>
                      update({
                        hidden: on
                          ? state.hidden.filter((h) => h !== c.id)
                          : [...state.hidden, c.id],
                      })
                    }
                    onSelect={(e) => e.preventDefault()}
                  >
                    {c.header}
                  </DropdownMenuCheckboxItem>
                ),
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        ) : null}
        <p className="ms-auto text-muted-foreground text-sm" aria-live="polite">
          {loading ? null : intl.formatMessage(m.tableRows, { count: shown.length })}
        </p>
      </div>

      {selectable && selected.size > 0 ? (
        <div
          role="toolbar"
          aria-label={intl.formatMessage(m.tableBulkActions)}
          className="flex flex-wrap items-center gap-2 rounded-md border border-primary/40 bg-primary-subtle px-3 py-2"
        >
          <span className="font-medium text-sm">
            {intl.formatMessage(m.tableSelected, { count: selected.size })}
          </span>
          <div className="flex flex-wrap gap-2">{bulkActions?.(selectedRows, clearSelection)}</div>
          <Button variant="ghost" size="sm" className="ms-auto" onClick={clearSelection}>
            <X aria-hidden="true" />
            {intl.formatMessage(m.tableClearSelection)}
          </Button>
        </div>
      ) : null}

      {!loading && rows.length === 0 && empty ? (
        empty
      ) : (
        <div
          ref={scrollRef}
          role="table"
          aria-labelledby={captionId}
          aria-rowcount={shown.length + 1}
          aria-busy={loading || undefined}
          className="relative overflow-auto rounded-lg border border-border bg-card"
          style={{ maxHeight }}
        >
          <span id={captionId} className="sr-only">
            {label}
          </span>
          <div
            role="rowgroup"
            className="sticky top-0 z-10 border-border border-b bg-surface-sunken"
          >
            <div
              role="row"
              aria-rowindex={1}
              className="grid h-row items-center"
              style={{ gridTemplateColumns: template }}
            >
              {selectable ? (
                <div role="columnheader" className="flex items-center justify-center">
                  <Checkbox
                    checked={allState}
                    disabled={shown.length === 0}
                    onCheckedChange={(on) =>
                      setSelected((current) => {
                        const next = new Set(current);
                        for (const k of shownKeys) {
                          if (on === true) {
                            next.add(k);
                          } else {
                            next.delete(k);
                          }
                        }
                        return next;
                      })
                    }
                    aria-label={intl.formatMessage(m.tableSelectAll)}
                  />
                </div>
              ) : null}
              {visible.map(headerCell)}
            </div>
          </div>
          {loading ? (
            <div role="rowgroup">
              {["a", "b", "c", "d", "e"].map((placeholder) => (
                <div
                  key={placeholder}
                  role="row"
                  className="grid h-row items-center border-border border-b"
                  style={{ gridTemplateColumns: template }}
                >
                  {selectable ? <div role="cell" /> : null}
                  {visible.map((c) => (
                    <div key={c.id} role="cell" className="px-cell-x">
                      <Skeleton className="h-4 w-3/4" />
                    </div>
                  ))}
                </div>
              ))}
            </div>
          ) : shown.length === 0 ? (
            <div className="flex flex-col items-center gap-2 px-4 py-10 text-center">
              <p className="text-muted-foreground text-sm">
                {intl.formatMessage(m.tableNoMatches)}
              </p>
              <Button
                variant="outline"
                size="sm"
                onClick={() => update({ search: "", filters: {} })}
              >
                {intl.formatMessage(m.tableClearFilters)}
              </Button>
            </div>
          ) : (
            <div
              role="rowgroup"
              className="relative"
              style={{ height: virtualizer.getTotalSize() }}
            >
              {virtualizer.getVirtualItems().map((item) => {
                const row = shown[item.index] as Row;
                const key = rowKey(row);
                const isSelected = selected.has(key);
                return (
                  <div
                    key={key}
                    role="row"
                    aria-rowindex={item.index + 2}
                    aria-selected={selectable ? isSelected : undefined}
                    data-index={item.index}
                    data-state={isSelected ? "selected" : undefined}
                    ref={virtualizer.measureElement}
                    className="absolute inset-x-0 top-0 grid min-h-row items-center border-border border-b hover:bg-muted/60 data-[state=selected]:bg-primary-subtle"
                    style={{
                      gridTemplateColumns: template,
                      transform: `translateY(${item.start}px)`,
                    }}
                  >
                    {selectable ? (
                      <div role="cell" className="flex items-center justify-center">
                        <Checkbox
                          checked={isSelected}
                          onCheckedChange={(on) => toggleRow(key, on === true)}
                          aria-label={intl.formatMessage(m.tableSelectRow, {
                            row: String(visible[0]?.value?.(row) ?? key),
                          })}
                        />
                      </div>
                    ) : null}
                    {visible.map((c) => (
                      <div
                        key={c.id}
                        role="cell"
                        className={cn(
                          "min-w-0 break-words px-cell-x py-cell-y text-sm",
                          c.align === "end" && "text-end tabular-nums",
                        )}
                      >
                        {c.cell ? c.cell(row) : String(c.value?.(row) ?? "")}
                      </div>
                    ))}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function ViewsMenu({
  views,
  activeView,
  onApply,
  onSave,
  onDelete,
}: {
  views: SavedView[];
  activeView: string;
  onApply: (id: string) => void;
  onSave: (name: string) => void;
  onDelete: (id: string) => void;
}) {
  const intl = useIntl();
  const [saving, setSaving] = useState(false);
  const [name, setName] = useState("");
  // The menu gives focus back to its button as it closes, which would take
  // it from the form that opens instead.
  const openingForm = useRef(false);
  const current = views.find((v) => v.id === activeView);
  return (
    <Popover open={saving} onOpenChange={setSaving}>
      <PopoverAnchor asChild>
        <div>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline">
                <Bookmark aria-hidden="true" />
                {current?.name ?? intl.formatMessage(m.tableViews)}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="start"
              onCloseAutoFocus={(e) => {
                if (openingForm.current) {
                  e.preventDefault();
                  openingForm.current = false;
                }
              }}
            >
              <DropdownMenuLabel>{intl.formatMessage(m.tableViews)}</DropdownMenuLabel>
              <DropdownMenuRadioGroup value={activeView} onValueChange={onApply}>
                <DropdownMenuRadioItem value="">
                  {intl.formatMessage(m.tableAllRows)}
                </DropdownMenuRadioItem>
                {views.map((v) => (
                  <DropdownMenuRadioItem key={v.id} value={v.id}>
                    {v.name}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onSelect={() => {
                  openingForm.current = true;
                  setSaving(true);
                }}
              >
                {intl.formatMessage(m.tableSaveView)}
              </DropdownMenuItem>
              {current ? (
                <DropdownMenuItem destructive onSelect={() => onDelete(current.id)}>
                  {intl.formatMessage(m.tableDeleteView, { name: current.name })}
                </DropdownMenuItem>
              ) : null}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </PopoverAnchor>
      <PopoverContent align="start">
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim() !== "") {
              onSave(name.trim());
              setName("");
              setSaving(false);
            }
          }}
        >
          <Field label={intl.formatMessage(m.tableViewName)}>
            <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          <Button type="submit" size="sm" className="self-end" disabled={name.trim() === ""}>
            {intl.formatMessage(m.tableSaveView)}
          </Button>
        </form>
      </PopoverContent>
    </Popover>
  );
}
