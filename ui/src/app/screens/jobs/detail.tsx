import { Link, useParams } from "@tanstack/react-router";
import { RotateCcw, Square } from "lucide-react";
import type { ReactNode } from "react";
import { useIntl } from "react-intl";
import { usePageTitle } from "@/app/page-title";
import { JobLog, stepValues } from "@/design/components/jobs-drawer";
import { Button } from "@/design/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Alert, Progress } from "@/design/components/ui/display";
import { ErrorState } from "@/design/components/ui/states";
import { designMessages } from "@/design/messages";
import { jobDone } from "@/lib/changes";
import { ScreenSkeleton } from "../catalog";
import { jobMessages as m } from "../messages";
import { useRecordRecent } from "../recent";
import { useKinds } from "../resources/kinds";
import { isMissing } from "../resources/queries";
import { Duration, ResourceLink, When } from "../resources/values";
import { useFollowJob } from "./follow";
import { JobStateDot, jobDuration, useJobActions } from "./list";

/**
 * One job: its state and progress, who started it and for what, why it
 * failed, what it returned, and its whole log, live while it runs.
 */
export default function JobScreen() {
  const intl = useIntl();
  const { jobId } = useParams({ strict: false }) as { jobId: string };
  const followed = useFollowJob(jobId);
  const kinds = useKinds().data ?? [];
  const { pending, act } = useJobActions();
  const job = followed.job;
  usePageTitle(job?.title);
  useRecordRecent(
    job ? { path: `/jobs/${job.id}`, title: job.title, what: intl.formatMessage(m.job) } : null,
  );
  if (!job) {
    if (followed.error) {
      return (
        <ErrorState
          title={intl.formatMessage(isMissing(followed.error) ? m.gone : m.jobFailed)}
          error={followed.error}
        />
      );
    }
    return <ScreenSkeleton />;
  }
  const running = !jobDone(job.state);
  const seconds = jobDuration(job);
  const row = (term: string, value: ReactNode) => (
    <div className="contents">
      <dt className="font-medium text-muted-foreground text-sm">{term}</dt>
      <dd className="min-w-0 text-sm">{value}</dd>
    </div>
  );
  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex min-w-0 flex-col gap-1">
          <p className="text-muted-foreground text-sm">{intl.formatMessage(m.job)}</p>
          <h1 className="break-words font-semibold text-2xl">{job.title}</h1>
          <JobStateDot state={job.state} />
        </div>
        <div className="flex gap-2">
          {running ? (
            <Button
              variant="outline"
              busy={pending === job.id}
              onClick={() => void act(job, "cancel")}
            >
              <Square aria-hidden="true" />
              {intl.formatMessage(designMessages.jobsCancel)}
            </Button>
          ) : null}
          {job.state === "failed" || job.state === "canceled" ? (
            <Button
              variant="outline"
              busy={pending === job.id}
              onClick={() => void act(job, "retry")}
            >
              <RotateCcw aria-hidden="true" />
              {intl.formatMessage(designMessages.jobsRetry)}
            </Button>
          ) : null}
        </div>
      </header>
      {running ? (
        <div className="flex flex-col gap-1" aria-live="polite">
          <Progress
            value={
              job.progress.total > 0 ? (job.progress.done / job.progress.total) * 100 : undefined
            }
            aria-label={intl.formatMessage(designMessages.jobsProgress, { title: job.title })}
          />
          <p className="text-muted-foreground text-sm">
            {job.progress.total > 0 || job.progress.message
              ? intl.formatMessage(designMessages.jobsSteps, stepValues(job))
              : null}
          </p>
        </div>
      ) : null}
      {job.error ? (
        <Alert tone="danger" title={intl.formatMessage(m.why)}>
          {job.error.message}
        </Alert>
      ) : null}
      <Card aria-labelledby="job-details">
        <CardHeader>
          <CardTitle id="job-details">{intl.formatMessage(m.details)}</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-[minmax(10rem,auto)_1fr]">
            {row(
              intl.formatMessage(m.type),
              <code className="font-mono text-xs" translate="no">
                {job.type}
              </code>,
            )}
            {job.actor ? row(intl.formatMessage(m.startedBy), job.actor.name) : null}
            {row(intl.formatMessage(m.created), <When time={job.createdAt} />)}
            {job.startedAt
              ? row(intl.formatMessage(m.started), <When time={job.startedAt} />)
              : null}
            {job.finishedAt
              ? row(intl.formatMessage(m.finished), <When time={job.finishedAt} />)
              : null}
            {seconds !== undefined
              ? row(intl.formatMessage(m.took), <Duration seconds={seconds} />)
              : null}
            {job.attempt > 1
              ? row(intl.formatMessage(m.attempt), intl.formatNumber(job.attempt))
              : null}
            {job.retryOf
              ? row(
                  intl.formatMessage(m.retryOf),
                  <Link
                    to={`/jobs/${job.retryOf}`}
                    className="font-mono text-primary text-xs underline-offset-4 hover:underline"
                  >
                    {job.retryOf}
                  </Link>,
                )
              : null}
            {job.changeSet
              ? row(
                  intl.formatMessage(m.changeSet),
                  <Link
                    to={`/changes/${job.changeSet}`}
                    className="font-mono text-primary text-xs underline-offset-4 hover:underline"
                  >
                    {job.changeSet}
                  </Link>,
                )
              : null}
            {job.resource
              ? row(
                  intl.formatMessage(m.resource),
                  <ResourceLink id={job.resource} kinds={kinds} />,
                )
              : null}
          </dl>
        </CardContent>
      </Card>
      {job.result !== undefined && job.result !== null ? (
        <Card aria-labelledby="job-result">
          <CardHeader>
            <CardTitle id="job-result">{intl.formatMessage(m.result)}</CardTitle>
          </CardHeader>
          <CardContent>
            <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-md bg-surface-sunken p-3 font-mono text-xs">
              {JSON.stringify(job.result, null, 2)}
            </pre>
          </CardContent>
        </Card>
      ) : null}
      <Card aria-labelledby="job-log">
        <CardHeader>
          <CardTitle id="job-log">{intl.formatMessage(designMessages.jobsLog)}</CardTitle>
        </CardHeader>
        <CardContent>
          <JobLog lines={followed.lines} loading={followed.loading} label={job.title} />
        </CardContent>
      </Card>
    </div>
  );
}
