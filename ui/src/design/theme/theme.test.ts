import { describe, expect, it } from "vitest";
import { checkTheme, consoleColors, contrastTable, isBundledFont, paletteColors } from "./check";
import { contrast, hue, isColor, luminance, mix, parseColor } from "./color";
import { cssDuration, cssLength, cssShadow, cssVar, renderLayerCSS, renderScopedCSS } from "./css";
import { fontLayer, layerAssetURLs } from "./files";
import { overlay, resolveLayers } from "./layers";
import {
  applyPreferences,
  defaultPreferences,
  loadPreferences,
  normalizePreferences,
  preferenceAttributes,
  preferencesKey,
  savePreferences,
} from "./prefs";
import { builtinTheme, builtinThemes, defaultTheme, themeSpec } from "./spec";
import type { Theme } from "./types";
import {
  findProtectedKeys,
  readThemeDocument,
  readThemeFile,
  themeToJSON,
  themeToYAML,
} from "./validate";

function memoryStorage(): Storage {
  const data = new Map<string, string>();
  return {
    get length() {
      return data.size;
    },
    clear: () => data.clear(),
    getItem: (k) => data.get(k) ?? null,
    key: (i) => [...data.keys()][i] ?? null,
    removeItem: (k) => void data.delete(k),
    setItem: (k, v) => void data.set(k, v),
  };
}

describe("color math", () => {
  it("matches internal/theme", () => {
    expect(contrast("#000000", "#FFFFFF")).toBeCloseTo(21, 5);
    expect(contrast("#777777", "#FFFFFF")).toBeCloseTo(4.48, 2);
    expect(contrast("#007CFD", "#FFFFFF")).toBeCloseTo(3.96, 2);
    expect(mix("#0062CC", "#FFFFFF", 0.12)).toBe("#E0ECF9");
    expect(mix("#000000", "#FFFFFF", 0.5)).toBe("#808080");
    expect(hue("#CF222E")?.hue).toBeCloseTo(355.8, 1);
    expect(hue("#9A6700")?.hue).toBeCloseTo(40.1, 1);
    expect(hue("#777777")?.saturation).toBe(0);
    expect(luminance("#FFFFFF")).toBe(1);
    expect(parseColor("#01214F66")).toEqual({ r: 1, g: 33, b: 79, a: 0.4 });
  });

  it("only accepts hex colors", () => {
    for (const bad of ["blue", "#12345", "#GGGGGG", "007CFD", ""]) {
      expect(isColor(bad)).toBe(false);
      expect(contrast(bad, "#FFFFFF")).toBeNull();
    }
  });
});

describe("theme checks", () => {
  it("pass for every built-in theme", () => {
    for (const theme of builtinThemes()) {
      expect(checkTheme(theme), theme.id).toEqual([]);
    }
  });

  it("derive the same subtle colors as the server", () => {
    const colors = paletteColors(defaultTheme.tokens.light);
    expect(colors.accentSubtle).toBe("#E0ECF9");
    expect(colors.dangerSubtle).toBe("#F9E4E6");
    expect(colors.chart8).toBe("#E34948");
  });

  it("report low contrast, broken colors and lost meanings", () => {
    const theme = builtinTheme("racksail");
    theme.tokens.light.textMuted = "#C0C8D0";
    theme.tokens.dark.danger = "#3FB950";
    theme.tokens.light.surface = "nope";
    const problems = checkTheme(theme);
    expect(problems).toContainEqual(
      expect.objectContaining({
        mode: "light",
        kind: "contrast",
        foreground: "textMuted",
        background: "surfaceRaised",
        minimum: 4.5,
      }),
    );
    expect(problems).toContainEqual({
      mode: "dark",
      kind: "meaning",
      foreground: "danger",
      purpose: "red",
    });
    expect(problems).toContainEqual(
      expect.objectContaining({ mode: "light", kind: "color", foreground: "surface" }),
    );
  });

  it("list every pair with its ratio for the contrast checker", () => {
    const rows = contrastTable(defaultTheme);
    const pairs = themeSpec.pairs.length + defaultTheme.tokens.light.chart.length;
    expect(rows).toHaveLength(pairs * 2 + themeSpec.consolePairs.length);
    expect(rows.filter((r) => r.mode === "console").map((r) => r.foreground)).toEqual([
      "foreground",
      "accent",
      "success",
      "warning",
      "danger",
    ]);
    expect(rows.every((r) => r.pass)).toBe(true);
    const text = rows.find(
      (r) => r.mode === "light" && r.foreground === "text" && r.background === "surface",
    );
    expect(text?.ratio).toBeGreaterThan(15);
  });
});

