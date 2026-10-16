import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Download, ScrollText, X } from "lucide-react";
import { useMemo, useState } from "react";
import { type IntlShape, useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/design/components/ui/dialog";
import { Badge } from "@/design/components/ui/display";
import { Field } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/design/components/ui/table";
import { useApi } from "@/lib/api/context";
import { type AuditQuery, type AuditRecord, auditExportURL, listAudit } from "../api";
import { ListSkeleton, PageHeader, TimeText } from "../components";
import { useAccountNames } from "../grants";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { formatTime, ownerLabel } from "../util";
import { listAllUsers } from "./users";

const everyone = "any";

interface Filters {
  outcome: string;
  actor: string;
  action: string;
  subject: string;
  kind: string;
  since: string;
  until: string;
}

const empty: Filters = {
  outcome: everyone,
  actor: everyone,
  action: "",
  subject: "",
  kind: "",
  since: "",
  until: "",
};

/** The query the filters make, for the list and the export alike. */
export function auditQuery(filters: Filters): AuditQuery {
  const query: AuditQuery = {};
  if (filters.outcome !== everyone) {
    query.outcome = filters.outcome as AuditQuery["outcome"];
  }
  if (filters.actor !== everyone) {
    query.actor = filters.actor;
  }
  const actions = filters.action
    .split(/[\s,]+/)
    .map((a) => a.trim())
    .filter(Boolean);
  if (actions.length > 0) {
    query.action = actions;
  }
  if (filters.subject.trim()) {
    query.subject = filters.subject.trim();
  }
  if (filters.kind.trim()) {
    query.kind = filters.kind.trim();
  }
  // Dates are the person's days: from the start of one to the end of the other.
  if (filters.since) {
    query.since = new Date(`${filters.since}T00:00:00`).toISOString();
  }
  if (filters.until) {
    query.until = new Date(`${filters.until}T23:59:59.999`).toISOString();
  }
  return query;
}

function Outcome({ outcome }: { outcome: AuditRecord["outcome"] }) {
  const intl = useIntl();
  switch (outcome) {
    case "success":
      return <Badge variant="success">{intl.formatMessage(t.outcomeSuccess)}</Badge>;
    case "denied":
      return <Badge variant="warning">{intl.formatMessage(t.outcomeDenied)}</Badge>;
  }
  return <Badge variant="danger">{intl.formatMessage(t.outcomeFailed)}</Badge>;
}

/** Who did something, for people: their name, through what, and for whom. */
function who(intl: IntlShape, record: AuditRecord): string {
  const actor = record.actor;
  const through = intl.formatMessage(t.auditVia, { name: actor.name, source: actor.source });
  return actor.impersonator
    ? intl.formatMessage(t.auditImpersonated, { who: through, admin: actor.impersonator })
    : through;
}

function Details({ record, onClose }: { record: AuditRecord | null; onClose: () => void }) {
  const intl = useIntl();
  const names = useAccountNames();
  return (
    <Sheet open={record !== null} onOpenChange={(open) => (open ? undefined : onClose())}>
      <SheetContent side="end" className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-lg">
        {record ? (
          <>
            <SheetTitle>{record.summary}</SheetTitle>
            <SheetDescription>{formatTime(intl, record.time)}</SheetDescription>
            <dl className="grid gap-x-4 gap-y-2 text-sm sm:grid-cols-[max-content_1fr]">
              <dt className="text-muted-foreground">{intl.formatMessage(t.auditWho)}</dt>
              <dd>{who(intl, record)}</dd>
              {record.actor.ip ? (
                <>
                  <dt className="text-muted-foreground">{intl.formatMessage(t.auditFrom)}</dt>
                  <dd className="font-mono">{record.actor.ip}</dd>
                </>
              ) : null}
              <dt className="text-muted-foreground">{intl.formatMessage(t.auditAction)}</dt>
              <dd className="font-mono">{record.action}</dd>
              <dt className="text-muted-foreground">{intl.formatMessage(t.auditOutcome)}</dt>
              <dd>
                <Outcome outcome={record.outcome} />
                {record.reason ? <span className="ms-2">{record.reason}</span> : null}
              </dd>
              <dt className="text-muted-foreground">{intl.formatMessage(t.auditOwner)}</dt>
              <dd>{ownerLabel(intl, record.scope, names)}</dd>
              {record.subject ? (
                <>
                  <dt className="text-muted-foreground">{intl.formatMessage(t.auditSubject)}</dt>
                  <dd className="break-all font-mono">{record.subject}</dd>
                </>
              ) : null}
              {record.changeSet ? (
                <>
                  <dt className="text-muted-foreground">{intl.formatMessage(t.auditChangeSet)}</dt>
                  <dd className="break-all font-mono">{record.changeSet}</dd>
                </>
              ) : null}
              {record.requestId ? (
                <>
                  <dt className="text-muted-foreground">{intl.formatMessage(t.auditRequest)}</dt>
                  <dd className="break-all font-mono">{record.requestId}</dd>
                </>
              ) : null}
            </dl>
            {record.details !== undefined && record.details !== null ? (
              <pre className="overflow-x-auto rounded-md bg-surface-sunken p-3 font-mono text-xs">
                {JSON.stringify(record.details, null, 2)}
              </pre>
            ) : null}
          </>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}

function Audit() {
  const intl = useIntl();
  const api = useApi();
  const users = useQuery({ queryKey: ["users"], queryFn: () => listAllUsers(api), retry: false });
  const [filters, setFilters] = useState<Filters>(empty);
  const [shown, setShown] = useState<AuditRecord | null>(null);
  const query = useMemo(() => auditQuery(filters), [filters]);
  const records = useInfiniteQuery({
    queryKey: ["audit", query],
    queryFn: ({ pageParam }) =>
      listAudit(api, { ...query, limit: 100, ...(pageParam ? { cursor: pageParam } : {}) }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
  });
  const rows = records.data?.pages.flatMap((p) => p.records) ?? [];
  const set = (key: keyof Filters) => (value: string) => setFilters({ ...filters, [key]: value });
  const filtered = JSON.stringify(filters) !== JSON.stringify(empty);
  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.auditTitle)}
        description={intl.formatMessage(t.auditDescription)}
        actions={
          <>
            <Button variant="outline" asChild>
              <a href={auditExportURL(query, "csv")} download="audit.csv">
                <Download aria-hidden="true" />
                {intl.formatMessage(t.auditExportCsv)}
              </a>
            </Button>
            <Button variant="outline" asChild>
              <a href={auditExportURL(query, "json")} download="audit.jsonl">
                <Download aria-hidden="true" />
                {intl.formatMessage(t.auditExportJson)}
              </a>
            </Button>
          </>
        }
      />
      <section
        aria-label={intl.formatMessage(t.auditFilters)}
        className="grid gap-3 rounded-lg border border-border bg-card p-4 sm:grid-cols-2 lg:grid-cols-4"
      >
        <Field label={intl.formatMessage(t.auditWho)}>
          <Select value={filters.actor} onValueChange={set("actor")}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={everyone}>{intl.formatMessage(t.auditAnyone)}</SelectItem>
              {(users.data ?? []).map((u) => (
                <SelectItem key={u.id} value={u.id}>
                  {u.name && u.name !== u.username ? `${u.name} (${u.username})` : u.username}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={intl.formatMessage(t.auditOutcome)}>
          <Select value={filters.outcome} onValueChange={set("outcome")}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={everyone}>{intl.formatMessage(t.auditAnyOutcome)}</SelectItem>
              <SelectItem value="success">{intl.formatMessage(t.outcomeSuccess)}</SelectItem>
              <SelectItem value="denied">{intl.formatMessage(t.outcomeDenied)}</SelectItem>
              <SelectItem value="failed">{intl.formatMessage(t.outcomeFailed)}</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field
          label={intl.formatMessage(t.auditAction)}
          description={intl.formatMessage(t.auditActionHelp)}
        >
          <Input
            value={filters.action}
            onChange={(e) => set("action")(e.target.value)}
            spellCheck={false}
          />
        </Field>
        <Field label={intl.formatMessage(t.auditSubject)}>
          <Input
            value={filters.subject}
            onChange={(e) => set("subject")(e.target.value)}
            spellCheck={false}
          />
        </Field>
        <Field label={intl.formatMessage(t.auditKind)}>
          <Input
            value={filters.kind}
            onChange={(e) => set("kind")(e.target.value)}
            spellCheck={false}
          />
        </Field>
        <Field label={intl.formatMessage(t.auditSince)}>
          <Input type="date" value={filters.since} onChange={(e) => set("since")(e.target.value)} />
        </Field>
        <Field label={intl.formatMessage(t.auditUntil)}>
          <Input type="date" value={filters.until} onChange={(e) => set("until")(e.target.value)} />
        </Field>
        <div className="flex items-end">
          <Button variant="ghost" onClick={() => setFilters(empty)} disabled={!filtered}>
            <X aria-hidden="true" />
            {intl.formatMessage(t.auditClear)}
          </Button>
        </div>
      </section>
      {records.isPending ? <ListSkeleton rows={6} /> : null}
      {records.isError ? (
        <ErrorState
          title={intl.formatMessage(t.auditFailed)}
          error={records.error}
          onRetry={() => void records.refetch()}
        />
      ) : null}
      {records.isSuccess && rows.length === 0 ? (
        <EmptyState
          icon={ScrollText}
          title={intl.formatMessage(filtered ? t.auditNoneFiltered : t.auditNone)}
        />
      ) : null}
      {rows.length > 0 ? (
        <div className="overflow-x-auto rounded-lg border border-border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{intl.formatMessage(t.auditWhen)}</TableHead>
                <TableHead>{intl.formatMessage(t.auditWho)}</TableHead>
                <TableHead>{intl.formatMessage(t.auditWhat)}</TableHead>
                <TableHead>{intl.formatMessage(t.auditOutcome)}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="whitespace-nowrap">
                    <TimeText value={r.time} />
                  </TableCell>
                  <TableCell>{who(intl, r)}</TableCell>
                  <TableCell>
                    <Button
                      variant="link"
                      className="h-auto whitespace-normal p-0 text-start"
                      onClick={() => setShown(r)}
                    >
                      {r.summary}
                    </Button>
                    <span className="block font-mono text-muted-foreground text-xs">
                      {r.action}
                    </span>
                  </TableCell>
                  <TableCell>
                    <Outcome outcome={r.outcome} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : null}
      {records.hasNextPage ? (
        <Button
          variant="outline"
          className="self-center"
          busy={records.isFetchingNextPage}
          onClick={() => void records.fetchNextPage()}
        >
          {intl.formatMessage(t.loadMore)}
        </Button>
      ) : null}
      <Details record={shown} onClose={() => setShown(null)} />
    </div>
  );
}

/** The audit log: who did what, when, and how it went, with filters and export. */
export default function AuditPage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={6} />}>
      <Audit />
    </IdentityMessages>
  );
}
