import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, type ReactNode, useContext, useState } from "react";
import { JobLog, JobsDrawer } from "@/design/components/jobs-drawer";
import { requestJSON } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { appendLogLine, type Job, type JobList, jobDone, type LogLine } from "@/lib/changes";
import type { TimelineEvent } from "@/lib/events";
import { upsertById, useLiveEvents, useLiveQuery } from "@/lib/live";

/**
 * Where the jobs API lives. A job's stream sends a "job" event when its
 * state or progress changes and a "log" event per log line; the event
 * timeline's stream says when work starts and ends.
 */
export interface JobsEndpoints {
  list: string;
  /** The event timeline's stream. */
  events: string;
  stream: (id: string) => string;
  logs: (id: string) => string;
  cancel: (id: string) => string;
  retry: (id: string) => string;
}

const job = (id: string) => `/api/v1/jobs/${encodeURIComponent(id)}`;

export const defaultJobsEndpoints: JobsEndpoints = {
  list: "/api/v1/jobs",
  events: "/api/v1/events/stream",
  stream: (id) => `${job(id)}/stream`,
  logs: (id) => `${job(id)}/logs`,
  cancel: (id) => `${job(id)}/cancel`,
  retry: (id) => `${job(id)}/retry`,
};

const EndpointsContext = createContext<JobsEndpoints>(defaultJobsEndpoints);

/** Points the jobs drawer at other endpoints. */
export function JobsEndpointsProvider({
  endpoints,
  children,
}: {
  endpoints: JobsEndpoints;
  children: ReactNode;
}) {
  return <EndpointsContext value={endpoints}>{children}</EndpointsContext>;
}

const jobsKey = ["jobs"] as const;

/**
 * The jobs the panel runs. The list loads again when the timeline reports
 * something about a job or a change set, which is when jobs start and end;
 * running jobs also stream their own progress while they're shown.
 */
export function useJobs(enabled = true) {
  const api = useApi();
  const endpoints = useContext(EndpointsContext);
  return useLiveQuery<Job[], TimelineEvent>({
    queryKey: jobsKey,
    queryFn: async () => {
      const answer = await requestJSON<JobList>(api, "GET", endpoints.list);
      return answer.jobs ?? [];
    },
    stream: { url: endpoints.events, types: ["event"] },
    apply: (jobs, event) =>
      event.data.job || event.data.type.startsWith("core.changeset.") ? undefined : jobs,
    enabled,
  });
}

/**
 * A job's log: streamed with its progress while the job runs, and read
 * once when it has finished.
 */
function LiveJobLog({ job }: { job: Job }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const endpoints = useContext(EndpointsContext);
  const done = jobDone(job.state);
  const [streamed, setStreamed] = useState<LogLine[]>([]);
  const status = useLiveEvents<Job | LogLine>(
    done ? null : endpoints.stream(job.id),
    ["job", "log"],
    (event) => {
      if (event.type === "log") {
        setStreamed((lines) => appendLogLine(lines, event.data as LogLine));
      } else {
        queryClient.setQueryData<Job[]>(jobsKey, (jobs) => upsertById(jobs, event.data as Job));
      }
    },
  );
  const history = useQuery({
    queryKey: [...jobsKey, job.id, "logs"],
    queryFn: async () =>
      (await requestJSON<{ lines: LogLine[] | null }>(api, "GET", endpoints.logs(job.id))).lines ??
      [],
    // A finished job's log doesn't change, so it's read once.
    enabled: done && streamed.length === 0,
    staleTime: Number.POSITIVE_INFINITY,
  });
  const lines = streamed.length > 0 ? streamed : (history.data ?? []);
  return (
    <JobLog
      lines={lines}
      loading={done ? history.isPending : status === "connecting"}
      label={job.title}
    />
  );
}

/** The jobs drawer connected to the panel. */
export function Jobs({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const endpoints = useContext(EndpointsContext);
  const jobs = useJobs(open);
  const act = useMutation({
    mutationFn: ({ job, action }: { job: Job; action: "cancel" | "retry" }) =>
      requestJSON<Job>(
        api,
        "POST",
        action === "cancel" ? endpoints.cancel(job.id) : endpoints.retry(job.id),
      ),
    // Canceling answers with the job, retrying with the new one.
    onSuccess: (answer) =>
      queryClient.setQueryData<Job[]>(jobsKey, (current) => upsertById(current, answer)),
  });
  return (
    <JobsDrawer
      open={open}
      onOpenChange={onOpenChange}
      jobs={jobs.data ?? []}
      loading={jobs.isPending}
      error={jobs.isError ? jobs.error : undefined}
      onRetryLoad={() => void jobs.refetch()}
      renderLog={(job) => <LiveJobLog job={job} />}
      onCancel={(job) => act.mutate({ job, action: "cancel" })}
      onRetry={(job) => act.mutate({ job, action: "retry" })}
    />
  );
}
