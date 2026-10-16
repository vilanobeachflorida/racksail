import { type InfiniteData, useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { History } from "lucide-react";
import { useId, useMemo } from "react";
import { useIntl } from "react-intl";
import { severityIcons } from "@/app/activity";
import { Button } from "@/design/components/ui/button";
import { Skeleton } from "@/design/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { useApi } from "@/lib/api/context";
import { type EventQuery, listEvents, streams } from "@/lib/api/operations";
import {
  eventText,
  type Severity,
  severities,
  type TimelineEvent,
  type TimelineEventList,
} from "@/lib/events";
import { useFormat } from "@/lib/format";
import { useLiveEvents } from "@/lib/live";
import { cn } from "@/lib/utils";
import { eventMessages as m } from "../messages";
import { kindOfId, resourcePath, useKinds } from "../resources/kinds";

/** What a timeline shows. */
export interface EventFilters {
  subject?: string;
  kind?: string;
  /** Event types as patterns, such as web.site.*. */
  type?: string[];
  minSeverity?: Severity;
  changeSet?: string;
  job?: string;
  since?: string;
  until?: string;
}

/** Whether an event type matches a pattern as the panel matches them: * and ?, and a.* for everything under a. */
export function matchType(pattern: string, type: string): boolean {
  const source = pattern
    .split("")
    .map((c) => (c === "*" ? ".*" : c === "?" ? "." : c.replace(/[.+^${}()|[\]\\]/g, "\\$&")))
    .join("");
  if (new RegExp(`^${source}$`).test(type)) {
    return true;
  }
  return pattern.endsWith(".*") && type.startsWith(pattern.slice(0, -1));
}

/** Whether a new event belongs on a timeline with these filters. */
export function eventMatches(event: TimelineEvent, f: EventFilters): boolean {
  const time = Date.parse(event.time);
  return (
    (!f.subject || event.subject === f.subject) &&
    (!f.kind || event.kind === f.kind) &&
    (!f.changeSet || event.changeSet === f.changeSet) &&
    (!f.job || event.job === f.job) &&
    (!f.type?.length || f.type.some((p) => matchType(p, event.type))) &&
    (!f.minSeverity || severities.indexOf(event.severity) >= severities.indexOf(f.minSeverity)) &&
    (!f.since || time >= Date.parse(f.since)) &&
    (!f.until || time < Date.parse(f.until))
  );
}

const pageSize = 50;

/**
 * Events from the timeline, newest first, a page at a time, with new ones
 * arriving at the top as they happen.
 */
export function useTimeline(filters: EventFilters) {
  const api = useApi();
  const queryClient = useQueryClient();
  const key = useMemo(() => ["events", "timeline", filters] as const, [filters]);
  const query = useInfiniteQuery({
    queryKey: key,
    queryFn: ({ pageParam }) => {
      const q: EventQuery = { ...filters, limit: pageSize };
      if (pageParam) {
        q.cursor = pageParam;
      }
      return listEvents(api, q);
    },
    initialPageParam: "",
    getNextPageParam: (last: TimelineEventList) => last.next || undefined,
    staleTime: Number.POSITIVE_INFINITY,
  });
  // A filter that ends in the past gets nothing new.
  const live = !filters.until || Date.parse(filters.until) > Date.now();
  useLiveEvents<TimelineEvent>(
    query.isSuccess && live ? streams.events : null,
    ["event"],
    (event) => {
      if (!eventMatches(event.data, filters)) {
        return;
      }
      queryClient.setQueryData<InfiniteData<TimelineEventList, string>>(key, (data) => {
        if (!data) {
          return data;
        }
        const [first, ...rest] = data.pages;
        if (!first || (first.events ?? []).some((e) => e.id === event.data.id)) {
          return data;
        }
        return {
          ...data,
          pages: [{ ...first, events: [event.data, ...(first.events ?? [])] }, ...rest],
        };
      });
    },
    () => void queryClient.invalidateQueries({ queryKey: key }),
  );
  const events = useMemo(
    () => (query.data?.pages ?? []).flatMap((p) => p.events ?? []),
    [query.data],
  );
  return { ...query, events };
}

/** One event: how urgent, what happened in the person's language, who did it and what it's about. */
export function EventItem({ event }: { event: TimelineEvent }) {
  const intl = useIntl();
  const f = useFormat();
  const kinds = useKinds().data ?? [];
  const { icon: Icon, tone } = severityIcons[event.severity] ?? severityIcons.info;
  const subjectKind = event.subject ? kindOfId(kinds, event.subject) : undefined;
  const date = new Date(event.time);
  return (
    <li className="flex gap-3 py-2">
      <Icon className={cn("mt-0.5 size-4 shrink-0", tone)} aria-hidden="true" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <p className="text-sm">
          <span className="sr-only">
            {intl.formatMessage(m.severity, { severity: event.severity })}{" "}
          </span>
          {eventText(intl, event)}
        </p>
        <p className="flex flex-wrap gap-x-3 gap-y-1 text-muted-foreground text-xs">
          <time dateTime={event.time} title={f.dateTime(date)}>
            {f.time(date)}
          </time>
          {event.actor?.name ? (
            <span>
              {intl.formatMessage(m.by, { actor: event.actor.name, source: event.actor.source })}
            </span>
          ) : null}
          <code className="font-mono" translate="no">
            {event.type}
          </code>
          {event.subject && subjectKind && !subjectKind.ui.hidden ? (
            <Link
              to={resourcePath(subjectKind, event.subject)}
              className="rounded-sm text-primary underline-offset-4 hover:underline focus-visible:focus-ring"
            >
              {intl.formatMessage(m.openSubject)}
            </Link>
          ) : null}
          {event.changeSet ? (
            <Link
              to={`/changes/${event.changeSet}`}
              className="rounded-sm text-primary underline-offset-4 hover:underline focus-visible:focus-ring"
            >
              {intl.formatMessage(m.openChange)}
            </Link>
          ) : null}
          {event.job ? (
            <Link
              to={`/jobs/${event.job}`}
              className="rounded-sm text-primary underline-offset-4 hover:underline focus-visible:focus-ring"
            >
              {intl.formatMessage(m.openJob)}
            </Link>
          ) : null}
        </p>
      </div>
    </li>
  );
}

/** A timeline: events grouped by day, newest first, with older ones a button away. */
export function EventTimeline({
  filters,
  emptyTitle,
  emptyHelp,
}: {
  filters: EventFilters;
  emptyTitle?: string;
  emptyHelp?: string;
}) {
  const intl = useIntl();
  const f = useFormat();
  const timeline = useTimeline(filters);
  const headingId = useId();
  if (timeline.isPending) {
    return (
      <div className="flex flex-col gap-3" aria-busy="true">
        <Skeleton className="h-4 w-1/3" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
      </div>
    );
  }
  if (timeline.isError) {
    return (
      <ErrorState
        title={intl.formatMessage(m.failed)}
        error={timeline.error}
        onRetry={() => void timeline.refetch()}
      />
    );
  }
  if (timeline.events.length === 0) {
    return (
      <EmptyState
        icon={History}
        title={emptyTitle ?? intl.formatMessage(m.empty)}
        description={emptyHelp ?? intl.formatMessage(m.emptyHelp)}
      />
    );
  }
  const days: { day: string; date: string; events: TimelineEvent[] }[] = [];
  for (const event of timeline.events) {
    const day = f.date(new Date(event.time));
    const last = days.at(-1);
    if (last?.day === day) {
      last.events.push(event);
    } else {
      days.push({ day, date: event.time, events: [event] });
    }
  }
  return (
    <div className="flex flex-col gap-4">
      {days.map(({ day, date, events }, i) => (
        <section key={day} aria-labelledby={`${headingId}-${i}`} className="flex flex-col">
          <h3
            id={`${headingId}-${i}`}
            className="sticky top-0 bg-background py-1 font-semibold text-muted-foreground text-xs"
          >
            <time dateTime={date}>{day}</time>
          </h3>
          <ol className="flex flex-col divide-y divide-border">
            {events.map((event) => (
              <EventItem key={event.id} event={event} />
            ))}
          </ol>
        </section>
      ))}
      {timeline.hasNextPage ? (
        <Button
          variant="outline"
          className="self-center"
          busy={timeline.isFetchingNextPage}
          onClick={() => void timeline.fetchNextPage()}
        >
          {intl.formatMessage(m.older)}
        </Button>
      ) : null}
    </div>
  );
}
