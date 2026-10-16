import { Link, useParams } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useIntl } from "react-intl";
import { undoable, useChangeTracker } from "@/app/changes/tracker";
import { usePageTitle } from "@/app/page-title";
import { ChangePreview } from "@/design/components/change-preview";
import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { ErrorState } from "@/design/components/ui/states";
import { useApi } from "@/lib/api/context";
import { ScreenSkeleton } from "../catalog";
import { changeListMessages as m } from "../messages";
import { useRecordRecent } from "../recent";
import { useKinds } from "../resources/kinds";
import { useKindLabels } from "../resources/labels";
import { isMissing } from "../resources/queries";
import { ResourceLink, When } from "../resources/values";
import { applyPreview, newIdempotencyKey } from "./api";
import { useChangeSet } from "./queries";

/**
 * One change: the preview of what it did, or would do, field by field and
 * on the server; who made it, through what and when; the job that carried
 * it out; and undo while that can be done. A preview that can still be
 * applied can be applied from here.
 */
export default function ChangeScreen() {
  const intl = useIntl();
  const api = useApi();
  const tracker = useChangeTracker();
  const labels = useKindLabels();
  const kinds = useKinds().data ?? [];
  const { changeSetId } = useParams({ strict: false }) as { changeSetId: string };
  const changeSet = useChangeSet(changeSetId);
  const cs = changeSet.data;
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
      <ChangePreview
        changeSet={cs}
        kindLabel={labels.kind}
        resourceName={labels.resourceName}
        {...(cs.state === "planned" && cs.dryRun
          ? {
              onApply: async () => {
                tracker.track(await applyPreview(api, cs.id, newIdempotencyKey()));
                void changeSet.refetch();
              },
            }
          : {})}
        {...(undoable(cs) ? { onUndo: () => tracker.undo(cs.id) } : {})}
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
    </div>
  );
}
