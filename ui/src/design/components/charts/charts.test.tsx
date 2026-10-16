import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/render";
import { Sparkline } from "./sparkline";
import { TimeSeries } from "./time-series";
import { readChartColors, withAlpha } from "./uplot";

const start = Date.UTC(2026, 9, 15, 12, 0);
const times = [0, 1, 2, 3].map((i) => start + i * 60_000);

describe("chart colors", () => {
  it("come from the theme's tokens", () => {
    const el = document.createElement("div");
    el.style.setProperty("--rs-chart1", "#1F6FEB");
    el.style.setProperty("--rs-chart2", "#D97706");
    el.style.setProperty("--rs-text", "#0F172A");
    el.style.setProperty("--rs-danger", "#B91C1C");
    document.body.append(el);
    const colors = readChartColors(el);
    expect(colors.series).toEqual(["#1F6FEB", "#D97706"]);
    expect(colors.text).toBe("#0F172A");
    expect(colors.danger).toBe("#B91C1C");
    el.remove();
  });

  it("fall back to the accent without chart tokens", () => {
    const el = document.createElement("div");
    el.style.setProperty("--rs-accent", "#007CFD");
    document.body.append(el);
    expect(readChartColors(el).series).toEqual(["#007CFD"]);
    el.remove();
  });

  it("take an alpha for fills", () => {
    expect(withAlpha("#007CFD", 0.12)).toBe("#007CFD1f");
    expect(withAlpha("#007CFD", 2)).toBe("#007CFDff");
    expect(withAlpha("currentColor", 0.5)).toBe("currentColor");
  });
});

describe("the time series chart", () => {
  it("names itself and its series, and has the same data as a table", async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <TimeSeries
        title="Requests per minute"
        times={times}
        series={[
          { label: "example.com", values: [120, 140, null, 90] },
          { label: "shop.example.org", values: [30, 35, 40, 38] },
        ]}
        format={(v) => `${v} req`}
      />,
    );
    const figure = screen.getByRole("figure", { name: "Requests per minute" });
    const legend = within(figure).getByRole("list", { name: "Series" });
    expect(
      within(legend)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual(["example.com", "shop.example.org"]);
    await waitFor(() => expect(figure.querySelector(".uplot")).not.toBeNull());

    await user.click(within(figure).getByRole("button", { name: "Show the data as a table" }));
    const table = within(figure).getByRole("table", { name: "Requests per minute" });
    const rows = within(table).getAllByRole("row");
    expect(rows).toHaveLength(5);
    expect(
      within(rows[0] as HTMLElement)
        .getAllByRole("columnheader")
        .map((h) => h.textContent),
    ).toEqual(["Time", "example.com", "shop.example.org"]);
    expect(
      within(rows[3] as HTMLElement)
        .getAllByRole("cell")
        .slice(1)
        .map((c) => c.textContent),
    ).toEqual(["No data", "40 req"]);
  });

  it("leaves the legend out for a single series, which the title names", () => {
    renderWithProviders(
      <TimeSeries title="CPU" times={times} series={[{ label: "CPU", values: [1, 2, 3, 4] }]} />,
    );
    expect(screen.queryByRole("list", { name: "Series" })).not.toBeInTheDocument();
  });

  it("moves an event's flag when new data moves the time axis", async () => {
    // jsdom lays nothing out, so the plotting area gets the width uPlot
    // gives it.
    const width = vi
      .spyOn(HTMLElement.prototype, "clientWidth", "get")
      .mockImplementation(function (this: HTMLElement) {
        return this.classList.contains("u-over") ? Number.parseFloat(this.style.width) || 0 : 0;
      });
    // The same events as data arrives, as a live chart keeps them.
    const events = [{ time: times[2] as number, label: "Deployed" }];
    const series = (values: number[]) => [{ label: "Load", values }];
    const { rerender } = renderWithProviders(
      <TimeSeries title="Load" times={times} series={series([1, 2, 3, 4])} events={events} />,
    );
    const flag = () => screen.getByRole("button", { name: /Deployed$/ });
    await waitFor(() => expect(flag().style.left).not.toBe(""));
    const before = Number.parseFloat(flag().style.left);
    // A minute later the chart starts a minute later, so the event sits
    // further left, with nothing else happening on the page.
    const later = times.map((t) => t + 60_000);
    rerender(
      <TimeSeries title="Load" times={later} series={series([2, 3, 4, 5])} events={events} />,
    );
    await waitFor(() => expect(Number.parseFloat(flag().style.left)).toBeLessThan(before));
    width.mockRestore();
  });

  it("keeps each series' color when others are filtered out", async () => {
    const root = document.documentElement;
    const palette = ["#007CFD", "#E2622C", "#139A6B"];
    palette.forEach((c, i) => {
      root.style.setProperty(`--rs-chart${i + 1}`, c);
    });
    const swatch = (label: string) =>
      (
        within(screen.getByRole("list", { name: "Series" })).getByText(label)
          .firstElementChild as HTMLElement
      ).style.backgroundColor;
    const all = [
      { label: "web1", values: [1, 2, 3, 4], color: 0 },
      { label: "web2", values: [2, 3, 4, 5], color: 1 },
      { label: "web3", values: [3, 4, 5, 6], color: 2 },
    ];
    const { rerender } = renderWithProviders(
      <TimeSeries title="Load" times={times} series={all} />,
    );
    await waitFor(() => expect(swatch("web3")).toBe("rgb(19, 154, 107)"));
    rerender(
      <TimeSeries
        title="Load"
        times={times}
        series={[all[0], all[2]].filter((s) => s !== undefined)}
      />,
    );
    expect(swatch("web3")).toBe("rgb(19, 154, 107)");
    palette.forEach((_, i) => {
      root.style.removeProperty(`--rs-chart${i + 1}`);
    });
  });
});

describe("the sparkline", () => {
  it("is an image described by its label", async () => {
    renderWithProviders(<Sparkline label="CPU, 23% now" values={[10, 30, null, 23]} />);
    const img = screen.getByRole("img", { name: "CPU, 23% now" });
    await waitFor(() => expect(img.querySelector(".uplot")).not.toBeNull());
  });
});
