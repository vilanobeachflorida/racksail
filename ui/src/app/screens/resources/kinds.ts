import { useQuery } from "@tanstack/react-query";
import type { IntlShape, MessageDescriptor } from "react-intl";
import { type ApiClient, ApiError, type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import type { FieldHint, UIHints } from "@/lib/forms/model";
import type { JSONSchema } from "@/lib/schema";
import type { ModuleUI } from "@/modules/types";

/** A kind of resource, as the panel describes it for screens built from it. */
export type Kind = Schemas["KindDescription"];
export type KindAction = Schemas["ActionInfo"];

/** A resource of any kind: its spec and status are JSON its kind's schemas describe. */
export type Resource = Schemas["Resource"];

export type Phase = "pending" | "applying" | "ready" | "degraded" | "failed" | "deleting";

/** The shared part of every kind's status. */
export interface CommonStatus {
  phase?: Phase;
  message?: string;
  code?: string;
  observedGeneration?: number;
  conditions?: Condition[] | null;
}

export interface Condition {
  type: string;
  status: "true" | "false" | "unknown";
  reason?: string;
  message?: string;
  code?: string;
  observedGeneration?: number;
  lastTransitionTime: string;
}

export const kindsKey = ["kinds"] as const;

/** Every kind the panel manages; a panel that doesn't describe kinds has none. */
export async function listKinds(api: ApiClient): Promise<Kind[]> {
  try {
    return (await unwrap(api.GET("/api/v1/kinds"))).kinds ?? [];
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      return [];
    }
    throw error;
  }
}

/** A kind with the JSON Schemas of its spec, status and actions. */
export function getKind(api: ApiClient, name: string): Promise<Kind> {
  return unwrap(api.GET("/api/v1/kinds/{name}", { params: { path: { name } } }));
}

/**
 * The kinds, which change with a new release of the panel, and when an
 * add-on that adds kinds comes or goes, which the change tracker notices.
 */
