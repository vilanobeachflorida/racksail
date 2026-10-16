import { useQueries } from "@tanstack/react-query";
import type { Client } from "openapi-fetch";
import { useMemo } from "react";
import { type AddonEntry, addonEntriesKey, addonPath, useAddonEntries } from "@/app/addons";
import type { ApiClient } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";

/** How many resources one request for a column's values names, so addresses stay short. */
export const columnBatch = 100;

/*
 * The routes an add-on serves aren't in the panel's OpenAPI document, since
 * each add-on declares its own; a column's route is called through the same
 * client as everything else and its answer checked here.
 */
type ColumnPath = `/api/v1/addons/${string}`;
interface ColumnPathItem {
  parameters: { query?: never; header?: never; path?: never; cookie?: never };
  get: {
    parameters: { query: { ids: string }; header?: never; path?: never; cookie?: never };
    requestBody?: never;
    responses: {
      200: { headers: Record<string, unknown>; content: { "application/json": unknown } };
      default: {
        headers: Record<string, unknown>;
        content: { "application/problem+json": unknown };
      };
    };
  };
}
const columnClient = (api: ApiClient) =>
  api as unknown as Client<Record<ColumnPath, ColumnPathItem>>;

/** The route an add-on's column answers at. */
export function columnPath(entry: Pick<AddonEntry, "addon" | "ui">): ColumnPath {
  const rest = addonPath(entry.ui.path).split("/").map(encodeURIComponent).join("/");
  return `/api/v1/addons/${encodeURIComponent(entry.addon)}/${rest}`;
}

/** The text values in an answer of an add-on's column; anything else in it is ignored. */
export function columnText(body: unknown): Record<string, string> {
  const found =
    typeof body === "object" && body !== null ? (body as { values?: unknown }).values : undefined;
  if (typeof found !== "object" || found === null || Array.isArray(found)) {
    return {};
  }
  return Object.fromEntries(
    Object.entries(found).filter((e): e is [string, string] => typeof e[1] === "string"),
  );
}

/**
 * The values of an add-on's column for some resources, from the API route
 * its entry names, which answers {"values": {"<id>": "<text>"}} for
 * ?ids=<id>,<id>. What it can't answer shows as nothing.
 */
async function columnValues(
  api: ApiClient,
  entry: AddonEntry,
  ids: readonly string[],
): Promise<Record<string, string>> {
  const batches: string[][] = [];
  for (let i = 0; i < ids.length; i += columnBatch) {
    batches.push(ids.slice(i, i + columnBatch));
  }
  const answers = await Promise.all(
    batches.map(async (batch) => {
      const { data, response } = await columnClient(api).GET(columnPath(entry), {
        params: { query: { ids: batch.join(",") } },
      });
      return response.ok ? columnText(data) : {};
    }),
  );
  return Object.assign({}, ...answers);
}

/** A column an add-on adds to a kind's list, with its values for the rows shown. */
export type AddonColumn = { entry: AddonEntry; values: Record<string, string> };

/** Each query's values; useQueries keeps the result the same while they don't change. */
function valuesOf(results: readonly { data?: Record<string, string> }[]): Record<string, string>[] {
  return results.map((r) => r.data ?? {});
}

/**
 * The columns add-ons add to a kind's list, with their values for some
 * resources. The values go under the add-ons' entries, so they're read
 * again when add-ons change.
 */
export function useAddonColumns(kind: string | undefined, ids: readonly string[]): AddonColumn[] {
  const api = useApi();
  const all = useAddonEntries("ui.column");
  const entries = useMemo(
    () => all.filter((e) => kind !== undefined && e.ui.kind === kind),
    [all, kind],
  );
  const sorted = useMemo(() => [...ids].sort(), [ids]);
  const values = useQueries({
    queries: entries.map((entry) => ({
      queryKey: [...addonEntriesKey, "column", entry.addon, addonPath(entry.ui.path), sorted],
      queryFn: () => columnValues(api, entry, sorted),
      enabled: sorted.length > 0,
      staleTime: 30_000,
    })),
    combine: valuesOf,
  });
  return useMemo(
    () => entries.map((entry, i) => ({ entry, values: values[i] ?? {} })),
    [entries, values],
  );
}
