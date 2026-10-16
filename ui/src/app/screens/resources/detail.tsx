import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Check, Copy, MoreHorizontal, Pencil, Trash2 } from "lucide-react";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { AddonFrame, addonEntryKey, useAddonEntries } from "@/app/addons";
import { usePermission } from "@/app/modules";
import { Button } from "@/design/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Alert, Badge } from "@/design/components/ui/display";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Tooltip,
} from "@/design/components/ui/overlay";
import { Skeleton } from "@/design/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/design/components/ui/tabs";
import { toast } from "@/design/components/ui/toaster";
import type { FieldHint } from "@/lib/forms/model";
import type { JSONSchema } from "@/lib/schema";
import { EventTimeline } from "../events/list";
import { resourceMessages as m } from "../messages";
import { useRecordRecent } from "../recent";
import { useResourceChanges } from "./actions";
import { ResourceHistory } from "./history";
import {
  actionTitle,
  type Condition,
  commonStatus,
  type Kind,
  kindPath,
  kindTitle,
  phaseOf,
  type Resource,
  resourcePath,
  resourceSubtitle,
  resourceTitle,
  useKindSchemas,
} from "./kinds";
import { useRouteKind } from "./layout";
import { useResource } from "./queries";
import { PhaseBadge, SchemaFields, type ValueContext, When } from "./values";

/** The status fields every kind shares, which the status tab shows on their own. */
const commonFields = new Set(["phase", "message", "code", "observedGeneration", "conditions"]);

/** A resource's ID, which people copy into tickets, commands and the API. */
function CopyID({ id }: { id: string }) {
  const intl = useIntl();
  const [copied, setCopied] = useState(false);
  // The browser may not let the page write to the clipboard, as without
  // HTTPS or when the person said no; the ID can still be selected.
  const failed = () => toast.error(intl.formatMessage(m.copyFailed));
  return (
    <span className="inline-flex items-center gap-1">
      <code className="font-mono text-muted-foreground text-xs" translate="no">
        {id}
      </code>
      <Tooltip content={intl.formatMessage(copied ? m.copied : m.copyID)}>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={intl.formatMessage(copied ? m.copied : m.copyID)}
          onClick={() => {
            if (!navigator.clipboard) {
              failed();
              return;
            }
            navigator.clipboard.writeText(id).then(() => setCopied(true), failed);
          }}
        >
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
        </Button>
      </Tooltip>
    </span>
  );
}

