import { CircleCheck, CircleX, Undo2 } from "lucide-react";
import type { ReactNode } from "react";
import { useIntl } from "react-intl";
import { undoable, useChangeTracker } from "@/app/changes/tracker";
import { ChangePreview } from "@/design/components/change-preview";
import { JobLog } from "@/design/components/jobs-drawer";
import { Button } from "@/design/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/design/components/ui/dialog";
import { Alert, Progress } from "@/design/components/ui/display";
import { Skeleton } from "@/design/components/ui/skeleton";
import { ErrorState } from "@/design/components/ui/states";
import { designMessages } from "@/design/messages";
import { type ChangeSet, jobDone, type ResourceChange } from "@/lib/changes";
import { useFollowJob } from "../jobs/follow";
import { changeMessages as m } from "../messages";
import type { ChangeFlow } from "./flow";
import { useChangeSet } from "./queries";

export interface ChangeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** What the change is, such as an action's name. */
  title: string;
  description?: string;
  flow: ChangeFlow;
  /** What is asked before the preview, such as an action's input. */
  form?: ReactNode;
  /** A kind's name for people. */
  kindLabel?: (kind: string) => string;
  /** A resource's name for people, which critical changes ask to be typed. */
  resourceName?: (change: ResourceChange) => string;
  /** What has to be typed before the change applies, such as the name of what it deletes. */
  confirmText?: string;
  /** Shows what an action returned. */
  renderOutput?: (change: ResourceChange) => ReactNode;
}

/** The applied change, followed until its job ends, then what came of it. */
function Applied({
  applied,
  renderOutput,
}: {
  applied: ChangeSet;
  renderOutput?: (change: ResourceChange) => ReactNode;
}) {
  const intl = useIntl();
  const followed = useFollowJob(applied.job);
  const job = followed.job;
  const running = !job || !jobDone(job.state);
  const percent =
    job && job.progress.total > 0 ? (job.progress.done / job.progress.total) * 100 : undefined;
  return (
    <div className="flex flex-col gap-3" aria-live="polite">
      {running ? (
        <div className="flex flex-col gap-1">
          <p className="font-medium">{intl.formatMessage(m.applying)}</p>
          <Progress value={percent} aria-label={intl.formatMessage(m.applying)} />
          {job?.progress.message ? (
            <p className="text-muted-foreground text-sm">{job.progress.message}</p>
          ) : null}
        </div>
      ) : job.state === "succeeded" ? (
        <p className="flex items-center gap-2 font-medium">
          <CircleCheck className="size-5 text-success" aria-hidden="true" />
          {intl.formatMessage(m.applied)}
        </p>
      ) : (
        <div className="flex flex-col gap-1">
          <p className="flex items-center gap-2 font-medium">
            <CircleX className="size-5 text-danger" aria-hidden="true" />
            {intl.formatMessage(job.state === "canceled" ? m.canceled : m.failed)}
          </p>
          {job.error ? <p className="text-danger text-sm">{job.error.message}</p> : null}
        </div>
      )}
      {job ? (
        <JobLog lines={followed.lines} loading={followed.loading} label={applied.title} />
      ) : (
        <Skeleton className="h-24 w-full" />
      )}
      {!running && job.state === "succeeded" && renderOutput ? (
        <FinishedOutput id={applied.id} renderOutput={renderOutput} />
      ) : null}
    </div>
  );
}

/** What the applied actions returned, read once the job has finished. */
function FinishedOutput({
  id,
  renderOutput,
}: {
  id: string;
  renderOutput: (change: ResourceChange) => ReactNode;
}) {
  const intl = useIntl();
  const final = useChangeSet(id).data;
  const outputs = (final?.changes ?? []).filter((c) => c.output !== undefined && c.output !== null);
  if (outputs.length === 0) {
    return null;
  }
  return (
    <section className="flex flex-col gap-2" aria-label={intl.formatMessage(m.result)}>
      <h3 className="font-semibold text-sm">{intl.formatMessage(m.result)}</h3>
      {outputs.map((change) => (
        <div key={`${change.id}:${change.action ?? ""}`}>{renderOutput(change)}</div>
      ))}
    </section>
  );
}

/**
 * Takes a change through its preview: what is asked first, the preview of
 * what the change does, applying it, and for changes someone waits for,
 * its progress and result. Closing it while the change runs leaves the
 * toasts to say how it ends.
 */
export function ChangeDialog({
  open,
  onOpenChange,
  title,
  description,
  flow,
  form,
  kindLabel,
  resourceName,
  confirmText,
  renderOutput,
}: ChangeDialogProps) {
  const intl = useIntl();
  const tracker = useChangeTracker();
  const { state } = flow;
  const close = () => {
    if (state.step === "applied") {
      tracker.track(state.applied);
    }
    onOpenChange(false);
    flow.reset();
  };
  let body: ReactNode = null;
  let footer: ReactNode = null;
  switch (state.step) {
    case "idle":
      body = form ?? null;
      break;
    case "previewing":
      body = (
        <div className="flex flex-col gap-3" role="status">
          <p className="text-muted-foreground text-sm">{intl.formatMessage(m.working)}</p>
          <Skeleton className="h-6 w-2/3" />
          <Skeleton className="h-24 w-full" />
        </div>
      );
      break;
    case "preview":
    case "applying":
      body = (
        <div className="flex flex-col gap-3">
          {state.step === "preview" && state.stale ? (
            <Alert tone="warning">{intl.formatMessage(m.stale)}</Alert>
          ) : null}
          <ChangePreview
            changeSet={state.preview}
            className="border-0 p-0 shadow-none"
            {...(kindLabel ? { kindLabel } : {})}
            {...(resourceName ? { resourceName } : {})}
            {...(confirmText ? { confirmText } : {})}
            applying={state.step === "applying"}
            onApply={flow.apply}
            onCancel={close}
          />
        </div>
      );
      break;
    case "applied":
      body = <Applied applied={state.applied} {...(renderOutput ? { renderOutput } : {})} />;
      footer = (
        <DialogFooter>
          {undoable({ ...state.applied, state: "applied" }) ? (
            <Button
              variant="outline"
              onClick={() => {
                void tracker.undo(state.applied.id);
                close();
              }}
            >
              <Undo2 aria-hidden="true" />
              {intl.formatMessage(m.undo)}
            </Button>
          ) : null}
          <Button onClick={close}>{intl.formatMessage(designMessages.close)}</Button>
        </DialogFooter>
      );
      break;
    case "error":
      body = (
        <ErrorState
          title={intl.formatMessage(state.preview ? m.applyFailed : m.previewFailed)}
          error={state.error}
        />
      );
      footer = (
        <DialogFooter>
          {form ? (
            <Button variant="outline" onClick={() => flow.reset()}>
              {intl.formatMessage(m.back)}
            </Button>
          ) : null}
          <Button onClick={close}>{intl.formatMessage(designMessages.close)}</Button>
        </DialogFooter>
      );
      break;
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          close();
        }
      }}
    >
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description ? <DialogDescription>{description}</DialogDescription> : null}
        </DialogHeader>
        {body}
        {footer}
      </DialogContent>
    </Dialog>
  );
}
