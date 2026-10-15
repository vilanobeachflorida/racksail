import { ListChecks, RotateCcw, Square } from "lucide-react";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { type Job, type JobState, jobDone, type LogLine } from "@/lib/changes";
import { useFormat } from "@/lib/format";
import { cn, withKeys } from "@/lib/utils";
import { designMessages as m } from "../messages";
import { Button } from "./ui/button";
import { Switch } from "./ui/choice";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "./ui/dialog";
import { Collapsible, CollapsibleContent, CollapsibleTrigger, Progress } from "./ui/display";
import { Label } from "./ui/field";
import { Skeleton } from "./ui/skeleton";
import { EmptyState, ErrorState, type Status, StatusDot } from "./ui/states";

const jobStatus: Record<JobState, Status> = {
  queued: "pending",
  running: "pending",
  succeeded: "ok",
  failed: "error",
  canceled: "off",
};

export interface JobLogProps {
  lines: readonly LogLine[];
  loading?: boolean;
  /** Names the log for screen readers, such as the job's title. */
  label: string;
}

/**
 * A job's log as it grows. It follows new lines until the reader scrolls
 * up or turns following off.
 */
export function JobLog({ lines, loading, label }: JobLogProps) {
  const intl = useIntl();
  const f = useFormat();
  const [follow, setFollow] = useState(true);
  const box = useRef<HTMLDivElement>(null);
  const switchId = `follow-${label.replace(/\W+/g, "-")}`;
  useEffect(() => {
    const el = box.current;
    if (follow && el && lines.length > 0) {
      el.scrollTop = el.scrollHeight;
    }
  }, [lines, follow]);
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <Switch id={switchId} checked={follow} onCheckedChange={setFollow} />
        <Label htmlFor={switchId} className="font-normal text-xs">
          {intl.formatMessage(m.jobsFollow)}
        </Label>
      </div>
      <div
        ref={box}
        role="log"
        aria-label={label}
        // biome-ignore lint/a11y/noNoninteractiveTabindex: a scrolling log must take focus so keyboard users can scroll it
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget;
          if (el.scrollTop + el.clientHeight < el.scrollHeight - 24) {
            setFollow(false);
          }
        }}
        className="max-h-72 overflow-auto rounded-md bg-surface-sunken p-2 font-mono text-xs focus-visible:focus-ring"
      >
        {loading && lines.length === 0 ? (
          <Skeleton className="h-4 w-2/3" />
        ) : lines.length === 0 ? (
          <p className="text-muted-foreground">{intl.formatMessage(m.jobsNoLog)}</p>
        ) : (
          withKeys(lines, (line) => `${line.time}:${line.message}`).map(([key, line]) => (
            <p key={key} className="flex gap-2 whitespace-pre-wrap break-all">
              <time dateTime={line.time} className="shrink-0 text-muted-foreground">
                {f.time(new Date(line.time))}
              </time>
              <span
                className={cn(
                  "shrink-0 uppercase",
                  line.level === "error"
                    ? "text-danger"
                    : line.level === "warn"
                      ? "text-warning"
                      : "text-muted-foreground",
                )}
              >
                {line.level}
              </span>
              <span>{line.message}</span>
            </p>
          ))
        )}
      </div>
    </div>
  );
}

