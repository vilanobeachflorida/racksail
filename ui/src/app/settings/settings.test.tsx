import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { builtinTheme, themeSpec } from "@/design/theme/spec";
import { themeToJSON } from "@/design/theme/validate";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, json, renderShell } from "@/test/render";

afterEach(() => {
  vi.restoreAllMocks();
});

const api = fakeApi({
  "GET /api/v1/jobs": () => json({ jobs: [] }),
  "GET /api/v1/events": () => json({ events: [] }),
});

async function editor() {
  const result = renderShell({
    modules: [],
    messages: shellMessages,
    api,
    path: "/settings/appearance/theme",
  });
  await screen.findByRole("heading", { level: 1, name: "Theme editor" });
  return result;
}

const layerStyle = () => document.head.querySelector("style[data-rs-theme-layer]");

describe("the theme editor", () => {
  it("checks contrast as colors change", async () => {
    const user = userEvent.setup();
    await editor();
    expect(
      screen.getByText("Every check passes, so the panel will accept this theme."),
    ).toBeInTheDocument();
    const checker = screen.getByRole("region", { name: "Contrast checker" });
    expect(within(checker).getByText(/^All \d+ checks pass WCAG 2.2 AA.$/)).toBeInTheDocument();

    const text = screen.getByRole("textbox", { name: "Text" });
    await user.clear(text);
    expect(text).toHaveAccessibleDescription("Use #RRGGBB.");
    await user.type(text, "#FFFFFF");
    expect(text).not.toHaveAccessibleDescription("Use #RRGGBB.");
    expect(
      await screen.findByText(/problems to fix before the theme can be used$/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /^Light mode: Text on Page surface has a contrast of [\d.]+:1 and needs 4\.5:1\./,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export theme.yaml" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Export JSON" })).toBeDisabled();

    expect(within(checker).getByText(/checks fail WCAG 2.2 AA.$/)).toBeInTheDocument();
    await user.click(within(checker).getByRole("switch", { name: "Only failing checks" }));
    const rows = within(checker).getAllByRole("row").slice(1);
    expect(rows.length).toBeGreaterThan(0);
    for (const row of rows) {
      expect(row).toHaveTextContent("Fails");
    }

    await user.click(screen.getByRole("button", { name: "Start over" }));
    expect(
      screen.getByText("Every check passes, so the panel will accept this theme."),
    ).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Text" })).not.toHaveValue("#FFFFFF");
  });

  it("starts from a built-in theme", async () => {
    const user = userEvent.setup();
    await editor();
    await user.click(screen.getByRole("combobox", { name: "Start from" }));
    await user.click(await screen.findByRole("option", { name: "High contrast" }));
    await user.click(screen.getByRole("tab", { name: "Details" }));
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("High contrast");
    expect(screen.getByRole("textbox", { name: /^ID/ })).toHaveValue("high-contrast");
  });

  it("imports theme files and explains what's wrong with them", async () => {
    const user = userEvent.setup();
    await editor();
    const input = screen.getByLabelText("Import", { selector: "input" });
    await user.upload(input, new File(["tokens: [unclosed"], "broken.yaml"));
    expect(
      await screen.findByText(
        "The file isn't valid YAML or JSON. Check it in an editor and import it again.",
      ),
    ).toBeInTheDocument();

    await user.upload(
      input,
      new File(["name: Loud\ntokens:\n  light:\n    criticalDanger: '#FF0000'\n"], "loud.yaml"),
    );
    expect(
      await screen.findByText(/^Themes can't set protected colors \(criticalDanger\)\./),
    ).toBeInTheDocument();

    await user.upload(input, new File(["id: Not An ID\n"], "partial.yaml"));
    expect(await screen.findByText(/^id: /)).toBeInTheDocument();

    await user.upload(input, new File([themeToJSON(builtinTheme("compact"))], "compact.json"));
    expect(await screen.findByText("Imported Compact")).toBeInTheDocument();
    expect(screen.queryByText("The file couldn't be imported")).not.toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: "Details" }));
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("Compact");
  });

  it("exports the theme as theme.yaml or JSON", async () => {
    const user = userEvent.setup();
    const files: Blob[] = [];
    vi.spyOn(URL, "createObjectURL").mockImplementation((blob) => {
      files.push(blob as Blob);
      return "blob:theme";
    });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    await editor();

    await user.click(screen.getByRole("button", { name: "Export JSON" }));
    expect((click.mock.contexts[0] as HTMLAnchorElement).download).toBe("racksail.json");
    expect(JSON.parse(await (files[0] as Blob).text())).toMatchObject({
      id: "racksail",
      name: "Racksail",
    });

    await user.click(screen.getByRole("button", { name: "Export theme.yaml" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(2));
    expect((click.mock.contexts[1] as HTMLAnchorElement).download).toBe("racksail.yaml");
    expect(await (files[1] as Blob).text()).toMatch(/^id: racksail$/m);
  });

  it("tries the theme on the whole panel until stopped or left", async () => {
    const user = userEvent.setup();
    const { router } = await editor();
    expect(layerStyle()).toBeNull();
    await user.click(screen.getByRole("button", { name: "Try on the whole panel" }));
    await waitFor(() => expect(layerStyle()).not.toBeNull());
    expect(layerStyle()?.textContent).toContain("@layer rs-theme");
    await user.click(screen.getByRole("button", { name: "Stop trying" }));
    await waitFor(() => expect(layerStyle()).toBeNull());

    await user.click(screen.getByRole("button", { name: "Try on the whole panel" }));
    await waitFor(() => expect(layerStyle()).not.toBeNull());
    await router.navigate({ to: "/settings" });
    await waitFor(() => expect(layerStyle()).toBeNull());
  });

  it("won't try a theme with problems, and stops trying one that gets them", async () => {
    const user = userEvent.setup();
    await editor();
    const tryButton = screen.getByRole("button", { name: "Try on the whole panel" });
    await user.click(tryButton);
    await waitFor(() => expect(layerStyle()).not.toBeNull());

    // A broken shadow or length would go straight into the page's CSS.
    const text = screen.getByRole("textbox", { name: "Text" });
    await user.clear(text);
    await user.type(text, "#FFFFFF");
    await waitFor(() => expect(layerStyle()).toBeNull());
    expect(
      screen.getByText(/^Stopped trying the theme on the whole panel: it has problems/),
    ).toBeInTheDocument();
    const blocked = screen.getByRole("button", { name: "Try on the whole panel" });
    expect(blocked).toBeDisabled();
    expect(blocked).toHaveAccessibleDescription(/^Stopped trying the theme/);

    await user.click(screen.getByRole("button", { name: "Start over" }));
    expect(screen.getByRole("button", { name: "Try on the whole panel" })).toBeEnabled();
  });

  it("refuses translucent colors except the overlay", async () => {
    const user = userEvent.setup();
    await editor();
    const overlay = screen.getByRole("textbox", { name: "Overlay" });
    await user.clear(overlay);
    await user.type(overlay, "#01214F80");
    expect(overlay).not.toHaveAccessibleDescription(/Use/);
    const text = screen.getByRole("textbox", { name: "Text" });
    await user.clear(text);
    await user.type(text, "#0B1B3300");
    expect(text).toHaveAccessibleDescription("Use #RRGGBB.");
    expect(
      screen.getByText("Every check passes, so the panel will accept this theme."),
    ).toBeInTheDocument();
  });

  it("edits and checks the console palette", async () => {
    const user = userEvent.setup();
    await editor();
    // The editor's tabs come first; the contrast checker has a Console tab too.
    await user.click(screen.getAllByRole("tab", { name: "Console" })[0] as HTMLElement);
    const preview = screen.getByRole("region", { name: "Console preview" });
    expect(preview).toHaveTextContent("Last night's backup failed");
    const background = screen.getByRole("textbox", { name: "Background" });
    expect(background).toHaveValue("#01214F");
    await user.clear(background);
    await user.type(background, "#FFFFFF");
    expect(
      await screen.findByText(
        /^Console: Text on Background has a contrast of [\d.]+:1 and needs 4\.5:1\./,
      ),
    ).toBeInTheDocument();
    const checker = screen.getByRole("region", { name: "Contrast checker" });
    await user.click(within(checker).getByRole("tab", { name: "Console" }));
    expect(within(checker).getByText("errors in the console")).toBeInTheDocument();

    // Emptying a color brings back Racksail's.
    await user.clear(background);
    expect(
      await screen.findByText("Every check passes, so the panel will accept this theme."),
    ).toBeInTheDocument();
  });

  it("asks for the files of a font the panel doesn't have", async () => {
    const user = userEvent.setup();
    await editor();
    await user.click(screen.getByRole("tab", { name: "Type" }));
    const family = screen.getByRole("textbox", { name: "Text font" });
    await user.clear(family);
    await user.type(family, "Acme Sans");
    expect(
      await screen.findByText(/^The panel doesn't have the font Acme Sans for text\./),
    ).toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "Text font files" }), "fonts/acme.woff2");
    expect(
      await screen.findByText("Every check passes, so the panel will accept this theme."),
    ).toBeInTheDocument();
  });

  it("names every checked pair and status hue in the user's language", () => {
    for (const pair of [...themeSpec.pairs, ...themeSpec.consolePairs]) {
      expect(shellMessages).toHaveProperty([
        `app.theme.purpose.${pair.foreground}.${pair.background}`,
      ]);
    }
    for (const meaning of themeSpec.meanings) {
      expect(shellMessages).toHaveProperty([`app.theme.hue.${meaning.token}`]);
    }
  });

  it("has no problems axe can find", async () => {
    const user = userEvent.setup();
    await editor();
    expect(await axeViolations(document, { page: true })).toEqual([]);
    for (const tab of ["Type", "Shape and motion", "Console", "Details"]) {
      await user.click(screen.getAllByRole("tab", { name: tab })[0] as HTMLElement);
      expect(await axeViolations(document, { page: true })).toEqual([]);
    }
  });
});
