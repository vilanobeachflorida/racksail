import { toast } from "@/design/components/ui/toaster";
import { streams } from "@/lib/api/operations";
import { type ChangeSet, type Job, jobDone, needsConfirmation } from "@/lib/changes";
import { errorMessage } from "@/lib/errors";
import { messages } from "../messages";
import {
  applyPreview,
  getChangeSet,
  newIdempotencyKey,
  undoChangeSet,
} from "../screens/changes/api";
import { type TrackerHost, type TrackOptions, undoable } from "./tracker";

/*
 * Following the changes people apply: their progress and outcome in a
 * toast, undo, and the queries that show what they changed. It loads with
 * the first change, apart from the shell.
 */

/**
 * The queries applying a change can make out of date, by the start of their
 * keys. Jobs, events and the inbox follow their own streams, so they're
 * current already.
 */
export const changedQueries = [["resources"], ["changesets"]];

/** Says how far a change has got, in the toast that stays until it ends. */
function announce(host: TrackerHost, cs: ChangeSet, progress?: Job["progress"]) {
  const { intl, shell } = host.current();
  const job = cs.job;
  toast(intl.formatMessage(messages.trackerApplying, { title: cs.title }), {
    id: `change-${cs.id}`,
    duration: Number.POSITIVE_INFINITY,
    description:
      progress && progress.total > 0
        ? intl.formatMessage(messages.trackerSteps, {
            done: progress.done,
            total: progress.total,
            message: progress.message ?? "",
            hasMessage: progress.message ? "yes" : "no",
          })
        : (progress?.message ?? intl.formatMessage(messages.trackerStarting)),
    ...(job
      ? {
          action: {
            label: intl.formatMessage(messages.trackerShowProgress),
            onClick: (event: { preventDefault: () => void }) => {
              // The toast stays to say how the change ends.
              event.preventDefault();
              shell.showJob(job);
            },
          },
        }
      : {}),
  });
}

/** Says how a change ended, offering to undo it, and loads again what it changed. */
async function finish(host: TrackerHost, cs: ChangeSet) {
  const entry = host.followed.get(cs.id);
  if (!entry || entry.done) {
    return;
  }
  entry.done = true;
  entry.stop?.();
  for (const queryKey of changedQueries) {
    void host.queryClient.invalidateQueries({ queryKey });
  }
  let final = cs;
  try {
    final = await getChangeSet(host.api, cs.id);
  } catch {
    // The change set as it was answered still says what was applied.
  }
  if (final.undoOf) {
    const undone = final.undoOf;
    host.setLast((l) => (l?.id === undone ? null : l));
  } else if (undoable(final)) {
    host.setLast(final);
  }
  if (entry.quiet) {
    return;
  }
  const { intl, shell } = host.current();
  const id = `change-${cs.id}`;
  const undo = undoable(final)
    ? { label: intl.formatMessage(messages.trackerUndo), onClick: () => void host.undo(cs.id) }
    : undefined;
  if (final.state === "failed") {
    toast.error(intl.formatMessage(messages.trackerFailed, { title: final.title }), {
      id,
      duration: Number.POSITIVE_INFINITY,
      description: final.error ?? null,
      action: undo ?? null,
      cancel: final.job
        ? {
            label: intl.formatMessage(messages.trackerShowLog),
            onClick: () => final.job && shell.showJob(final.job),
          }
        : null,
    });
    return;
  }
  // The same toast said how far the change had got; what it said goes.
  toast.success(intl.formatMessage(messages.trackerApplied, { title: final.title }), {
    id,
    duration: 10_000,
    description: null,
    action: undo ?? null,
    cancel: null,
  });
}

/** Follows an applied change set's job until it ends. */
export function track(host: TrackerHost, cs: ChangeSet, options: TrackOptions = {}) {
  const quiet = options.quiet === true;
  const known = host.followed.get(cs.id);
  if (known) {
    // A dialog that followed the change hands it over as it closes.
    if (known.quiet && !quiet) {
      known.quiet = false;
      if (known.done) {
        known.done = false;
        void finish(host, cs);
      } else {
        announce(host, cs);
      }
    }
    return;
  }
  const entry = { quiet, stop: null as (() => void) | null, done: false };
  host.followed.set(cs.id, entry);
  if (!cs.job || cs.state === "applied" || cs.state === "failed") {
    void finish(host, cs);
    return;
  }
  if (!quiet) {
    announce(host, cs);
  }
  entry.stop = host.live.subscribe(streams.job(cs.job), ["job"], {
    onEvent: (event) => {
      const job = event.data as Job;
      if (jobDone(job.state)) {
        void finish(host, cs);
      } else if (!entry.quiet) {
        announce(host, cs, job.progress);
      }
    },
  });
}

/** Undoes a change set, showing its preview first when the undo deletes something or is risky. */
export async function undo(host: TrackerHost, id: string) {
  const { intl } = host.current();
  const failed = (error: unknown) =>
    toast.error(
      intl.formatMessage(messages.trackerUndoFailed, { reason: errorMessage(intl, error) }),
      { id: `undo-${id}` },
    );
  let preview: ChangeSet;
  try {
    preview = await undoChangeSet(host.api, id, { dryRun: true });
  } catch (error) {
    failed(error);
    return;
  }
  // Undoing a creation deletes what was made: that, anything risky and
  // anything that changed since gets its preview first.
  if (
    needsConfirmation(preview) ||
    (preview.conflicts ?? []).length > 0 ||
    (preview.changes ?? []).some((c) => c.op === "delete")
  ) {
    host.confirm(preview, id);
    return;
  }
  try {
    host.track(await applyPreview(host.api, preview.id, newIdempotencyKey()));
  } catch (error) {
    failed(error);
  }
}
