import { Link } from "@tanstack/react-router";
import { ExternalLink } from "lucide-react";
import type { ReactNode } from "react";
import { useIntl } from "react-intl";
import { Badge } from "@/design/components/ui/display";
import { type Status, StatusDot } from "@/design/components/ui/states";
import { useFormat } from "@/lib/format";
import type { FieldHint } from "@/lib/forms/model";
import { humanize } from "@/lib/forms/model";
import { type JSONSchema, resolveRef, schemaTypes } from "@/lib/schema";
import { resourceMessages as m } from "../messages";
import { fieldLabel, type Kind, kindOfId, type Phase, resourcePath, valueLabel } from "./kinds";

const phaseStatus: Record<Phase, Status> = {
  pending: "pending",
  applying: "pending",
  ready: "ok",
  degraded: "warning",
  failed: "error",
  deleting: "off",
};

/** A resource's phase, as a dot with its name, so the state never rests on color alone. */
export function PhaseBadge({ phase, className }: { phase: Phase; className?: string }) {
  const intl = useIntl();
  return (
    <StatusDot
      status={phaseStatus[phase] ?? "unknown"}
      label={intl.formatMessage(m.phase, { phase })}
      {...(className ? { className } : {})}
    />
  );
}

/** A moment, relative when recent, with the full date on hover. */
export function When({ time }: { time: string | undefined }) {
  const f = useFormat();
  if (!time) {
    return null;
  }
  const date = new Date(time);
  return (
    <time dateTime={time} title={f.dateTime(date)}>
      {f.relative(date)}
    </time>
  );
}

/** How long something took, in its two largest units, marked up as the duration it is. */
export function Duration({ seconds }: { seconds: number }) {
  const f = useFormat();
  return <time dateTime={`PT${Math.max(0, Math.round(seconds))}S`}>{f.duration(seconds)}</time>;
}

/** A link to a resource the panel knows by its ID, or the ID as it is. */
export function ResourceLink({ id, kinds }: { id: string; kinds: readonly Kind[] }) {
  const kind = kindOfId(kinds, id);
  if (!kind || kind.ui.hidden) {
    return <code className="font-mono text-xs">{id}</code>;
  }
  return (
    <Link
      to={resourcePath(kind, id)}
      className="rounded-sm font-mono text-primary text-xs underline-offset-4 hover:underline focus-visible:focus-ring"
    >
      {id}
    </Link>
  );
}

const urlPattern = /^https?:\/\/\S+$/i;

/** A value of a list's column, shown in the column's format. */
export function ColumnValue({
  value,
  format,
  kinds,
}: {
  value: unknown;
  format: string | undefined;
  kinds: readonly Kind[];
}): ReactNode {
  const intl = useIntl();
  const f = useFormat();
  if (value === undefined || value === null || value === "") {
    return <span className="text-muted-foreground">{intl.formatMessage(m.none)}</span>;
  }
  switch (format) {
    case "status":
      return typeof value === "string" ? <PhaseBadge phase={value as Phase} /> : String(value);
    case "date":
      return typeof value === "string" ? <When time={value} /> : String(value);
    case "bytes":
      return typeof value === "number" ? f.bytes(value) : String(value);
    case "duration":
      return typeof value === "number" ? <Duration seconds={value} /> : String(value);
    case "badge": {
      const items = Array.isArray(value) ? value : [value];
      return (
        <span className="flex flex-wrap gap-1">
          {items.map((item) => (
            <Badge key={String(item)} variant="neutral" className="font-mono">
              {String(item)}
            </Badge>
          ))}
        </span>
      );
    }
    case "link":
      if (typeof value === "string" && urlPattern.test(value)) {
        return (
          <a
            href={value}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex min-w-0 items-center gap-1 break-all rounded-sm text-primary underline-offset-4 hover:underline focus-visible:focus-ring"
          >
            {value}
            <ExternalLink className="size-3 shrink-0" aria-hidden="true" />
          </a>
        );
      }
      if (typeof value === "string" && kindOfId(kinds, value)) {
        return <ResourceLink id={value} kinds={kinds} />;
      }
      return String(value);
  }
  return <PlainValue value={value} />;
}

/** A value with no schema to go by. */
function PlainValue({ value }: { value: unknown }): ReactNode {
  const intl = useIntl();
  if (typeof value === "boolean") {
    return intl.formatMessage(value ? m.yes : m.no);
  }
  if (typeof value === "number") {
    return intl.formatNumber(value);
  }
  if (typeof value === "string") {
    return value;
  }
  if (Array.isArray(value)) {
    return value.map((v) => (typeof v === "object" ? JSON.stringify(v) : String(v))).join(", ");
  }
  return (
    <code className="whitespace-pre-wrap break-all font-mono text-xs">
      {JSON.stringify(value, null, 2)}
    </code>
  );
}

export interface ValueContext {
  kind: Kind;
  kinds: readonly Kind[];
  /** The document $refs resolve against. */
  root: JSONSchema;
  hints: Record<string, FieldHint>;
  /** Whose labels the fields take in the kind's catalog: the spec's, or the status's. */
  labels?: "fields" | "status";
}

