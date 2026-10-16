import { Direction } from "radix-ui";
import { createContext, type ReactNode, useContext } from "react";
import { type IntlConfig, IntlProvider } from "react-intl";
import appEnglish from "@/app/messages/en.json";
import type { Messages } from "./messages";
import { isPseudoLocale, type PseudoLocale, pseudoLocales } from "./pseudo-locales";

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
// The shell's screens, such as the lists and details of resources, the
// jobs and the timeline, have a catalog of their own that loads with the
// first of them, so the shell shows without it. The attention inbox, the
// home screen's first card, has a small one of its own that loads with it,
// and the core's kinds have their words apart, for the home screen to name
// them without loading the screens' catalog.
const screenCatalogs = import.meta.glob<Messages>(
  [
    "../app/screens/messages/*.json",
    "../app/screens/inbox/messages/*.json",
    "../app/screens/kinds/messages/*.json",
  ],
  { import: "default" },
);

/** The catalogs of the shell's screens: their words, the inbox's, and the core's kinds'. */
export type ScreenCatalog = "screens" | "inbox" | "kinds";

// The screens name the core's kinds too, so their catalog brings those words.
const screenCatalogDirs: Record<ScreenCatalog, string[]> = {
  screens: ["../app/screens/messages", "../app/screens/kinds/messages"],
  inbox: ["../app/screens/inbox/messages"],
  kinds: ["../app/screens/kinds/messages"],
};

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
 * English rather than as a message id. Pseudo-locales are made from the
 * English messages.
 */
export async function loadMessages(
  locale: string,
  moduleIds: readonly string[],
): Promise<Messages> {
  if (isPseudoLocale(locale)) {
    const [{ pseudoMessages }, english] = await Promise.all([
      import("./pseudo"),
      loadMessages(defaultLocale, moduleIds),
    ]);
    return pseudoMessages(english, locale);
  }
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

/**
 * Loads the messages of the shell's screens, of the inbox or of the core's
 * kinds, English with any translation on top, as loadMessages does for the
 * shell.
 */
export async function loadScreenMessages(
  locale: string,
  catalog: ScreenCatalog = "screens",
): Promise<Messages> {
  if (isPseudoLocale(locale)) {
    const [{ pseudoMessages }, english] = await Promise.all([
      import("./pseudo"),
      loadScreenMessages(defaultLocale, catalog),
    ]);
    return pseudoMessages(english, locale);
  }
  const layers = locale === defaultLocale ? [defaultLocale] : [defaultLocale, locale];
  const loaded = await Promise.all(
    layers.flatMap((layer) =>
      screenCatalogDirs[catalog].map((dir) => load(screenCatalogs, `${dir}/${layer}.json`)),
    ),
  );
  return Object.assign({}, ...loaded);
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

export const localeKey = "racksail.locale";

/** The language someone chose in this browser, or null to follow the browser's languages. */
export function storedLocale(): string | null {
  try {
    const value = window.localStorage.getItem(localeKey);
    return value && (locales.includes(value) || isPseudoLocale(value)) ? value : null;
  } catch {
    return null;
  }
}

/** The locale to show: the one chosen in this browser, or the best match for its languages. */
export function initialLocale(): string {
  return storedLocale() ?? pickLocale(typeof navigator === "undefined" ? [] : navigator.languages);
}

/**
 * The languages people can choose. Pseudo-locales are offered in
 * development, where they're for checking translations and layouts.
 */
export function availableLocales(
  dev: boolean = import.meta.env.DEV,
): { tag: string; name: string }[] {
  const named = locales.map((tag) => ({
    tag,
    name: new Intl.DisplayNames([tag], { type: "language" }).of(tag) ?? tag,
  }));
  return dev
    ? [...named, ...pseudoLocales.map((tag: PseudoLocale) => ({ tag, name: tag }))]
    : named;
}

interface LocaleChoice {
  choice: string | null;
  setChoice: (locale: string | null) => void;
}

const LocaleContext = createContext<LocaleChoice>({ choice: null, setChoice: () => {} });

/** Lets settings change the panel's language. */
export function LocaleChoiceProvider({
  value,
  children,
}: {
  value: LocaleChoice;
  children: ReactNode;
}) {
  return <LocaleContext value={value}>{children}</LocaleContext>;
}

/** The language chosen in this browser, and a way to change it; null follows the browser. */
export function useLocaleChoice(): [string | null, (locale: string | null) => void] {
  const { choice, setChoice } = useContext(LocaleContext);
  return [choice, setChoice];
}

export interface I18nProviderProps {
  locale: string;
  messages: Messages;
  /** Called for formatting problems, such as a missing message; tests make it throw. */
  onError?: IntlConfig["onError"];
  /** The time zone dates and times show in; the browser's when left out. */
  timeZone?: string;
  /** A 12-hour or 24-hour clock; the language's when left out. */
  hourCycle?: "h12" | "h23";
  children: ReactNode;
}

const ShownLocaleContext = createContext<string>(defaultLocale);

/**
 * The locale the panel shows, pseudo-locales included, which react-intl
 * formats as the language they stand in for. Parts of the panel that load
 * their own messages with their screens load them for it.
 */
export function useShownLocale(): string {
  return useContext(ShownLocaleContext);
}

/** Provides react-intl with the loaded messages. Pseudo-locales format like English. */
export function I18nProvider({
  locale,
  messages,
  onError,
  timeZone,
  hourCycle,
  children,
}: I18nProviderProps) {
  const language = isPseudoLocale(locale) ? (locale === "ar-XB" ? "ar" : defaultLocale) : locale;
  return (
    <IntlProvider
      // The clock rides on the locale as a Unicode extension, so every
      // date and time formatter follows it.
      locale={hourCycle && !language.includes("-u-") ? `${language}-u-hc-${hourCycle}` : language}
      defaultLocale={defaultLocale}
      messages={messages}
      {...(onError ? { onError } : {})}
      {...(timeZone ? { timeZone } : {})}
    >
      <ShownLocaleContext value={locale}>
        {/* Radix components set their own direction, left to right unless told otherwise. */}
        <Direction.Provider dir={isRightToLeft(locale) ? "rtl" : "ltr"}>
          {children}
        </Direction.Provider>
      </ShownLocaleContext>
    </IntlProvider>
  );
}
