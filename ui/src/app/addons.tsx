import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { ApiError, type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";

/*
 * What add-ons add to the panel declaratively: navigation entries, tabs on
 * a kind's detail screen, home cards, command-bar actions, settings pages
 * and columns of a kind's list, as GET /api/v1/addons/entries lists those
 * of the add-ons the person may use. An add-on's pages are its own: they
 * load in a sandboxed frame without the panel's origin, so they can never
 * read the panel's session. The panel sends the frame on to an address of
 * its own, which signs in what the page loads and asks for there.
 *
 * The shell only lists the entries. What shows them, the frames, the icons
 * entries name and the columns' values, loads apart from the shell, so the
 * first screen of a panel without add-ons downloads none of it.
 */

/** An entry an add-on adds to the panel. */
export type AddonEntry = Schemas["UIEntry"];

/** Where in the panel an entry goes, such as ui.nav. */
export type AddonPoint = AddonEntry["point"];

/**
 * The query of the add-ons' entries, which the columns' values go under:
 * read again once add-ons change, as installing, turning off or removing
 * one does, and as safe mode does.
 */
export const addonEntriesKey = ["addons", "entries"] as const;
const noEntries: AddonEntry[] = [];

/**
 * The entries of the add-ons the person may use, at one place or all of
 * them. A panel without add-ons, or one that doesn't list them, has none.
 */
export function useAddonEntries(point?: AddonPoint): AddonEntry[] {
  const api = useApi();
  const entries = useQuery({
    queryKey: addonEntriesKey,
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

/** An entry's path without the slashes it may start with. */
export function addonPath(path: string | undefined): string {
  return (path ?? "").replace(/^\/+/, "");
}

/** Where the panel shows the add-on page an entry opens. */
export function addonScreenPath(entry: Pick<AddonEntry, "addon" | "ui">): string {
  return `/addons/${encodeURIComponent(entry.addon)}/${addonPath(entry.ui.path)}`;
}
