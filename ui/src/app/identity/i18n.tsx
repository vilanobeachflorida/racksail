import { type ReactNode, useEffect, useMemo, useState } from "react";
import { type IntlConfig, useIntl } from "react-intl";
import {
  defaultLocale,
  I18nProvider,
  loadMessages,
  shellMessages,
  useShownLocale,
} from "@/lib/i18n";
import type { Messages } from "@/lib/messages";
import { isPseudoLocale } from "@/lib/pseudo-locales";
import english from "./messages/en.json";

// The sign-in screens and the account and people screens load with their
// own messages, so the panel's first load doesn't carry them. English comes
// with the screens; other languages load the first time they're needed.
const translations = import.meta.glob<Messages>(["./messages/*.json", "!./messages/en.json"], {
  import: "default",
});

const loaded = new Map<string, Messages>([[defaultLocale, english]]);

/** The identity screens' messages in a locale, translations on top of English. */
export async function identityMessages(locale: string): Promise<Messages> {
  const cached = loaded.get(locale);
  if (cached) {
    return cached;
  }
  let messages: Messages;
  if (isPseudoLocale(locale)) {
    const { pseudoMessages } = await import("@/lib/pseudo");
    messages = pseudoMessages(english, locale);
  } else {
    const load = translations[`./messages/${locale}.json`];
    messages = { ...english, ...(load ? await load() : {}) };
  }
  loaded.set(locale, messages);
  return messages;
}

/**
 * Adds the identity screens' messages to the panel's for what it wraps.
 * Until a translation has loaded, it shows the fallback.
 */
export function IdentityMessages({
  children,
  fallback = null,
}: {
  children: ReactNode;
  fallback?: ReactNode;
}) {
  const intl = useIntl();
  const locale = useShownLocale();
  const [ready, setReady] = useState<{ locale: string; messages: Messages } | null>(() => {
    const cached = loaded.get(locale);
    return cached ? { locale, messages: cached } : null;
  });
  useEffect(() => {
    if (ready?.locale === locale) {
      return;
    }
    let live = true;
    void identityMessages(locale).then((messages) => {
      if (live) {
        setReady({ locale, messages });
      }
    });
    return () => {
      live = false;
    };
  }, [locale, ready]);
  const outer = intl.messages as Messages;
  const merged = useMemo(() => ({ ...outer, ...(ready?.messages ?? {}) }), [outer, ready]);
  if (ready?.locale !== locale) {
    return <>{fallback}</>;
  }
  return (
    <I18nProvider locale={locale} messages={merged} onError={intl.onError}>
      {children}
    </I18nProvider>
  );
}

/**
 * The panel's own messages and the identity screens', for screens that
 * show before the panel has loaded, such as signing in. It shows nothing
 * until they've loaded; English falls back on what ships with the shell.
 */
export function StandaloneMessages({
  locale,
  onError,
  children,
}: {
  locale: string;
  onError?: IntlConfig["onError"];
  children: ReactNode;
}) {
  const [messages, setMessages] = useState<Messages | null>(null);
  useEffect(() => {
    let live = true;
    Promise.all([loadMessages(locale, []), identityMessages(locale)])
      .then(([app, own]) => live && setMessages({ ...app, ...own }))
      .catch(() => live && setMessages({ ...shellMessages, ...english }));
    return () => {
      live = false;
    };
  }, [locale]);
  if (!messages) {
    return null;
  }
  return (
    <I18nProvider locale={locale} messages={messages} onError={onError}>
      {children}
    </I18nProvider>
  );
}
