import { type MessageFormatElement, parse, TYPE } from "@formatjs/icu-messageformat-parser";
import type { IntlShape } from "react-intl";
import { errorMessageId } from "@/lib/errors";
import { eventText, messageArguments, type TimelineEvent } from "@/lib/events";
import type { InboxItem, InboxProblem } from "./api";

/**
 * The message id of an item's words: inbox.<code> for what the core finds,
 * <module>.inbox.<code> in the module's catalog for what a module finds.
 */
export function inboxMessageId(item: Pick<InboxItem, "source" | "code">, part?: "message"): string {
  const base = item.source === "core" ? `inbox.${item.code}` : `${item.source}.inbox.${item.code}`;
  return part ? `${base}.${part}` : base;
}

/** The values a message counts with, which the panel sends as text like every param. */
function countedArguments(text: string): Set<string> {
  const names = new Set<string>();
  const walk = (elements: MessageFormatElement[]) => {
    for (const el of elements) {
      if (el.type === TYPE.plural) {
        names.add(el.value);
      }
      if (el.type === TYPE.plural || el.type === TYPE.select) {
        for (const option of Object.values(el.options)) {
          walk(option.value);
        }
      }
    }
  };
  try {
    walk(parse(text, { ignoreTag: true }));
  } catch {
    // A message that doesn't parse isn't used.
  }
  return names;
}

/**
 * A kind's name in the person's language when a catalog that has loaded
 * has it: a module's kinds are in its catalog, which loads with the shell,
 * and the core's in one of their own, which the inbox loads for them.
 */
function kindWord(intl: IntlShape, kind: string): string | undefined {
  const cut = kind.indexOf(".");
  const id = `${kind.slice(0, cut)}.kinds.${kind.slice(cut + 1)}.title`;
  return typeof intl.messages[id] === "string" ? intl.formatMessage({ id }) : undefined;
}

/**
 * The values for an item's message: its params, with moments in the
 * person's own format, kinds in their language and counts as numbers.
 */
function inboxValues(
  intl: IntlShape,
  item: InboxItem,
  text: string,
): Record<string, string | number> {
  const values: Record<string, string | number> = { ...(item.params ?? {}) };
  for (const key of ["date", "time"] as const) {
    const at = item.params?.[key];
    if (at && !Number.isNaN(Date.parse(at))) {
      values[key] = intl.formatDate(
        at,
        key === "date" ? { dateStyle: "medium" } : { dateStyle: "medium", timeStyle: "short" },
      );
    }
  }
  const kind = item.kind && "kind" in values ? kindWord(intl, item.kind) : undefined;
  if (kind) {
    values.kind = kind;
  }
  for (const name of countedArguments(text)) {
    const n = Number(values[name]);
    if (values[name] !== undefined && Number.isFinite(n)) {
      values[name] = n;
    }
  }
  return values;
}

/** The translation of an item's words, unless the catalogs lack it or the item lacks a value it needs. */
function translated(intl: IntlShape, id: string, item: InboxItem): string | undefined {
  const text = intl.messages[id];
  if (typeof text !== "string") {
    return undefined;
  }
  const values = inboxValues(intl, item, text);
  if (!messageArguments(text).every((name) => name in values)) {
    return undefined;
  }
  return intl.formatMessage({ id }, values);
}

/**
 * What an item says, in the person's language when the catalogs have its
 * code and the panel's English otherwise. Alerts read as the event they
 * stand for.
 */
export function inboxTitle(intl: IntlShape, item: InboxItem): string {
  if (item.code === "alert" && item.source === "core" && item.event) {
    return eventText(intl, item.event as TimelineEvent);
  }
  return translated(intl, inboxMessageId(item), item) ?? item.title;
}

/** The line under an item's title, when it has one. */
export function inboxMessage(intl: IntlShape, item: InboxItem): string {
  if (!item.message) {
    return "";
  }
  return translated(intl, inboxMessageId(item, "message"), item) ?? item.message;
}

/**
 * Why part of the inbox couldn't be checked: the translation of the
 * problem's code with its values, unless the catalogs lack it or the
 * problem lacks a value it needs, and the panel's English otherwise.
 */
export function inboxProblemText(intl: IntlShape, problem: InboxProblem): string {
  const id = errorMessageId(problem.code);
  const text = intl.messages[id];
  if (typeof text !== "string") {
    return problem.message;
  }
  const values: Record<string, string | number | boolean> = {};
  for (const [key, value] of Object.entries(problem.params ?? {})) {
    values[key] =
      typeof value === "string" || typeof value === "number" || typeof value === "boolean"
        ? value
        : JSON.stringify(value);
  }
  if (!messageArguments(text).every((name) => name in values)) {
    return problem.message;
  }
  return intl.formatMessage({ id }, values);
}