/** A condition's state, in words and with a dot, as the status tab lists them. */
function ConditionsTable({ conditions }: { conditions: Condition[] }) {
  const intl = useIntl();
  return (
    <table className="w-full text-sm">
      <caption className="sr-only">{intl.formatMessage(m.conditions)}</caption>
      <thead>
        <tr className="border-border border-b text-muted-foreground text-xs">
          <th scope="col" className="py-2 pe-4 text-start font-medium">
            {intl.formatMessage(m.conditionType)}
          </th>
          <th scope="col" className="py-2 pe-4 text-start font-medium">
            {intl.formatMessage(m.conditionStatus)}
          </th>
          <th scope="col" className="py-2 pe-4 text-start font-medium">
            {intl.formatMessage(m.conditionReason)}
          </th>
          <th scope="col" className="py-2 text-start font-medium">
            {intl.formatMessage(m.conditionSince)}
          </th>
        </tr>
      </thead>
      <tbody>
        {conditions.map((c) => (
          <tr key={c.type} className="border-border border-b align-top last:border-b-0">
            <th scope="row" className="py-2 pe-4 text-start font-medium" translate="no">
              {c.type}
            </th>
            <td className="py-2 pe-4">
              <Badge
                variant={
                  c.status === "true" ? "success" : c.status === "false" ? "danger" : "neutral"
                }
              >
                {intl.formatMessage(m.conditionValue, { status: c.status })}
              </Badge>
            </td>
            <td className="py-2 pe-4">
              {c.reason ? (
                <span className="font-mono text-xs" translate="no">
                  {c.reason}
                </span>
              ) : null}
              {c.message ? <p className="text-muted-foreground">{c.message}</p> : null}
            </td>
            <td className="py-2 text-muted-foreground">
              <When time={c.lastTransitionTime} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** Who owns a resource, where it runs and its bookkeeping. */
function Facts({ kind, resource }: { kind: Kind; resource: Resource }) {
  const intl = useIntl();
  const labels = Object.entries(resource.labels ?? {});
  const row = (term: string, value: ReactNode) => (
    <div className="contents">
      <dt className="font-medium text-muted-foreground text-sm">{term}</dt>
      <dd className="min-w-0 text-sm">{value}</dd>
    </div>
  );
  return (
    <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-[minmax(10rem,auto)_1fr]">
      {row(intl.formatMessage(m.factKind), kindTitle(intl, kind))}
      {row(
        intl.formatMessage(m.factOwner),
        intl.formatMessage(m.scope, { level: resource.scope.level, id: resource.scope.id ?? "" }),
      )}
      {resource.server ? row(intl.formatMessage(m.factServer), resource.server) : null}
      {row(intl.formatMessage(m.factCreated), <When time={resource.createdAt} />)}
      {row(intl.formatMessage(m.factChanged), <When time={resource.updatedAt} />)}
      {row(
        intl.formatMessage(m.factVersion),
        intl.formatMessage(m.versionOf, {
          generation: resource.generation,
          revision: resource.revision,
        }),
      )}
      {row(
        intl.formatMessage(m.factLabels),
        labels.length > 0 ? (
          <ul className="flex flex-wrap gap-1">
            {labels.map(([key, value]) => (
              <li key={key}>
                <Badge variant="outline" className="font-mono">
                  {value ? `${key}=${value}` : key}
                </Badge>
              </li>
            ))}
          </ul>
        ) : (
          <span className="text-muted-foreground">{intl.formatMessage(m.none)}</span>
        ),
      )}
    </dl>
  );
}

/**
 * A resource, until its module has a screen of its own: its settings,
 * what Racksail last saw of it with the reason for that, the history of its
 * settings with undo, its events, and its actions, which go through the
 * change preview like every change.
 */
export default function ResourceDetail() {
  const intl = useIntl();
  const navigate = useNavigate();
  const allowed = usePermission();
  const { kind, kinds, id } = useRouteKind();
  const resource = useResource(kind, id);
  const schemas = useKindSchemas(kind?.name);
  const changes = useResourceChanges();
  const data = resource.data;
  // Add-ons' tabs on the kind's resources show their pages about the one
  // on screen.
  const addonTabs = useAddonEntries("ui.tab").filter((e) => kind && e.ui.kind === kind.name);
  // The command bar opens the screen with ?action= to run one of the
  // resource's actions, or delete it, through the same preview.
  const search = useSearch({ strict: false }) as { action?: unknown };
  const asked = typeof search.action === "string" ? search.action : "";
  const handled = useRef("");
  useEffect(() => {
    if (!asked) {
      handled.current = "";
      return;
    }
    if (!kind || !data || handled.current === asked) {
      return;
    }
    handled.current = asked;
    void navigate({ to: resourcePath(kind, data.id), replace: true });
    if (data.deletedAt) {
      return;
    }
    if (asked === "delete" && allowed(`${kind.name}.delete`)) {
      changes.launch({
        kind,
        resources: [data],
        onApplied: () => void navigate({ to: kindPath(kind) }),
      });
      return;
    }
    const action = (kind.actions ?? []).find((a) => a.name === asked);
    if (action && allowed(`${kind.name}.${action.name}`)) {
      changes.launch({ kind, resources: [data], action });
    }
  }, [asked, kind, data, navigate, allowed, changes]);
  useRecordRecent(
    kind && data
      ? {
          path: resourcePath(kind, data.id),
          title: resourceTitle(kind, data),
          what: kindTitle(intl, kind),
        }
      : null,
  );
  if (!kind || !data) {
    return null;
  }
  const may = (operation: string) => allowed(`${kind.name}.${operation}`);
  const status = commonStatus(data);
  const phase = phaseOf(data);
  const deleting = Boolean(data.deletedAt);
  const behind = !deleting && (status.observedGeneration ?? 0) < data.generation;
  const spec = schemas.data?.spec as JSONSchema | undefined;
  const statusSchema = schemas.data?.status as JSONSchema | undefined;
  const context: ValueContext = {
    kind,
    kinds,
    root: spec ?? {},
    hints: (kind.ui.fields ?? {}) as Record<string, FieldHint>,
  };
  const extraStatus = Object.keys(statusSchema?.properties ?? {}).filter(
    (k) => !commonFields.has(k),
  );
  const actions = (kind.actions ?? []).filter((a) => may(a.name));
  const subtitle = resourceSubtitle(kind, data);
  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex min-w-0 flex-col gap-1">
          <p className="text-muted-foreground text-sm">{kindTitle(intl, kind)}</p>
          <h1 className="break-words font-semibold text-2xl">{resourceTitle(kind, data)}</h1>
          {subtitle ? <p className="text-muted-foreground">{subtitle}</p> : null}
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
            <PhaseBadge phase={phase} />
            <CopyID id={data.id} />
          </div>
        </div>
        {deleting ? null : (
          <div className="flex flex-wrap gap-2">
            {may("update") ? (
              <Button asChild variant="outline">
                <Link to={`${resourcePath(kind, data.id)}/edit`}>
                  <Pencil aria-hidden="true" />
                  {intl.formatMessage(m.edit)}
                </Link>
              </Button>
            ) : null}
            {actions.length > 0 ? (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline">
                    <MoreHorizontal aria-hidden="true" />
                    {intl.formatMessage(m.actions)}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  {actions.map((a) => (
                    <DropdownMenuItem
                      key={a.name}
                      onSelect={() => changes.launch({ kind, resources: [data], action: a })}
                    >
                      {actionTitle(intl, kind, a)}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            ) : null}
            {may("delete") ? (
              <Button
                variant="outline"
                onClick={() =>
                  changes.launch({
                    kind,
                    resources: [data],
                    onApplied: () => void navigate({ to: kindPath(kind) }),
                  })
                }
              >
                <Trash2 aria-hidden="true" />
                {intl.formatMessage(m.delete)}
              </Button>
            ) : null}
          </div>
        )}
      </header>

      {deleting ? <Alert tone="warning">{intl.formatMessage(m.beingRemoved)}</Alert> : null}
      {behind ? <Alert tone="info">{intl.formatMessage(m.applyingLatest)}</Alert> : null}
      {status.message && (phase === "failed" || phase === "degraded") ? (
        <Alert
          tone={phase === "failed" ? "danger" : "warning"}
          title={intl.formatMessage(m.phase, { phase })}
        >
          {status.message}
        </Alert>
      ) : null}

      <Tabs defaultValue="overview" className="flex flex-col">
        <TabsList aria-label={intl.formatMessage(m.sections)}>
          <TabsTrigger value="overview">{intl.formatMessage(m.tabOverview)}</TabsTrigger>
          <TabsTrigger value="status">{intl.formatMessage(m.tabStatus)}</TabsTrigger>
          <TabsTrigger value="history">{intl.formatMessage(m.tabHistory)}</TabsTrigger>
          <TabsTrigger value="events">{intl.formatMessage(m.tabEvents)}</TabsTrigger>
          {addonTabs.map((entry) => (
            <TabsTrigger key={addonEntryKey(entry)} value={addonEntryKey(entry)}>
              {entry.ui.label}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="overview" className="flex flex-col gap-4">
          <Card aria-labelledby="settings-title">
            <CardHeader>
              <CardTitle id="settings-title">{intl.formatMessage(m.settings)}</CardTitle>
            </CardHeader>
            <CardContent>
              {schemas.isPending ? (
                <Skeleton className="h-24 w-full" />
              ) : (
                <SchemaFields value={data.spec} schema={spec} base="" context={context} />
              )}
            </CardContent>
          </Card>
          <Card aria-labelledby="facts-title">
            <CardHeader>
              <CardTitle id="facts-title">{intl.formatMessage(m.details)}</CardTitle>
            </CardHeader>
            <CardContent>
              <Facts kind={kind} resource={data} />
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="status" className="flex flex-col gap-4">
          <Card aria-labelledby="state-title">
            <CardHeader>
              <CardTitle id="state-title">{intl.formatMessage(m.state)}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <PhaseBadge phase={phase} />
              <p>{status.message || intl.formatMessage(m.phaseHelp, { phase })}</p>
              <p className="text-muted-foreground text-xs">
                {intl.formatMessage(m.observed, {
                  observed: status.observedGeneration ?? 0,
                  generation: data.generation,
                })}
              </p>
            </CardContent>
          </Card>
          <Card aria-labelledby="conditions-title">
            <CardHeader>
              <CardTitle id="conditions-title">{intl.formatMessage(m.conditions)}</CardTitle>
            </CardHeader>
            <CardContent>
              {status.conditions?.length ? (
                <ConditionsTable conditions={status.conditions} />
              ) : (
                <p className="text-muted-foreground">{intl.formatMessage(m.noConditions)}</p>
              )}
            </CardContent>
          </Card>
          {extraStatus.length > 0 ? (
            <Card aria-labelledby="observed-title">
              <CardHeader>
                <CardTitle id="observed-title">{intl.formatMessage(m.observedDetails)}</CardTitle>
              </CardHeader>
              <CardContent>
                <SchemaFields
                  value={data.status}
                  schema={statusSchema}
                  base=""
                  only={extraStatus}
                  context={{ ...context, root: statusSchema ?? {}, hints: {}, labels: "status" }}
                />
              </CardContent>
            </Card>
          ) : null}
        </TabsContent>
        <TabsContent value="history">
          <ResourceHistory kind={kind} resource={data} />
        </TabsContent>
        <TabsContent value="events" className="flex flex-col gap-3">
          <EventTimeline
            filters={{ subject: data.id }}
            emptyTitle={intl.formatMessage(m.noEvents)}
          />
          <Button asChild variant="link" className="self-start">
            <Link to="/events" search={{ subject: data.id }}>
              {intl.formatMessage(m.allEvents)}
            </Link>
          </Button>
        </TabsContent>
        {addonTabs.map((entry) => (
          <TabsContent key={addonEntryKey(entry)} value={addonEntryKey(entry)}>
            <AddonFrame
              addon={entry.addon}
              path={entry.ui.path ?? ""}
              title={entry.ui.label}
              params={{ resource: data.id }}
              className="h-96"
            />
          </TabsContent>
        ))}
      </Tabs>
      {changes.dialog}
    </div>
  );
}
