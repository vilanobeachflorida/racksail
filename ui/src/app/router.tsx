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
import { ScreenOutlet, screen } from "./screens/catalog";
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
 *
 * Every kind of resource has generic screens at /<module>/<plural> until its
 * module has screens of its own: a module's routes are static, so they take
 * the paths they name, and the generic ones answer the rest.
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
      component: screen(() => import("./settings/index")),
    }),
    createRoute({
      getParentRoute: () => settings,
      path: "general",
      component: screen(() => import("./settings/general")),
      staticData: { title: messages.generalTitle },
    }),
    appearance.addChildren([
      createRoute({
        getParentRoute: () => appearance,
        path: "/",
        component: screen(() => import("./settings/appearance")),
      }),
      createRoute({
        getParentRoute: () => appearance,
        path: "theme",
        component: lazyRouteComponent(() => import("./settings/theme-editor")),
        staticData: { title: messages.themeEditorTitle },
      }),
    ]),
    createRoute({
      getParentRoute: () => settings,
      path: "modules",
      component: lazyRouteComponent(() => import("./settings/modules")),
      staticData: { title: messages.modulesTitle },
    }),
    createRoute({
      getParentRoute: () => settings,
      path: "rules",
      component: lazyRouteComponent(() => import("./settings/rules")),
      staticData: { title: messages.rulesTitle },
    }),
    createRoute({
      getParentRoute: () => settings,
      path: "notifications",
      component: lazyRouteComponent(() => import("./settings/notifications")),
      staticData: { title: messages.notifyTitle },
    }),
    createRoute({
      getParentRoute: () => settings,
      path: "updates",
      component: lazyRouteComponent(() => import("./settings/updates")),
      staticData: { title: messages.updatesTitle },
    }),
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

  // The panel's own screens: jobs, events, changes, search and resources.
  const jobs = createRoute({
    getParentRoute: () => root,
    path: "jobs",
    component: ScreenOutlet,
    staticData: { title: messages.jobsTitle },
  });
  const changes = createRoute({
    getParentRoute: () => root,
    path: "changes",
    component: ScreenOutlet,
    staticData: { title: messages.changesTitle },
  });
  const panelScreens = [
    jobs.addChildren([
      createRoute({
        getParentRoute: () => jobs,
        path: "/",
        component: lazyRouteComponent(() => import("./screens/jobs/list")),
      }),
      createRoute({
        getParentRoute: () => jobs,
        path: "$jobId",
        component: lazyRouteComponent(() => import("./screens/jobs/detail")),
      }),
    ]),
    changes.addChildren([
      createRoute({
        getParentRoute: () => changes,
        path: "/",
        component: lazyRouteComponent(() => import("./screens/changes/list")),
      }),
      createRoute({
        getParentRoute: () => changes,
        path: "$changeSetId",
        component: lazyRouteComponent(() => import("./screens/changes/detail")),
      }),
    ]),
    createRoute({
      getParentRoute: () => root,
      path: "events",
      component: screen(() => import("./screens/events/screen")),
      staticData: { title: messages.eventsTitle },
    }),
    createRoute({
      getParentRoute: () => root,
      path: "search",
      component: screen(() => import("./screens/search/page")),
      staticData: { title: messages.searchTitle },
    }),
    createRoute({
      getParentRoute: () => root,
      path: "resources",
      component: screen(() => import("./screens/resources/index")),
      staticData: { title: messages.resourcesTitle },
    }),
  ];

  // The generic screens of every kind: /<module>/<plural>[/new | /<id>[/edit]].
  const moduleRoute = createRoute({
    getParentRoute: () => root,
    path: "$module",
    component: screen(() => import("./screens/resources/layout")),
  });
  const kindRoute = createRoute({
    getParentRoute: () => moduleRoute,
    path: "$plural",
    component: lazyRouteComponent(() => import("./screens/resources/layout"), "KindLayout"),
  });
  const resourceRoute = createRoute({
    getParentRoute: () => kindRoute,
    path: "$id",
    component: lazyRouteComponent(() => import("./screens/resources/layout"), "ResourceLayout"),
  });
  const generic = moduleRoute.addChildren([
    createRoute({
      getParentRoute: () => moduleRoute,
      path: "/",
      component: lazyRouteComponent(() => import("./screens/resources/module")),
    }),
    kindRoute.addChildren([
      createRoute({
        getParentRoute: () => kindRoute,
        path: "/",
        component: lazyRouteComponent(() => import("./screens/resources/list")),
      }),
      createRoute({
        getParentRoute: () => kindRoute,
        path: "new",
        component: lazyRouteComponent(() => import("./screens/resources/form")),
        staticData: { title: messages.resourcesNew },
      }),
      resourceRoute.addChildren([
        createRoute({
          getParentRoute: () => resourceRoute,
          path: "/",
          component: lazyRouteComponent(() => import("./screens/resources/detail")),
        }),
        createRoute({
          getParentRoute: () => resourceRoute,
          path: "edit",
          component: lazyRouteComponent(() => import("./screens/resources/form"), "EditResource"),
          staticData: { title: messages.resourcesEdit },
        }),
      ]),
    ]),
  ]);

  const routes: AnyRoute[] = [
    createRoute({
      getParentRoute: () => root,
      path: "/",
      component: Home,
      staticData: { title: messages.homeTitle },
    }),
    settingsTree,
    ...panelScreens,
    generic,
  ];
  for (const module of modules) {
    if (module.routes?.length) {
      const base = createRoute({
        getParentRoute: () => root,
        path: module.id,
        staticData: { title: module.name },
      });
      const children: AnyRoute[] = module.routes.map((route) =>
        createRoute({
          getParentRoute: () => base,
          path: route.path,
          component: lazyRouteComponent(route.screen),
          staticData: { title: route.title },
        }),
      );
      // Without a first screen of its own, the module's page lists its kinds.
      if (!module.routes.some((route) => route.path === "/" || route.path === "")) {
        children.push(
          createRoute({
            getParentRoute: () => base,
            path: "/",
            component: screen(() => import("./screens/resources/module")),
          }),
        );
      }
      routes.push(base.addChildren(children));
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
