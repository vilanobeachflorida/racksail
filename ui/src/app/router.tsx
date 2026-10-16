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
  // Your own account, under Settings.
  const accountPage = (
    path: string,
    title: MessageDescriptor,
    page: Parameters<typeof lazyRouteComponent>[0],
  ) =>
    createRoute({
      getParentRoute: () => settings,
      path,
      component: lazyRouteComponent(page),
      staticData: { title },
    });
  const settingsTree = settings.addChildren([
    createRoute({
      getParentRoute: () => settings,
      path: "/",
      component: lazyRouteComponent(() => import("./settings/index")),
    }),
    accountPage("profile", messages.routeProfile, () => import("./identity/account/profile")),
    accountPage("security", messages.routeSecurity, () => import("./identity/account/security")),
    accountPage("sessions", messages.routeSessions, () => import("./identity/account/sessions")),
    accountPage("tokens", messages.routeTokens, () => import("./identity/account/tokens")),
    accountPage("ssh-keys", messages.routeSSHKeys, () => import("./identity/account/ssh-keys")),
    accountPage(
      "sign-in",
      messages.routeSignIn,
      () => import("./identity/people/sign-in-settings"),
    ),
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
  // People and access: users, roles, accounts and the audit log.
  const section = (
    path: string,
    title: MessageDescriptor,
    list: Parameters<typeof lazyRouteComponent>[0],
    item?: {
      param: string;
      title: MessageDescriptor;
      page: Parameters<typeof lazyRouteComponent>[0];
    },
  ) => {
    const base = createRoute({ getParentRoute: () => root, path, staticData: { title } });
    const children: AnyRoute[] = [
      createRoute({ getParentRoute: () => base, path: "/", component: lazyRouteComponent(list) }),
    ];
    if (item) {
      children.push(
        createRoute({
          getParentRoute: () => base,
          path: `$${item.param}`,
          component: lazyRouteComponent(item.page),
          staticData: { title: item.title },
        }),
      );
    }
    return base.addChildren(children);
  };
  const routes: AnyRoute[] = [
    createRoute({
      getParentRoute: () => root,
      path: "/",
      component: Home,
      staticData: { title: messages.homeTitle },
    }),
    settingsTree,
    section("users", messages.navUsers, () => import("./identity/people/users"), {
      param: "userId",
      title: messages.routeUser,
      page: () => import("./identity/people/user"),
    }),
    section("accounts", messages.navAccounts, () => import("./identity/people/accounts"), {
      param: "accountId",
      title: messages.routeAccount,
      page: () => import("./identity/people/account"),
    }),
    section("roles", messages.routeRoles, () => import("./identity/people/roles")),
    section("audit", messages.navAudit, () => import("./identity/people/audit")),
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
