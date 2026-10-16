import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Plus, RefreshCw, Trash2 } from "lucide-react";
import { useEffect, useId, useMemo, useState } from "react";
import { useIntl } from "react-intl";
import { usePageTitle } from "@/app/page-title";
import { DiffRow } from "@/design/components/change-preview";
import { SchemaForm } from "@/design/components/schema-form";
import { Button } from "@/design/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/design/components/ui/card";
import {
  Alert,
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/design/components/ui/display";
import { Field } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { Skeleton } from "@/design/components/ui/skeleton";
import { ApiError, type Problem } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import type { ChangeSet } from "@/lib/changes";
import { errorMessage } from "@/lib/errors";
import type { FieldNode } from "@/lib/forms/model";
import type { JSONSchema } from "@/lib/schema";
import { ChangeDialog } from "../changes/dialog";
import { useChangeFlow } from "../changes/flow";
import { historyMessages as hm, formMessages as m, resourceMessages as rm } from "../messages";
import { createResource, listResources, updateResource } from "./api";
import { diffSpecs } from "./diff";
import { newResourceID } from "./ids";
import {
  fieldLabel,
  formHints,
  groupLabel,
  type Kind,
  kindDescription,
  kindNoun,
  kindTitle,
  type Resource,
  resourcePath,
  resourceTitle,
  sensitivePaths,
  useKindSchemas,
  valueLabel,
} from "./kinds";
import { useKindLabels } from "./labels";
import { useRouteKind } from "./layout";
import { resourcesKey, useResource } from "./queries";

type FieldProblem = NonNullable<Problem["errors"]>[number];

const labelKey = /^[a-z0-9]([a-z0-9._/-]{0,61}[a-z0-9])?$/;
const typeID = /^[a-z][a-z0-9_]{0,62}_[0-9a-z]{26}$/;

/** A label being edited: its key and value, and a key of its own for React. */
interface LabelRow {
  key: string;
  value: string;
  row: number;
}

let nextRow = 0;
const rowsOf = (labels: Record<string, string> | undefined): LabelRow[] =>
  Object.entries(labels ?? {}).map(([key, value]) => ({ key, value, row: nextRow++ }));

/** The labels of a resource, as rows of keys and values. */
function LabelsEditor({
  rows,
  onChange,
  problem,
}: {
  rows: LabelRow[];
  onChange: (rows: LabelRow[]) => void;
  problem: string | undefined;
}) {
  const intl = useIntl();
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1 font-medium text-sm">{intl.formatMessage(m.labels)}</legend>
      <p className="text-muted-foreground text-xs">{intl.formatMessage(m.labelsHelp)}</p>
      {rows.map((r, i) => {
        const bad = r.key !== "" && !labelKey.test(r.key);
        return (
          <div key={r.row} className="flex flex-wrap items-start gap-2">
            <Field
              label={intl.formatMessage(m.labelKey, { n: i + 1 })}
              error={bad ? intl.formatMessage(m.labelKeyBad) : undefined}
              className="min-w-40 flex-1"
            >
              <Input
                value={r.key}
                spellCheck={false}
                onChange={(e) =>
                  onChange(rows.map((x) => (x.row === r.row ? { ...x, key: e.target.value } : x)))
                }
              />
            </Field>
            <Field
              label={intl.formatMessage(m.labelValue, { n: i + 1 })}
              className="min-w-40 flex-1"
            >
              <Input
                value={r.value}
                maxLength={255}
                onChange={(e) =>
                  onChange(rows.map((x) => (x.row === r.row ? { ...x, value: e.target.value } : x)))
                }
              />
            </Field>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="mt-6"
              aria-label={intl.formatMessage(m.labelRemove, { n: i + 1 })}
              onClick={() => onChange(rows.filter((x) => x.row !== r.row))}
            >
              <Trash2 aria-hidden="true" />
            </Button>
          </div>
        );
      })}
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="self-start"
        disabled={rows.length >= 64}
        onClick={() => onChange([...rows, { key: "", value: "", row: nextRow++ }])}
      >
        <Plus aria-hidden="true" />
        {intl.formatMessage(m.labelAdd)}
      </Button>
      {problem ? (
        <p className="text-danger text-xs" role="alert">
          {problem}
        </p>
      ) : null}
    </fieldset>
  );
}

