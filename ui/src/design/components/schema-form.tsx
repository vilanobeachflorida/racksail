import { Eye, EyeOff, Plus, Trash2 } from "lucide-react";
import { type FormEvent, type ReactNode, useId, useMemo, useState } from "react";
import { type IntlShape, useIntl } from "react-intl";
import type { Problem } from "@/lib/api/client";
import { errorMessageId } from "@/lib/errors";
import {
  buildForm,
  defaultValue,
  type FieldNode,
  getAt,
  humanize,
  isFilled,
  setAt,
  type UIHints,
} from "@/lib/forms/model";
import { type JSONSchema, type SchemaIssue, validate } from "@/lib/schema";
import { cn } from "@/lib/utils";
import { designMessages as m } from "../messages";
import { CodeEditor } from "./code-editor";
import { Button } from "./ui/button";
import { Switch } from "./ui/choice";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "./ui/display";
import { Field, type useFieldControl } from "./ui/field";

type FieldControl = ReturnType<typeof useFieldControl>;

import { Input, Textarea } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

type FieldProblem = NonNullable<Problem["errors"]>[number];

/** A form's value: the spec to send and, apart, the secrets typed into sensitive fields. */
export interface FormResult {
  spec: unknown;
  /** Secret values by field path; they go in the change's secrets, never in the spec. */
  secrets: Record<string, string>;
}

export interface SchemaFormProps {
  /** The JSON Schema of the value, usually a kind's spec. */
  schema: JSONSchema;
  /** The document $refs resolve against, such as the OpenAPI document's components. */
  root?: JSONSchema;
  hints?: UIHints;
  /** The value to start from; defaults from the schema otherwise. */
  initialValue?: unknown;
  /** Update forms keep create-only fields read-only. */
  mode?: "create" | "update";
  /** Problems the server reported for this form's fields. */
  problems?: readonly FieldProblem[];
  /** Where the form's value sits in the server's field locations, such as "body.spec". */
  problemPrefix?: string;
  /** A translated label for a field, when there is one; the schema's title otherwise. */
  label?: (field: FieldNode) => string | undefined;
  /** A translated name for a group of fields. */
  groupLabel?: (name: string) => string;
  /** Choices for fields that refer to other resources. */
  resourceOptions?: (field: FieldNode) => readonly { value: string; label: string }[];
  submitLabel: ReactNode;
  busy?: boolean;
  onSubmit: (result: FormResult) => void;
  onCancel?: () => void;
  /** Called with every change, for previews that update as people type. */
  onChange?: (value: unknown) => void;
  className?: string;
}

/** Explains a schema problem in the user's language. */
export function issueMessage(intl: IntlShape, issue: SchemaIssue): string {
  switch (issue.keyword) {
    case "required":
      return intl.formatMessage(m.formRequired);
    case "type":
      return intl.formatMessage(m.formType, { expected: String(issue.params.expected) });
    case "minLength":
      return intl.formatMessage(m.formMinLength, { min: issue.params.min });
    case "maxLength":
      return intl.formatMessage(m.formMaxLength, { max: issue.params.max });
    case "pattern":
      return intl.formatMessage(m.formPattern);
    case "format":
      // Message select keys can't contain hyphens, so date-time is dateTime.
      return intl.formatMessage(m.formFormat, {
        format: String(issue.params.format).replace(/-(\w)/g, (_, c: string) => c.toUpperCase()),
      });
    case "minimum":
      return intl.formatMessage(m.formMinimum, { min: issue.params.min });
    case "maximum":
      return intl.formatMessage(m.formMaximum, { max: issue.params.max });
    case "multipleOf":
      return intl.formatMessage(m.formMultipleOf, { step: issue.params.step });
    case "enum":
    case "const":
      return intl.formatMessage(m.formEnum);
    case "minItems":
      return intl.formatMessage(m.formMinItems, { min: issue.params.min });
    case "maxItems":
      return intl.formatMessage(m.formMaxItems, { max: issue.params.max });
    case "uniqueItems":
      return intl.formatMessage(m.formUnique);
    case "additionalProperties":
      return intl.formatMessage(m.formUnknownField);
    default:
      return intl.formatMessage(m.formInvalid);
  }
}

function problemText(intl: IntlShape, p: FieldProblem): string {
  const id = errorMessageId(p.code);
  if (Object.hasOwn(intl.messages, id)) {
    const values: Record<string, string> = {};
    for (const [k, v] of Object.entries(p.params ?? {})) {
      values[k] = typeof v === "string" ? v : JSON.stringify(v);
    }
    return intl.formatMessage({ id }, values);
  }
  return p.message;
}

