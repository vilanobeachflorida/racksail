import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/render";
import { Button } from "../ui/button";
import { DataTable } from "./data-table";
import {
  type Column,
  emptyState,
  filterRows,
  loadViews,
  nextSort,
  normalizeState,
  saveViews,
  sortRows,
  viewsKey,
} from "./model";

interface Site {
  id: string;
  domain: string;
  state: "running" | "sleeping" | "failed";
  requests: number | null;
  created: Date;
}

const sites: Site[] = [
  {
    id: "s1",
    domain: "example.com",
    state: "running",
    requests: 1200,
    created: new Date("2026-03-01"),
  },
  {
    id: "s2",
    domain: "shop10.example.org",
    state: "failed",
    requests: 30,
    created: new Date("2026-01-15"),
  },
  {
    id: "s3",
    domain: "shop2.example.org",
    state: "sleeping",
    requests: null,
    created: new Date("2026-05-20"),
  },
  {
    id: "s4",
    domain: "Blog.example.net",
    state: "running",
    requests: 450,
    created: new Date("2025-11-02"),
  },
];

const columns: Column<Site>[] = [
  { id: "domain", header: "Domain", value: (s) => s.domain, sortable: true, filter: "text" },
  {
    id: "state",
    header: "State",
    value: (s) => s.state,
    sortable: true,
    filter: {
      options: [
        { value: "running", label: "Running" },
        { value: "failed", label: "Failed" },
      ],
    },
  },
  { id: "requests", header: "Requests", value: (s) => s.requests, sortable: true, align: "end" },
  {
    id: "created",
    header: "Created",
    value: (s) => s.created,
    sortable: true,
    cell: (s) => s.created.toISOString().slice(0, 10),
  },
];

describe("the table model", () => {
  it("searches every column and applies column filters", () => {
    expect(
      filterRows(sites, columns, { ...emptyState, search: "SHOP" }, "en").map((s) => s.id),
    ).toEqual(["s2", "s3"]);
    expect(
      filterRows(sites, columns, { ...emptyState, filters: { state: "running" } }, "en").map(
        (s) => s.id,
      ),
    ).toEqual(["s1", "s4"]);
    expect(
      filterRows(
        sites,
        columns,
        { ...emptyState, search: "example", filters: { domain: "blog" } },
        "en",
      ).map((s) => s.id),
    ).toEqual(["s4"]);
  });

  it("sorts text naturally, numbers and dates by value, and empty values last", () => {
    const by = (column: string, direction: "asc" | "desc") =>
      sortRows(sites, columns, { column, direction }, "en").map((s) => s.id);
    expect(by("domain", "asc")).toEqual(["s4", "s1", "s3", "s2"]);
    expect(by("requests", "asc")).toEqual(["s2", "s4", "s1", "s3"]);
    expect(by("requests", "desc")).toEqual(["s1", "s4", "s2", "s3"]);
    expect(by("created", "desc")).toEqual(["s3", "s1", "s2", "s4"]);
    expect(nextSort(null, "domain")).toEqual({ column: "domain", direction: "asc" });
    expect(nextSort({ column: "domain", direction: "asc" }, "domain")).toEqual({
      column: "domain",
      direction: "desc",
    });
    expect(nextSort({ column: "domain", direction: "desc" }, "domain")).toBeNull();
  });

  it("keeps saved views that still fit the table", () => {
    const store = window.localStorage;
    saveViews(
      "sites",
      [{ id: "v1", name: "Failing", state: { ...emptyState, filters: { state: "failed" } } }],
      store,
    );
    expect(loadViews("sites", ["domain", "state"], store)).toEqual([
      {
        id: "v1",
        name: "Failing",
        state: { sort: null, search: "", filters: { state: "failed" }, hidden: [] },
      },
    ]);
    store.setItem(
      viewsKey("sites"),
      '[{"id":"v2","name":"Old","state":{"sort":{"column":"gone","direction":"asc"},"hidden":["gone"]}}]',
    );
    expect(loadViews("sites", ["domain"], store)[0]?.state).toEqual(emptyState);
    store.setItem(viewsKey("sites"), "not json");
    expect(loadViews("sites", ["domain"], store)).toEqual([]);
    expect(
      normalizeState({ sort: { column: "domain", direction: "sideways" } }, ["domain"]).sort,
    ).toBeNull();
  });
});

function renderTable(props: Partial<Parameters<typeof DataTable<Site>>[0]> = {}) {
  return renderWithProviders(
    <DataTable
      id="sites"
      label="Sites"
      rows={sites}
      rowKey={(s) => s.id}
      columns={columns}
      {...props}
    />,
  );
}

const domains = () =>
  screen
    .getAllByRole("row")
    .slice(1)
    .map((r) => within(r).getAllByRole("cell").at(-4)?.textContent);

