import {
  type QueryClient,
  QueryClientProvider,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { type RouterHistory, RouterProvider } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import type { IntlConfig } from "react-intl";
import { TooltipProvider } from "@/design/components/ui/overlay";
import type { ThemeLayers } from "@/design/theme/layers";
import type { Preferences } from "@/design/theme/prefs";
import { ThemeProvider } from "@/design/theme/provider";
import type { ApiClient } from "@/lib/api/client";
import { ApiProvider } from "@/lib/api/context";
import {
  applyLocale,
  defaultLocale,
  I18nProvider,
  initialLocale,
  LocaleChoiceProvider,
  loadMessages,
  localeKey,
  shellMessages,
  storedLocale,
} from "@/lib/i18n";
import { type LiveClient, LiveProvider } from "@/lib/live";
import type { Messages } from "@/lib/messages";
import { createQueryClient } from "@/lib/query";
import { loadModules, type ModuleId } from "@/modules/registry";
import type { ModuleUI } from "@/modules/types";
import { BootError } from "./errors";
import { type ModuleState, ModulesProvider } from "./modules";
import { createAppRouter } from "./router";
import { ShellSkeleton } from "./shell";
import { ShellStateProvider } from "./shell-state";

export interface AppProps {
  api: ApiClient;
  queryClient?: QueryClient;
  /** The locale to show; by default the one chosen in this browser, or the best match for its languages. */
  locale?: string;
  /** Where the router keeps its location; by default the browser's address bar. */
  history?: RouterHistory;
  /** Called for message formatting problems, such as a missing message. */
  onIntlError?: IntlConfig["onError"];
  /** The server and reseller themes on top of the default one. */
  themeLayers?: ThemeLayers;
  /** Where a theme package's fonts are served. */
  fontURL?: (path: string) => string;
  /** Which modules are on; every module counts as on without it. */
  moduleStates?: Partial<Record<ModuleId, ModuleState>> | null;
  /** Whether the signed-in person has a permission. */
  can?: (permission: string) => boolean;
  /** Keeps personal preferences with the account as well as in the browser. */
  onPreferencesChange?: (preferences: Preferences) => void;
  /** Live data connections; tests give one with fake event sources. */
  live?: LiveClient;
}

/** The web UI: providers, then the shell once modules and messages have loaded. */
export function App({
  api,
  queryClient,
  locale,
  history,
  onIntlError,
  themeLayers,
  fontURL,
  moduleStates,
  can,
  onPreferencesChange,
  live,
}: AppProps) {
  const [client] = useState(() => queryClient ?? createQueryClient());
  const [choice, setChoice] = useState<string | null>(() => (locale ? null : storedLocale()));
  const shown = locale ?? choice ?? initialLocale();
  const localeChoice = useMemo(
    () => ({
      choice,
      setChoice: (next: string | null) => {
        try {
          if (next === null) {
            window.localStorage.removeItem(localeKey);
          } else {
            window.localStorage.setItem(localeKey, next);
          }
        } catch {
          // The choice lasts until the page is closed.
        }
        setChoice(next);
      },
    }),
    [choice],
  );
  return (
    <ApiProvider client={api}>
      <QueryClientProvider client={client}>
        <LiveProvider {...(live ? { client: live } : {})}>
          <ThemeProvider
            {...(themeLayers ? { layers: themeLayers } : {})}
            {...(fontURL ? { fontURL } : {})}
            {...(onPreferencesChange ? { onPreferencesChange } : {})}
          >
            <LocaleChoiceProvider value={localeChoice}>
              <Panel
                locale={shown}
                history={history}
                onIntlError={onIntlError}
                moduleStates={moduleStates ?? null}
                can={can}
              />
            </LocaleChoiceProvider>
          </ThemeProvider>
        </LiveProvider>
      </QueryClientProvider>
    </ApiProvider>
  );
}

interface Loaded {
  modules: ModuleUI[];
  messages: Messages;
}

/**
 * Loads every module's entry file and the messages they need. Entries are
 * small; the screens they declare load on first visit.
 */
async function load(locale: string): Promise<Loaded> {
  const modules = await loadModules();
  const messages = await loadMessages(
    locale,
    modules.map((module) => module.id),
  );
  return { modules, messages };
}

function Panel({
  locale,
  history,
  onIntlError,
  moduleStates,
  can,
}: {
  locale: string;
  history: RouterHistory | undefined;
  onIntlError: IntlConfig["onError"] | undefined;
  moduleStates: Partial<Record<ModuleId, ModuleState>> | null;
  can: ((permission: string) => boolean) | undefined;
}) {
  const queryClient = useQueryClient();
  const loaded = useQuery({
    queryKey: ["panel", locale],
    queryFn: () => load(locale),
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  const modules = loaded.data?.modules;
  const router = useMemo(
    () => (modules ? createAppRouter(modules, queryClient, history) : null),
    [modules, queryClient, history],
  );
  const assistant = modules?.find((m) => m.assistant)?.assistant ?? null;

  useEffect(() => applyLocale(locale), [locale]);

  if (loaded.isError || !loaded.data || !router) {
    return (
      <I18nProvider locale={defaultLocale} messages={shellMessages} onError={onIntlError}>
        {loaded.isError ? <BootError error={loaded.error} /> : <ShellSkeleton />}
      </I18nProvider>
    );
  }
  return (
    <I18nProvider locale={locale} messages={loaded.data.messages} onError={onIntlError}>
      <TooltipProvider delayDuration={400}>
        <ModulesProvider
          modules={loaded.data.modules}
          states={moduleStates}
          {...(can ? { can } : {})}
        >
          <ShellStateProvider assistant={assistant}>
            <RouterProvider router={router} />
          </ShellStateProvider>
        </ModulesProvider>
      </TooltipProvider>
    </I18nProvider>
  );
}
