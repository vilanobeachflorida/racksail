import { type JSONSchema, resolveRef, schemaTypes } from "../schema";

/**
 * How a kind's fields appear on its generated form, as the API describes
 * them next to the kind's JSON Schema (store.UIHints in Go).
 */
export interface FieldHint {
  /** textarea, code, password, select, toggle, size, duration or resource. */
  widget?:
    | "textarea"
    | "code"
    | "password"
    | "select"
    | "toggle"
    | "size"
    | "duration"
    | "resource";
  placeholder?: string;
  advanced?: boolean;
  /** The field holds a secret: its value goes with the change's secrets, never in the spec. */
  sensitive?: boolean;
  /** Can be set on create and is read-only afterwards. */
  createOnly?: boolean;
  order?: number;
  /** The kind of the resources a resource field names by ID. */
  kind?: string;
}

export interface FieldGroup {
  name: string;
  fields: string[];
  advanced?: boolean;
}

export interface UIHints {
  groups?: FieldGroup[];
  fields?: Record<string, FieldHint>;
}

export type FieldKind =
  | "string"
  | "number"
  | "integer"
  | "boolean"
  | "enum"
  | "object"
  | "array"
  | "json";

/** One field of the form, from one property of the schema. */
export interface FieldNode {
  /** Its path in the value, with dots: tls.mode. */
  path: string;
  key: string;
  schema: JSONSchema;
  kind: FieldKind;
  required: boolean;
  hint: FieldHint;
  /** An object's fields. */
  children?: FieldNode[];
  /** What an array holds. */
  item?: FieldNode;
}

export interface GroupNode {
  /** The group's name from the hints; empty for fields no group names. */
  name: string;
  advanced: boolean;
  fields: FieldNode[];
}

function kindOf(schema: JSONSchema): FieldKind {
  if (schema.enum && schema.enum.length > 0) {
    return "enum";
  }
  const types = schemaTypes(schema).filter((t) => t !== "null");
  const t = types[0];
  switch (t) {
    case "string":
    case "number":
    case "integer":
    case "boolean":
    case "object":
    case "array":
      return types.length === 1 || t !== "object" ? t : "json";
    default:
      return schema.properties ? "object" : "json";
  }
}

function join(base: string, key: string): string {
  return base === "" ? key : `${base}.${key}`;
}

/** Builds the field tree of a schema, following $refs, ordered by the hints then the schema. */
export function buildField(
  schema: JSONSchema,
  root: JSONSchema,
  hints: UIHints,
  path: string,
  key: string,
  required: boolean,
  depth = 0,
): FieldNode {
  const s = resolveRef(schema, root);
  const kind = depth > 8 ? "json" : kindOf(s);
  const node: FieldNode = {
    path,
    key,
    schema: s,
    kind,
    required,
    hint: hints.fields?.[path] ?? {},
  };
  if (kind === "object" && s.properties) {
    const requiredKeys = new Set(s.required ?? []);
    node.children = Object.entries(s.properties)
      .map(([k, child]) =>
        buildField(child, root, hints, join(path, k), k, requiredKeys.has(k), depth + 1),
      )
      .filter((f) => !f.schema.readOnly)
      .sort((a, b) => (a.hint.order ?? 0) - (b.hint.order ?? 0));
  }
  if (kind === "array" && s.items) {
    node.item = buildField(s.items, root, hints, `${path}[]`, key, false, depth + 1);
  }
  return node;
}

/**
 * The form's groups: one per group the hints name, in their order, then
 * the fields no group names, and advanced groups and fields last.
 */
export function buildForm(
  schema: JSONSchema,
  hints: UIHints = {},
  root: JSONSchema = schema,
): GroupNode[] {
  const top = buildField(schema, root, hints, "", "", true);
  const fields = top.children ?? [];
  const byPath = new Map(fields.map((f) => [f.path, f]));
  const placed = new Set<string>();
  const groups: GroupNode[] = [];
  for (const g of hints.groups ?? []) {
    const members = g.fields
      .map((p) => byPath.get(p))
      .filter((f): f is FieldNode => f !== undefined);
    for (const f of members) {
      placed.add(f.path);
    }
    if (members.length > 0) {
      groups.push({ name: g.name, advanced: g.advanced ?? false, fields: members });
    }
  }
  const rest = fields.filter((f) => !placed.has(f.path));
  const basic = rest.filter((f) => !f.hint.advanced);
  const advanced = rest.filter((f) => f.hint.advanced);
  const out: GroupNode[] = [];
  if (basic.length > 0) {
    out.push({ name: "", advanced: false, fields: basic });
  }
  out.push(...groups.filter((g) => !g.advanced));
  out.push(...groups.filter((g) => g.advanced));
  if (advanced.length > 0) {
    out.push({ name: "", advanced: true, fields: advanced });
  }
  return out;
}

