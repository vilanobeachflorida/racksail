import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { History, Undo2 } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { mayUndo, useChangeTracker } from "@/app/changes/tracker";
import { usePermission } from "@/app/modules";
import { RiskBadge } from "@/design/components/change-preview";
import { DataTable } from "@/design/components/data-table/data-table";
import type { Column } from "@/design/components/data-table/model";
import { Button } from "@/design/components/ui/button";
import { Switch } from "@/design/components/ui/choice";
import { Badge } from "@/design/components/ui/display";
import { Label } from "@/design/components/ui/field";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { designMessages } from "@/design/messages";
import { useApi } from "@/lib/api/context";
import { streams } from "@/lib/api/operations";
import type { ChangeSet, ChangeSetState } from "@/lib/changes";
import type { TimelineEvent } from "@/lib/events";
import { useLiveEvents } from "@/lib/live";
import { changeListMessages as m } from "../messages";
import { When } from "../resources/values";
import { listChangeSets } from "./api";

const states: ChangeSetState[] = ["planned", "applying", "applied", "failed", "undone"];

/** Undo for a change, when the person may undo it. */
function UndoButton({ changeSet }: { changeSet: ChangeSet }) {
  const intl = useIntl();
  const tracker = useChangeTracker();
  const allowed = usePermission();
  if (!mayUndo(changeSet, allowed)) {
    return null;
  }
  return (
    <Button variant="ghost" size="sm" onClick={() => void tracker.undo(changeSet.id)}>
      <Undo2 aria-hidden="true" />
      {intl.formatMessage(m.undo)}
    </Button>
  );
}

/** A change set's state, as a badge in the preview's words. */
export function ChangeState({ state }: { state: ChangeSetState }) {
  const intl = useIntl();
  return (
    <Badge
      variant={
        state === "applied"
          ? "success"
          : state === "failed"
            ? "danger"
            : state === "undone"
              ? "outline"
              : "neutral"
      }
    >
      {intl.formatMessage(designMessages.previewState, { state })}
    </Badge>
  );
}

/**
 * Every change the person can see, newest first: who made it, what it
 * touched and how applying it went, with undo while it can be done. With
 * previews on, it lists the dry runs that can still be applied.
 */
export default function ChangesScreen() {
  const intl = useIntl();
  const api = useApi();
  const queryClient = useQueryClient();
  const [previews, setPreviews] = useState(false);
  const changes = useInfiniteQuery({
    queryKey: ["changesets", "all", previews],
    queryFn: ({ pageParam }) =>
      listChangeSets(api, { limit: 200, previews, ...(pageParam ? { cursor: pageParam } : {}) }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next,
  });
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reload = () => {
    if (!timer.current) {
      timer.current = setTimeout(() => {
        timer.current = null;
        void queryClient.invalidateQueries({ queryKey: ["changesets", "all"] });
      }, 250);
    }
  };
  useLiveEvents<TimelineEvent>(
    streams.events,
    ["event"],
    (event) => {
      if (event.data.type.startsWith("core.changeset.") || event.data.changeSet) {
        reload();
      }
    },
    reload,
  );
  const rows = useMemo(
    () => (changes.data?.pages ?? []).flatMap((p) => p.changeSets),
    [changes.data],
  );
  const columns = useMemo<Column<ChangeSet>[]>(
    () => [
      {
        id: "title",
        header: intl.formatMessage(m.columnTitle),
        value: (cs) => cs.title,
        sortable: true,
        width: "minmax(14rem, 2fr)",
        cell: (cs) => (
          <Link
            to={`/changes/${cs.id}`}
            className="rounded-sm font-medium text-primary underline-offset-4 hover:underline focus-visible:focus-ring"
          >
            {cs.title}
          </Link>
        ),
      },
      {
        id: "state",
        header: intl.formatMessage(m.columnState),
        value: (cs) => cs.state,
        sortable: true,
        filter: {
          options: states.map((s) => ({
            value: s,
            label: intl.formatMessage(designMessages.previewState, { state: s }),
          })),
        },
        cell: (cs) => <ChangeState state={cs.state} />,
      },
      {
        id: "risk",
        header: intl.formatMessage(m.columnRisk),
        value: (cs) => cs.risk,
        sortable: true,
        cell: (cs) => <RiskBadge risk={cs.risk} />,
      },
      {
        id: "actor",
        header: intl.formatMessage(m.columnActor),
        value: (cs) => cs.actor?.name ?? "",
        sortable: true,
        filter: "text",
      },
      {
        id: "changes",
        header: intl.formatMessage(m.columnChanges),
        value: (cs) => (cs.changes ?? []).length,
        sortable: true,
        align: "end",
      },
      {
        id: "createdAt",
        header: intl.formatMessage(m.columnWhen),
        value: (cs) => new Date(cs.appliedAt ?? cs.createdAt),
        sortable: true,
        cell: (cs) => <When time={cs.appliedAt ?? cs.createdAt} />,
      },
      {
        id: "undo",
        header: intl.formatMessage(m.columnUndo),
        width: "7rem",
        align: "end",
        hideable: false,
        cell: (cs) => <UndoButton changeSet={cs} />,
      },
    ],
    [intl],
  );
  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h1 className="font-semibold text-2xl">{intl.formatMessage(m.title)}</h1>
          <p className="text-muted-foreground">{intl.formatMessage(m.description)}</p>
        </div>
        <div className="flex items-center gap-2">
          <Switch id="changes-previews" checked={previews} onCheckedChange={setPreviews} />
          <Label htmlFor="changes-previews">{intl.formatMessage(m.previews)}</Label>
        </div>
      </div>
      {changes.isError ? (
        <ErrorState
          title={intl.formatMessage(m.failed)}
          error={changes.error}
          onRetry={() => void changes.refetch()}
        />
      ) : (
        <DataTable
          id={previews ? "changes.previews" : "changes"}
          label={intl.formatMessage(m.title)}
          rows={rows}
          rowKey={(cs) => cs.id}
          columns={columns}
          loading={changes.isPending}
          empty={
            <EmptyState
              icon={History}
              title={intl.formatMessage(previews ? m.noPreviews : m.empty)}
              description={intl.formatMessage(m.emptyHelp)}
            />
          }
        />
      )}
      {changes.hasNextPage ? (
        <Button
          variant="outline"
          className="self-center"
          busy={changes.isFetchingNextPage}
          onClick={() => void changes.fetchNextPage()}
        >
          {intl.formatMessage(m.more)}
        </Button>
      ) : null}
    </div>
  );
}
