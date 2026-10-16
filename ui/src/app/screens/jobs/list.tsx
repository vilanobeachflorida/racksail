import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ListChecks, RotateCcw, Square } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { DataTable } from "@/design/components/data-table/data-table";
import type { Column } from "@/design/components/data-table/model";
import { Button } from "@/design/components/ui/button";
import { Progress } from "@/design/components/ui/display";
import { EmptyState, ErrorState, type Status, StatusDot } from "@/design/components/ui/states";
import { toast } from "@/design/components/ui/toaster";
import { designMessages } from "@/design/messages";
import { useApi } from "@/lib/api/context";
import { cancelJob, listJobs, retryJob, streams } from "@/lib/api/operations";
import { type Job, type JobList, type JobState, jobDone } from "@/lib/changes";
import { errorMessage } from "@/lib/errors";
import type { TimelineEvent } from "@/lib/events";
import { useLiveEvents } from "@/lib/live";
import { jobMessages as m } from "../messages";
import { Duration, When } from "../resources/values";

export const jobStatus: Record<JobState, Status> = {
  queued: "pending",
  running: "pending",
  succeeded: "ok",
  failed: "error",
  canceled: "off",
};

const states: JobState[] = ["queued", "running", "succeeded", "failed", "canceled"];

/** How long a job ran, or has run so far. */
export function jobDuration(job: Job, now = Date.now()): number | undefined {
  if (!job.startedAt) {
    return undefined;
  }
  const end = job.finishedAt ? Date.parse(job.finishedAt) : now;
  return Math.max(0, (end - Date.parse(job.startedAt)) / 1000);
}

/** A job's state, as a dot with its name. */
export function JobStateDot({ state }: { state: JobState }) {
  const intl = useIntl();
  return (
    <StatusDot
      status={jobStatus[state]}
      label={intl.formatMessage(designMessages.jobsState, { state })}
    />
  );
}

/** Cancels or retries a job, saying so when that fails. */
export function useJobActions() {
  const intl = useIntl();
  const api = useApi();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<string | null>(null);
  const act = async (job: Job, action: "cancel" | "retry") => {
    setPending(job.id);
    try {
      await (action === "cancel" ? cancelJob(api, job.id) : retryJob(api, job.id));
      void queryClient.invalidateQueries({ queryKey: ["jobs"] });
    } catch (error) {
      toast.error(
        intl.formatMessage(designMessages.jobsActionFailed, {
          action,
          reason: errorMessage(intl, error),
        }),
      );
    } finally {
      setPending(null);
    }
  };
  return { pending, act };
}

/** Loads the job lists again, a moment after the timeline says a job or a change moved on. */
function useJobUpdates() {
  const queryClient = useQueryClient();
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reload = () => {
    if (timer.current) {
      return;
    }
    timer.current = setTimeout(() => {
      timer.current = null;
      void queryClient.invalidateQueries({ queryKey: ["jobs", "all"] });
    }, 250);
  };
  useLiveEvents<TimelineEvent>(
    streams.events,
    ["event"],
    (event) => {
      if (event.data.job || event.data.type.startsWith("core.changeset.")) {
        reload();
      }
    },
    reload,
  );
}

/**
 * Every job the person can see, newest first: what runs now, what ran and
 * how it ended, with each job's log a click away, and cancel and retry.
 */
