import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { History, Undo2 } from "lucide-react";
import { useIntl } from "react-intl";
import { mayUndo, useChangeTracker } from "@/app/changes/tracker";
import { usePermission } from "@/app/modules";
import { DiffRow } from "@/design/components/change-preview";
import { Button } from "@/design/components/ui/button";
import { Badge } from "@/design/components/ui/display";
import { Skeleton } from "@/design/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { useApi } from "@/lib/api/context";
import type { ChangeSet } from "@/lib/changes";
import { listChangeSets } from "../changes/api";
import { historyMessages as m } from "../messages";
import type { Version } from "./api";
import { diffSpecs } from "./diff";
import { type Kind, type Resource, sensitivePaths } from "./kinds";
import { useHistory } from "./queries";
import { When } from "./values";

/** Who made a version: the change set's actor when it's known, or what the store recorded. */
function author(version: Version, changeSet: ChangeSet | undefined): string {
  return changeSet?.actor?.name ?? version.actor ?? "";
}

/**
 * Every version of a resource's settings, newest first, each with what it
 * changed field by field, who changed it and when, and a way to undo the
 * change that made it while that can still be done.
 */
export function ResourceHistory({ kind, resource }: { kind: Kind; resource: Resource }) {
  const intl = useIntl();
  const api = useApi();
  const tracker = useChangeTracker();
  const allowed = usePermission();
  const history = useHistory(kind, resource.id);
  const changeSets = useQuery({
    queryKey: ["changesets", "resource", resource.id],
    queryFn: async () =>
      (await listChangeSets(api, { resource: resource.id, limit: 200 })).changeSets,
  });
  if (history.isPending) {
    return (
      <div className="flex flex-col gap-3" aria-busy="true">
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
      </div>
    );
  }
  if (history.isError) {
    return (
      <ErrorState
        title={intl.formatMessage(m.failed)}
        error={history.error}
        onRetry={() => void history.refetch()}
      />
    );
  }
  const versions = history.data;
  if (versions.length === 0) {
    return <EmptyState icon={History} title={intl.formatMessage(m.empty)} />;
  }
  const byID = new Map((changeSets.data ?? []).map((cs) => [cs.id, cs]));
  const secret = sensitivePaths(kind);
  return (
    <ol className="flex flex-col gap-3" aria-label={intl.formatMessage(m.label)}>
      {versions
        .map((version, i) => ({ version, previous: versions[i - 1] }))
        .reverse()
        .map(({ version, previous }) => {
          const cs = version.changeSet ? byID.get(version.changeSet) : undefined;
          // The first version shows every field it set.
          const diffs = version.deleted
            ? []
            : diffSpecs(previous?.spec ?? {}, version.spec, secret);
          const labelsChanged =
            previous !== undefined &&
            JSON.stringify(previous.labels ?? {}) !== JSON.stringify(version.labels ?? {});
          const who = author(version, cs);
          return (
            <li
              key={`${version.generation}:${version.time}`}
              className="flex flex-col gap-2 rounded-lg border border-border bg-card p-4"
            >
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                <span className="font-medium">
                  {intl.formatMessage(
                    version.deleted ? m.deleted : previous ? m.version : m.created,
                    { generation: version.generation },
                  )}
                </span>
                {cs?.state === "undone" ? (
                  <Badge variant="outline">{intl.formatMessage(m.undone)}</Badge>
                ) : null}
                <span className="text-muted-foreground text-sm">
                  {intl.formatMessage(m.when, {
                    time: <When key="time" time={version.time} />,
                    actor: who ?? "",
                    hasActor: who ? "yes" : "no",
                  })}
                </span>
                <span className="ms-auto flex gap-2">
                  {version.changeSet ? (
                    <Button asChild variant="ghost" size="sm">
                      <Link to={`/changes/${version.changeSet}`}>
                        {intl.formatMessage(m.showChange)}
                      </Link>
                    </Button>
                  ) : null}
                  {cs && mayUndo(cs, allowed) ? (
                    <Button variant="outline" size="sm" onClick={() => void tracker.undo(cs.id)}>
                      <Undo2 aria-hidden="true" />
                      {intl.formatMessage(m.undo)}
                    </Button>
                  ) : null}
                </span>
              </div>
              {diffs.length > 0 ? (
                <ul aria-label={intl.formatMessage(m.fields)} className="divide-y divide-border">
                  {diffs.map((d) => (
                    <DiffRow key={`${d.op}:${d.path}`} diff={d} />
                  ))}
                </ul>
              ) : previous && !version.deleted && !labelsChanged ? (
                <p className="text-muted-foreground text-sm">{intl.formatMessage(m.noFields)}</p>
              ) : null}
              {labelsChanged ? (
                <p className="text-muted-foreground text-sm">{intl.formatMessage(m.labels)}</p>
              ) : null}
            </li>
          );
        })}
    </ol>
  );
}
