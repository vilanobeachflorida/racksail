import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { BellOff, CircleCheck, EllipsisVertical, ExternalLink, RotateCcw } from "lucide-react";
import { type ReactNode, useId, useState } from "react";
import { type IntlShape, useIntl } from "react-intl";
import { severityIcons, severityLine } from "@/app/activity";
import { useChangeTracker } from "@/app/changes/tracker";
import { useModules } from "@/app/modules";
import { useShell } from "@/app/shell-state";
import { docsURL } from "@/app/topbar";
import { Button } from "@/design/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Badge } from "@/design/components/ui/display";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/design/components/ui/overlay";
import { Skeleton } from "@/design/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { toast, toastWithUndo } from "@/design/components/ui/toaster";
import { useApi } from "@/lib/api/context";
import { retryJob } from "@/lib/api/operations";
import { errorMessage } from "@/lib/errors";
import type { Severity } from "@/lib/events";
import { useFormat } from "@/lib/format";
import { cn } from "@/lib/utils";
import { ScreenMessages } from "../catalog";
import {
  dismissItem,
  type InboxAction,
  type InboxItem,
  inboxKey,
  restoreItem,
  useInbox,
} from "./api";
import { inboxMessages as m } from "./messages";
import { inboxMessage, inboxProblemText, inboxTitle } from "./text";

/** How many items show before the rest are asked for. */
const shown = 5;

const day = 86_400_000;

/** When something happened, relative while it's recent, with the full date on hover. */
function Moment({ time }: { time: string }) {
  const f = useFormat();
  const date = new Date(time);
  return (
    <time dateTime={time} title={f.dateTime(date)}>
      {f.relative(date)}
    </time>
  );
}

/** A button's words for what an item's action does. */
function actionLabel(
  intl: IntlShape,
  action: InboxAction,
  moduleTitle: (id: string) => string,
): string {
  switch (action.type) {
    case "open_resource":
      return intl.formatMessage(m.actionOpen);
    case "open_job":
      return intl.formatMessage(m.actionLog);
    case "open_changeset":
      return intl.formatMessage(m.actionChange);
    case "retry_job":
      return intl.formatMessage(m.actionRetry);
    case "undo_changeset":
      return intl.formatMessage(m.actionUndo);
    case "open_events":
      return intl.formatMessage(m.actionEvents);
    case "docs": {
      const module = action.target?.match(/^modules\/([a-z0-9-]+)$/)?.[1];
      if (module) {
        return intl.formatMessage(m.actionAbout, { module: moduleTitle(module) });
      }
      return action.target === "install/updates"
        ? intl.formatMessage(m.actionUpdates)
        : action.title;
    }
  }
}

/** The label of an action that opens a new tab, which says so to screen readers. */
export function newTabLabel(intl: IntlShape, label: string): ReactNode {
  return intl.formatMessage(m.newTab, {
    label,
    hidden: (chunks) => (
      <span key="new-tab" className="sr-only">
        {chunks}
      </span>
    ),
  });
}

/** Runs an item's actions: most go somewhere, retry and undo act at once. */
function useItemActions() {
  const intl = useIntl();
  const api = useApi();
  const navigate = useNavigate();
  const shell = useShell();
  const tracker = useChangeTracker();
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const available = (action: InboxAction): boolean => Boolean(action.target);
  const run = async (item: InboxItem, action: InboxAction) => {
    const target = action.target ?? "";
    switch (action.type) {
      case "open_resource": {
        // A resource's screen is found by its kind, which loads when someone
        // goes there rather than with the home screen. A kind without
        // screens leads to the resource's events instead.
        const { kindOfId, kindsKey, listKinds, resourcePath } = await import("../resources/kinds");
        const kinds = await queryClient.fetchQuery({
          queryKey: kindsKey,
          queryFn: () => listKinds(api),
          staleTime: Number.POSITIVE_INFINITY,
        });
        const kind = kindOfId(kinds, target);
        void navigate(
          kind && !kind.ui.hidden
            ? { to: resourcePath(kind, target) }
            : { to: "/events", search: { subject: target } },
        );
        return;
      }
      case "open_job":
        shell.showJob(target);
        return;
      case "open_changeset":
        void navigate({ to: `/changes/${target}` });
        return;
      case "open_events":
        // Resource IDs never hold a dot and event types always do.
        void navigate({
          to: "/events",
          search: target.includes(".") ? { type: target } : { subject: target },
        });
        return;
      case "docs":
        window.open(`${docsURL}${target}/`, "_blank", "noopener,noreferrer");
        return;
      case "undo_changeset":
        await tracker.undo(target);
        return;
      case "retry_job": {
        setBusy(item.id);
        try {
          const job = await retryJob(api, target);
          void queryClient.invalidateQueries({ queryKey: inboxKey });
          shell.showJob(job.id);
        } catch (error) {
          toast.error(intl.formatMessage(m.retryFailed, { reason: errorMessage(intl, error) }));
        } finally {
          setBusy(null);
        }
        return;
      }
    }
  };
  return { available, run, busy };
}