export default function JobsScreen() {
  const intl = useIntl();
  const api = useApi();
  const jobs = useInfiniteQuery({
    queryKey: ["jobs", "all"],
    queryFn: ({ pageParam }) =>
      listJobs(api, { limit: 200, ...(pageParam ? { cursor: pageParam } : {}) }),
    initialPageParam: "",
    getNextPageParam: (last: JobList) => last.next || undefined,
  });
  useJobUpdates();
  const { pending, act } = useJobActions();
  const rows = useMemo(() => (jobs.data?.pages ?? []).flatMap((p) => p.jobs ?? []), [jobs.data]);
  const columns = useMemo<Column<Job>[]>(
    () => [
      {
        id: "title",
        header: intl.formatMessage(m.columnTitle),
        value: (j) => j.title,
        sortable: true,
        width: "minmax(14rem, 2fr)",
        cell: (j) => (
          <Link
            to={`/jobs/${j.id}`}
            className="rounded-sm font-medium text-primary underline-offset-4 hover:underline focus-visible:focus-ring"
          >
            {j.title}
          </Link>
        ),
      },
      {
        id: "state",
        header: intl.formatMessage(m.columnState),
        value: (j) => j.state,
        sortable: true,
        filter: {
          options: states.map((s) => ({
            value: s,
            label: intl.formatMessage(designMessages.jobsState, { state: s }),
          })),
        },
        cell: (j) =>
          jobDone(j.state) ? (
            <JobStateDot state={j.state} />
          ) : (
            <div className="flex flex-col gap-1">
              <JobStateDot state={j.state} />
              <Progress
                value={
                  j.progress.total > 0 ? (j.progress.done / j.progress.total) * 100 : undefined
                }
                aria-label={intl.formatMessage(designMessages.jobsProgress, { title: j.title })}
              />
            </div>
          ),
      },
      {
        id: "type",
        header: intl.formatMessage(m.columnType),
        value: (j) => j.type,
        sortable: true,
        filter: "text",
        cell: (j) => (
          <code className="font-mono text-xs" translate="no">
            {j.type}
          </code>
        ),
      },
      {
        id: "actor",
        header: intl.formatMessage(m.columnActor),
        value: (j) => j.actor?.name ?? "",
        sortable: true,
        filter: "text",
      },
      {
        id: "createdAt",
        header: intl.formatMessage(m.columnCreated),
        value: (j) => new Date(j.createdAt),
        sortable: true,
        cell: (j) => <When time={j.createdAt} />,
      },
      {
        id: "duration",
        header: intl.formatMessage(m.columnDuration),
        value: (j) => jobDuration(j),
        sortable: true,
        align: "end",
        cell: (j) => {
          const seconds = jobDuration(j);
          return seconds === undefined ? "" : <Duration seconds={seconds} />;
        },
      },
      {
        id: "actions",
        header: intl.formatMessage(m.columnActions),
        width: "8rem",
        align: "end",
        hideable: false,
        cell: (j) =>
          !jobDone(j.state) ? (
            <Button
              variant="ghost"
              size="sm"
              busy={pending === j.id}
              onClick={() => void act(j, "cancel")}
            >
              <Square aria-hidden="true" />
              {intl.formatMessage(designMessages.jobsCancel)}
            </Button>
          ) : j.state === "failed" || j.state === "canceled" ? (
            <Button
              variant="ghost"
              size="sm"
              busy={pending === j.id}
              onClick={() => void act(j, "retry")}
            >
              <RotateCcw aria-hidden="true" />
              {intl.formatMessage(designMessages.jobsRetry)}
            </Button>
          ) : null,
      },
    ],
    [intl, pending, act],
  );
  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{intl.formatMessage(m.title)}</h1>
        <p className="text-muted-foreground">{intl.formatMessage(m.description)}</p>
      </div>
      {jobs.isError ? (
        <ErrorState
          title={intl.formatMessage(m.failed)}
          error={jobs.error}
          onRetry={() => void jobs.refetch()}
        />
      ) : (
        <DataTable
          id="jobs"
          label={intl.formatMessage(m.title)}
          rows={rows}
          rowKey={(j) => j.id}
          columns={columns}
          loading={jobs.isPending}
          empty={
            <EmptyState
              icon={ListChecks}
              title={intl.formatMessage(designMessages.jobsEmpty)}
              description={intl.formatMessage(designMessages.jobsEmptyBody)}
            />
          }
        />
      )}
      {jobs.hasNextPage ? (
        <Button
          variant="outline"
          className="self-center"
          busy={jobs.isFetchingNextPage}
          onClick={() => void jobs.fetchNextPage()}
        >
          {intl.formatMessage(m.more)}
        </Button>
      ) : null}
    </div>
  );
}
