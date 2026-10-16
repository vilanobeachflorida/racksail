import { act, render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { describe, expect, it, vi } from "vitest";
import { type Preferences, preferencesKey } from "./prefs";
import { ThemeProvider, useTheme } from "./provider";

function Probe() {
  const { preferences, setPreferences } = useTheme();
  return (
    <button type="button" onClick={() => setPreferences({ contrast: "high" })}>
      {preferences.mode} {preferences.contrast}
    </button>
  );
}

describe("the theme provider", () => {
  it("saves and reports each change once, even when React renders twice", () => {
    const onChange = vi.fn();
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    render(
      <StrictMode>
        <ThemeProvider onPreferencesChange={onChange}>
          <Probe />
        </ThemeProvider>
      </StrictMode>,
    );
    act(() => screen.getByRole("button").click());
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ contrast: "high" }));
    expect(setItem.mock.calls.filter(([key]) => key === preferencesKey)).toHaveLength(1);
    expect(document.documentElement).toHaveAttribute("data-contrast", "high");
    setItem.mockRestore();
  });

  it("takes the account's preferences when they arrive, and keeps them in the browser", () => {
    const { rerender } = render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
    expect(screen.getByRole("button")).toHaveTextContent("system system");
    const account: Partial<Preferences> = { mode: "dark", textSize: "large" };
    rerender(
      <ThemeProvider preferences={account}>
        <Probe />
      </ThemeProvider>,
    );
    expect(screen.getByRole("button")).toHaveTextContent("dark system");
    expect(document.documentElement).toHaveAttribute("data-text-size", "large");
    expect(JSON.parse(window.localStorage.getItem(preferencesKey) ?? "{}")).toMatchObject(account);
    // The same preferences again, as a new object, change nothing.
    act(() => screen.getByRole("button").click());
    rerender(
      <ThemeProvider preferences={{ ...account }}>
        <Probe />
      </ThemeProvider>,
    );
    expect(screen.getByRole("button")).toHaveTextContent("dark high");
  });
});
