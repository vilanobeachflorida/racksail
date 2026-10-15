import { paletteColors } from "./check";
import { themeSpec } from "./spec";
import type { Font, Palette, ProtectedPalette, Theme } from "./types";
import { paletteKeys } from "./types";

/**
 * Renders themes as CSS custom properties, line for line as internal/theme
 * does, so a server or reseller theme applied in the browser looks exactly
 * like one rendered by the panel. The tests compare both renderers.
 */

/** The cascade layer themes go in; preferences and the protected palette rank above it. */
export const themeLayer = "rs-theme";

type Vars = [string, string][];

/** The custom property of a token: surfaceRaised becomes --rs-surface-raised. */
export function cssVar(token: string): string {
  if (token.includes("-")) {
    return `--rs-${token}`;
  }
  return `--rs-${token.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`)}`;
}

function trim(value: number, decimals: number): string {
  const f = 10 ** decimals;
  return String(Math.round(value * f) / f);
}

function paletteVars(palette: Palette): Vars {
  const vars: Vars = paletteKeys.map((key) => [key, palette[key] ?? ""]);
  (palette.chart ?? []).forEach((c, i) => {
    vars.push([`chart${i + 1}`, c]);
  });
  const colors = paletteColors(palette);
  for (const d of themeSpec.derivations) {
    vars.push([d.name, colors[d.name] ?? ""]);
  }
  return vars;
}

const typeSteps: [string, number][] = [
  ["xs", -2],
  ["sm", -1],
  ["base", 0],
  ["lg", 1],
  ["xl", 2],
  ["2xl", 3],
  ["3xl", 4],
  ["4xl", 5],
];

function typeScale(ratio: number): Vars {
  const vars: Vars = [];
  for (const [name, exponent] of typeSteps) {
    vars.push([`text-${name}`, `${trim(ratio ** exponent, 4)}rem`]);
    vars.push([`leading-${name}`, trim(Math.max(1.2, 1.5 - 0.06 * Math.max(exponent, 0)), 2)]);
  }
  return vars;
}

export const densityVars: Record<"comfortable" | "compact", Vars> = {
  comfortable: [
    ["control-sm", "2rem"],
    ["control-md", "2.25rem"],
    ["control-lg", "2.5rem"],
    ["row", "2.75rem"],
    ["cell-x", "0.75rem"],
    ["cell-y", "0.625rem"],
    ["gap", "1rem"],
    ["card-pad", "1.25rem"],
  ],
  compact: [
    ["control-sm", "1.75rem"],
    ["control-md", "2rem"],
    ["control-lg", "2.25rem"],
    ["row", "2.25rem"],
    ["cell-x", "0.5rem"],
    ["cell-y", "0.375rem"],
    ["gap", "0.75rem"],
    ["card-pad", "1rem"],
  ],
};

function fontStack(family: string | undefined, fallback: string): string {
  return family ? `${JSON.stringify(family)}, ${fallback}` : fallback;
}

function sharedVars(theme: Theme): Vars {
  const t = theme.tokens;
  const typo = t.typography ?? {};
  const vars: Vars = [
    ["fontSans", fontStack(typo.sans?.family, "ui-sans-serif, system-ui, sans-serif")],
    ["fontMono", fontStack(typo.mono?.family, "ui-monospace, monospace")],
    ["fontSizeBase", typo.baseSize ?? ""],
  ];
  const w = typo.weights;
  if (w?.regular) {
    vars.push(
      ["weightRegular", String(w.regular)],
      ["weightMedium", String(w.medium ?? 0)],
      ["weightSemibold", String(w.semibold ?? 0)],
      ["weightBold", String(w.bold ?? 0)],
    );
  }
  if (typo.scale) {
    vars.push(...typeScale(typo.scale));
  }
  vars.push(
    ["radiusSm", t.radius?.sm ?? ""],
    ["radiusMd", t.radius?.md ?? ""],
    ["radiusLg", t.radius?.lg ?? ""],
    ["radiusXl", t.radius?.xl ?? ""],
    ["shadowSm", t.shadows?.sm ?? ""],
    ["shadowMd", t.shadows?.md ?? ""],
    ["shadowLg", t.shadows?.lg ?? ""],
    ["motionFast", t.motion?.fast ?? ""],
    ["motionNormal", t.motion?.normal ?? ""],
    ["motionSlow", t.motion?.slow ?? ""],
    ["focusWidth", t.focusRing?.width ?? ""],
    ["focusOffset", t.focusRing?.offset ?? ""],
  );
  if (t.density && t.density in densityVars) {
    vars.push(...densityVars[t.density]);
  }
  return vars;
}

class Writer {
  private out = "";
  private indent = "";

  line(text = ""): void {
    this.out += text === "" ? "\n" : `${this.indent}${text}\n`;
  }

  open(text: string): void {
    this.line(`${text} {`);
    this.indent += "  ";
  }

  close(): void {
    this.indent = this.indent.slice(2);
    this.line("}");
  }

  block(selector: string, ...groups: Vars[]): void {
    this.open(selector);
    for (const group of groups) {
      for (const [name, value] of group) {
        if (value !== "") {
          this.line(`${cssVar(name)}: ${value};`);
        }
      }
    }
    this.close();
  }

  toString(): string {
    return this.out;
  }
}

function fontFormat(file: string): string {
  return file.toLowerCase().endsWith(".woff") ? "woff" : "woff2";
}

/**
 * The theme alone, inside its cascade layer, as the panel applies a server
 * or reseller theme on top of the default stylesheet. fontURL gives the URL
 * a font file of the theme package is served from, given the file and the
 * font it belongs to.
 */
export function renderLayerCSS(
  theme: Theme,
  fontURL?: (path: string, font: "sans" | "mono") => string,
): string {
  const w = new Writer();
  if (fontURL) {
    const fonts: ["sans" | "mono", Font | undefined][] = [
      ["sans", theme.tokens.typography?.sans],
      ["mono", theme.tokens.typography?.mono],
    ];
    for (const [name, font] of fonts) {
      for (const file of font?.files ?? []) {
        w.open("@font-face");
        w.line(`font-family: ${JSON.stringify(font?.family ?? "")};`);
        w.line(
          `src: url(${JSON.stringify(fontURL(file, name))}) format(${JSON.stringify(fontFormat(file))});`,
        );
        w.line("font-weight: 100 900;");
        w.line("font-display: swap;");
        w.close();
      }
    }
  }
  const light = paletteVars(theme.tokens.light);
  const dark = paletteVars(theme.tokens.dark);
  w.open(`@layer ${themeLayer}`);
  w.block(":root", light, sharedVars(theme));
  w.line();
  w.block(':root[data-theme="dark"]', dark);
  w.line();
  w.open("@media (prefers-color-scheme: dark)");
  w.block(':root:not([data-theme="light"])', dark);
  w.close();
  w.close();
  return w.toString();
}

function protectedVars(p: ProtectedPalette): Vars {
  return [
    ["criticalSurface", p.surface],
    ["criticalText", p.text],
    ["criticalBorder", p.border],
    ["criticalDanger", p.danger],
    ["criticalDangerText", p.dangerText],
    ["criticalWarning", p.warning],
    ["criticalWarningText", p.warningText],
    ["criticalFocus", p.focus],
  ];
}

/**
 * Every token of one mode of a theme on one element, for previews that show
 * a theme inside the page without changing the page itself. The protected
 * palette comes along, so previews show safety-critical areas as they are.
 */
export function renderScopedCSS(
  theme: Theme,
  selector: string,
  mode: "light" | "dark",
  highContrast = false,
): string {
  const w = new Writer();
  const key = highContrast ? (mode === "dark" ? "highContrastDark" : "highContrastLight") : mode;
  w.block(
    selector,
    paletteVars(theme.tokens[mode]),
    sharedVars(theme),
    protectedVars(themeSpec.protected[key]),
  );
  return w.toString();
}
