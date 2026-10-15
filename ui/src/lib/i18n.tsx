import type { ReactNode } from "react";
import { type IntlConfig, IntlProvider } from "react-intl";
import appEnglish from "@/app/messages/en.json";
import type { Messages } from "./messages";

type Loader = () => Promise<Messages>;

export const defaultLocale = "en";

// English app messages ship with the shell, so it can always explain a
// problem. Everything else is split per locale and per module, so the panel
// only downloads messages it can show.
const appCatalogs = import.meta.glob<Messages>(
  ["../app/messages/*.json", "!../app/messages/en.json"],
  { import: "default" },
);
const moduleCatalogs = import.meta.glob<Messages>("../modules/*/messages/*.json", {
  import: "default",
});

function localeOf(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1, -".json".length);
}

/** The locales the panel has messages for. */
export const locales: readonly string[] = [
  defaultLocale,
  ...Object.keys(appCatalogs).map(localeOf),
].sort();

/** The English messages of the shell. */
export const shellMessages: Messages = appEnglish;

/**
 * Picks the best available locale for the user's preferred languages, as in
 * navigator.languages: an exact match first, then the same language.
 */
export function pickLocale(
  preferred: readonly string[],
  available: readonly string[] = locales,
): string {
  for (const tag of preferred) {
    const wanted = tag.toLowerCase();
    const exact = available.find((l) => l.toLowerCase() === wanted);
    if (exact) {
      return exact;
    }
    const language = wanted.split("-")[0];
    const sameLanguage = available.find((l) => l.toLowerCase() === language);
    if (sameLanguage) {
      return sameLanguage;
    }
  }
  return defaultLocale;
}

const rightToLeft = new Set(["ar", "ckb", "dv", "fa", "he", "ps", "sd", "ug", "ur", "yi"]);

/** Whether text in the locale runs right to left. */
export function isRightToLeft(locale: string): boolean {
  return rightToLeft.has(locale.toLowerCase().split("-")[0] ?? "");
}

/**
 * Loads the messages of the app and of the given modules. Translations go on
 * top of the English messages, so anything not translated yet shows in
 * English rather than as a message id.
 */
export async function loadMessages(
  locale: string,
  moduleIds: readonly string[],
): Promise<Messages> {
  const layers = locale === defaultLocale ? [defaultLocale] : [defaultLocale, locale];
  const loads: Promise<Messages>[] = [];
  for (const layer of layers) {
    loads.push(
      layer === defaultLocale
        ? Promise.resolve(shellMessages)
        : load(appCatalogs, `../app/messages/${layer}.json`),
    );
    for (const id of moduleIds) {
      loads.push(load(moduleCatalogs, `../modules/${id}/messages/${layer}.json`));
    }
  }
  return Object.assign({}, ...(await Promise.all(loads)));
}

function load(catalogs: Record<string, Loader>, path: string): Promise<Messages> {
  const loader = catalogs[path];
  return loader ? loader() : Promise.resolve({});
}

/** Applies the locale's language and writing direction to the document. */
export function applyLocale(locale: string, root: HTMLElement = document.documentElement): void {
  root.lang = locale;
  root.dir = isRightToLeft(locale) ? "rtl" : "ltr";
}

export interface I18nProviderProps {
  locale: string;
  messages: Messages;
  /** Called for formatting problems, such as a missing message; tests make it throw. */
  onError?: IntlConfig["onError"];
  children: ReactNode;
}

/** Provides react-intl with the loaded messages. */
export function I18nProvider({ locale, messages, onError, children }: I18nProviderProps) {
  return (
    <IntlProvider
      locale={locale}
      defaultLocale={defaultLocale}
      messages={messages}
      {...(onError ? { onError } : {})}
    >
      {children}
    </IntlProvider>
  );
}