/** Dismisses, snoozes and brings back items, offering to take a dismissal back. */
function useDismissals() {
  const intl = useIntl();
  const api = useApi();
  const queryClient = useQueryClient();
  const reload = () => queryClient.invalidateQueries({ queryKey: inboxKey });
  const dismiss = async (item: InboxItem, title: string, until?: Date) => {
    try {
      await dismissItem(api, item.id, until);
    } catch (error) {
      toast.error(intl.formatMessage(m.dismissFailed, { reason: errorMessage(intl, error) }));
      return;
    }
    void reload();
    toastWithUndo({
      message: until
        ? intl.formatMessage(m.snoozed, {
            title,
            until: intl.formatDate(until, { dateStyle: "medium", timeStyle: "short" }),
          })
        : intl.formatMessage(m.dismissed, { title }),
      undo: async () => {
        await restoreItem(api, item.id);
        await reload();
      },
      labels: {
        undo: intl.formatMessage(m.undo),
        undoing: intl.formatMessage(m.restoring),
        undone: intl.formatMessage(m.restored),
        failed: (error) =>
          intl.formatMessage(m.restoreFailed, { reason: errorMessage(intl, error) }),
      },
    });
  };
  const restore = async (item: InboxItem) => {
    try {
      await restoreItem(api, item.id);
      void reload();
    } catch (error) {
      toast.error(intl.formatMessage(m.restoreFailed, { reason: errorMessage(intl, error) }));
    }
  };
  return { dismiss, restore };
}

