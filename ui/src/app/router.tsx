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
    /** The page title, shown in the browser tab. */
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
  const routes: AnyRoute[] = [
    createRoute({
      getParentRoute: () => root,
      path: "/",
      component: Home,
      staticData: { title: messages.homeTitle },
    }),
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
    for (const page of module.settings ?? []) {
      routes.push(
        createRoute({
          getParentRoute: () => root,
          path: `settings/${module.id}/${page.id}`,
          component: lazyRouteComponent(page.page),
          staticData: { title: page.title },
        }),
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