// Units are Intl unit names, so their abbreviations come out in the user's
// language. Sizes use decimal units, as byte counts do elsewhere.
const sizeUnits = [
  ["byte", 1],
  ["kilobyte", 1e3],
  ["megabyte", 1e6],
  ["gigabyte", 1e9],
  ["terabyte", 1e12],
] as const;

const durationUnits = [
  ["second", 1],
  ["minute", 60],
  ["hour", 3600],
  ["day", 86400],
] as const;

function unitName(intl: IntlShape, unit: string): string {
  const parts = intl.formatNumberToParts(1, { style: "unit", unit, unitDisplay: "short" });
  return parts.find((p) => p.type === "unit")?.value ?? unit;
}

function pickUnit(value: number, units: readonly (readonly [string, number])[]): number {
  let best = 0;
  units.forEach(([, size], i) => {
    if (value !== 0 && value % size === 0) {
      best = i;
    }
  });
  return best;
}

interface Context {
  value: unknown;
  set: (path: string, next: unknown) => void;
  errors: Record<string, string>;
  touch: (path: string) => void;
  secrets: Record<string, string>;
  setSecret: (path: string, next: string) => void;
  mode: "create" | "update";
  props: SchemaFormProps;
}

function labelFor(field: FieldNode, props: SchemaFormProps): string {
  return props.label?.(field) ?? field.schema.title ?? humanize(field.key);
}