describe("translucent colors", () => {
  it("have no contrast of their own", () => {
    expect(contrast("#0B1B3300", "#FFFFFF")).toBeNull();
    expect(contrast("#000000", "#FFFFFF80")).toBeNull();
    expect(contrast("#007CFDFF", "#FFFFFF")).toBeCloseTo(3.96, 2);
  });

  it("are refused everywhere but the overlay", () => {
    const theme = builtinTheme("racksail");
    theme.tokens.light.text = "#0B1B3300";
    theme.tokens.dark.danger = "#F8514980";
    theme.tokens.light.overlay = "#01214FCC";
    const problems = checkTheme(theme);
    expect(problems).toContainEqual({ mode: "light", kind: "alpha", foreground: "text" });
    expect(problems).toContainEqual({ mode: "dark", kind: "alpha", foreground: "danger" });
    expect(problems.some((p) => p.foreground === "overlay")).toBe(false);
    // Contrast isn't measured for them; the alpha is the problem.
    expect(problems.some((p) => p.kind === "contrast" && p.foreground === "text")).toBe(false);
    expect(
      contrastTable(theme).find(
        (r) => r.mode === "light" && r.foreground === "text" && r.background === "surface",
      ),
    ).toMatchObject({ ratio: null, pass: false });
    expect(readThemeDocument(theme).issues).toContainEqual(
      expect.objectContaining({ path: "tokens.light.text", keyword: "pattern" }),
    );
  });
});

describe("the console palette", () => {
  it("is checked like the panel's, with the default's colors where a theme sets none", () => {
    const theme = builtinTheme("racksail");
    theme.console = { background: "#FFFFFF" };
    expect(consoleColors(theme).foreground).toBe(defaultTheme.console?.foreground);
    const problems = checkTheme(theme);
    expect(problems.every((p) => p.mode === "console")).toBe(true);
    expect(problems.filter((p) => p.kind === "contrast").length).toBeGreaterThanOrEqual(4);

    theme.console = {
      background: "#FFFFFF",
      foreground: "#0B1B33",
      accent: "#0049B8",
      success: "#0A6B26",
      warning: "#7A4F00",
      danger: "#0A6B26",
    };
    expect(checkTheme(theme)).toEqual([
      { mode: "console", kind: "meaning", foreground: "danger", purpose: "red" },
    ]);
    theme.console.foreground = "#0B1B3380";
    expect(checkTheme(theme)).toContainEqual({
      mode: "console",
      kind: "alpha",
      foreground: "foreground",
    });
  });
});

describe("fonts", () => {
  it("come with their files unless the panel bundles them", () => {
    const theme = builtinTheme("racksail");
    theme.tokens.typography = { ...theme.tokens.typography, sans: { family: "Acme Sans" } };
    expect(checkTheme(theme)).toEqual([
      { mode: "typography", kind: "font", foreground: "sans", purpose: "Acme Sans" },
    ]);
    theme.tokens.typography.sans = { family: "Acme Sans", files: ["fonts/acme.woff2"] };
    theme.tokens.typography.mono = { family: "jetbrains mono variable" };
    expect(checkTheme(theme)).toEqual([]);
    expect(isBundledFont("Inter Variable")).toBe(true);
    expect(isBundledFont("Inter")).toBe(false);
  });
});