/**
 * The value a schema starts with: its default, or for objects the defaults
 * of their properties. Fields without a default stay unset.
 */
export function defaultValue(schema: JSONSchema, root: JSONSchema, depth = 0): unknown {
  const s = resolveRef(schema, root);
  if (s.default !== undefined) {
    return structuredClone(s.default);
  }
  if (depth < 8 && s.properties && schemaTypes(s).includes("object")) {
    const out: Record<string, unknown> = {};
    for (const [k, child] of Object.entries(s.properties)) {
      const v = defaultValue(child, root, depth + 1);
      if (v !== undefined) {
        out[k] = v;
      }
    }
    return out;
  }
  return undefined;
}

/**
 * The value without the optional objects nobody filled in that can't be
 * empty, such as an owner left empty, which would otherwise ask for its
 * required fields and reach the server as an empty object. An empty object
 * whose fields are all optional is a value of its own and stays.
 */
export function withoutEmptyObjects(
  schema: JSONSchema,
  value: unknown,
  root: JSONSchema,
  depth = 0,
): unknown {
  let s: JSONSchema;
  try {
    s = resolveRef(schema, root);
  } catch {
    return value;
  }
  if (
    depth > 8 ||
    !s.properties ||
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value)
  ) {
    return value;
  }
  const required = new Set(s.required ?? []);
  const out: Record<string, unknown> = {};
  for (const [key, v] of Object.entries(value)) {
    const child = s.properties[key];
    const next = child ? withoutEmptyObjects(child, v, root, depth + 1) : v;
    const empty =
      next !== null &&
      typeof next === "object" &&
      !Array.isArray(next) &&
      Object.keys(next).length === 0;
    if (!(empty && !required.has(key) && child && needsFields(child, root))) {
      out[key] = next;
    }
  }
  return out;
}

/** Whether an object's schema requires fields, so that it can't be empty. */
function needsFields(schema: JSONSchema, root: JSONSchema): boolean {
  try {
    return (resolveRef(schema, root).required ?? []).length > 0;
  } catch {
    return false;
  }
}

/** Splits a path like spec.domains[0] into keys and indexes. */
export function parsePath(path: string): (string | number)[] {
  const parts: (string | number)[] = [];
  for (const piece of path.split(".")) {
    const match = /^([^[\]]*)((?:\[\d+\])*)$/.exec(piece);
    if (!match) {
      parts.push(piece);
      continue;
    }
    if (match[1]) {
      parts.push(match[1]);
    }
    for (const index of (match[2] ?? "").matchAll(/\[(\d+)\]/g)) {
      parts.push(Number(index[1]));
    }
  }
  return parts;
}

/** The value at a path, or undefined. */
export function getAt(value: unknown, path: string): unknown {
  let current = value;
  for (const part of parsePath(path)) {
    if (current === null || typeof current !== "object") {
      return undefined;
    }
    current = (current as Record<string | number, unknown>)[part];
  }
  return current;
}

/**
 * A copy of value with the path set; undefined removes the key, so unset
 * fields don't reach the server as empty strings.
 */
export function setAt(value: unknown, path: string, next: unknown): unknown {
  const parts = parsePath(path);
  const write = (current: unknown, i: number): unknown => {
    const part = parts[i];
    if (part === undefined) {
      return next;
    }
    if (typeof part === "number") {
      const list = Array.isArray(current) ? [...current] : [];
      list[part] = write(list[part], i + 1);
      return list;
    }
    const obj =
      current && typeof current === "object" && !Array.isArray(current)
        ? { ...(current as object) }
        : {};
    const child = write((obj as Record<string, unknown>)[part], i + 1);
    if (child === undefined) {
      delete (obj as Record<string, unknown>)[part];
    } else {
      (obj as Record<string, unknown>)[part] = child;
    }
    return obj;
  };
  return write(value, 0);
}

/** A property name as a label when the schema has no title: tlsMode becomes "Tls mode". */
export function humanize(key: string): string {
  const words = key
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/[_-]+/g, " ")
    .trim()
    .toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** Whether a value counts as filled in for a required field: present and not empty text. */
export function isFilled(value: unknown): boolean {
  return value !== undefined && value !== null && value !== "";
}
