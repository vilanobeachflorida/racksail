import { type QueryKey, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { streams } from "@/lib/api/operations";
import type { TimelineEvent } from "@/lib/events";
import { type LiveStatus, useLiveEvents } from "@/lib/live";

/** Where notices of changes to resources of some kinds stream from. */
export function resourceStream(kinds: readonly string[]): string {
  const query = new URLSearchParams(kinds.map((kind) => ["kind", kind]));
  return `/api/v1/resources/stream?${query.toString()}`;
}

/**
 * Reads the given queries again; with a pause, at most once per pause, for
 * streams busy enough that reading on every message would be too often.
 */
function useRefresh(queryKeys: readonly QueryKey[], pause = 0) {
  const queryClient = useQueryClient();
  const keys = useRef(queryKeys);
  keys.current = queryKeys;
  const waiting = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (waiting.current) {
        clearTimeout(waiting.current);
      }
    },
    [],
  );
  const now = () => {
    waiting.current = null;
    for (const queryKey of keys.current) {
      void queryClient.invalidateQueries({ queryKey });
    }
  };
  return () => {
    if (pause <= 0) {
      now();
    } else if (!waiting.current) {
      waiting.current = setTimeout(now, pause);
    }
  };
}

/**
 * Keeps queries current while resources of some kinds change, as the panel
 * reports over server-sent events: their settings, and their status, which
 * the panel updates on its own, such as a module turning active or an
 * update finishing. Missed notices read the queries again too.
 */
export function useResourceUpdates(
  kinds: readonly string[],
  queryKeys: readonly QueryKey[],
): LiveStatus {
  const refresh = useRefresh(queryKeys);
  return useLiveEvents(resourceStream(kinds), ["notice"], refresh, refresh);
}

/**
 * Reads queries again when the timeline has events of some types, as
 * patterns such as core.safe_mode.*, for what isn't a resource. Without
 * types, every event counts, and the queries are read at most once per
 * pause.
 */
export function useTimelineUpdates(
  types: readonly string[] | null,
  queryKeys: readonly QueryKey[],
  pause = 0,
): LiveStatus {
  const refresh = useRefresh(queryKeys, pause);
  return useLiveEvents<TimelineEvent>(
    streams.events,
    ["event"],
    (event) => {
      if (types === null || types.some((t) => matches(t, event.data.type))) {
        refresh();
      }
    },
    refresh,
  );
}

/**
 * Reads queries again a second after a moment, for what changes on a
 * schedule rather than on anything a stream reports. A null moment, in
 * milliseconds since the epoch, waits for nothing.
 */
export function useRefreshAt(at: number | null, queryKeys: readonly QueryKey[]) {
  const refresh = useRefresh(queryKeys);
  const latest = useRef(refresh);
  latest.current = refresh;
  useEffect(() => {
    if (at === null) {
      return;
    }
    // Timers can't wait longer than about 24 days.
    const delay = Math.min(Math.max(at - Date.now(), 0) + 1000, 2 ** 31 - 1);
    const timer = setTimeout(() => latest.current(), delay);
    return () => clearTimeout(timer);
  }, [at]);
}

/** Whether an event type matches a pattern, where * stands for any part of one segment. */
function matches(pattern: string, type: string): boolean {
  const ps = pattern.split(".");
  const ts = type.split(".");
  return (
    ps.length === ts.length &&
    ps.every((p, i) => {
      const part = ts[i] ?? "";
      if (!p.includes("*")) {
        return p === part;
      }
      const [head = "", tail = ""] = p.split("*", 2);
      return (
        part.startsWith(head) && part.endsWith(tail) && part.length >= head.length + tail.length
      );
    })
  );
}

/** Hands the person a JSON document to save, as a download. */
export function saveJSON(name: string, data: unknown) {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  // The download has its own copy by the time the link is clicked.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
