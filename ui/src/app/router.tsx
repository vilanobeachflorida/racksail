import type { QueryClient } from "@tanstack/react-query";
import {
  type AnyRoute,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  lazyRouteComponent,
  type RouterHistory,
} from "@tanstack/react-router";
import type { MessageDescriptor } from "react-intl";
import type { ModuleUI } from "@/modules/types";
import { NotFound, RouteError } from "./errors";
import { Home } from "./home";
import { messages } from "./messages";
import { Shell } from "./shell";

declare module "@tanstack/react-router" {
  interface StaticDataRouteOption {
    /** The page title, shown in the browser tab and the breadcrumb. */
    title?: MessageDescriptor;
  }
}

export interface RouterContext {
  queryClient: QueryClient;
}

/**
 * Builds the router from the shell's own routes and the routes of the given
 * modules. Each module's screens live under /<module id>, and its settings
 * pages under /settings/<module id>/<page id>. Screens load on first visit.
 */
export function createAppRouter(
  modules: readonly ModuleUI[],
  queryClient: QueryClient,
  history?: RouterHistory,
) {
  const root = createRootRouteWithContext<RouterContext>()({
    component: Shell,
    notFoundComponent: NotFound,
    errorComponent: RouteError,
  });
  const settings = createRoute({
    getParentRoute: () => root,
    path: "settings",
    staticData: { title: messages.settingsTitle },
  });
  const appearance = createRoute({
    getParentRoute: () => settings,
    path: "appearance",
    staticData: { title: messages.appearanceTitle },
  });
  const settingsTree = settings.addChildren([
    createRoute({
      getParentRoute: () => settings,
      path: "/",
      component: lazyRouteComponent(() => import("./settings/index")),
    }),
    appearance.addChildren([
      createRoute({
        getParentRoute: () => appearance,
        path: "/",
        component: lazyRouteComponent(() => import("./settings/appearance")),
      }),
      createRoute({
        getParentRoute: () => appearance,
        path: "theme",
        component: lazyRouteComponent(() => import("./settings/theme-editor")),
        staticData: { title: messages.themeEditorTitle },
      }),
    ]),
    ...modules.flatMap((module) =>
      (module.settings ?? []).map((page) =>
        createRoute({
          getParentRoute: () => settings,
          path: `${module.id}/${page.id}`,
          component: lazyRouteComponent(page.page),
          staticData: { title: page.title },
        }),
      ),
    ),
  ]);
  const routes: AnyRoute[] = [
    createRoute({
      getParentRoute: () => root,
      path: "/",
      component: Home,
      staticData: { title: messages.homeTitle },
    }),
    settingsTree,
  ];
  for (const module of modules) {
    if (module.routes?.length) {
      const base = createRoute({
        getParentRoute: () => root,
        path: module.id,
        staticData: { title: module.name },
      });
      routes.push(
        base.addChildren(
          module.routes.map((route) =>
            createRoute({
              getParentRoute: () => base,
              path: route.path,
              component: lazyRouteComponent(route.screen),
              staticData: { title: route.title },
            }),
          ),
        ),
      );
    }
  }
  return createRouter({
    routeTree: root.addChildren(routes),
    context: { queryClient },
    defaultPreload: "intent",
    scrollRestoration: true,
    ...(history ? { history } : {}),
  });
}
