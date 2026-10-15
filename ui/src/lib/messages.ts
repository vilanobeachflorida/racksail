import type { MessageDescriptor } from "react-intl";

/** A message catalog: message ids and their ICU message text. */
export type Messages = Readonly<Record<string, string>>;

/**
 * Returns a function that makes message descriptors from a catalog's ids, so
 * a mistyped id fails type checking instead of showing up on screen.
 *
 *   import type messages from "./messages/en.json";
 *   const m = messageIds<typeof messages>();
 *   const title = m("web.sites.title");
 */
export function messageIds<Catalog extends Messages>() {
  return <Id extends keyof Catalog & string>(id: Id): MessageDescriptor => ({ id });
}
