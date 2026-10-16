import { CircleCheck, CircleX, Undo2 } from "lucide-react";
import { type ReactNode, useEffect, useRef } from "react";
import { useIntl } from "react-intl";
import { mayUndo, useChangeTracker } from "@/app/changes/tracker";
import { usePermission } from "@/app/modules";
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

/**
 * The applied change, followed until its job ends, then what came of it,
 * and undo once there is something to undo.
 */
function Applied({
  applied,
  renderOutput,
  onClose,
  onUndo,
}: {
  applied: ChangeSet;
  renderOutput?: (change: ResourceChange) => ReactNode;
  onClose: () => void;
  onUndo: () => void;
}) {
  const intl = useIntl();
  const allowed = usePermission();
  const followed = useFollowJob(applied.job);
  const job = followed.job;
  const running = applied.job ? !job || !jobDone(job.state) : applied.state === "applying";
  const succeeded = job ? job.state === "succeeded" : applied.state === "applied";
  const percent =
    job && job.progress.total > 0 ? (job.progress.done / job.progress.total) * 100 : undefined;
  // A job that was canceled ends its change set as failed, which can be undone too.
  const ended: ChangeSet | null = running
    ? null
    : { ...applied, state: succeeded ? "applied" : "failed" };
  return (
    <>
      <div className="flex flex-col gap-3" aria-live="polite">
        {running ? (
          <div className="flex flex-col gap-1">
            <p className="font-medium">{intl.formatMessage(m.applying)}</p>
            <Progress value={percent} aria-label={intl.formatMessage(m.applying)} />
            {job?.progress.message ? (
              <p className="text-muted-foreground text-sm">{job.progress.message}</p>
            ) : null}
          </div>
        ) : succeeded ? (
          <p className="flex items-center gap-2 font-medium">
            <CircleCheck className="size-5 text-success" aria-hidden="true" />
            {intl.formatMessage(m.applied)}
          </p>
        ) : (
          <div className="flex flex-col gap-1">
            <p className="flex items-center gap-2 font-medium">
              <CircleX className="size-5 text-danger" aria-hidden="true" />
              {intl.formatMessage(job?.state === "canceled" ? m.canceled : m.failed)}
            </p>
            {job?.error ? <p className="text-danger text-sm">{job.error.message}</p> : null}
          </div>
        )}
        {job ? (
          <JobLog lines={followed.lines} loading={followed.loading} label={applied.title} />
        ) : applied.job ? (
          <Skeleton className="h-24 w-full" />
        ) : null}
        {!running && succeeded && renderOutput ? (
          <FinishedOutput id={applied.id} renderOutput={renderOutput} />
        ) : null}
      </div>
      <DialogFooter>
        {ended && mayUndo(ended, allowed) ? (
          <Button variant="outline" onClick={onUndo}>
            <Undo2 aria-hidden="true" />
            {intl.formatMessage(m.undo)}
          </Button>
        ) : null}
        <Button onClick={onClose}>{intl.formatMessage(designMessages.close)}</Button>
      </DialogFooter>
    </>
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
 * its progress and result. Once a change the toasts follow is applied, the
 * dialog closes itself; closing it while a change it follows runs hands
 * that change to the toasts too.
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
  const { state, reset } = flow;
  const close = () => {
    if (state.step === "applied") {
      tracker.track(state.applied);
    }
    onOpenChange(false);
    reset();
  };
  // A change the toasts follow needs nothing more from the dialog, whose
  // owner hears it closing as it would from the person.
  const openChange = useRef(onOpenChange);
  useEffect(() => {
    openChange.current = onOpenChange;
  });
  useEffect(() => {
    if (state.step === "tracked") {
      openChange.current(false);
      reset();
    }
  }, [state.step, reset]);
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
            onApply={async () => {
              await flow.apply();
            }}
            onCancel={close}
          />
        </div>
      );
      break;
    case "applied": {
      const applied = state.applied;
      body = (
        <Applied
          applied={applied}
          {...(renderOutput ? { renderOutput } : {})}
          onClose={close}
          onUndo={() => {
            // The change is being undone, so the toasts don't take it over
            // to offer the same; the undo has toasts of its own.
            onOpenChange(false);
            reset();
            void tracker.undo(applied.id);
          }}
        />
      );
      break;
    }
    case "tracked":
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
            <Button variant="outline" onClick={reset}>
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
      open={open && state.step !== "tracked"}
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
