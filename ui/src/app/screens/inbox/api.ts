import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ApiClient, type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { useLiveEvents } from "@/lib/live";

export type InboxItem = Schemas["InboxItem"];
export type InboxAction = Schemas["InboxAction"];

export interface Inbox {
  items: InboxItem[];
  dismissed: number;
  unavailable: Schemas["InboxProblem"][];
  checking: string[];
}

export const inboxKey = ["inbox"] as const;
export const inboxStream = "/api/v1/inbox/stream";

export async function getInbox(api: ApiClient, dismissed: boolean): Promise<Inbox> {
  const answer = await unwrap(
    api.GET("/api/v1/inbox", { params: { query: dismissed ? { dismissed } : {} } }),
  );
  return {
    items: answer.items ?? [],
    dismissed: answer.dismissed,
    unavailable: answer.unavailable ?? [],
    checking: answer.checking ?? [],
  };
}

export async function dismissItem(api: ApiClient, id: string, until?: Date): Promise<void> {
  await unwrap(
    api.POST("/api/v1/inbox/{id}/dismiss", {
      params: { path: { id } },
      ...(until ? { body: { until: until.toISOString() } } : {}),
    }),
  );
}

export async function restoreItem(api: ApiClient, id: string): Promise<void> {
  await unwrap(api.DELETE("/api/v1/inbox/{id}/dismiss", { params: { path: { id } } }));
}

/**
 * What needs the person's attention, read again whenever the panel says it
 * may have changed.
 */
export function useInbox(dismissed: boolean) {
  const api = useApi();
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: [...inboxKey, dismissed],
    queryFn: () => getInbox(api, dismissed),
    staleTime: Number.POSITIVE_INFINITY,
    // Showing or hiding dismissed items keeps the list until the other loads.
    placeholderData: (previous) => previous,
  });
  const reload = () => void queryClient.invalidateQueries({ queryKey: inboxKey });
  useLiveEvents(query.isSuccess ? inboxStream : null, ["changed"], reload, reload);
  return query;
}
