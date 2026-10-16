import { useApi } from "@/lib/api/context";
import { listJobs, streams } from "@/lib/api/operations";
import type { Job } from "@/lib/changes";
import type { TimelineEvent } from "@/lib/events";
import { useLiveQuery } from "@/lib/live";

export const jobsKey = ["jobs"] as const;

/**
 * The jobs the panel runs. The list loads again when the timeline reports
 * something about a job or a change set, which is when jobs start and end,
 * and after the stream reconnects; running jobs also stream their own
 * progress while they're shown.
 */
export function useJobs(enabled = true) {
  const api = useApi();
  return useLiveQuery<Job[], TimelineEvent>({
    queryKey: jobsKey,
    queryFn: async () => (await listJobs(api)).jobs ?? [],
    stream: { url: streams.events, types: ["event"] },
    apply: (jobs, event) =>
      event.data.job || event.data.type.startsWith("core.changeset.") ? undefined : jobs,
    enabled,
  });
}