function ItemRow({
  item,
  actions,
  dismissals,
  moduleTitle,
}: {
  item: InboxItem;
  actions: ReturnType<typeof useItemActions>;
  dismissals: ReturnType<typeof useDismissals>;
  moduleTitle: (id: string) => string;
}) {
  const intl = useIntl();
  const titleId = useId();
  const title = inboxTitle(intl, item);
  const message = inboxMessage(intl, item);
  const { icon: Icon, tone } = severityIcons[item.severity as Severity] ?? severityIcons.info;
  const dismissal = item.dismissal;
  return (
    <li
      aria-labelledby={titleId}
      className={cn("flex gap-3 py-3 first:pt-0 last:pb-0", dismissal && "opacity-75")}
    >
      <Icon className={cn("mt-0.5 size-4 shrink-0", tone)} aria-hidden="true" />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <p id={titleId} className="break-words font-medium">
          {severityLine(intl, item.severity, title)}
        </p>
        {message ? <p className="break-words text-muted-foreground">{message}</p> : null}
        <p className="flex flex-wrap gap-x-3 text-muted-foreground text-xs">
          <span>{intl.formatMessage(m.category, { category: item.category })}</span>
          <Moment time={item.time} />
          {item.count && item.count > 1 ? (
            <span>{intl.formatMessage(m.times, { count: item.count })}</span>
          ) : null}
          {dismissal ? (
            <span>
              {dismissal.until
                ? intl.formatMessage(m.snoozedUntil, {
                    until: intl.formatDate(dismissal.until, {
                      dateStyle: "medium",
                      timeStyle: "short",
                    }),
                  })
                : intl.formatMessage(m.dismissedNote)}
            </span>
          ) : null}
        </p>
        {(item.actions ?? []).some(actions.available) ? (
          <div className="flex flex-wrap gap-2 pt-1">
            {(item.actions ?? []).filter(actions.available).map((action, i) => (
              <Button
                key={`${action.type}:${action.target ?? ""}`}
                size="sm"
                variant={i === 0 ? "outline" : "ghost"}
                busy={action.type === "retry_job" && actions.busy === item.id}
                onClick={() => void actions.run(item, action)}
              >
                {action.type === "retry_job" ? <RotateCcw aria-hidden="true" /> : null}
                {action.type === "docs" ? (
                  <>
                    {newTabLabel(intl, actionLabel(intl, action, moduleTitle))}
                    <ExternalLink aria-hidden="true" />
                  </>
                ) : (
                  actionLabel(intl, action, moduleTitle)
                )}
              </Button>
            ))}
          </div>
        ) : null}
      </div>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label={intl.formatMessage(m.more, { title })}>
            <EllipsisVertical aria-hidden="true" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {dismissal ? (
            <DropdownMenuItem onSelect={() => void dismissals.restore(item)}>
              <RotateCcw aria-hidden="true" />
              {intl.formatMessage(m.restore)}
            </DropdownMenuItem>
          ) : (
            <>
              <DropdownMenuItem onSelect={() => void dismissals.dismiss(item, title)}>
                <BellOff aria-hidden="true" />
                {intl.formatMessage(m.dismiss)}
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={() => void dismissals.dismiss(item, title, new Date(Date.now() + day))}
              >
                {intl.formatMessage(m.snoozeDay)}
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={() =>
                  void dismissals.dismiss(item, title, new Date(Date.now() + 7 * day))
                }
              >
                {intl.formatMessage(m.snoozeWeek)}
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  );
}

/**
 * The home screen's first card: what needs the person's attention, most
 * urgent first, kept current as things happen. Items can be acted on where
 * they are, dismissed until they change, or snoozed. Its words load with it.
 */
export default function InboxCard() {
  return (
    <ScreenMessages catalog="inbox" fallback={<Skeleton className="h-40 w-full rounded-lg" />}>
      <Inbox />
    </ScreenMessages>
  );
}

function Inbox() {
  const intl = useIntl();
  const [showDismissed, setShowDismissed] = useState(false);
  const [all, setAll] = useState(false);
  const inbox = useInbox(showDismissed);
  const modules = useModules();
  const actions = useItemActions();
  const dismissals = useDismissals();
  const moduleTitle = (id: string) => {
    const ui = modules.find((module) => module.id === id);
    return ui ? intl.formatMessage(ui.name) : id;
  };
  const data = inbox.data;
  const items = data?.items ?? [];
  const visible = all ? items : items.slice(0, shown);
  const waiting = items.filter((i) => !i.dismissal).length;
  const source = (name: string) => intl.formatMessage(m.source, { source: name });
  const coreKinds = visible.some((item) => item.kind?.startsWith("core."));
  const list = (
    <ul className="flex flex-col divide-y divide-border">
      {visible.map((item) => (
        <ItemRow
          key={item.id}
          item={item}
          actions={actions}
          dismissals={dismissals}
          moduleTitle={moduleTitle}
        />
      ))}
    </ul>
  );
  return (
    <Card aria-labelledby="inbox-title" aria-busy={inbox.isPending}>
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle id="inbox-title" className="flex items-center gap-2">
          {intl.formatMessage(m.title)}{" "}
          {waiting > 0 ? (
            <Badge variant="neutral">
              <span className="sr-only">{intl.formatMessage(m.count, { count: waiting })}</span>
              <span aria-hidden="true">{intl.formatNumber(waiting)}</span>
            </Badge>
          ) : null}
        </CardTitle>
        {data && (data.dismissed > 0 || showDismissed) ? (
          <Button
            variant="ghost"
            size="sm"
            aria-pressed={showDismissed}
            onClick={() => setShowDismissed((s) => !s)}
          >
            {intl.formatMessage(m.showDismissed, { count: data.dismissed })}
          </Button>
        ) : null}
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {inbox.isPending ? (
          <div className="flex flex-col gap-3">
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-4 w-1/2" />
            <Skeleton className="h-4 w-2/3" />
          </div>
        ) : inbox.isError ? (
          <ErrorState
            title={intl.formatMessage(m.failed)}
            error={inbox.error}
            onRetry={() => void inbox.refetch()}
          />
        ) : (
          <>
            {data && data.checking.length > 0 ? (
              <p role="status" className="text-muted-foreground">
                {intl.formatMessage(m.checking, {
                  parts: intl.formatList(data.checking.map(source), { type: "conjunction" }),
                })}
              </p>
            ) : null}
            {(data?.unavailable ?? []).map((p) => (
              <p key={p.source} className="text-muted-foreground">
                {intl.formatMessage(m.unavailable, {
                  part: source(p.source),
                  reason: inboxProblemText(intl, p),
                })}
              </p>
            ))}
            {items.length === 0 ? (
              <EmptyState
                icon={CircleCheck}
                title={intl.formatMessage(m.empty)}
                description={intl.formatMessage(m.emptyHelp)}
                className="py-6"
              />
            ) : coreKinds ? (
              // The core's kinds are named in a catalog of their own; until
              // it has loaded, or if it can't, they read in English.
              <ScreenMessages catalog="kinds" fallback={list} optional>
                {list}
              </ScreenMessages>
            ) : (
              list
            )}
            {items.length > shown ? (
              <Button
                variant="link"
                className="self-start"
                aria-expanded={all}
                onClick={() => setAll((a) => !a)}
              >
                {all
                  ? intl.formatMessage(m.fewer)
                  : intl.formatMessage(m.all, { count: items.length })}
              </Button>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  );
}