export function useKinds() {
  const api = useApi();
  return useQuery({
    queryKey: kindsKey,
    queryFn: () => listKinds(api),
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/** A kind with its schemas, once its name is known. */
export function useKindSchemas(name: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: [...kindsKey, name],
    queryFn: () => getKind(api, name ?? ""),
    enabled: name !== undefined,
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/** The kind a screen's path names: /<module>/<plural>. Hidden kinds have no screens. */
export function kindAt(kinds: readonly Kind[], module: string, plural: string): Kind | undefined {
  return kinds.find((k) => k.module === module && k.plural === plural && !k.ui.hidden);
}

/** The kind of a resource, from the prefix of its ID. */
export function kindOfId(kinds: readonly Kind[], id: string): Kind | undefined {
  const cut = id.lastIndexOf("_");
  if (cut <= 0) {
    return undefined;
  }
  const prefix = id.slice(0, cut);
  return kinds.find((k) => k.prefix === prefix);
}

/** Where a kind's list is: /<module>/<plural>, where a module's own screen takes over when it has one. */
export function kindPath(kind: Pick<Kind, "module" | "plural">): string {
  return `/${kind.module}/${kind.plural}`;
}

/** Where a resource's screen is. */
export function resourcePath(kind: Pick<Kind, "module" | "plural">, id: string): string {
  return `${kindPath(kind)}/${encodeURIComponent(id)}`;
}

/** The value at a dotted path, such as tls.mode. */
export function valueAt(value: unknown, path: string): unknown {
  let current = value;
  for (const key of path.split(".")) {
    if (current === null || typeof current !== "object" || Array.isArray(current)) {
      return undefined;
    }
    current = (current as Record<string, unknown>)[key];
  }
  return current;
}

/** The text at a dotted path: the value itself, or the first of a list. */
export function textAt(value: unknown, path: string | undefined): string {
  if (!path) {
    return "";
  }
  let v = valueAt(value, path);
  if (Array.isArray(v)) {
    v = v[0];
  }
  return typeof v === "string"
    ? v
    : typeof v === "number" || typeof v === "boolean"
      ? String(v)
      : "";
}

/** What a resource is called: its kind's title field, or its ID. */
export function resourceTitle(
  kind: Kind | undefined,
  resource: Pick<Resource, "id" | "spec">,
): string {
  return textAt(resource.spec, kind?.ui.titleField) || resource.id;
}

/** The line under a resource's name, from its kind's subtitle field. */
export function resourceSubtitle(kind: Kind | undefined, resource: Pick<Resource, "spec">): string {
  return textAt(resource.spec, kind?.ui.subtitleField);
}

/** The shared part of a resource's status. */
export function commonStatus(resource: Pick<Resource, "status">): CommonStatus {
  const status = resource.status;
  return status !== null && typeof status === "object" ? (status as CommonStatus) : {};
}

/** The phase a resource's status reports; one no reconciler has looked at is pending. */
export function phaseOf(resource: Pick<Resource, "status" | "deletedAt">): Phase {
  if (resource.deletedAt) {
    return "deleting";
  }
  return commonStatus(resource).phase ?? "pending";
}

/** A kind's UI hints in the form the form generator takes. */
export function formHints(kind: Kind): UIHints {
  return {
    groups: (kind.ui.groups ?? []).map((g) => ({
      name: g.name,
      fields: g.fields ?? [],
      ...(g.advanced ? { advanced: true } : {}),
    })),
    fields: Object.fromEntries(
      Object.entries(kind.ui.fields ?? {}).map(([path, hint]) => [path, hint as FieldHint]),
    ),
  };
}

/** A kind's spec schema, as the form generator reads it. */
export function specSchema(kind: Kind): JSONSchema | undefined {
  return kind.spec !== null && typeof kind.spec === "object"
    ? (kind.spec as JSONSchema)
    : undefined;
}

/** The fields whose values are secrets, which forms send apart from the spec. */
export function sensitivePaths(kind: Kind): string[] {
  return Object.entries(kind.ui.fields ?? {})
    .filter(([, hint]) => hint.sensitive)
    .map(([path]) => path);
}

/**
 * The message id of a kind's own words: <module>.kinds.<name>.<part>, in
 * its module's catalog, or the panel's for the core's kinds.
 */
export function kindMessageId(kind: string, part: string): string {
  const cut = kind.indexOf(".");
  return `${kind.slice(0, cut)}.kinds.${kind.slice(cut + 1)}.${part}`;
}

/** A kind's words in the person's language when a catalog has them, and the panel's English otherwise. */
export function kindText(intl: IntlShape, kind: string, part: string, english: string): string {
  const id = kindMessageId(kind, part);
  return Object.hasOwn(intl.messages, id) ? intl.formatMessage({ id }) : english;
}

/** Text written for people from a path segment: mail-domains becomes "Mail domains". */
export function sentence(text: string): string {
  const words = text.replace(/[-_]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** A kind's name for one resource: "Webhook". */
export function kindTitle(intl: IntlShape, kind: Pick<Kind, "name" | "title">): string {
  return kindText(intl, kind.name, "title", kind.title);
}

/** Whether a word is an acronym, written in capitals wherever it stands: DNS, PHP. */
function acronym(word: string): boolean {
  return word.length > 1 && word === word.toUpperCase() && word !== word.toLowerCase();
}

/** A kind's plural path in words, keeping its title's acronyms: dns-zones becomes "DNS zones". */
function pluralWords(kind: Pick<Kind, "title" | "plural">): string {
  const acronyms = kind.title.split(" ").filter(acronym);
  return kind.plural
    .split(/[-_]+/)
    .filter((w) => w !== "")
    .map((w) => acronyms.find((a) => a.toLowerCase() === w.toLowerCase()) ?? w)
    .join(" ");
}

/** A kind's name for several: "Webhooks". */
export function kindPlural(intl: IntlShape, kind: Pick<Kind, "name" | "title" | "plural">): string {
  const words = pluralWords(kind);
  return kindText(intl, kind.name, "plural", words.charAt(0).toUpperCase() + words.slice(1));
}

/** A kind's name inside a sentence, where sentence case leaves it in small letters: "webhook", "DNS zone". */
export function kindNoun(intl: IntlShape, kind: Pick<Kind, "name" | "title">): string {
  const [first = ""] = kind.title.split(" ");
  const english = acronym(first)
    ? kind.title
    : kind.title.charAt(0).toLowerCase() + kind.title.slice(1);
  return kindText(intl, kind.name, "noun", english);
}

/** Several of a kind inside a sentence: "webhooks", "DNS zones". */
export function kindNouns(intl: IntlShape, kind: Pick<Kind, "name" | "title" | "plural">): string {
  return kindText(intl, kind.name, "nouns", pluralWords(kind));
}

export function kindDescription(intl: IntlShape, kind: Pick<Kind, "name" | "description">): string {
  return kindText(intl, kind.name, "description", kind.description);
}

/** An action's button label. */
export function actionTitle(intl: IntlShape, kind: Pick<Kind, "name">, action: KindAction): string {
  return kindText(
    intl,
    kind.name,
    `actions.${action.name}.title`,
    action.title || action.summary.replace(/\.$/, ""),
  );
}

/** What an action does, in a sentence. */
export function actionSummary(
  intl: IntlShape,
  kind: Pick<Kind, "name">,
  action: KindAction,
): string {
  return kindText(
    intl,
    kind.name,
    `actions.${action.name}.description`,
    action.description || action.summary,
  );
}

/**
 * A field's label when a catalog has one, a spec field's or, with
 * part status, a status field's; forms fall back on the schema.
 */
export function fieldLabel(
  intl: IntlShape,
  kind: string,
  path: string,
  part: "fields" | "status" = "fields",
): string | undefined {
  const id = kindMessageId(kind, `${part}.${path}`);
  return Object.hasOwn(intl.messages, id) ? intl.formatMessage({ id }) : undefined;
}

/** The name of one of a field's choices when a catalog has one, such as a severity's. */
export function valueLabel(
  intl: IntlShape,
  kind: string,
  path: string,
  value: string,
): string | undefined {
  const id = kindMessageId(kind, `values.${path}.${value}`);
  return Object.hasOwn(intl.messages, id) ? intl.formatMessage({ id }) : undefined;
}

/** A field group's name. */
export function groupLabel(intl: IntlShape, kind: string, group: string): string {
  return kindText(intl, kind, `groups.${group}`, group);
}

/** What a module is called: its UI's name, the panel's own word for the core, or its id. */
export function moduleName(
  intl: IntlShape,
  modules: readonly ModuleUI[],
  id: string,
  core: MessageDescriptor,
): string {
  if (id === "core") {
    return intl.formatMessage(core);
  }
  const module = modules.find((m) => m.id === id);
  return module ? intl.formatMessage(module.name) : sentence(id);
}