/**
 * A value as its schema and hints describe it: text, numbers in the
 * person's locale, yes or no, links to resources and addresses, lists,
 * nested settings, and secrets only as whether they're set.
 */
export function SchemaValue({
  value,
  schema,
  path,
  context,
}: {
  value: unknown;
  schema: JSONSchema | undefined;
  path: string;
  context: ValueContext;
}): ReactNode {
  const intl = useIntl();
  const f = useFormat();
  const hint = context.hints[path] ?? {};
  if (hint.sensitive) {
    return (
      <span className="text-muted-foreground italic">
        {intl.formatMessage(value ? m.secretSet : m.secretUnset)}
      </span>
    );
  }
  let s: JSONSchema | undefined;
  try {
    s = schema ? resolveRef(schema, context.root) : undefined;
  } catch {
    s = undefined;
  }
  // Settings that are off are left out of a spec, so a missing switch is off.
  if (value === undefined && s && schemaTypes(s).includes("boolean")) {
    return intl.formatMessage(m.no);
  }
  if (
    value === undefined ||
    value === null ||
    value === "" ||
    (Array.isArray(value) && value.length === 0)
  ) {
    return <span className="text-muted-foreground">{intl.formatMessage(m.none)}</span>;
  }
  if (hint.widget === "resource" && typeof value === "string") {
    return <ResourceLink id={value} kinds={context.kinds} />;
  }
  if (typeof value === "string") {
    if (s?.format === "date-time") {
      return <When time={value} />;
    }
    if ((s?.format === "uri" || s?.format === "url") && urlPattern.test(value)) {
      return <ColumnValue value={value} format="link" kinds={context.kinds} />;
    }
    if (hint.widget === "code" || hint.widget === "textarea") {
      return (
        <pre className="whitespace-pre-wrap break-all rounded-md bg-surface-sunken p-2 font-mono text-xs">
          {value}
        </pre>
      );
    }
    const choice =
      s?.enum && context.labels !== "status"
        ? valueLabel(intl, context.kind.name, path, value)
        : undefined;
    return <span className="break-words">{choice ?? value}</span>;
  }
  if (typeof value === "number") {
    if (hint.widget === "size") {
      return f.bytes(value);
    }
    if (hint.widget === "duration") {
      return <Duration seconds={value} />;
    }
    return intl.formatNumber(value);
  }
  if (typeof value === "boolean") {
    return intl.formatMessage(value ? m.yes : m.no);
  }
  if (Array.isArray(value)) {
    const item = s?.items;
    if (value.every((v) => typeof v !== "object")) {
      return (
        <ul className="flex flex-wrap gap-1">
          {value.map((v, i) => (
            // Values can repeat, so their place keeps them apart.
            // biome-ignore lint/suspicious/noArrayIndexKey: items have no identity of their own
            <li key={i}>
              <Badge variant="neutral" className="font-mono">
                {String(v)}
              </Badge>
            </li>
          ))}
        </ul>
      );
    }
    return (
      <ol className="flex flex-col gap-2">
        {value.map((v, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: items have no identity of their own
          <li key={i} className="rounded-md border border-border p-2">
            <SchemaValue value={v} schema={item} path={`${path}[]`} context={context} />
          </li>
        ))}
      </ol>
    );
  }
  if (typeof value === "object") {
    return <SchemaFields value={value} schema={s} base={path} context={context} />;
  }
  return <PlainValue value={value} />;
}

/** The fields of an object, labelled as its kind's form labels them. */
export function SchemaFields({
  value,
  schema,
  base,
  context,
  only,
}: {
  value: unknown;
  schema: JSONSchema | undefined;
  base: string;
  context: ValueContext;
  /** Shows just these properties, in this order. */
  only?: readonly string[];
}) {
  const intl = useIntl();
  const record =
    value !== null && typeof value === "object" ? (value as Record<string, unknown>) : {};
  const properties = schema?.properties ?? {};
  const keys = only ?? [
    ...Object.keys(properties).sort(
      (a, b) =>
        (context.hints[join(base, a)]?.order ?? 0) - (context.hints[join(base, b)]?.order ?? 0),
    ),
    // Fields the schema doesn't know show too, as the server sent them.
    ...Object.keys(record).filter((k) => !(k in properties)),
  ];
  return (
    <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-[minmax(10rem,auto)_1fr]">
      {keys.map((key) => {
        const path = join(base, key);
        const child = properties[key];
        const label =
          fieldLabel(intl, context.kind.name, path, context.labels) ??
          (child && !schemaTypes(child).includes("object") ? child.title : undefined) ??
          humanize(key);
        return (
          <div key={path} className="contents">
            <dt className="font-medium text-muted-foreground text-sm">{label}</dt>
            <dd className="min-w-0 text-sm">
              <SchemaValue value={record[key]} schema={child} path={path} context={context} />
            </dd>
          </div>
        );
      })}
    </dl>
  );
}

function join(base: string, key: string): string {
  return base === "" ? key : `${base}.${key}`;
}
