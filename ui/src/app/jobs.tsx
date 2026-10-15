import { useMutation } from "@tanstack/react-query";
import { createContext, type ReactNode, useContext, useState } from "react";
import { JobLog, JobsDrawer } from "@/design/components/jobs-drawer";
import { requestJSON } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { type Job, jobDone, type LogLine } from "@/lib/changes";
import { upsertById, useLiveEvents, useLiveQuery } from "@/lib/live";

/**
 * Where the jobs API lives. Jobs stream their changes and log lines as
 * server-sent events: "job" events carry a job, "log" events a log line.
 */
export interface JobsEndpoints {
  list: string;
  stream: string;
  log: (id: string) => string;
  cancel: (id: string) => string;
  retry: (id: string) => string;
}

export const defaultJobsEndpoints: JobsEndpoints = {
  list: "/api/v1/jobs",
  stream: "/api/v1/jobs/events",
  log: (id) => `/api/v1/jobs/${encodeURIComponent(id)}/events`,
  cancel: (id) => `/api/v1/jobs/${encodeURIComponent(id)}/actions/cancel`,
  retry: (id) => `/api/v1/jobs/${encodeURIComponent(id)}/actions/retry`,
};

const EndpointsContext = createContext<JobsEndpoints>(defaultJobsEndpoints);

/** Points the jobs drawer at other endpoints, as tests do. */
export function JobsEndpointsProvider({
  endpoints,
  children,
}: {
  endpoints: JobsEndpoints;
  children: ReactNode;
}) {
  return <EndpointsContext value={endpoints}>{children}</EndpointsContext>;
}

interface JobList {
  items?: Job[];
}

/** The jobs the panel runs, kept current by their event stream. */
export function useJobs(enabled = true) {
  const api = useApi();
  const endpoints = useContext(EndpointsContext);
  return useLiveQuery<Job[], Job>({
    queryKey: ["jobs"],
    queryFn: async () => {
      const answer = await requestJSON<Job[] | JobList>(api, "GET", endpoints.list);
      return Array.isArray(answer) ? answer : (answer.items ?? []);
    },
    stream: { url: endpoints.stream, types: ["job"] },
    apply: (jobs, event) => upsertById(jobs, event.data),
    enabled,
  });
}

function LiveJobLog({ job }: { job: Job }) {
  const endpoints = useContext(EndpointsContext);
  const [lines, setLines] = useState<LogLine[]>([]);
  const status = useLiveEvents<LogLine>(endpoints.log(job.id), ["log"], (event) => {
    setLines((current) => [...current.slice(-1999), event.data]);
  });
  return (
    <JobLog
      lines={lines}
      loading={status === "connecting" && !jobDone(job.state)}
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
  const endpoints = useContext(EndpointsContext);
  const jobs = useJobs(open);
  const act = useMutation({
    mutationFn: ({ job, action }: { job: Job; action: "cancel" | "retry" }) =>
      requestJSON<Job>(
        api,
        "POST",
        action === "cancel" ? endpoints.cancel(job.id) : endpoints.retry(job.id),
      ),
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
