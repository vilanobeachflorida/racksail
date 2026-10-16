import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { X } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Field } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { type Severity, severities } from "@/lib/events";
import { eventMessages as m } from "../messages";
import { kindPlural, useKinds } from "../resources/kinds";
import { ResourceLink } from "../resources/values";
import { type EventFilters, EventTimeline } from "./list";

const ranges = ["all", "hour", "day", "week", "month"] as const;
type Range = (typeof ranges)[number];
const rangeSeconds: Record<Exclude<Range, "all">, number> = {
  hour: 3600,
  day: 86_400,
  week: 7 * 86_400,
  month: 30 * 86_400,
};

/** The filters the screen keeps in its address, so a view can be shared and comes back. */
interface EventSearch {
  subject?: string;
  kind?: string;
  type?: string;
  severity?: Severity;
  range?: Range;
}

const any = "__any";

/** The event types the panel publishes, for suggestions as people type a pattern. */
function useEventTypes() {
  const api = useApi();
  return useQuery({
    queryKey: ["events", "types"],
    queryFn: async () => (await unwrap(api.GET("/api/v1/events/types"))).types ?? [],
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/**
 * The timeline of everything that happened that the person can see: by
 * day, newest first, live, narrowed by how urgent, what kind of thing, type
 * patterns, a resource and a time, all kept in the address.
 */
export default function EventsScreen() {
  const intl = useIntl();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as EventSearch;
  const kinds = useKinds().data ?? [];
  const types = useEventTypes();
  const typesId = useId();
  const [typing, setTyping] = useState(search.type ?? "");
  const range: Range = ranges.includes(search.range as Range) ? (search.range as Range) : "all";
  const severity = severities.includes(search.severity as Severity) ? search.severity : undefined;
  const set = (change: Partial<EventSearch>) =>
    void navigate({
      to: "/events",
      search: (prev: EventSearch) => {
        const next: Record<string, unknown> = { ...prev, ...change };
        for (const key of Object.keys(next)) {
          if (next[key] === undefined || next[key] === "") {
            delete next[key];
          }
        }
        return next;
      },
      replace: true,
    });
  // The start of a range is fixed when it's chosen, so the timeline isn't
  // asked for again on every render.
  const since = useMemo(
    () =>
      range === "all" ? undefined : new Date(Date.now() - rangeSeconds[range] * 1000).toISOString(),
    [range],
  );
  const filters = useMemo<EventFilters>(
    () => ({
      ...(search.subject ? { subject: search.subject } : {}),
      ...(search.kind ? { kind: search.kind } : {}),
      ...(search.type ? { type: [search.type] } : {}),
      ...(severity ? { minSeverity: severity } : {}),
      ...(since ? { since } : {}),
    }),
    [search.subject, search.kind, search.type, severity, since],
  );
  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h1 className="font-semibold text-2xl">{intl.formatMessage(m.title)}</h1>
          <p className="text-muted-foreground">{intl.formatMessage(m.description)}</p>
        </div>
        <Button asChild variant="outline">
          <Link to="/changes">{intl.formatMessage(m.changes)}</Link>
        </Button>
      </div>
      <section
        aria-label={intl.formatMessage(m.filters)}
        className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4"
      >
        <Field label={intl.formatMessage(m.severityFilter)}>
          {(control) => (
            <Select
              value={severity ?? any}
              onValueChange={(v) => set({ severity: v === any ? undefined : (v as Severity) })}
            >
              <SelectTrigger {...control}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={any}>{intl.formatMessage(m.anySeverity)}</SelectItem>
                {severities.map((s) => (
                  <SelectItem key={s} value={s}>
                    {intl.formatMessage(m.atLeast, { severity: s })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field label={intl.formatMessage(m.kindFilter)}>
          {(control) => (
            <Select
              value={search.kind ?? any}
              onValueChange={(v) => set({ kind: v === any ? undefined : v })}
            >
              <SelectTrigger {...control}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={any}>{intl.formatMessage(m.anyKind)}</SelectItem>
                {kinds.map((k) => (
                  <SelectItem key={k.name} value={k.name}>
                    {kindPlural(intl, k)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field
          label={intl.formatMessage(m.typeFilter)}
          description={intl.formatMessage(m.typeHelp)}
        >
          <Input
            value={typing}
            list={typesId}
            spellCheck={false}
            onChange={(e) => setTyping(e.target.value)}
            onBlur={() => set({ type: typing.trim() || undefined })}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                set({ type: typing.trim() || undefined });
              }
            }}
          />
        </Field>
        <datalist id={typesId}>
          {(types.data ?? []).map((t) => (
            <option key={t.name} value={t.name}>
              {t.description}
            </option>
          ))}
        </datalist>
        <Field label={intl.formatMessage(m.rangeFilter)}>
          {(control) => (
            <Select
              value={range}
              onValueChange={(v) => set({ range: v === "all" ? undefined : (v as Range) })}
            >
              <SelectTrigger {...control}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ranges.map((r) => (
                  <SelectItem key={r} value={r}>
                    {intl.formatMessage(m.range, { range: r })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
      </section>
      {search.subject ? (
        <p className="flex flex-wrap items-center gap-2 text-sm">
          {intl.formatMessage(m.aboutSubject)}
          <ResourceLink id={search.subject} kinds={kinds} />
          <Button variant="ghost" size="sm" onClick={() => set({ subject: undefined })}>
            <X aria-hidden="true" />
            {intl.formatMessage(m.clearSubject)}
          </Button>
        </p>
      ) : null}
      <EventTimeline filters={filters} />
    </div>
  );
}