function JobItem({
  job,
  renderLog,
  onCancel,
  onRetry,
}: {
  job: Job;
  renderLog: (job: Job) => ReactNode;
  onCancel?: (job: Job) => void;
  onRetry?: (job: Job) => void;
}) {
  const intl = useIntl();
  const f = useFormat();
  const [open, setOpen] = useState(job.state === "running");
  const { done, total, message } = job.progress;
  const percent = total > 0 ? (done / total) * 100 : undefined;
  return (
    <li className="flex flex-col gap-2 border-border border-b py-3 last:border-b-0">
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 flex-col gap-1">
          <p className="font-medium text-sm">{job.title}</p>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-muted-foreground text-xs">
            <StatusDot
              status={jobStatus[job.state]}
              label={intl.formatMessage(m.jobsState, { state: job.state })}
              className="text-xs"
            />
            <time dateTime={job.createdAt}>
              {f.relative(new Date(job.startedAt ?? job.createdAt))}
            </time>
            {job.attempt > 1 ? (
              <span>{intl.formatMessage(m.jobsAttempt, { attempt: job.attempt })}</span>
            ) : null}
          </div>
        </div>
        <div className="flex shrink-0 gap-1">
          {!jobDone(job.state) && onCancel ? (
            <Button variant="ghost" size="sm" onClick={() => onCancel(job)}>
              <Square aria-hidden="true" />
              {intl.formatMessage(m.jobsCancel)}
            </Button>
          ) : null}
          {(job.state === "failed" || job.state === "canceled") && onRetry ? (
            <Button variant="ghost" size="sm" onClick={() => onRetry(job)}>
              <RotateCcw aria-hidden="true" />
              {intl.formatMessage(m.jobsRetry)}
            </Button>
          ) : null}
        </div>
      </div>
      {!jobDone(job.state) ? (
        <div className="flex flex-col gap-1">
          <Progress
            value={percent}
            aria-label={intl.formatMessage(m.jobsProgress, { title: job.title })}
          />
          <p className="text-muted-foreground text-xs">
            {total > 0 ? intl.formatMessage(m.jobsSteps, { done, total }) : null}
            {message ? ` ${message}` : null}
          </p>
        </div>
      ) : null}
      {job.error ? <p className="text-danger text-sm">{job.error.message}</p> : null}
      <Collapsible open={open} onOpenChange={setOpen}>
        <CollapsibleTrigger className="text-muted-foreground text-xs">
          {intl.formatMessage(m.jobsLog)}
        </CollapsibleTrigger>
        <CollapsibleContent className="mt-2">{open ? renderLog(job) : null}</CollapsibleContent>
      </Collapsible>
    </li>
  );
}

export interface JobsDrawerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  jobs: readonly Job[];
  loading?: boolean;
  error?: unknown;
  onRetryLoad?: () => void;
  /** The live log of a job, usually a JobLog fed by the job's event stream. */
  renderLog: (job: Job) => ReactNode;
  onCancel?: (job: Job) => void;
  onRetry?: (job: Job) => void;
}

/** The drawer with every job the panel is running or ran recently, and their live logs. */
export function JobsDrawer({
  open,
  onOpenChange,
  jobs,
  loading,
  error,
  onRetryLoad,
  renderLog,
  onCancel,
  onRetry,
}: JobsDrawerProps) {
  const intl = useIntl();
  const running = jobs.filter((j) => !jobDone(j.state));
  const finished = jobs.filter((j) => jobDone(j.state));
  const list = (items: readonly Job[]) => (
    <ul className="flex flex-col">
      {items.map((job) => (
        <JobItem
          key={job.id}
          job={job}
          renderLog={renderLog}
          onCancel={onCancel}
          onRetry={onRetry}
        />
      ))}
    </ul>
  );
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="end" className="flex flex-col gap-4 overflow-y-auto">
        <div className="flex flex-col gap-1 pe-8">
          <SheetTitle>{intl.formatMessage(m.jobsTitle)}</SheetTitle>
          <SheetDescription>{intl.formatMessage(m.jobsDescription)}</SheetDescription>
        </div>
        {error !== undefined && error !== null ? (
          <ErrorState
            title={intl.formatMessage(m.jobsLoadFailed)}
            error={error}
            onRetry={onRetryLoad}
          />
        ) : loading ? (
          <div className="flex flex-col gap-3">
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
          </div>
        ) : jobs.length === 0 ? (
          <EmptyState
            icon={ListChecks}
            title={intl.formatMessage(m.jobsEmpty)}
            description={intl.formatMessage(m.jobsEmptyBody)}
          />
        ) : (
          <>
            {running.length > 0 ? (
              <section aria-label={intl.formatMessage(m.jobsRunning)}>
                <h3 className="font-semibold text-sm">{intl.formatMessage(m.jobsRunning)}</h3>
                {list(running)}
              </section>
            ) : null}
            {finished.length > 0 ? (
              <section aria-label={intl.formatMessage(m.jobsFinished)}>
                <h3 className="font-semibold text-sm">{intl.formatMessage(m.jobsFinished)}</h3>
                {list(finished)}
              </section>
            ) : null}
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