function UnitInput({
  field,
  label,
  value,
  units,
  onChange,
  onBlur,
  control,
}: {
  field: FieldNode;
  label: string;
  value: number | undefined;
  units: readonly (readonly [string, number])[];
  onChange: (next: number | undefined) => void;
  onBlur: () => void;
  control: FieldControl;
}) {
  const intl = useIntl();
  const [unit, setUnit] = useState(() => pickUnit(value ?? 0, units));
  const size = units[unit]?.[1] ?? 1;
  return (
    <div className="flex gap-2">
      <Input
        {...control}
        type="number"
        inputMode="decimal"
        min={0}
        value={value === undefined ? "" : String(value / size)}
        onChange={(e) => {
          const n = e.target.valueAsNumber;
          onChange(Number.isNaN(n) ? undefined : Math.round(n * size));
        }}
        onBlur={onBlur}
        placeholder={field.hint.placeholder}
      />
      <Select
        value={String(unit)}
        onValueChange={(v) => {
          const next = Number(v);
          // The unit applies to the number typed: 512 and then MB is 512 MB.
          if (value !== undefined) {
            onChange(Math.round((value / size) * (units[next]?.[1] ?? 1)));
          }
          setUnit(next);
        }}
      >
        <SelectTrigger
          className="w-24"
          id={`${field.path}-unit`}
          aria-describedby={undefined}
          aria-invalid={false}
          aria-label={intl.formatMessage(m.formUnit, { field: label })}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {units.map(([name], i) => (
            <SelectItem key={name} value={String(i)}>
              {unitName(intl, name)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function SecretInput({
  control,
  value,
  onChange,
  placeholder,
}: {
  control: FieldControl;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
}) {
  const intl = useIntl();
  const [shown, setShown] = useState(false);
  return (
    <div className="relative">
      <Input
        {...control}
        type={shown ? "text" : "password"}
        autoComplete="new-password"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="pe-10"
      />
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        className="absolute end-1 top-1/2 -translate-y-1/2"
        aria-label={intl.formatMessage(shown ? m.formHideSecret : m.formShowSecret)}
        onClick={() => setShown((s) => !s)}
      >
        {shown ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
      </Button>
    </div>
  );
}

function FieldView({ field, ctx }: { field: FieldNode; ctx: Context }) {
  const intl = useIntl();
  const value = getAt(ctx.value, field.path);
  const label = labelFor(field, ctx.props);
  const error = ctx.errors[field.path];
  const description = field.schema.description;
  const locked = ctx.mode === "update" && field.hint.createOnly;
  const required = field.required;
  const requiredLabel = intl.formatMessage(m.formRequiredMark);
  const set = (next: unknown) => ctx.set(field.path, next);
  const blur = () => ctx.touch(field.path);
  const fieldProps = {
    label,
    description: locked ? intl.formatMessage(m.formCreateOnly) : description,
    error,
    required,
    requiredLabel,
  };

  if (field.kind === "object" && field.children) {
    return (
      <fieldset className="flex flex-col gap-4 rounded-md border border-border p-4">
        <legend className="px-1 font-medium text-sm">{label}</legend>
        {description ? <p className="-mt-2 text-muted-foreground text-xs">{description}</p> : null}
        {field.children.map((child) => (
          <FieldView key={child.path} field={child} ctx={ctx} />
        ))}
      </fieldset>
    );
  }

  if (field.kind === "array" && field.item) {
    const items = Array.isArray(value) ? value : [];
    const item = field.item;
    return (
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1 font-medium text-sm">
          {label}
          {required ? (
            <span className="ms-2 font-normal text-muted-foreground text-xs">{requiredLabel}</span>
          ) : null}
        </legend>
        {description ? <p className="text-muted-foreground text-xs">{description}</p> : null}
        {items.map((_, i) => {
          const path = `${field.path}[${i}]`;
          const child: FieldNode = {
            ...item,
            path,
            key: `${field.key} ${i + 1}`,
            required: false,
            children: item.children?.map((c) => rebase(c, item.path, path)),
          };
          return (
            <div key={path} className="flex items-start gap-2">
              <div className="min-w-0 flex-1">
                <FieldView field={child} ctx={ctx} />
              </div>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className={item.kind === "object" ? "mt-0" : "mt-6"}
                aria-label={intl.formatMessage(m.formRemove, { item: `${label} ${i + 1}` })}
                onClick={() => set(items.filter((__, j) => j !== i))}
                disabled={locked}
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
          onClick={() =>
            set([
              ...items,
              item.kind === "object"
                ? (defaultValue(item.schema, ctx.props.root ?? ctx.props.schema) ?? {})
                : (item.schema.default ?? ""),
            ])
          }
          disabled={
            locked || (field.schema.maxItems !== undefined && items.length >= field.schema.maxItems)
          }
        >
          <Plus aria-hidden="true" />
          {intl.formatMessage(m.formAdd, { item: label })}
        </Button>
        {error ? (
          <p className="text-danger text-xs" role="alert">
            {error}
          </p>
        ) : null}
      </fieldset>
    );
  }

  if (field.kind === "boolean" || field.hint.widget === "toggle") {
    return (
      <div className="flex items-start gap-3">
        <Switch
          id={`f-${field.path}`}
          checked={value === true}
          onCheckedChange={(on) => set(on)}
          disabled={locked}
          aria-describedby={description ? `f-${field.path}-d` : undefined}
        />
        <div className="flex flex-col gap-0.5">
          <label htmlFor={`f-${field.path}`} className="font-medium text-sm leading-5">
            {label}
          </label>
          {description ? (
            <p id={`f-${field.path}-d`} className="text-muted-foreground text-xs">
              {description}
            </p>
          ) : null}
          {error ? <p className="text-danger text-xs">{error}</p> : null}
        </div>
      </div>
    );
  }

  return (
    <Field {...fieldProps}>
      {(control) => {
        if (field.hint.sensitive || field.hint.widget === "password") {
          return (
            <SecretInput
              control={control}
              value={ctx.secrets[field.path] ?? ""}
              onChange={(v) => ctx.setSecret(field.path, v)}
              placeholder={
                ctx.mode === "update"
                  ? intl.formatMessage(m.formSecretUnchanged)
                  : field.hint.placeholder
              }
            />
          );
        }
        if (
          field.kind === "enum" ||
          field.hint.widget === "select" ||
          field.hint.widget === "resource"
        ) {
          const options =
            field.hint.widget === "resource"
              ? (ctx.props.resourceOptions?.(field) ?? [])
              : (field.schema.enum ?? []).map((o) => ({ value: String(o), label: String(o) }));
          return (
            <Select
              value={value === undefined || value === null ? "" : String(value)}
              onValueChange={(v) => {
                const typed = field.schema.enum?.find((o) => String(o) === v);
                set(typed ?? v);
                blur();
              }}
              disabled={locked}
            >
              <SelectTrigger {...control}>
                <SelectValue placeholder={intl.formatMessage(m.formChoose)} />
              </SelectTrigger>
              <SelectContent>
                {options.map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          );
        }
        if (field.hint.widget === "size" || field.hint.widget === "duration") {
          return (
            <UnitInput
              field={field}
              label={label}
              control={control}
              value={typeof value === "number" ? value : undefined}
              units={field.hint.widget === "size" ? sizeUnits : durationUnits}
              onChange={set}
              onBlur={blur}
            />
          );
        }
        if (field.kind === "number" || field.kind === "integer") {
          return (
            <Input
              {...control}
              type="number"
              inputMode={field.kind === "integer" ? "numeric" : "decimal"}
              min={field.schema.minimum}
              max={field.schema.maximum}
              step={field.kind === "integer" ? 1 : "any"}
              value={typeof value === "number" ? String(value) : ""}
              onChange={(e) => {
                const n = e.target.valueAsNumber;
                set(Number.isNaN(n) ? undefined : n);
              }}
              onBlur={blur}
              disabled={locked}
              placeholder={field.hint.placeholder}
            />
          );
        }
        if (field.kind === "json" || field.hint.widget === "code") {
          return (
            <JsonField
              label={label}
              value={value}
              asText={field.kind === "string"}
              onChange={set}
              readOnly={locked}
            />
          );
        }
        if (field.hint.widget === "textarea") {
          return (
            <Textarea
              {...control}
              value={typeof value === "string" ? value : ""}
              onChange={(e) => set(e.target.value === "" ? undefined : e.target.value)}
              onBlur={blur}
              disabled={locked}
              placeholder={field.hint.placeholder}
            />
          );
        }
        const format = field.schema.format;
        return (
          <Input
            {...control}
            type={format === "email" ? "email" : format === "uri" ? "url" : "text"}
            value={typeof value === "string" ? value : ""}
            onChange={(e) => set(e.target.value === "" ? undefined : e.target.value)}
            onBlur={blur}
            disabled={locked}
            maxLength={field.schema.maxLength}
            placeholder={
              field.hint.placeholder ?? (field.schema.examples?.[0] as string | undefined)
            }
            spellCheck={format ? false : undefined}
          />
        );
      }}
    </Field>
  );
}

function rebase(node: FieldNode, from: string, to: string): FieldNode {
  return {
    ...node,
    path: to + node.path.slice(from.length),
    children: node.children?.map((c) => rebase(c, from, to)),
  };
}

function JsonField({
  label,
  value,
  asText,
  onChange,
  readOnly,
}: {
  label: string;
  value: unknown;
  asText: boolean;
  onChange: (v: unknown) => void;
  readOnly?: boolean;
}) {
  const intl = useIntl();
  const [text, setText] = useState(() =>
    asText
      ? typeof value === "string"
        ? value
        : ""
      : value === undefined
        ? ""
        : JSON.stringify(value, null, 2),
  );
  const [bad, setBad] = useState(false);
  return (
    <div className="flex flex-col gap-1">
      <CodeEditor
        label={label}
        value={text}
        language={asText ? null : "json"}
        height="10rem"
        readOnly={readOnly}
        onChange={(next) => {
          setText(next);
          if (asText) {
            onChange(next === "" ? undefined : next);
            return;
          }
          try {
            onChange(next.trim() === "" ? undefined : JSON.parse(next));
            setBad(false);
          } catch {
            setBad(true);
          }
        }}
      />
      {bad ? <p className="text-danger text-xs">{intl.formatMessage(m.formBadJSON)}</p> : null}
    </div>
  );
}

/**
 * A form generated from a JSON Schema and the kind's UI hints: fields in
 * their groups, defaults filled in, help from the schema's descriptions,
 * problems shown as people leave each field, and rarely needed settings in
 * an Advanced section. Every kind gets a usable screen from it before it
 * has one of its own.
 */
export function SchemaForm(props: SchemaFormProps) {
  const intl = useIntl();
  const { schema, hints, mode = "create" } = props;
  const root = props.root ?? schema;
  const formId = useId();
  const groups = useMemo(() => buildForm(schema, hints, root), [schema, hints, root]);
  const [value, setValue] = useState<unknown>(
    () => props.initialValue ?? defaultValue(schema, root) ?? {},
  );
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [touched, setTouched] = useState<ReadonlySet<string>>(new Set());
  const [submitted, setSubmitted] = useState(false);

  const sensitive = useMemo(() => {
    const paths: FieldNode[] = [];
    const walk = (f: FieldNode) => {
      if (f.hint.sensitive) {
        paths.push(f);
      }
      f.children?.forEach(walk);
    };
    for (const g of groups) {
      g.fields.forEach(walk);
    }
    return paths;
  }, [groups]);

  const errors = useMemo(() => {
    const out: Record<string, string> = {};
    const visible = (path: string) =>
      submitted ||
      touched.has(path) ||
      [...touched].some((t) => path.startsWith(`${t}.`) || path.startsWith(`${t}[`));
    for (const issue of validate(schema, value, root)) {
      const isSecret = sensitive.some((f) => f.path === issue.path);
      if (isSecret || out[issue.path] || !visible(issue.path)) {
        continue;
      }
      out[issue.path] = issueMessage(intl, issue);
    }
    if (mode === "create") {
      for (const f of sensitive) {
        if (f.required && !isFilled(secrets[f.path]) && visible(f.path)) {
          out[f.path] = intl.formatMessage(m.formRequired);
        }
      }
    }
    const prefix = props.problemPrefix ? `${props.problemPrefix}.` : "";
    for (const p of props.problems ?? []) {
      const loc = p.location ?? "";
      const path = prefix && loc.startsWith(prefix) ? loc.slice(prefix.length) : loc;
      if (path && !out[path]) {
        out[path] = problemText(intl, p);
      }
    }
    return out;
  }, [
    schema,
    root,
    value,
    touched,
    submitted,
    sensitive,
    secrets,
    mode,
    intl,
    props.problems,
    props.problemPrefix,
  ]);

  const ctx: Context = {
    value,
    set: (path, next) => {
      setValue((v: unknown) => {
        const updated = setAt(v, path, next);
        props.onChange?.(updated);
        return updated;
      });
    },
    errors,
    touch: (path) => setTouched((t) => new Set(t).add(path)),
    secrets,
    setSecret: (path, next) => setSecrets((s) => ({ ...s, [path]: next })),
    mode,
    props,
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setSubmitted(true);
    const issues = validate(schema, value, root).filter(
      (i) => !sensitive.some((f) => f.path === i.path),
    );
    const missingSecret =
      mode === "create" && sensitive.some((f) => f.required && !isFilled(secrets[f.path]));
    if (issues.length > 0 || missingSecret) {
      // Take people to the first problem.
      requestAnimationFrame(() => {
        const first = document.querySelector<HTMLElement>(
          `#${CSS.escape(formId)} [aria-invalid="true"]`,
        );
        first?.focus();
      });
      return;
    }
    const filled: Record<string, string> = {};
    for (const [k, v] of Object.entries(secrets)) {
      if (v !== "") {
        filled[k] = v;
      }
    }
    props.onSubmit({ spec: value, secrets: filled });
  };

  const basic = groups.filter((g) => !g.advanced);
  const advanced = groups.filter((g) => g.advanced);
  const errorCount = Object.keys(errors).length;
  const renderGroup = (g: (typeof groups)[number], i: number) =>
    g.name ? (
      <fieldset key={`${g.name}-${i}`} className="flex flex-col gap-4">
        <legend className="mb-2 font-semibold text-base">
          {props.groupLabel?.(g.name) ?? g.name}
        </legend>
        {g.fields.map((f) => (
          <FieldView key={f.path} field={f} ctx={ctx} />
        ))}
      </fieldset>
    ) : (
      <div key={`fields-${i}`} className="flex flex-col gap-4">
        {g.fields.map((f) => (
          <FieldView key={f.path} field={f} ctx={ctx} />
        ))}
      </div>
    );

  return (
    <form
      id={formId}
      noValidate
      onSubmit={submit}
      className={cn("flex flex-col gap-6", props.className)}
    >
      {submitted && errorCount > 0 ? (
        <p
          role="alert"
          className="rounded-md border border-danger/40 bg-danger-subtle px-3 py-2 text-sm"
        >
          {intl.formatMessage(m.formHasProblems, { count: errorCount })}
        </p>
      ) : null}
      {basic.map(renderGroup)}
      {advanced.length > 0 ? (
        <Collapsible defaultOpen={advanced.some((g) => g.fields.some((f) => errors[f.path]))}>
          <CollapsibleTrigger>{intl.formatMessage(m.formAdvanced)}</CollapsibleTrigger>
          <CollapsibleContent className="mt-4 flex flex-col gap-6">
            {advanced.map(renderGroup)}
          </CollapsibleContent>
        </Collapsible>
      ) : null}
      <div className="flex flex-wrap justify-end gap-2">
        {props.onCancel ? (
          <Button type="button" variant="outline" onClick={props.onCancel}>
            {intl.formatMessage(m.cancel)}
          </Button>
        ) : null}
        <Button type="submit" busy={props.busy}>
          {props.submitLabel}
        </Button>
      </div>
    </form>
  );
}
