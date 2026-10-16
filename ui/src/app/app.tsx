import {
  type QueryClient,
  QueryClientProvider,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { type RouterHistory, RouterProvider } from "@tanstack/react-router";
import {
  type ComponentProps,
  lazy,
  type ReactNode,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";
import { type IntlConfig, useIntl } from "react-intl";
import { TooltipProvider } from "@/design/components/ui/overlay";
import { toast } from "@/design/components/ui/toaster";
import type { ThemeFileURL } from "@/design/theme/files";
import type { ThemeLayers } from "@/design/theme/layers";
import type { Preferences } from "@/design/theme/prefs";
import { ThemeProvider } from "@/design/theme/provider";
import { type ApiClient, ApiError } from "@/lib/api/client";
import { ApiProvider } from "@/lib/api/context";
import { getSession, savePreferences } from "@/lib/api/operations";
import { type PanelSession, permits, sessionModuleStates, themeFileURL } from "@/lib/api/session";
import { errorMessage } from "@/lib/errors";
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
import { GeneralPreferencesProvider, useGeneralPreferences } from "./general";
import { SessionProvider, useSignOutWatch } from "./identity/session";
import { StepUpProvider } from "./identity/step-up";
import { messages } from "./messages";
import { type ModuleState, ModulesProvider } from "./modules";
import { createAppRouter } from "./router";
import { ShellSkeleton } from "./shell";
import { ShellStateProvider } from "./shell-state";

// Signing in, and what someone must do before the panel opens, load only
// when they're needed.
const AuthScreens = lazy(() => import("./identity/sign-in"));
const PendingScreens = lazy(() => import("./identity/pending"));

/** What the session query holds when nobody is signed in. */
const signedOut = { signedOut: true } as const;

export interface AppProps {
  api: ApiClient;
  queryClient?: QueryClient;
  /** The locale to show; by default the one chosen in this browser, or the best match for its languages. */
  locale?: string;
  /** Where the router keeps its location; by default the browser's address bar. */
  history?: RouterHistory;
  /** Called for message formatting problems, such as a missing message. */
  onIntlError?: IntlConfig["onError"];
  /**
   * Who is signed in, what they may do, which modules are on and how the
   * panel looks for them. Read from the panel when left out; null runs
   * without a session, as with a panel that doesn't serve one, where every
   * module counts as on and nothing is hidden for lack of a permission.
   */
  session?: PanelSession | null;
  /** Live data connections; tests give one with fake event sources. */
  live?: LiveClient;
}

/** The web UI: providers, then the shell once the session, modules and messages have loaded. */
export function App({ api, queryClient, locale, history, onIntlError, session, live }: AppProps) {
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
          <GeneralPreferencesProvider>
            <SessionPanel
              api={api}
              given={session}
              locale={shown}
              history={history}
              onIntlError={onIntlError}
              localeChoice={localeChoice}
            />
          </GeneralPreferencesProvider>
        </LiveProvider>
      </QueryClientProvider>
    </ApiProvider>
  );
}

/**
 * Reads the session before the shell, unless it was given. A panel that
 * doesn't serve sessions answers 404, and the UI runs without one. Nobody
 * signed in gets the sign-in screens instead of the shell, and someone who
 * must first set up a second step or choose a password gets those.
 */
function SessionPanel({
  api,
  given,
  locale,
  history,
  onIntlError,
  localeChoice,
}: {
  api: ApiClient;
  given: PanelSession | null | undefined;
  locale: string;
  history: RouterHistory | undefined;
  onIntlError: IntlConfig["onError"] | undefined;
  localeChoice: ComponentProps<typeof LocaleChoiceProvider>["value"];
}) {
  const queryClient = useQueryClient();
  const loaded = useQuery({
    queryKey: ["session"],
    queryFn: async () => {
      try {
        return await getSession(api);
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) {
          return null;
        }
        if (error instanceof ApiError && error.status === 401) {
          return signedOut;
        }
        throw error;
      }
    },
    enabled: given === undefined,
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  const ready = given !== undefined || loaded.isSuccess;
  const current = given !== undefined ? given : (loaded.data ?? null);
  const out = current !== null && "signedOut" in current;
  const session = current === null || "signedOut" in current ? null : current;
  const refresh = useCallback(async () => {
    if (given === undefined) {
      await queryClient.refetchQueries({ queryKey: ["session"], exact: true });
    }
  }, [queryClient, given]);
  const onSignedOut = useCallback(() => void refresh(), [refresh]);
  useSignOutWatch(queryClient, onSignedOut);
  const sessionValue = useMemo(() => ({ session, refresh }), [session, refresh]);
  const states = useMemo(() => (session ? sessionModuleStates(session) : null), [session]);
  const can = useMemo(
    () => (session ? (permission: string) => permits(session.permissions, permission) : undefined),
    [session],
  );
  const waiting = (
    <I18nProvider locale={defaultLocale} messages={shellMessages} onError={onIntlError}>
      <ShellSkeleton />
    </I18nProvider>
  );
  let content: ReactNode;
  if (out) {
    content = (
      <Suspense fallback={waiting}>
        <AuthScreens locale={locale} onSignedIn={() => void refresh()} onIntlError={onIntlError} />
      </Suspense>
    );
  } else if (session?.pending) {
    content = (
      <Suspense fallback={waiting}>
        <PendingScreens
          locale={locale}
          session={session}
          onDone={() => void refresh()}
          onIntlError={onIntlError}
        />
      </Suspense>
    );
  } else {
    content = (
      <Panel
        locale={locale}
        history={history}
        onIntlError={onIntlError}
        moduleStates={states}
        can={can}
      />
    );
  }
  return (
    <SessionTheme api={api} session={session}>
      {ready ? (
        <SessionProvider value={sessionValue}>
          <LocaleChoiceProvider value={localeChoice}>{content}</LocaleChoiceProvider>
        </SessionProvider>
      ) : (
        <I18nProvider locale={defaultLocale} messages={shellMessages} onError={onIntlError}>
          {loaded.isError ? <BootError error={loaded.error} /> : <ShellSkeleton />}
        </I18nProvider>
      )}
    </SessionTheme>
  );
}

/** Tells the person their preferences only apply in this browser for now. */
function PreferencesNotKept({ error }: { error: unknown }) {
  const intl = useIntl();
  return (
    <>{intl.formatMessage(messages.preferencesNotKept, { reason: errorMessage(intl, error) })}</>
  );
}

/** Applies the themes and preferences the session brings. */
function SessionTheme({
  api,
  session,
  children,
}: {
  api: ApiClient;
  session: PanelSession | null;
  children: ReactNode;
}) {
  const theme = session?.theme;
  const server = theme?.server ?? null;
  const reseller = theme?.reseller ?? null;
  const layers = useMemo<ThemeLayers>(() => ({ server, reseller }), [server, reseller]);
  const serverFiles = theme?.files?.server;
  const resellerFiles = theme?.files?.reseller;
  // A layer whose files the session doesn't say where to find has none to
  // show; an empty URL counts as no file.
  const fileURL = useCallback<ThemeFileURL>(
    (layer, path) => {
      const base = layer === "server" ? serverFiles : resellerFiles;
      return base ? themeFileURL(base, path) : "";
    },
    [serverFiles, resellerFiles],
  );
  const signedIn = session !== null;
  const keep = useCallback(
    (preferences: Preferences) => {
      if (!signedIn) {
        return;
      }
      savePreferences(api, preferences).catch((error: unknown) => {
        toast.error(<PreferencesNotKept error={error} />);
      });
    },
    [api, signedIn],
  );
  return (
    <ThemeProvider
      layers={layers}
      fileURL={fileURL}
      {...(theme?.brandName ? { brandName: theme.brandName } : {})}
      {...(theme?.customCSS ? { customCSSURL: theme.customCSS } : {})}
      preferences={session?.preferences ?? null}
      onPreferencesChange={keep}
    >
      {children}
    </ThemeProvider>
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
  const { preferences: general } = useGeneralPreferences();
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
    <I18nProvider
      locale={locale}
      messages={loaded.data.messages}
      onError={onIntlError}
      {...(general.timeZone ? { timeZone: general.timeZone } : {})}
      {...(general.hourCycle ? { hourCycle: general.hourCycle } : {})}
    >
      <TooltipProvider delayDuration={400}>
        <ModulesProvider
          modules={loaded.data.modules}
          states={moduleStates}
          {...(can ? { can } : {})}
        >
          <ShellStateProvider assistant={assistant}>
            <StepUpProvider>
              <RouterProvider router={router} />
            </StepUpProvider>
          </ShellStateProvider>
        </ModulesProvider>
      </TooltipProvider>
    </I18nProvider>
  );
}
