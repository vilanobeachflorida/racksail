import type { ReactNode } from "react";

/** A value a column sorts and filters by. */
export type CellValue = string | number | boolean | Date | null | undefined;

export interface ColumnFilterOption {
  value: string;
  label: string;
}

export interface Column<Row> {
  /** Unique within the table; saved views refer to columns by it. */
  id: string;
  /** The column's heading, translated. */
  header: string;
  /** The value the column sorts and filters by, and shows when there's no cell. */
  value?: (row: Row) => CellValue;
  /** What the cell shows; by default the value, as text. */
  cell?: (row: Row) => ReactNode;
  sortable?: boolean;
  /**
   * A filter for this column: free text, or a choice among options, such as
   * the states a resource can be in.
   */
  filter?: "text" | { options: ColumnFilterOption[] };
  /** The column's width as a CSS grid track, such as "minmax(12rem, 2fr)". */
  width?: string;
  align?: "start" | "end";
  /** Whether people may hide the column. The first column can't be hidden. */
  hideable?: boolean;
}

export interface Sort {
  column: string;
  direction: "asc" | "desc";
}

/** What a table shows: its sort, search, filters and hidden columns. */
export interface TableState {
  sort: Sort | null;
  search: string;
  filters: Record<string, string>;
  hidden: string[];
}

export const emptyState: TableState = { sort: null, search: "", filters: {}, hidden: [] };

/** A table state someone named to come back to. */
export interface SavedView {
  id: string;
  name: string;
  state: TableState;
}

function asText(value: CellValue, locale: string): string {
  if (value === null || value === undefined) {
    return "";
  }
  if (value instanceof Date) {
    return value.toISOString();
  }
  return String(value).toLocaleLowerCase(locale);
}

/**
 * The rows that match the search, across every column with a value, and
 * each column filter.
 */
export function filterRows<Row>(
  rows: readonly Row[],
  columns: readonly Column<Row>[],
  state: TableState,
  locale: string,
): Row[] {
  const search = state.search.trim().toLocaleLowerCase(locale);
  const active = columns.filter((c) => c.value && state.filters[c.id]);
  if (search === "" && active.length === 0) {
    return [...rows];
  }
  return rows.filter((row) => {
    if (search !== "") {
      const hit = columns.some((c) => c.value && asText(c.value(row), locale).includes(search));
      if (!hit) {
        return false;
      }
    }
    return active.every((c) => {
      const wanted = state.filters[c.id] ?? "";
      const value = c.value?.(row);
      if (c.filter === "text") {
        return asText(value, locale).includes(wanted.toLocaleLowerCase(locale));
      }
      return String(value ?? "") === wanted;
    });
  });
}

/**
 * The rows in the state's order. Text compares the way the locale sorts,
 * with numbers inside text in numeric order; empty values go last either
 * way.
 */
export function sortRows<Row>(
  rows: readonly Row[],
  columns: readonly Column<Row>[],
  sort: Sort | null,
  locale: string,
): Row[] {
  const column = sort ? columns.find((c) => c.id === sort.column) : undefined;
  if (!sort || !column?.value) {
    return [...rows];
  }
  const get = column.value;
  const collator = new Intl.Collator(locale, { numeric: true, sensitivity: "base" });
  const direction = sort.direction === "asc" ? 1 : -1;
  return rows
    .map((row, index) => ({ row, index, value: get(row) }))
    .sort((a, b) => {
      const av = a.value;
      const bv = b.value;
      const aEmpty = av === null || av === undefined || av === "";
      const bEmpty = bv === null || bv === undefined || bv === "";
      if (aEmpty || bEmpty) {
        return aEmpty === bEmpty ? a.index - b.index : aEmpty ? 1 : -1;
      }
      let cmp: number;
      if (av instanceof Date && bv instanceof Date) {
        cmp = av.getTime() - bv.getTime();
      } else if (typeof av === "number" && typeof bv === "number") {
        cmp = av - bv;
      } else if (typeof av === "boolean" && typeof bv === "boolean") {
        cmp = Number(av) - Number(bv);
      } else {
        cmp = collator.compare(String(av), String(bv));
      }
      return cmp * direction || a.index - b.index;
    })
    .map((entry) => entry.row);
}

/** The next sort when a column's heading is pressed: ascending, descending, none. */
export function nextSort(current: Sort | null, column: string): Sort | null {
  if (current?.column !== column) {
    return { column, direction: "asc" };
  }
  return current.direction === "asc" ? { column, direction: "desc" } : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Reads a stored state, keeping only what refers to the table's columns. */
export function normalizeState(value: unknown, columnIds: readonly string[]): TableState {
  if (!isRecord(value)) {
    return { ...emptyState };
  }
  const known = new Set(columnIds);
  let sort: Sort | null = null;
  if (
    isRecord(value.sort) &&
    typeof value.sort.column === "string" &&
    known.has(value.sort.column) &&
    (value.sort.direction === "asc" || value.sort.direction === "desc")
  ) {
    sort = { column: value.sort.column, direction: value.sort.direction };
  }
  const filters: Record<string, string> = {};
  if (isRecord(value.filters)) {
    for (const [k, v] of Object.entries(value.filters)) {
      if (known.has(k) && typeof v === "string" && v !== "") {
        filters[k] = v;
      }
    }
  }
  const hidden = Array.isArray(value.hidden)
    ? value.hidden.filter((h): h is string => typeof h === "string" && known.has(h))
    : [];
  return { sort, search: typeof value.search === "string" ? value.search : "", filters, hidden };
}

/** Where a table's saved views are kept in this browser. */
export function viewsKey(tableId: string): string {
  return `racksail.table.${tableId}.views`;
}

/** The views saved for a table in this browser. */
export function loadViews(
  tableId: string,
  columnIds: readonly string[],
  storage: Storage | null,
): SavedView[] {
  try {
    const raw = storage?.getItem(viewsKey(tableId));
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed
      .filter(
        (v): v is Record<string, unknown> =>
          isRecord(v) && typeof v.id === "string" && typeof v.name === "string",
      )
      .map((v) => ({
        id: v.id as string,
        name: v.name as string,
        state: normalizeState(v.state, columnIds),
      }));
  } catch {
    return [];
  }
}

/** Saves a table's views; without storage they last until the page closes. */
export function saveViews(tableId: string, views: readonly SavedView[], storage: Storage | null) {
  try {
    storage?.setItem(viewsKey(tableId), JSON.stringify(views));
  } catch {
    // Private windows can refuse storage; the views still work for now.
  }
}