describe("problem order", () => {
  it("is the server's: sorted colors, then pairs and meanings, palette by palette", () => {
    const theme = builtinTheme("racksail");
    theme.tokens.light.surface = "nope";
    theme.tokens.light.brand = "blue";
    theme.tokens.dark.focus = "#0F2036";
    theme.console = { accent: "#01214F" };
    theme.tokens.typography = { ...theme.tokens.typography, mono: { family: "Acme Mono" } };
    const problems = checkTheme(theme);
    expect(problems.slice(0, 2)).toEqual([
      { mode: "light", kind: "color", foreground: "brand", purpose: "blue" },
      { mode: "light", kind: "color", foreground: "surface", purpose: "nope" },
    ]);
    const places = problems.map((p) => p.mode).filter((m, i, all) => all[i - 1] !== m);
    expect(places).toEqual(["light", "dark", "console", "typography"]);
    expect(checkTheme(theme)).toEqual(problems);
  });
});

describe("theme CSS", () => {
  it("is what internal/theme renders, line for line", () => {
    for (const theme of builtinThemes()) {
      expect(renderLayerCSS(theme), theme.id).toBe(themeSpec.layerCss[theme.id]);
    }
  });

  it("names custom properties like the server", () => {
    expect(cssVar("surfaceRaised")).toBe("--rs-surface-raised");
    expect(cssVar("chart1")).toBe("--rs-chart1");
    expect(cssVar("text-2xl")).toBe("--rs-text-2xl");
  });

  it("loads bundled fonts from the panel", () => {
    const theme = builtinTheme("racksail");
    theme.tokens.typography = {
      ...theme.tokens.typography,
      sans: { family: "Acme Sans", files: ["fonts/acme.woff2"] },
    };
    const css = renderLayerCSS(theme, (p) => `/themes/acme/${p}`);
    expect(css).toContain('font-family: "Acme Sans";');
    expect(css).toContain('src: url("/themes/acme/fonts/acme.woff2") format("woff2");');
  });

  it("clears chart colors a layer doesn't have", () => {
    const theme = builtinTheme("racksail");
    theme.tokens.light.chart = theme.tokens.light.chart.slice(0, 6);
    const css = renderLayerCSS(theme);
    const light = css.slice(0, css.indexOf('[data-theme="dark"]'));
    expect(light).toContain("--rs-chart6: #008300;");
    expect(light).toContain("--rs-chart7: initial;");
    expect(light).toContain("--rs-chart12: initial;");
    const scoped = renderScopedCSS(theme, ".preview", "light");
    expect(scoped).toContain("--rs-chart7: initial;");
  });

  it("leaves out shadows that would leave a parenthesis open", () => {
    const theme = builtinTheme("racksail");
    theme.tokens.shadows = {
      sm: "0 1px 2px rgba(0, 0, 0, 0.1",
      lg: "0 12px 32px rgba(1, 33, 79, 0.14), inset 0 0 0 1px #FFFFFF",
    };
    const css = renderLayerCSS(theme);
    expect(css).not.toContain("--rs-shadow-sm");
    expect(css).toContain(
      "--rs-shadow-lg: 0 12px 32px rgba(1, 33, 79, 0.14), inset 0 0 0 1px #FFFFFF;",
    );
    expect(css.split("(").length).toBe(css.split(")").length);
    expect(cssShadow("none")).toBe("none");
    expect(cssShadow("0 1px url(x)")).toBe("");
    expect(readThemeDocument(theme).issues).toContainEqual(
      expect.objectContaining({ path: "tokens.shadows.sm", keyword: "pattern" }),
    );
  });

  it("leaves out lengths and durations that would close the rule early", () => {
    const theme = builtinTheme("racksail");
    theme.tokens.radius = { ...theme.tokens.radius, sm: "4px}main{display:none}x{" };
    theme.tokens.typography = { ...theme.tokens.typography, baseSize: "1rem;color:red" };
    theme.tokens.motion = { ...theme.tokens.motion, fast: "120ms}" };
    theme.tokens.focusRing = { width: "3px" };
    for (const css of [renderLayerCSS(theme), renderScopedCSS(theme, ".preview", "light")]) {
      for (const gone of ["--rs-radius-sm", "--rs-font-size-base", "--rs-motion-fast", "main{"]) {
        expect(css).not.toContain(gone);
      }
      expect(css).toContain("--rs-radius-md: 6px;");
      expect(css).toContain("--rs-motion-slow: 320ms;");
      expect(css).toContain("--rs-focus-width: 3px;");
      expect(css.split("{").length).toBe(css.split("}").length);
    }
    expect(cssLength("0.5rem")).toBe("0.5rem");
    expect(cssLength("var(--x)")).toBe("");
    expect(cssDuration("200ms")).toBe("200ms");
    expect(cssDuration("0.2s")).toBe("");
    expect(readThemeDocument(theme).issues).toContainEqual(
      expect.objectContaining({ path: "tokens.radius.sm", keyword: "pattern" }),
    );
  });

  it("leaves out weights a theme doesn't set, as internal/theme does", () => {
    const theme = builtinTheme("racksail");
    theme.tokens.typography = { ...theme.tokens.typography, weights: { medium: 450, bold: 800 } };
    const css = renderLayerCSS(theme);
    expect(css).toContain("--rs-weight-medium: 450;");
    expect(css).toContain("--rs-weight-bold: 800;");
    expect(css).not.toContain("--rs-weight-regular");
    expect(css).not.toContain("--rs-weight-semibold");
  });

  it("scopes a preview to one element and one mode", () => {
    const css = renderScopedCSS(defaultTheme, ".preview", "dark", true);
    expect(css.startsWith(".preview {\n")).toBe(true);
    expect(css).toContain("--rs-surface: #0A1626;");
    expect(css).toContain("--rs-critical-danger: #FF7A7A;");
    expect(css).not.toContain("@media");
  });
});

