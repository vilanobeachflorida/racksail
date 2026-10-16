import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, json, renderShell } from "@/test/render";
import { webhookKind } from "@/test/resources";

function panel() {
  const asked: URLSearchParams[] = [];
  const api = fakeApi({
    "GET /api/v1/kinds": () => json({ kinds: [webhookKind] }),
    "GET /api/v1/jobs": () => json({ jobs: [] }),
    "GET /api/v1/search": (request) => {
      const query = new URL(request.url).searchParams;
      asked.push(query);
      if (query.get("q") === "nothing") {
        return json({ results: [] });
      }
      return json({
        results: [
          {
            id: "whk_01jq0000000000000000000001",
            kind: "core.webhook",
            scope: { level: "server" },
            title: "https://hooks.example.com/racksail",
            subtitle: "Ops alerts",
            phase: "ready",
            updatedAt: "2026-10-14T09:30:00Z",
          },
        ],
        more: ["core.webhook"],
      });
    },
  });
  return { api, asked };
}

describe("the search screen", () => {
  it("finds resources of every kind, grouped by kind, with the text in the address", async () => {
    const user = userEvent.setup();
    const { api, asked } = panel();
    const { router } = renderShell({ modules: [], messages: shellMessages, path: "/search", api });
    expect(
      await screen.findByText(
        "Type at least 2 characters to search every kind of resource you can see.",
      ),
    ).toBeInTheDocument();
    await user.type(screen.getByRole("searchbox", { name: "Search resources" }), "hooks");
    await waitFor(() => expect(router.state.location.search).toEqual({ q: "hooks" }));
    const group = await screen.findByRole("region", { name: "Webhooks" });
    expect(
      within(group).getByRole("link", { name: "https://hooks.example.com/racksail" }),
    ).toHaveAttribute("href", "/core/webhooks/whk_01jq0000000000000000000001");
    expect(within(group).getByText("Ops alerts")).toBeInTheDocument();
    expect(within(group).getByRole("link", { name: "Show all webhooks" })).toHaveAttribute(
      "href",
      "/core/webhooks",
    );
    expect(asked.at(-1)?.get("limit")).toBe("20");
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("says when nothing matches", async () => {
    const { api } = panel();
    renderShell({ modules: [], messages: shellMessages, path: "/search?q=nothing", api });
    expect(await screen.findByText("Nothing matches “nothing”")).toBeInTheDocument();
  });
});
