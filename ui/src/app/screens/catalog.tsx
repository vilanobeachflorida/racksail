import { useQuery } from "@tanstack/react-query";
import { lazyRouteComponent, Outlet } from "@tanstack/react-router";
import { type ReactNode, useMemo } from "react";
import { IntlProvider, useIntl } from "react-intl";
import { Skeleton } from "@/design/components/ui/skeleton";
import { loadScreenMessages, type ScreenCatalog, useShownLocale } from "@/lib/i18n";

/** What a screen shows while its code or messages load. */
export function ScreenSkeleton() {
  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-4" aria-busy="true">
      <Skeleton className="h-8 w-56" />
      <Skeleton className="h-4 w-80" />
      <Skeleton className="h-64 w-full" />
    </div>
  );
}

/**
 * Shows its children with the screens' messages beside the shell's, once
 * they have loaded. They load the first time any screen needs them; the
 * inbox has a catalog of its own, which the home screen loads alone.
 */
export function ScreenMessages({
  children,
  fallback = <ScreenSkeleton />,
  catalog = "screens",
}: {
  children: ReactNode;
  fallback?: ReactNode;
  catalog?: ScreenCatalog;
}) {
  const intl = useIntl();
  const locale = useShownLocale();
  const loaded = useQuery({
    queryKey: ["screen-messages", catalog, locale],
    queryFn: () => loadScreenMessages(locale, catalog),
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  const messages = useMemo(
    () => (loaded.data ? { ...(intl.messages as Record<string, string>), ...loaded.data } : null),
    [intl.messages, loaded.data],
  );
  if (loaded.isError) {
    // The route's error screen says what went wrong.
    throw loaded.error;
  }
  if (!messages) {
    return fallback;
  }
  return (
    <IntlProvider
      locale={intl.locale}
      defaultLocale={intl.defaultLocale}
      messages={messages}
      onError={intl.onError}
      {...(intl.timeZone ? { timeZone: intl.timeZone } : {})}
      formats={intl.formats}
      defaultFormats={intl.defaultFormats}
      textComponent={intl.textComponent}
    >
      {children}
    </IntlProvider>
  );
}

/**
 * A screen the router loads the first time it's shown, or when a link to
 * it is about to be followed, with the screens' messages. The screen is
 * the module's default export unless another is named.
 */
export function screen<T extends Record<string, unknown>>(
  load: () => Promise<T>,
  name = "default",
) {
  const Screen = lazyRouteComponent(load as () => Promise<Record<string, () => ReactNode>>, name);
  function Route() {
    return (
      <ScreenMessages>
        <Screen />
      </ScreenMessages>
    );
  }
  return Object.assign(Route, { preload: Screen.preload });
}

/** A route whose screens below it all have the screens' messages. */
export function ScreenOutlet() {
  return (
    <ScreenMessages>
      <Outlet />
    </ScreenMessages>
  );
}