describe("theme layers", () => {
  it("override only what a layer sets", () => {
    const merged = overlay(defaultTheme, {
      name: "Reseller",
      tokens: { light: { brand: "#5B21B6" } } as unknown as Theme["tokens"],
    });
    expect(merged.name).toBe("Reseller");
    expect(merged.tokens.light.brand).toBe("#5B21B6");
    expect(merged.tokens.light.text).toBe(defaultTheme.tokens.light.text);
    expect(merged.tokens.light.chart).toHaveLength(8);
    expect(defaultTheme.tokens.light.brand).toBe("#01214F");
  });

  it("stack the server and reseller themes over the default", () => {
    const server = builtinTheme("compact");
    const reseller = builtinTheme("racksail");
    reseller.name = "Acme";
    reseller.tokens.density = undefined;
    reseller.tokens.light.accent = "#5B21B6";
    const theme = resolveLayers({ server, reseller });
    expect(theme.name).toBe("Acme");
    expect(theme.tokens.density).toBe("compact");
    expect(theme.tokens.light.accent).toBe("#5B21B6");
    expect(resolveLayers({})).toEqual(defaultTheme);
  });

  it("keep a font's files and the brand files with the layer that set them", () => {
    const server = builtinTheme("racksail");
    server.tokens.typography = {
      ...server.tokens.typography,
      sans: { family: "Acme Sans", files: ["fonts/acme.woff2"] },
      mono: { family: "Acme Mono", files: ["fonts/acme-mono.woff2"] },
    };
    server.assets = {
      logoLight: "server-logo.svg",
      logoDark: "server-logo-dark.svg",
      favicon: "server.ico",
      loginBackground: "sea.jpg",
    };
    const reseller = builtinTheme("racksail");
    reseller.tokens.typography = {
      ...reseller.tokens.typography,
      sans: { family: "Inter Variable" },
      mono: undefined,
    };
    reseller.assets = { logoLight: "reseller-logo.svg" };
    const theme = resolveLayers({ server, reseller });
    expect(theme.tokens.typography?.sans).toEqual({ family: "Inter Variable" });
    expect(theme.tokens.typography?.mono).toEqual({
      family: "Acme Mono",
      files: ["fonts/acme-mono.woff2"],
    });
    expect(theme.assets).toEqual({ logoLight: "reseller-logo.svg", loginBackground: "sea.jpg" });

    expect(fontLayer({ server, reseller }, "sans")).toBe("reseller");
    expect(fontLayer({ server }, "mono")).toBe("server");
    expect(fontLayer({}, "sans")).toBeNull();
    const url = (layer: string, path: string) => `/api/v1/themes/${layer}/files/${path}`;
    expect(layerAssetURLs({ server, reseller }, url)).toEqual({
      logoLight: "/api/v1/themes/reseller/files/reseller-logo.svg",
      loginBackground: "/api/v1/themes/server/files/sea.jpg",
    });
    expect(layerAssetURLs({ server }, url)).toMatchObject({
      logoDark: "/api/v1/themes/server/files/server-logo-dark.svg",
      favicon: "/api/v1/themes/server/files/server.ico",
    });
    expect(layerAssetURLs({}, url)).toEqual({});
  });
});

