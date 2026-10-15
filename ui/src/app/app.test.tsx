import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Globe } from "lucide-react";
import type { IntlConfig } from "react-intl";
import { describe, expect, it } from "vitest";
import { type ApiClient, createApiClient } from "@/lib/api/client";
import { ApiProvider } from "@/lib/api/context";
import { I18nProvider, shellMessages } from "@/lib/i18n";
import type { ModuleUI } from "@/modules/types";
import { App } from "./app";
import { ModulesProvider } from "./modules";
import { createAppRouter } from "./router";

const failOnIntlError: IntlConfig["onError"] = (error) => {
  throw error;
};

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const version = {
  version: "1.2.3",
  channel: "stable",
  goVersion: "go1.27.0",
  os: "linux",
  arch: "arm64",
};

function fakeApi(health: () => Response | Promise<Response>): ApiClient {
  return createApiClient({
    baseUrl: "http://panel.test",
    fetch: async (request: Request) => {
      switch (new URL(request.url).pathname) {
        case "/api/v1/health":
          return health();
        case "/api/v1/version":
          return json(version);
        default:
          return new Response("not found", { status: 404 });
      }
    },
  });
}

const queryClient = () => new QueryClient({ defaultOptions: { queries: { retry: false } } });

function renderApp(api: ApiClient, path = "/") {
  return render(
    <App
      api={api}
      locale="en"
      queryClient={queryClient()}
      history={createMemoryHistory({ initialEntries: [path] })}
      onIntlError={failOnIntlError}
    />,
  );
}

describe("the shell", () => {
  it("shows the mark, the name and the panel's status", async () => {
    renderApp(fakeApi(() => json({ status: "ok", checks: [{ name: "agent", status: "ok" }] })));
    expect(await screen.findByRole("link", { name: "Racksail" })).toHaveAttribute("href", "/");
    expect(screen.getByRole("navigation", { name: "Main" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();
    expect(await screen.findByText("Everything is working.")).toBeInTheDocument();
    expect(screen.getByText("agent: working")).toBeInTheDocument();
    expect(await screen.findByText("Racksail 1.2.3, stable channel")).toBeInTheDocument();
    expect(screen.getByText("Built with go1.27.0 for linux/arm64")).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("Home · Racksail"));
    expect(document.documentElement.lang).toBe("en");
    expect(document.documentElement.dir).toBe("ltr");
  });

  it("lists failing health checks", async () => {
    renderApp(
      fakeApi(() =>
        json(
          {
            status: "degraded",
            checks: [{ name: "agent", status: "failing", message: "the socket is gone" }],
          },
          503,
        ),
      ),
    );
    expect(await screen.findByText("Something needs attention.")).toBeInTheDocument();
    expect(screen.getByText("agent: the socket is gone")).toBeInTheDocument();
  });

  it("says so when the panel doesn't answer", async () => {
    renderApp(
      fakeApi(() => {
        throw new TypeError("Failed to fetch");
      }),
    );
    expect(await screen.findByText(/The panel isn't answering/)).toBeInTheDocument();
  });

  it("has a page for addresses that lead nowhere", async () => {
    renderApp(
      fakeApi(() => json({ status: "ok", checks: [] })),
      "/nowhere",
    );
    expect(
      await screen.findByRole("heading", { name: "This page doesn't exist" }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "Go to the home screen" }));
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();
  });

  it("lets keyboard users skip to the content", async () => {
    renderApp(fakeApi(() => json({ status: "ok", checks: [] })));
    await screen.findByRole("heading", { level: 1, name: "Home" });
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Skip to content" })).toHaveFocus();
    expect(screen.getByRole("main")).toHaveAttribute("id", "content");
  });
});

describe("module contributions", () => {
  const web: ModuleUI = {
    id: "web",
    name: { id: "web.name" },
    description: { id: "web.description" },
    icon: Globe,
    nav: [{ id: "sites", label: { id: "web.sites" }, icon: Globe, to: "/sites" }],
    routes: [
      {
        path: "/sites",
        title: { id: "web.sites" },
        screen: async () => ({ default: () => <h1>All sites</h1> }),
      },
    ],
    homeCards: [
      {
        id: "summary",
        title: { id: "web.summary" },
        card: async () => ({ default: () => <p>Three sites are running.</p> }),
      },
    ],
    settings: [
      {
        id: "php",
        title: { id: "web.php" },
        page: async () => ({ default: () => <h1>PHP defaults</h1> }),
      },
    ],
  };
  const messages = {
    ...shellMessages,
    "web.name": "Web hosting",
    "web.description": "Sites.",
    "web.sites": "Sites",
    "web.summary": "Sites at a glance",
    "web.php": "PHP",
  };

  function renderWith(path: string) {
    const client = queryClient();
    const router = createAppRouter([web], client, createMemoryHistory({ initialEntries: [path] }));
    render(
      <ApiProvider client={fakeApi(() => json({ status: "ok", checks: [] }))}>
        <QueryClientProvider client={client}>
          <I18nProvider locale="en" messages={messages} onError={failOnIntlError}>
            <ModulesProvider modules={[web]}>
              <RouterProvider router={router} />
            </ModulesProvider>
          </I18nProvider>
        </QueryClientProvider>
      </ApiProvider>,
    );
    return router;
  }

  it("puts navigation entries in the rail and cards on the home screen", async () => {
    renderWith("/");
    const nav = await screen.findByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "Sites" })).toHaveAttribute("href", "/web/sites");
    expect(await screen.findByText("Three sites are running.")).toBeInTheDocument();
  });

  it("mounts screens under the module's path", async () => {
    renderWith("/");
    await userEvent.click(await screen.findByRole("link", { name: "Sites" }));
    expect(await screen.findByRole("heading", { name: "All sites" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sites" })).toHaveAttribute("aria-current", "page");
    await waitFor(() => expect(document.title).toBe("Sites · Racksail"));
  });

  it("mounts settings pages under /settings", async () => {
    renderWith("/settings/web/php");
    expect(await screen.findByRole("heading", { name: "PHP defaults" })).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("PHP · Racksail"));
  });
});
