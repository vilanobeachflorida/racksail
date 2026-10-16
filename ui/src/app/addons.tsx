import { useQueries, useQuery } from "@tanstack/react-query";
import {
  Activity,
  Bell,
  ChartLine,
  Cloud,
  Database,
  FileText,
  Globe,
  HardDrive,
  KeyRound,
  Lock,
  type LucideIcon,
  Mail,
  Puzzle,
  Server,
  Shield,
  ShieldCheck,
  Users,
  Wrench,
} from "lucide-react";
import type { Client } from "openapi-fetch";
import { useMemo } from "react";
import { type ApiClient, ApiError, type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { cn } from "@/lib/utils";

/*
 * What add-ons add to the panel declaratively: navigation entries, tabs on
 * a kind's detail screen, home cards, command-bar actions, settings pages
 * and columns of a kind's list, as GET /api/v1/addons/entries lists those
 * of the add-ons the person may use. An add-on's pages are its own: they
 * load in a sandboxed frame without the panel's origin, so they can never
 * read the panel's session. The panel sends the frame on to an address of
 * its own, which signs in what the page loads and asks for there.
 */

/** An entry an add-on adds to the panel. */
export type AddonEntry = Schemas["UIEntry"];

/** Where in the panel an entry goes, such as ui.nav. */
export type AddonPoint = AddonEntry["point"];

const entriesKey = ["addons", "entries"] as const;
const noEntries: AddonEntry[] = [];

/**
 * The entries of the add-ons the person may use, at one place or all of
 * them. A panel without add-ons, or one that doesn't list them, has none.
 */
export function useAddonEntries(point?: AddonPoint): AddonEntry[] {
  const api = useApi();
  const entries = useQuery({
    queryKey: entriesKey,
    queryFn: async () => {
      try {
        return (await unwrap(api.GET("/api/v1/addons/entries"))).entries ?? [];
      } catch (error) {
        if (error instanceof ApiError && [401, 403, 404].includes(error.status)) {
          return [];
        }
        throw error;
      }
    },
    staleTime: 60_000,
  });
  const all = entries.data ?? noEntries;
  return useMemo(() => (point ? all.filter((e) => e.point === point) : all), [all, point]);
}

/** Tells an entry apart from every other, for keys and tab values. */
export function addonEntryKey(entry: AddonEntry): string {
  return `${entry.point}:${entry.addon}:${entry.ui.path ?? ""}:${entry.ui.label}`;
}

function trimmed(path: string | undefined): string {
  return (path ?? "").replace(/^\/+/, "");
}

/**
 * The icons an entry can name, by their names in Lucide. The panel bundles
 * these few rather than every icon; any other name shows the add-on icon.
 */
export const addonIcons: Readonly<Record<string, LucideIcon>> = {
  activity: Activity,
  bell: Bell,
  "chart-line": ChartLine,
  cloud: Cloud,
  database: Database,
  "file-text": FileText,
  globe: Globe,
  "hard-drive": HardDrive,
  "key-round": KeyRound,
  lock: Lock,
  mail: Mail,
  puzzle: Puzzle,
  server: Server,
  shield: Shield,
  "shield-check": ShieldCheck,
  users: Users,
  wrench: Wrench,
};

/** The icon of an add-on's entry. */
export function addonIcon(name: string | undefined): LucideIcon {
  return (name !== undefined && Object.hasOwn(addonIcons, name) && addonIcons[name]) || Puzzle;
}

/** Where the panel shows the add-on page an entry opens. */
export function addonScreenPath(entry: Pick<AddonEntry, "addon" | "ui">): string {
  return `/addons/${encodeURIComponent(entry.addon)}/${trimmed(entry.ui.path)}`;
}

/** The address of one of an add-on's pages, which its frame loads. */
export function addonPageSrc(addon: string, path: string, params?: Record<string, string>): string {
  const rest = trimmed(path).split("/").map(encodeURIComponent).join("/");
  const query = params && Object.keys(params).length > 0 ? `?${new URLSearchParams(params)}` : "";
  return `/api/v1/addons/${encodeURIComponent(addon)}/ui/${rest}${query}`;
}

/**
 * One of an add-on's pages, in a frame that runs its scripts without the
 * panel's origin, so it can't reach the panel's cookies or pages; its own
 * address lets it load what it serves and call the add-on's routes.
 */
export function AddonFrame({
  addon,
  path,
  title,
  params,
  className,
}: {
  addon: string;
  path: string;
  title: string;
  params?: Record<string, string>;
  className?: string;
}) {
  return (
    <iframe
      src={addonPageSrc(addon, path, params)}
      title={title}
      sandbox="allow-scripts allow-forms"
      referrerPolicy="no-referrer"
      loading="lazy"
      className={cn("w-full rounded-md border border-border bg-background", className)}
    />
  );
}

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
  const rest = trimmed(entry.ui.path).split("/").map(encodeURIComponent).join("/");
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

/** The columns add-ons add to a kind's list, with their values for some resources. */
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
      queryKey: [...entriesKey, "column", entry.addon, trimmed(entry.ui.path), sorted],
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
