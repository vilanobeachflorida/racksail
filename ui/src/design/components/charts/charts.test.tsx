import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
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
    const table = within(figure).getByRole("table");
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
    ).toEqual(["–", "40 req"]);
  });

  it("leaves the legend out for a single series, which the title names", () => {
    renderWithProviders(
      <TimeSeries title="CPU" times={times} series={[{ label: "CPU", values: [1, 2, 3, 4] }]} />,
    );
    expect(screen.queryByRole("list", { name: "Series" })).not.toBeInTheDocument();
  });
});

describe("the sparkline", () => {
  it("is an image described by its label", async () => {
    renderWithProviders(<Sparkline label="CPU, 23% now" values={[10, 30, null, 23]} />);
    const img = screen.getByRole("img", { name: "CPU, 23% now" });
    await waitFor(() => expect(img.querySelector(".uplot")).not.toBeNull());
  });
});