describe("the data table", () => {
  it("renders an ARIA table with every row in view", () => {
    renderTable();
    const table = screen.getByRole("table", { name: "Sites" });
    expect(table).toHaveAttribute("aria-rowcount", "5");
    expect(screen.getAllByRole("row")).toHaveLength(5);
    expect(screen.getByText("4 rows")).toBeInTheDocument();
    expect(within(table).getByText("2026-03-01")).toBeInTheDocument();
  });

  it("sorts when a heading is pressed and says how", async () => {
    renderTable();
    await userEvent.click(screen.getByRole("button", { name: /^Domain/ }));
    expect(screen.getByRole("columnheader", { name: /Domain/ })).toHaveAttribute(
      "aria-sort",
      "ascending",
    );
    expect(domains()).toEqual([
      "Blog.example.net",
      "example.com",
      "shop2.example.org",
      "shop10.example.org",
    ]);
    await userEvent.click(screen.getByRole("button", { name: /^Domain/ }));
    expect(screen.getByRole("columnheader", { name: /Domain/ })).toHaveAttribute(
      "aria-sort",
      "descending",
    );
  });

  it("filters by search and by column, and can clear them", async () => {
    renderTable();
    await userEvent.type(screen.getByRole("searchbox", { name: "Filter rows" }), "zzz");
    expect(screen.getByText("Nothing matches the search and filters.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.getByText("4 rows")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await userEvent.type(await screen.findByRole("textbox", { name: "Domain" }), "shop");
    await waitFor(() => expect(screen.getByText("2 rows")).toBeInTheDocument());
  });

  it("selects rows for bulk actions", async () => {
    const restart = vi.fn();
    renderTable({
      bulkActions: (selected, clear) => (
        <Button
          size="sm"
          onClick={() => {
            restart(selected.map((s) => s.id));
            clear();
          }}
        >
          Restart
        </Button>
      ),
    });
    await userEvent.click(screen.getByRole("checkbox", { name: "Select example.com" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Blog.example.net" }));
    expect(screen.getByRole("checkbox", { name: "Select all rows" })).toHaveAttribute(
      "aria-checked",
      "mixed",
    );
    const toolbar = screen.getByRole("toolbar", { name: "Actions for the selected rows" });
    expect(toolbar).toHaveTextContent("2 selected");
    await userEvent.click(within(toolbar).getByRole("button", { name: "Restart" }));
    expect(restart).toHaveBeenCalledWith(["s1", "s4"]);
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    expect(screen.getByRole("toolbar")).toHaveTextContent("4 selected");
  });

  it("saves the current view and brings it back", async () => {
    renderTable();
    await userEvent.type(screen.getByRole("searchbox", { name: "Filter rows" }), "shop");
    await userEvent.click(screen.getByRole("button", { name: "Views" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Save this view" }));
    await userEvent.type(await screen.findByRole("textbox", { name: "View name" }), "Shops");
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("button", { name: "Shops" })).toBeInTheDocument();
    expect(
      loadViews(
        "sites",
        columns.map((c) => c.id),
        window.localStorage,
      )[0]?.state.search,
    ).toBe("shop");
    await userEvent.clear(screen.getByRole("searchbox", { name: "Filter rows" }));
    expect(screen.getByText("4 rows")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Views" }));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: "Shops" }));
    expect(screen.getByText("2 rows")).toBeInTheDocument();
  });

  it("hides columns and shows a placeholder while loading or empty", async () => {
    const { unmount } = renderTable();
    await userEvent.click(screen.getByRole("button", { name: "Columns" }));
    await userEvent.click(await screen.findByRole("menuitemcheckbox", { name: "Requests" }));
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("columnheader", { name: /Requests/ })).not.toBeInTheDocument();
    unmount();
    renderWithProviders(
      <DataTable
        id="t"
        label="Sites"
        rows={[]}
        rowKey={(s: Site) => s.id}
        columns={columns}
        empty={<p>No sites yet</p>}
      />,
    );
    expect(screen.getByText("No sites yet")).toBeInTheDocument();
  });

  it("renders only the rows in view of a long list, and follows the scroll", async () => {
    const many: Site[] = Array.from({ length: 5000 }, (_, i) => ({
      id: `s${i}`,
      domain: `site${String(i).padStart(4, "0")}.example.com`,
      state: i % 3 === 0 ? "failed" : "running",
      requests: i,
      created: new Date(Date.UTC(2026, 0, 1 + (i % 300))),
    }));
    renderTable({ rows: many });
    const table = screen.getByRole("table", { name: "Sites" });
    expect(table).toHaveAttribute("aria-rowcount", "5001");
    expect(screen.getByText("5,000 rows")).toBeInTheDocument();
    const indexes = () =>
      screen
        .getAllByRole("row")
        .slice(1)
        .map((r) => Number(r.getAttribute("aria-rowindex")));
    const first = indexes();
    // A screenful and a few more, never all of them.
    expect(first.length).toBeGreaterThan(5);
    expect(first.length).toBeLessThan(60);
    expect(first[0]).toBe(2);
    expect(within(table).getByText("site0000.example.com")).toBeInTheDocument();
    expect(within(table).queryByText("site4999.example.com")).not.toBeInTheDocument();

    // Scrolling to the end shows the last rows, numbered for screen readers.
    act(() => {
      table.scrollTop = 44 * many.length;
      table.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() =>
      expect(within(table).getByText("site4999.example.com")).toBeInTheDocument(),
    );
    const last = indexes();
    expect(last.at(-1)).toBe(5001);
    expect(last.length).toBeLessThan(60);
    expect(within(table).queryByText("site0000.example.com")).not.toBeInTheDocument();

    // Sorting and filtering work on every row, not only the rendered ones.
    await userEvent.type(screen.getByRole("searchbox", { name: "Filter rows" }), "site4998");
    await waitFor(() => expect(screen.getByText("1 row")).toBeInTheDocument());
    expect(within(table).getByText("site4998.example.com")).toBeInTheDocument();
  });
});
