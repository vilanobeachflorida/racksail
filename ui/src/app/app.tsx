import {
  type QueryClient,
  QueryClientProvider,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { type RouterHistory, RouterProvider } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import type { IntlConfig } from "react-intl";
import type { ApiClient } from "@/lib/api/client";
import { ApiProvider } from "@/lib/api/context";
import {
  applyLocale,
  defaultLocale,
  I18nProvider,
  loadMessages,
  pickLocale,
  shellMessages,
} from "@/lib/i18n";
import type { Messages } from "@/lib/messages";
import { createQueryClient } from "@/lib/query";
import { loadModules } from "@/modules/registry";
import type { ModuleUI } from "@/modules/types";
import { BootError } from "./errors";
import { ModulesProvider } from "./modules";
import { createAppRouter } from "./router";
import { ShellSkeleton } from "./shell";

export interface AppProps {
  api: ApiClient;
  queryClient?: QueryClient;
  /** The locale to show; by default the best match for the browser's languages. */
  locale?: string;
  /** Where the router keeps its location; by default the browser's address bar. */
  history?: RouterHistory;
  /** Called for message formatting problems, such as a missing message. */
  onIntlError?: IntlConfig["onError"];
}

/** The web UI: providers, then the shell once modules and messages have loaded. */
export function App({ api, queryClient, locale, history, onIntlError }: AppProps) {
  const [client] = useState(() => queryClient ?? createQueryClient());
  const [shown] = useState(() => locale ?? pickLocale(navigator.languages));
  return (
    <ApiProvider client={api}>
      <QueryClientProvider client={client}>
        <Panel locale={shown} history={history} onIntlError={onIntlError} />
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
}: {
  locale: string;
  history: RouterHistory | undefined;
  onIntlError: IntlConfig["onError"] | undefined;
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
      <ModulesProvider modules={loaded.data.modules}>
        <RouterProvider router={router} />
      </ModulesProvider>
    </I18nProvider>
  );
}