describe("preferences", () => {
  it("fill in defaults and drop unknown values", () => {
    expect(normalizePreferences(null)).toEqual(defaultPreferences);
    expect(
      normalizePreferences({ mode: "dark", contrast: "sepia", textSize: "large", extra: 1 }),
    ).toEqual({
      ...defaultPreferences,
      mode: "dark",
      textSize: "large",
    });
  });

  it("become attributes on the root element", () => {
    const prefs = {
      ...defaultPreferences,
      mode: "dark",
      density: "compact",
      motion: "reduce",
    } as const;
    expect(preferenceAttributes(prefs)).toEqual({
      "data-theme": "dark",
      "data-density": "compact",
      "data-motion": "reduce",
    });
    const root = document.createElement("html");
    root.setAttribute("data-contrast", "high");
    applyPreferences(prefs, root);
    expect(root.getAttribute("data-theme")).toBe("dark");
    expect(root.hasAttribute("data-contrast")).toBe(false);
    applyPreferences(defaultPreferences, root);
    expect(root.getAttributeNames()).toEqual([]);
  });

  it("are kept in the browser", () => {
    const storage = memoryStorage();
    expect(loadPreferences(storage)).toEqual(defaultPreferences);
    savePreferences({ ...defaultPreferences, contrast: "high" }, storage);
    expect(loadPreferences(storage).contrast).toBe("high");
    storage.setItem(preferencesKey, "{not json");
    expect(loadPreferences(storage)).toEqual(defaultPreferences);
    expect(loadPreferences(null)).toEqual(defaultPreferences);
  });
});

describe("reading theme files", () => {
  it("round-trips the built-in themes through YAML and JSON", async () => {
    for (const theme of builtinThemes()) {
      const fromYAML = await readThemeFile(await themeToYAML(theme));
      expect(fromYAML.theme).toEqual(theme);
      expect(fromYAML.problems).toEqual([]);
      const fromJSON = await readThemeFile(themeToJSON(theme));
      expect(fromJSON.theme).toEqual(theme);
    }
  });

  it("refuses protected tokens, schema mismatches and broken files", async () => {
    const theme = builtinTheme("racksail") as unknown as Record<string, unknown>;
    const withProtected = structuredClone(theme);
    (withProtected.tokens as { light: Record<string, string> }).light.criticalDanger = "#00FF00";
    const reading = readThemeDocument(withProtected);
    expect(reading.error).toBe("protected");
    expect(reading.protectedKeys).toEqual(["tokens.light.criticalDanger"]);
    expect(
      findProtectedKeys({
        criticalSurface: "#000000",
        tokens: { critical: {}, dark: { CriticalFocus: "#00FF00" } },
        console: { criticalText: "#FFFFFF" },
      }),
    ).toEqual([
      "console.criticalText",
      "criticalSurface",
      "tokens.critical",
      "tokens.dark.CriticalFocus",
    ]);

    // Names people choose are only names: an illustration may be called anything.
    const illustrated = structuredClone(theme);
    illustrated.assets = { illustrations: { criticalError: "art/error.svg" } };
    expect(readThemeDocument(illustrated)).toMatchObject({ error: null, issues: [], problems: [] });

    const remote = structuredClone(theme);
    remote.assets = { logoLight: "https://example.com/logo.png" };
    expect(readThemeDocument(remote).issues).toContainEqual(
      expect.objectContaining({ path: "assets.logoLight", keyword: "pattern" }),
    );
    expect(readThemeDocument({ format: 1 }).issues.map((i) => i.path)).toEqual(
      expect.arrayContaining(["id", "name", "tokens"]),
    );
    expect((await readThemeFile("format: [1")).error).toBe("unreadable");
    expect((await readThemeFile("a: &x 1\nb: *x\n")).error).toBe("unreadable");
    expect((await readThemeFile("x".repeat(256 * 1024 + 1))).error).toBe("too-large");
  });
});