/**
 * The kind whose resources own resources of a scope: the core's account
 * and reseller kinds, or by the panel's convention a module's kind of that
 * name that isn't itself an account's.
 */
function ownerKind(kinds: readonly Kind[], level: string): Kind | undefined {
  return (
    kinds.find((k) => k.name === `core.${level}`) ??
    kinds.find((k) => k.name.endsWith(`.${level}`) && k.scope !== "account")
  );
}

/** Whether a kind's resources own themselves, as an account belongs to itself. */
function ownsItself(kind: Kind, kinds: readonly Kind[]): boolean {
  return kind.scope !== "server" && ownerKind(kinds, kind.scope)?.name === kind.name;
}

/** Who a new resource belongs to: an account or a reseller, chosen from those the person can see. */
function OwnerField({
  kind,
  kinds,
  value,
  onChange,
  error,
}: {
  kind: Kind;
  kinds: readonly Kind[];
  value: string;
  onChange: (id: string) => void;
  error: string | undefined;
}) {
  const intl = useIntl();
  const api = useApi();
  const owners = ownerKind(kinds, kind.scope);
  const list = useQuery({
    queryKey: ["resources", owners?.name ?? "", "owners"],
    queryFn: () => listResources(api, owners as Kind, { limit: 1000 }),
    enabled: owners !== undefined,
  });
  const label = intl.formatMessage(m.owner, { level: kind.scope });
  const help = intl.formatMessage(m.ownerHelp, { level: kind.scope });
  if (owners && list?.data && list.data.items.length > 0) {
    return (
      <Field
        label={label}
        description={help}
        error={error}
        required
        requiredLabel={intl.formatMessage(m.required)}
      >
        {(control) => (
          <Select value={value} onValueChange={onChange}>
            <SelectTrigger {...control} className="max-w-md">
              <SelectValue placeholder={intl.formatMessage(m.choose)} />
            </SelectTrigger>
            <SelectContent>
              {list.data.items.map((r) => (
                <SelectItem key={r.id} value={r.id}>
                  {resourceTitle(owners, r)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
    );
  }
  return (
    <Field
      label={label}
      description={help}
      error={error}
      required
      requiredLabel={intl.formatMessage(m.required)}
    >
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value.trim())}
        spellCheck={false}
        translate="no"
        className="max-w-md font-mono"
        {...(owners ? { placeholder: `${owners.prefix}_…` } : {})}
      />
    </Field>
  );
}

/** Puts the server's problems with a change where the form shows them. */
function placeProblems(error: unknown): {
  fields: FieldProblem[];
  owner?: string;
  labels?: string;
  other: boolean;
} {
  if (!(error instanceof ApiError) || !error.problem.errors?.length) {
    return { fields: [], other: true };
  }
  const fields: FieldProblem[] = [];
  let owner: string | undefined;
  let labels: string | undefined;
  let other = false;
  for (const p of error.problem.errors) {
    const location = (p.location ?? "").replace(/^body\./, "");
    if (location.startsWith("spec.")) {
      fields.push({ ...p, location: location.slice("spec.".length) });
    } else if (location.startsWith("secrets.")) {
      fields.push({ ...p, location: location.slice("secrets.".length) });
    } else if (location.startsWith("scope")) {
      owner = p.message;
    } else if (location.startsWith("labels")) {
      labels = p.message;
    } else {
      other = true;
    }
  }
  return { fields, ...(owner ? { owner } : {}), ...(labels ? { labels } : {}), other };
}

/**
 * Choices for fields that name other resources of a kind their hints name,
 * loaded before the form shows. A field whose kind isn't known, or whose
 * resources the person can't list, takes the ID as text instead.
 */
function useResourceChoices(kind: Kind, kinds: readonly Kind[]) {
  const api = useApi();
  const targets = useMemo(
    () =>
      [
        ...new Set(
          Object.values(kind.ui.fields ?? {})
            .map((h) => (h as { kind?: string }).kind)
            .filter((k): k is string => typeof k === "string"),
        ),
      ]
        .map((name) => kinds.find((k) => k.name === name))
        .filter((k): k is Kind => k !== undefined),
    [kind, kinds],
  );
  const lists = useQueries({
    queries: targets.map((target) => ({
      queryKey: ["resources", target.name, "choices"],
      queryFn: () => listResources(api, target, { limit: 1000 }),
    })),
  });
  const pending = lists.some((l) => l.isPending);
  const choices = (field: FieldNode) => {
    const i = targets.findIndex((t) => t.name === field.hint.kind);
    const target = targets[i];
    const items = lists[i]?.data?.items;
    return target && items
      ? items.map((r) => ({ value: r.id, label: resourceTitle(target, r) }))
      : undefined;
  };
  return { pending, choices };
}

/** Whether the panel refused a change because the resource changed since it was read. */
function isConflict(error: unknown): boolean {
  return error instanceof ApiError && error.code === "core.conflict";
}

/**
 * What someone else changed in a resource since the form opened it, or that
 * they deleted it, with a way to start again from the latest settings.
 */
function ChangedSince({
  kind,
  opened,
  latest,
  onReload,
}: {
  kind: Kind;
  opened: Resource;
  latest: Resource;
  onReload: () => void;
}) {
  const intl = useIntl();
  const listId = useId();
  if (latest.deletedAt) {
    return (
      <Alert
        tone="danger"
        title={intl.formatMessage(m.deletedTitle, { kind: kindNoun(intl, kind) })}
      >
        {intl.formatMessage(m.deletedBody)}
      </Alert>
    );
  }
  const diffs = diffSpecs(opened.spec, latest.spec, sensitivePaths(kind));
  const labelsChanged = JSON.stringify(opened.labels ?? {}) !== JSON.stringify(latest.labels ?? {});
  return (
    <Alert
      tone="warning"
      title={intl.formatMessage(m.changedTitle, { kind: kindNoun(intl, kind) })}
      actions={
        <Button variant="outline" size="sm" onClick={onReload}>
          <RefreshCw aria-hidden="true" />
          {intl.formatMessage(m.reload)}
        </Button>
      }
    >
      <p>{intl.formatMessage(m.changedBody)}</p>
      {diffs.length > 0 ? (
        <>
          <p id={listId} className="mt-2 font-medium text-sm">
            {intl.formatMessage(m.changedFields)}
          </p>
          <ul aria-labelledby={listId} className="divide-y divide-border">
            {diffs.map((d) => (
              <DiffRow key={`${d.op}:${d.path}`} diff={d} />
            ))}
          </ul>
        </>
      ) : null}
      {labelsChanged ? <p className="mt-2 text-sm">{intl.formatMessage(hm.labels)}</p> : null}
    </Alert>
  );
}

/**
 * The form a kind's schema and hints make, for a new resource or new
 * settings of one, whose change goes through the preview before it's
 * applied. Problems the server finds show under their fields.
 *
 * New settings are made against the resource as the form opened it, its
 * settings, labels and revision, so the panel refuses them when someone
 * else changed the resource meanwhile rather than quietly undo what they
 * did; the form then says what they changed and offers to start again
 * from the latest settings. A kind whose resources own themselves, as an
 * account does, gets its new resource's ID chosen here, since it names
 * itself as its owner.
 */
function ResourceForm({
  kind,
  kinds,
  resource: latest,
  onReload,
}: {
  kind: Kind;
  kinds: readonly Kind[];
  /** The resource whose settings change, as the panel says it is now. */
  resource?: Resource;
  /** Opens the form again on the latest settings. */
  onReload?: () => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const labels = useKindLabels();
  const schemas = useKindSchemas(kind.name);
  const choices = useResourceChoices(kind, kinds);
  const hints = useMemo(() => formHints(kind), [kind]);
  const [resource] = useState(latest);
  const self = !resource && ownsItself(kind, kinds);
  const [newID] = useState(() => (self ? newResourceID(kind.prefix) : ""));
  const [owner, setOwner] = useState("");
  const [rows, setRows] = useState<LabelRow[]>(() => rowsOf(resource?.labels));
  const [labelsOpen, setLabelsOpen] = useState(() => rows.length > 0);
  const [problems, setProblems] = useState<ReturnType<typeof placeProblems> | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [refused, setRefused] = useState(false);
  const [applied, setApplied] = useState(false);
  const [open, setOpen] = useState(false);
  const errorId = useId();
  const flow = useChangeFlow({
    onApplied: (cs: ChangeSet) => {
      setApplied(true);
      const id = resource?.id ?? cs.changes?.[0]?.id;
      if (id) {
        void navigate({ to: resourcePath(kind, id) });
      }
    },
  });
  // The panel refused the change for being made against what someone else
  // changed since, here or when it was applied.
  const conflict = refused || (flow.state.step === "error" && isConflict(flow.state.error));
  // The panel may know of the change before the stream says so.
  useEffect(() => {
    if (conflict) {
      void queryClient.invalidateQueries({ queryKey: resourcesKey(kind.name) });
    }
  }, [conflict, queryClient, kind.name]);
  const schema = schemas.data?.spec as JSONSchema | undefined;
  if (schemas.isPending || choices.pending || !schema) {
    return <Skeleton className="h-64 w-full" />;
  }
  // Someone else changed it, as the live resource or a refusal says. The
  // person's own change moves it on too, until the form has gone.
  const changedSince =
    resource !== undefined &&
    latest !== undefined &&
    !applied &&
    (conflict || latest.revision !== resource.revision || Boolean(latest.deletedAt));
  const secretFields = sensitivePaths(kind);
  const ownerMissing = !resource && !self && kind.scope !== "server" && !typeID.test(owner);
  const submit = async ({ spec, secrets }: { spec: unknown; secrets: Record<string, string> }) => {
    setError(null);
    if (ownerMissing) {
      setProblems({ fields: [], owner: intl.formatMessage(m.ownerMissing), other: false });
      return;
    }
    if (rows.some((r) => !labelKey.test(r.key))) {
      setProblems({ fields: [], labels: intl.formatMessage(m.labelKeyBad), other: false });
      return;
    }
    setProblems(null);
    const labelMap = Object.fromEntries(rows.map((r) => [r.key, r.value]));
    const kept = Object.fromEntries(
      Object.entries(secrets).filter(([path]) => secretFields.includes(path)),
    );
    const carriesSecrets = Object.keys(kept).length > 0;
    const scope =
      kind.scope === "server"
        ? {}
        : {
            scope: { level: kind.scope, id: self ? newID : owner },
            ...(self ? { id: newID } : {}),
          };
    setOpen(true);
    try {
      await flow.preview({
        carriesSecrets,
        send: (options) =>
          resource
            ? updateResource(
                api,
                kind,
                resource.id,
                {
                  spec,
                  labels: labelMap,
                  revision: resource.revision,
                  ...(carriesSecrets ? { secrets: kept } : {}),
                },
                options,
              )
            : createResource(
                api,
                kind,
                {
                  spec,
                  ...scope,
                  ...(rows.length > 0 ? { labels: labelMap } : {}),
                  ...(carriesSecrets ? { secrets: kept } : {}),
                },
                options,
              ),
      });
    } catch (e) {
      // The form shows what kept the change from planning.
      setOpen(false);
      flow.reset();
      if (isConflict(e)) {
        setRefused(true);
        return;
      }
      const placed = placeProblems(e);
      setProblems(placed);
      if (placed.other) {
        setError(e);
      }
    }
  };
  return (
    <div className="flex flex-col gap-6">
      {changedSince && resource && latest ? (
        <ChangedSince kind={kind} opened={resource} latest={latest} onReload={() => onReload?.()} />
      ) : null}
      {error ? (
        <Alert tone="danger" title={intl.formatMessage(m.problem)} id={errorId}>
          {errorMessage(intl, error)}
        </Alert>
      ) : null}
      {!resource && kind.scope !== "server" && !self ? (
        <OwnerField
          kind={kind}
          kinds={kinds}
          value={owner}
          onChange={setOwner}
          error={problems?.owner}
        />
      ) : null}
      <SchemaForm
        schema={schema}
        root={schema}
        hints={hints}
        {...(resource ? { initialValue: resource.spec } : {})}
        mode={resource ? "update" : "create"}
        problems={problems?.fields ?? []}
        label={(field) => fieldLabel(intl, kind.name, field.path)}
        groupLabel={(name) => groupLabel(intl, kind.name, name)}
        optionLabel={(field, value) => valueLabel(intl, kind.name, field.path, value)}
        resourceOptions={choices.choices}
        submitLabel={intl.formatMessage(m.review)}
        busy={flow.state.step === "previewing"}
        onSubmit={(result) => void submit(result)}
        onCancel={() =>
          void navigate({
            to: resource ? resourcePath(kind, resource.id) : `/${kind.module}/${kind.plural}`,
          })
        }
        extra={
          <Collapsible
            open={labelsOpen || problems?.labels !== undefined}
            onOpenChange={setLabelsOpen}
          >
            <CollapsibleTrigger>{intl.formatMessage(m.labels)}</CollapsibleTrigger>
            <CollapsibleContent className="mt-3">
              <LabelsEditor rows={rows} onChange={setRows} problem={problems?.labels} />
            </CollapsibleContent>
          </Collapsible>
        }
      />
      <ChangeDialog
        open={open}
        onOpenChange={setOpen}
        title={intl.formatMessage(resource ? m.reviewUpdate : m.reviewCreate, {
          kind: kindNoun(intl, kind),
          name: resource ? resourceTitle(kind, resource) : "",
        })}
        flow={flow}
        kindLabel={labels.kind}
        resourceName={labels.resourceName}
      />
    </div>
  );
}

/** /<module>/<plural>/new: a new resource of the kind. */
export default function CreateResource() {
  const intl = useIntl();
  const { kind, kinds } = useRouteKind();
  if (!kind) {
    return null;
  }
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">
          {intl.formatMessage(rm.create, { kind: kindNoun(intl, kind) })}
        </h1>
        <p className="text-muted-foreground">{kindDescription(intl, kind)}</p>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>{intl.formatMessage(m.settings)}</CardTitle>
          <CardDescription>{intl.formatMessage(m.settingsHelp)}</CardDescription>
        </CardHeader>
        <CardContent>
          <ResourceForm kind={kind} kinds={kinds} />
        </CardContent>
      </Card>
    </div>
  );
}

/** /<module>/<plural>/<id>/edit: new settings for a resource. */
export function EditResource() {
  const intl = useIntl();
  const { kind, kinds, id } = useRouteKind();
  const resource = useResource(kind, id);
  // Bumped to open the form again on the latest settings.
  const [opened, setOpened] = useState(0);
  const name = kind && resource.data ? resourceTitle(kind, resource.data) : undefined;
  usePageTitle(
    name ? intl.formatMessage(m.editCrumb) : undefined,
    name ? intl.formatMessage(m.editTitle, { name }) : undefined,
  );
  if (!kind || !resource.data) {
    return null;
  }
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <p className="text-muted-foreground text-sm">{kindTitle(intl, kind)}</p>
        <h1 className="break-words font-semibold text-2xl">
          {intl.formatMessage(m.editTitle, { name: name ?? "" })}
        </h1>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>{intl.formatMessage(m.settings)}</CardTitle>
          <CardDescription>{intl.formatMessage(m.settingsHelpUpdate)}</CardDescription>
        </CardHeader>
        <CardContent>
          <ResourceForm
            key={opened}
            kind={kind}
            kinds={kinds}
            resource={resource.data}
            onReload={() => {
              // The panel may know of a change the stream hasn't told yet.
              void resource.refetch().then(() => setOpened((n) => n + 1));
            }}
          />
        </CardContent>
      </Card>
    </div>
  );
}
