import { useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useIntl } from "react-intl";
import { mayUndo, useChangeTracker } from "@/app/changes/tracker";
import { usePermission } from "@/app/modules";
import { usePageTitle } from "@/app/page-title";
import { ChangePreview } from "@/design/components/change-preview";
import { Button } from "@/design/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Alert } from "@/design/components/ui/display";
import { ErrorState } from "@/design/components/ui/states";
import { ApiError } from "@/lib/api/client";
import type { ChangeSet } from "@/lib/changes";
import { errorMessage } from "@/lib/errors";
import { ScreenSkeleton } from "../catalog";
import { changeMessages as cm, changeListMessages as m } from "../messages";
import { useRecordRecent } from "../recent";
import { type Kind, kindPath, resourcePath, useKinds } from "../resources/kinds";
import { useKindLabels } from "../resources/labels";
import { isMissing } from "../resources/queries";
import { ResourceLink, When } from "../resources/values";
import { ChangeDialog } from "./dialog";
import { useChangeFlow } from "./flow";
import { useChangeSet } from "./queries";

/**
 * Where to make a change again when its preview can't be applied: the form
 * of the one resource it creates or changes, when its kind has one.
 */
function redoPath(cs: ChangeSet, kinds: readonly Kind[]): string | undefined {
  const requested = (cs.changes ?? []).filter((c) => c.source.type === "request");
  const [change] = requested;
  if (requested.length !== 1 || !change) {
    return undefined;
  }
  const kind = kinds.find((k) => k.name === change.kind);
  if (!kind || kind.ui.hidden) {
    return undefined;
  }
  if (change.op === "create") {
    return `${kindPath(kind)}/new`;
  }
  return change.op === "update" ? `${resourcePath(kind, change.id)}/edit` : undefined;
}

/**
 * One change: the preview of what it did, or would do, field by field and
 * on the server; who made it, through what and when; the job that carried
 * it out; and undo while that can be done. A preview that can still be
 * applied is applied from here as the change dialog applies one: when the
 * change would now do something else, the dialog shows what, to apply that
 * instead, and what kept it from applying shows above it.
 */
export default function ChangeScreen() {
  const { changeSetId } = useParams({ strict: false }) as { changeSetId: string };
  // What applying one change ran into isn't another's.
  return <Change key={changeSetId} id={changeSetId} />;
}

function Change({ id: changeSetId }: { id: string }) {
  const intl = useIntl();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const tracker = useChangeTracker();
  const allowed = usePermission();
  const labels = useKindLabels();
  const kinds = useKinds().data ?? [];
  const changeSet = useChangeSet(changeSetId);
  const cs = changeSet.data;
  const [fresh, setFresh] = useState(false);
  const flow = useChangeFlow({
    onApplied: (applied) => {
      // The preview became the applied change set, or the fresh preview did.
      queryClient.setQueryData(["changesets", applied.id], applied);
      if (applied.id !== changeSetId) {
        void navigate({ to: `/changes/${applied.id}` });
      }
    },
  });
  usePageTitle(cs?.title);
  useRecordRecent(
    cs ? { path: `/changes/${cs.id}`, title: cs.title, what: intl.formatMessage(m.change) } : null,
  );
  if (changeSet.isPending) {
    return <ScreenSkeleton />;
  }
  if (!cs) {
    return (
      <ErrorState
        title={intl.formatMessage(isMissing(changeSet.error) ? m.gone : m.changeFailed)}
        error={changeSet.error}
      />
    );
  }
  const apply = async () => {
    flow.show(cs);
    const end = await flow.apply();
    if (end.step === "preview") {
      setFresh(true);
    }
  };
  const failure = flow.state.step === "error" && !fresh ? flow.state.error : null;
  // A preview that carried secret values is made again from its form.
  const redo =
    failure instanceof ApiError && failure.code === "changeset.preview_has_secrets"
      ? redoPath(cs, kinds)
      : undefined;
  const row = (term: string, value: ReactNode) => (
    <div className="contents">
      <dt className="font-medium text-muted-foreground text-sm">{term}</dt>
      <dd className="min-w-0 text-sm">{value}</dd>
    </div>
  );
  const link = (to: string, text: string) => (
    <Link
      to={to}
      className="font-mono text-primary text-xs underline-offset-4 hover:underline focus-visible:focus-ring"
    >
      {text}
    </Link>
  );
  const resources = [...new Set((cs.changes ?? []).map((c) => c.id))];
  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <p className="text-muted-foreground text-sm">{intl.formatMessage(m.change)}</p>
        <h1 className="break-words font-semibold text-2xl">{cs.title}</h1>
      </div>
      {failure ? (
        <Alert
          tone="danger"
          title={intl.formatMessage(cm.applyFailed)}
          {...(redo
            ? {
                actions: (
                  <Button asChild variant="outline" size="sm">
                    <Link to={redo}>{intl.formatMessage(m.redo)}</Link>
                  </Button>
                ),
              }
            : {})}
        >
          {errorMessage(intl, failure)}
        </Alert>
      ) : null}
      <ChangePreview
        changeSet={cs}
        kindLabel={labels.kind}
        resourceName={labels.resourceName}
        applying={flow.state.step === "applying" && !fresh}
        {...(cs.state === "planned" && cs.dryRun ? { onApply: apply } : {})}
        {...(mayUndo(cs, allowed) ? { onUndo: () => tracker.undo(cs.id) } : {})}
      />
      <Card aria-labelledby="change-details">
        <CardHeader>
          <CardTitle id="change-details">{intl.formatMessage(m.details)}</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-[minmax(10rem,auto)_1fr]">
            {cs.actor
              ? row(
                  intl.formatMessage(m.madeBy),
                  intl.formatMessage(m.actor, { name: cs.actor.name, source: cs.actor.source }),
                )
              : null}
            {row(intl.formatMessage(m.created), <When time={cs.createdAt} />)}
            {cs.appliedAt ? row(intl.formatMessage(m.applied), <When time={cs.appliedAt} />) : null}
            {cs.job ? row(intl.formatMessage(m.job), link(`/jobs/${cs.job}`, cs.job)) : null}
            {cs.undoOf
              ? row(intl.formatMessage(m.undoOf), link(`/changes/${cs.undoOf}`, cs.undoOf))
              : null}
            {row(
              intl.formatMessage(m.resources),
              <ul className="flex flex-col gap-1">
                {resources.map((id) => (
                  <li key={id}>
                    <ResourceLink id={id} kinds={kinds} />
                  </li>
                ))}
              </ul>,
            )}
            {(cs.snapshots ?? []).length > 0
              ? row(
                  intl.formatMessage(m.snapshots),
                  <ul className="flex flex-col gap-1">
                    {(cs.snapshots ?? []).map((s) => (
                      <li key={s.path}>
                        <code className="font-mono text-xs" translate="no">
                          {s.source} → {s.path}
                        </code>
                      </li>
                    ))}
                  </ul>,
                )
              : null}
          </dl>
        </CardContent>
      </Card>
      <ChangeDialog
        open={fresh}
        onOpenChange={setFresh}
        title={cs.title}
        flow={flow}
        kindLabel={labels.kind}
        resourceName={labels.resourceName}
      />
    </div>
  );
}
