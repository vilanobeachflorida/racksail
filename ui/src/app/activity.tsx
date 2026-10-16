import { Link, useNavigate } from "@tanstack/react-router";
import {
  Bell,
  CircleX,
  Info,
  type LucideIcon,
  OctagonAlert,
  Search,
  TriangleAlert,
} from "lucide-react";
import { lazy, type ReactNode, Suspense } from "react";
import { type IntlShape, useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Skeleton } from "@/design/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { useApi } from "@/lib/api/context";
import { listEvents, streams } from "@/lib/api/operations";
import { eventText, type Severity, type TimelineEvent } from "@/lib/events";
import { useFormat } from "@/lib/format";
import { useLiveQuery } from "@/lib/live";
import { cn } from "@/lib/utils";
import { messages } from "./messages";
import { moduleCommands, useActiveModules, usePermission } from "./modules";
import { useShell } from "./shell-state";
import { ariaKeyShortcuts } from "./shortcuts";

const shown = 8;

/** How many quick actions the card offers before the command bar. */
const quickActions = 6;

// New resources of kinds no module starts itself fill the card, with code
// and words that load apart from the shell.
const NewResourceActions = lazy(() => import("./screens/resources/quick"));

/** How each severity looks: an icon, and the color that goes with it. */
export const severityIcons: Record<Severity, { icon: LucideIcon; tone: string }> = {
  info: { icon: Info, tone: "text-muted-foreground" },
  notice: { icon: Bell, tone: "text-info" },
  warning: { icon: TriangleAlert, tone: "text-warning" },
  error: { icon: CircleX, tone: "text-danger" },
  critical: { icon: OctagonAlert, tone: "text-danger" },
};

/**
 * A line of activity, such as an event or an item that needs attention,
 * with how urgent it is said first to screen readers, as its icon shows it.
 */
export function severityLine(intl: IntlShape, severity: string, text: string): ReactNode {
  return intl.formatMessage(messages.activityLine, {
    severity,
    text,
    hidden: (chunks) => (
      <span key="severity" className="sr-only">
        {chunks}
      </span>
    ),
  });
}

/** The home card of the latest events, kept current as they happen. */
export function RecentActivity() {
  const intl = useIntl();
  const f = useFormat();
  const api = useApi();
  const events = useLiveQuery<TimelineEvent[], TimelineEvent>({
    queryKey: ["events", "recent"],
    queryFn: async () => (await listEvents(api, { limit: shown })).events ?? [],
    stream: { url: streams.events, types: ["event"] },
    apply: (list, event) =>
      [event.data, ...(list ?? []).filter((e) => e.id !== event.data.id)].slice(0, shown),
  });
  const title = intl.formatMessage(messages.activityTitle);
  return (
    <Card aria-labelledby="activity-title" aria-busy={events.isPending}>
      <CardHeader>
        <CardTitle id="activity-title">{title}</CardTitle>
      </CardHeader>
      <CardContent>
        {events.isPending ? (
          <div className="flex flex-col gap-3">
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-4 w-1/2" />
          </div>
        ) : events.isError ? (
          <ErrorState
            title={intl.formatMessage(messages.activityFailed)}
            error={events.error}
            onRetry={() => void events.refetch()}
          />
        ) : events.data.length === 0 ? (
          <EmptyState
            title={intl.formatMessage(messages.activityEmpty)}
            description={intl.formatMessage(messages.activityEmptyHelp)}
            className="py-6"
          />
        ) : (
          <ol className="flex flex-col gap-3">
            {events.data.map((event) => {
              const { icon: Icon, tone } = severityIcons[event.severity] ?? severityIcons.info;
              return (
                <li key={event.id} className="flex gap-3">
                  <Icon className={cn("mt-0.5 size-4 shrink-0", tone)} aria-hidden="true" />
                  <div className="flex min-w-0 flex-col gap-0.5">
                    <p className="text-sm">
                      {severityLine(intl, event.severity, eventText(intl, event))}
                    </p>
                    <p className="text-muted-foreground text-xs">
                      {intl.formatMessage(messages.activityWhen, {
                        time: (
                          <time dateTime={event.time} title={f.dateTime(new Date(event.time))}>
                            {f.relative(new Date(event.time))}
                          </time>
                        ),
                        hasActor: event.actor?.name ? "yes" : "no",
                        actor: event.actor?.name ?? "",
                      })}
                    </p>
                  </div>
                </li>
              );
            })}
          </ol>
        )}
        <Button asChild variant="link" className="mt-3">
          <Link to="/events">{intl.formatMessage(messages.activityAll)}</Link>
        </Button>
      </CardContent>
    </Card>
  );
}

/**
 * The home card of things to start: the actions modules offer to the
 * person, new resources of the kinds they may create, and the command bar.
 */
export function QuickActions() {
  const intl = useIntl();
  const shell = useShell();
  const navigate = useNavigate();
  const modules = useActiveModules();
  const allowed = usePermission();
  const actions = moduleCommands(modules, allowed).slice(0, quickActions);
  return (
    <Card aria-labelledby="quick-actions-title">
      <CardHeader>
        <CardTitle id="quick-actions-title">
          {intl.formatMessage(messages.quickActionsTitle)}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {actions.map((action) => (
          <Button
            key={action.key}
            variant="outline"
            className="justify-start"
            aria-keyshortcuts={action.shortcut ? ariaKeyShortcuts(action.shortcut) : undefined}
            onClick={() => void action.run({ navigate: (to) => void navigate({ to }) })}
          >
            {action.icon ? <action.icon aria-hidden="true" /> : null}
            {intl.formatMessage(action.label)}
          </Button>
        ))}
        <Suspense fallback={null}>
          <NewResourceActions count={quickActions - actions.length} />
        </Suspense>
        <Button
          variant="ghost"
          className="justify-start text-muted-foreground"
          aria-keyshortcuts={ariaKeyShortcuts("mod+k")}
          onClick={() => shell.setCommandOpen(true)}
        >
          <Search aria-hidden="true" />
          {intl.formatMessage(messages.quickActionsMore)}
        </Button>
      </CardContent>
    </Card>
  );
}
