import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import type { IntlConfig } from "react-intl";
import { App, type AppProps } from "@/app/app";
import { ModulesProvider, type ModuleState } from "@/app/modules";
import { createAppRouter } from "@/app/router";
import { ShellStateProvider } from "@/app/shell-state";
import { TooltipProvider } from "@/design/components/ui/overlay";
import { ThemeProvider } from "@/design/theme/provider";
import { type ApiClient, createApiClient } from "@/lib/api/client";
import { ApiProvider } from "@/lib/api/context";
import { I18nProvider, shellMessages } from "@/lib/i18n";
import { LiveClient, LiveProvider } from "@/lib/live";
import type { Messages } from "@/lib/messages";
import type { ModuleId } from "@/modules/registry";
import type { ModuleUI } from "@/modules/types";

/** Makes any message formatting problem, such as a missing message, fail the test. */
export const failOnIntlError: IntlConfig["onError"] = (error) => {
  throw error;
};

export const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

type Route = (request: Request) => Response | Promise<Response>;

/**
 * An API client that answers from the given routes, by method and path,
 * such as "GET /api/v1/version", and with 404 for anything else.
 */
export function fakeApi(routes: Record<string, Route> = {}): ApiClient {
  return createApiClient({
    baseUrl: "http://panel.test",
    fetch: async (request: Request) => {
      const route = routes[`${request.method} ${new URL(request.url).pathname}`];
      return route ? route(request) : new Response("not found", { status: 404 });
    },
  });
}

/** An EventSource the test drives by hand. */
export class FakeEventSource {
  readonly url: string;
  readyState = 0;
  closed = false;
  private listeners = new Map<string, Set<(event: Event) => void>>();

  constructor(url: string) {
    this.url = url;
  }

  addEventListener(type: string, listener: (event: Event) => void) {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(listener);
  }

  removeEventListener(type: string, listener: (event: Event) => void) {
    this.listeners.get(type)?.delete(listener);
  }

  close() {
    this.closed = true;
    this.readyState = 2;
  }

  private dispatch(type: string, event: Event) {
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event);
    }
  }

  /** The connection opens. */
  open() {
    this.readyState = 1;
    this.dispatch("open", new Event("open"));
  }

  /** The server sends an event, its data as JSON. */
  emit(type: string, data: unknown, id = "") {
    this.dispatch(type, new MessageEvent(type, { data: JSON.stringify(data), lastEventId: id }));
  }

  /** The connection fails; closed means the browser won't retry by itself. */
  fail(closed: boolean) {
    this.readyState = closed ? 2 : 0;
    this.dispatch("error", new Event("error"));
  }
}

/** A live client whose event sources the test can reach. */
export function fakeLive(): { live: LiveClient; sources: FakeEventSource[] } {
  const sources: FakeEventSource[] = [];
  const live = new LiveClient({
    open: (url) => {
      const source = new FakeEventSource(url);
      sources.push(source);
      return source as unknown as EventSource;
    },
    maxDelay: 10,
  });
  return { live, sources };
}

export const testQueryClient = () =>
  new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });

/** Renders the whole app at a path, against a fake API, in English. */
export function renderApp(
  api: ApiClient,
  path = "/",
  options: Partial<AppProps> & { locale?: string } = {},
) {
  const { live } = fakeLive();
  return render(
    <App
      api={api}
      locale={options.locale ?? "en"}
      queryClient={testQueryClient()}
      history={createMemoryHistory({ initialEntries: [path] })}
      onIntlError={failOnIntlError}
      live={live}
      {...options}
    />,
  );
}

/** The providers a component needs: API, queries, live data, theme, messages and tooltips. */
export function Providers({
  children,
  messages = shellMessages,
  locale = "en",
  api = fakeApi(),
  live = fakeLive().live,
}: {
  children: ReactNode;
  messages?: Messages;
  locale?: string;
  api?: ApiClient;
  live?: LiveClient;
}) {
  return (
    <ApiProvider client={api}>
      <QueryClientProvider client={testQueryClient()}>
        <LiveProvider client={live}>
          <ThemeProvider>
            <I18nProvider locale={locale} messages={messages} onError={failOnIntlError}>
              <TooltipProvider delayDuration={0}>{children}</TooltipProvider>
            </I18nProvider>
          </ThemeProvider>
        </LiveProvider>
      </QueryClientProvider>
    </ApiProvider>
  );
}

/** Renders a component with every provider it may need. */
export function renderWithProviders(ui: ReactNode, options: Omit<Parameters<typeof Providers>[0], "children"> = {}) {
  return render(<Providers {...options}>{ui}</Providers>);
}

/**
 * Renders the shell with the given module contributions instead of the
 * bundled modules, at a path.
 */
export function renderShell({
  modules,
  messages,
  path = "/",
  api = fakeApi(),
  states = null,
  can,
}: {
  modules: ModuleUI[];
  messages: Messages;
  path?: string;
  api?: ApiClient;
  states?: Partial<Record<ModuleId, ModuleState>> | null;
  can?: (permission: string) => boolean;
}) {
  const client = testQueryClient();
  const router = createAppRouter(modules, client, createMemoryHistory({ initialEntries: [path] }));
  const assistant = modules.find((m) => m.assistant)?.assistant ?? null;
  const result = render(
    <ApiProvider client={api}>
      <QueryClientProvider client={client}>
        <LiveProvider client={fakeLive().live}>
          <ThemeProvider>
            <I18nProvider locale="en" messages={messages} onError={failOnIntlError}>
              <TooltipProvider delayDuration={0}>
                <ModulesProvider modules={modules} states={states} {...(can ? { can } : {})}>
                  <ShellStateProvider assistant={assistant}>
                    <RouterProvider router={router} />
                  </ShellStateProvider>
                </ModulesProvider>
              </TooltipProvider>
            </I18nProvider>
          </ThemeProvider>
        </LiveProvider>
      </QueryClientProvider>
    </ApiProvider>,
  );
  return { ...result, router };
}
