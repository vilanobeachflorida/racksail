import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import { useIntl } from "react-intl";
import {
  type JobAction,
  type JobActionState,
  JobLog,
  JobsDrawer,
} from "@/design/components/jobs-drawer";
import { useApi } from "@/lib/api/context";
import { cancelJob, getJobLogs, retryJob, streams } from "@/lib/api/operations";
import { appendLogLine, type Job, jobDone, type LogLine } from "@/lib/changes";
import { errorMessage } from "@/lib/errors";
import { upsertById, useLiveEvents } from "@/lib/live";
import { jobsKey, useJobs } from "./jobs";

// The drawer loads the first time it opens; the shell keeps the job list,
// which the top bar counts, without it.

/**
 * A job's log: streamed with its progress while the job runs, and read
 * once when it has finished. A stream that starts again sends the lines so
 * far, which the log already has.
 */
function LiveJobLog({ job }: { job: Job }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const done = jobDone(job.state);
  const [streamed, setStreamed] = useState<LogLine[]>([]);
  const status = useLiveEvents<Job | LogLine>(
    done ? null : streams.job(job.id),
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
    queryFn: async () => (await getJobLogs(api, job.id)).lines ?? [],
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
export default function Jobs({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const queryClient = useQueryClient();
  const jobs = useJobs(open);
  // Each job's cancel or retry in progress, or why the last one failed.
  const [actions, setActions] = useState<Readonly<Record<string, JobActionState>>>({});
  const act = useCallback(
    async (job: Job, action: JobAction) => {
      setActions((all) => ({ ...all, [job.id]: { pending: action } }));
      try {
        const answer =
          action === "cancel" ? await cancelJob(api, job.id) : await retryJob(api, job.id);
        // Canceling answers with the job, retrying with the new one.
        queryClient.setQueryData<Job[]>(jobsKey, (current) => upsertById(current, answer));
        setActions(({ [job.id]: _, ...rest }) => rest);
      } catch (error) {
        setActions((all) => ({
          ...all,
          [job.id]: { error: { action, message: errorMessage(intl, error) } },
        }));
      }
    },
    [api, intl, queryClient],
  );
  return (
    <JobsDrawer
      open={open}
      onOpenChange={onOpenChange}
      jobs={jobs.data ?? []}
      loading={jobs.isPending}
      error={jobs.isError ? jobs.error : undefined}
      onRetryLoad={() => void jobs.refetch()}
      renderLog={(job) => <LiveJobLog job={job} />}
      actions={actions}
      onCancel={(job) => void act(job, "cancel")}
      onRetry={(job) => void act(job, "retry")}
    />
  );
}
