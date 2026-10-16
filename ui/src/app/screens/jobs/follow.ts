import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { type ApiClient, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { getJobLogs, streams } from "@/lib/api/operations";
import { appendLogLine, type Job, jobDone, type LogLine } from "@/lib/changes";
import { type LiveStatus, useLiveEvents } from "@/lib/live";

export function getJob(api: ApiClient, id: string): Promise<Job> {
  return unwrap(api.GET("/api/v1/jobs/{id}", { params: { path: { id } } })) as Promise<Job>;
}

export interface FollowedJob {
  job: Job | undefined;
  lines: LogLine[];
  /** Whether the job or its log is still loading. */
  loading: boolean;
  error: unknown;
  status: LiveStatus;
}

/**
 * A job as it runs: its state and progress, and its log line by line, over
 * the job's stream while it runs and read once when it has finished.
 */
export function useFollowJob(id: string | undefined): FollowedJob {
  const api = useApi();
  const queryClient = useQueryClient();
  const key = ["jobs", id];
  const loaded = useQuery({
    queryKey: key,
    queryFn: () => getJob(api, id ?? ""),
    enabled: id !== undefined,
  });
  const [streamed, setStreamed] = useState<LogLine[]>([]);
  const job = loaded.data;
  const done = job ? jobDone(job.state) : false;
  // A finished job's stream sends its whole log and ends, so a job that
  // has finished is read instead of followed.
  const status = useLiveEvents<Job | LogLine>(
    id !== undefined && job !== undefined && !done ? streams.job(id) : null,
    ["job", "log"],
    (event) => {
      if (event.type === "log") {
        setStreamed((lines) => appendLogLine(lines, event.data as LogLine));
      } else {
        queryClient.setQueryData<Job>(key, event.data as Job);
      }
    },
  );
  const history = useQuery({
    queryKey: ["jobs", id, "logs"],
    queryFn: async () => (await getJobLogs(api, id ?? "")).lines ?? [],
    enabled: id !== undefined && done && streamed.length === 0,
    staleTime: Number.POSITIVE_INFINITY,
  });
  return {
    job,
    lines: streamed.length > 0 ? streamed : (history.data ?? []),
    loading: loaded.isPending || (done && streamed.length === 0 && history.isPending),
    error: loaded.error ?? history.error,
    status,
  };
}
