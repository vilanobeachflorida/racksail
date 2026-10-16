import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { ApiError } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { useLiveEvents } from "@/lib/live";
import { getHistory, getResource, listResources, type ResourceQuery } from "./api";
import type { Kind, Phase, Resource } from "./kinds";

/** Where the panel says what changed among the resources the person can see. */
export const resourceStream = "/api/v1/resources/stream";

/** A committed write to a resource, as the resource stream tells it. */
export interface Notice {
  op: "created" | "updated" | "status" | "deleted" | "purged";
  id: string;
  kind: string;
  scope: { level: string; id?: string };
  generation: number;
  revision: number;
  phase: Phase;
  changeSet?: string;
}

/** The queries of a kind's resources start with this. */
export const resourcesKey = (kind: string) => ["resources", kind] as const;

/** How long notices settle before what they touched loads again; applying a change writes a few at once. */
const settle = 250;

/**
 * Loads a kind's resources again when the panel says one of them changed,
 * or one in particular, after a moment so a burst of writes loads once.
 */
export function useResourceUpdates(kind: string | undefined, id?: string) {
  const queryClient = useQueryClient();
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) {
        clearTimeout(timer.current);
      }
    },
    [],
  );
  const reload = () => {
    if (!kind || timer.current) {
      return;
    }
    timer.current = setTimeout(() => {
      timer.current = null;
      void queryClient.invalidateQueries({ queryKey: resourcesKey(kind) });
    }, settle);
  };
  useLiveEvents<Notice>(
    kind ? resourceStream : null,
    ["notice"],
    (event) => {
      if (event.data.kind === kind && (id === undefined || event.data.id === id)) {
        reload();
      }
    },
    reload,
  );
}

/** At most this many resources load into a list, five pages of the largest the API gives. */
export const listCap = 5000;
const pageSize = 1000;

export interface LoadedList {
  items: Resource[];
  /** Whether there were more than the list loads. */
  truncated: boolean;
}

/** Every resource of a kind the person can see, up to listCap, kept current as they change. */
export function useResourceList(kind: Kind | undefined, query: ResourceQuery = {}) {
  const api = useApi();
  useResourceUpdates(kind?.name);
  return useQuery({
    queryKey: [...resourcesKey(kind?.name ?? ""), "list", query],
    queryFn: async (): Promise<LoadedList> => {
      if (!kind) {
        return { items: [], truncated: false };
      }
      const items: Resource[] = [];
      let cursor: string | undefined;
      do {
        const page = await listResources(api, kind, {
          ...query,
          limit: pageSize,
          ...(cursor ? { cursor } : {}),
        });
        items.push(...page.items);
        cursor = page.next;
      } while (cursor && items.length < listCap);
      return { items: items.slice(0, listCap), truncated: cursor !== undefined };
    },
    enabled: kind !== undefined,
  });
}

/** One resource, kept current as it changes. */
export function useResource(kind: Kind | undefined, id: string | undefined) {
  const api = useApi();
  useResourceUpdates(kind?.name, id);
  return useQuery({
    queryKey: [...resourcesKey(kind?.name ?? ""), "one", id],
    queryFn: () => getResource(api, kind as Kind, id ?? ""),
    enabled: kind !== undefined && id !== undefined,
  });
}

/** Every recorded version of a resource's settings. */
export function useHistory(kind: Kind | undefined, id: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: [...resourcesKey(kind?.name ?? ""), "history", id],
    queryFn: () => getHistory(api, kind as Kind, id ?? ""),
    enabled: kind !== undefined && id !== undefined,
  });
}

/** Whether an error says the resource isn't there, or not for this person. */
export function isMissing(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}
