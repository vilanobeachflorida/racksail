import { type MessageFormatElement, parse, TYPE } from "@formatjs/icu-messageformat-parser";
import type { IntlShape } from "react-intl";
import type { Actor, Scope } from "./changes";

/** How urgent an event is, from least to most. */
export const severities = ["info", "notice", "warning", "error", "critical"] as const;
export type Severity = (typeof severities)[number];

/**
 * Something that happened, from the event timeline: the JSON form of
 * internal/events. Its message is the panel's English; eventText shows it
 * in the user's language when the catalogs have the event's type.
 */
export interface TimelineEvent {
  id: string;
  /** <kind>.<past-tense verb>, such as web.site.created. */
  type: string;
  time: string;
  severity: Severity;
  scope: Scope;
  message: string;
  actor?: Actor;
  /** The resource the event is about. */
  subject?: string;
  /** The subject's kind. */
  kind?: string;
  changeSet?: string;
  job?: string;
  data?: unknown;
}

/** A page of events, newest first. */
export interface TimelineEventList {
  events: TimelineEvent[] | null;
  next?: string;
}

/** The message id that translates an event type. */
export function eventMessageId(type: string): string {
  return `event.${type}`;
}

/**
 * An event in the user's language: the catalog's message for its type,
 * filled in from the event's data, or the panel's English message when the
 * catalogs don't have one or the data doesn't fit it.
 */
export function eventText(intl: IntlShape, event: TimelineEvent): string {
  const id = eventMessageId(event.type);
  if (!Object.hasOwn(intl.messages, id)) {
    return event.message;
  }
  const values: Record<string, string> = {
    subject: event.subject ?? "",
    kind: event.kind ?? "",
  };
  if (typeof event.data === "object" && event.data !== null) {
    for (const [key, value] of Object.entries(event.data)) {
      values[key] = typeof value === "string" ? value : JSON.stringify(value);
    }
  }
  const text = intl.messages[id];
  if (typeof text !== "string" || !messageArguments(text).every((name) => name in values)) {
    return event.message;
  }
  return intl.formatMessage({ id }, values);
}

/** The names of the values a message needs. */
export function messageArguments(text: string): string[] {
  let ast: MessageFormatElement[];
  try {
    ast = parse(text, { ignoreTag: true });
  } catch {
    return [];
  }
  const names = new Set<string>();
  const walk = (elements: MessageFormatElement[]) => {
    for (const el of elements) {
      if ("value" in el && typeof el.value === "string" && el.type !== TYPE.literal) {
        names.add(el.value);
      }
      if (el.type === TYPE.plural || el.type === TYPE.select) {
        for (const option of Object.values(el.options)) {
          walk(option.value);
        }
      }
    }
  };
  walk(ast);
  return [...names];
}
