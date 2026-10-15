import { contrast, hasAlpha, hue, isColor, mix } from "./color";
import { defaultTheme, themeSpec } from "./spec";
import type { Pair, Palette, ProblemPlace, Theme, ThemeProblem } from "./types";
import { consoleKeys, paletteKeys } from "./types";

/** Contrast minimums from WCAG 2.2 AA. */
export const textContrast = 4.5;
export const nonTextContrast = 3;

/**
 * Every color token of a palette by name: the ones themes set, chart1 to
 * chartN and the derived ones, as internal/theme computes them.
 */
export function paletteColors(palette: Palette): Record<string, string> {
  const colors: Record<string, string> = {};
  for (const key of paletteKeys) {
    const value = palette[key];
    if (typeof value === "string") {
      colors[key] = value;
    }
  }
  (palette.chart ?? []).forEach((c, i) => {
    colors[`chart${i + 1}`] = c;
  });
  for (const d of themeSpec.derivations) {
    const from = colors[d.from];
    const over = colors[d.over];
    const mixed = from && over ? mix(from, over, d.share) : null;
    if (mixed) {
      colors[d.name] = mixed;
    }
  }
  return colors;
}

/**
 * The console palette terminals get from a theme: its own colors, and the
 * default theme's where it sets none.
 */
export function consoleColors(theme: Theme): Record<string, string> {
  const colors: Record<string, string> = {};
  for (const key of consoleKeys) {
    const own = theme.console?.[key];
    const fallback = defaultTheme.console?.[key];
    const value = own ? own : fallback;
    if (value) {
      colors[key] = value;
    }
  }
  return colors;
}

/** The pairs checked for a palette: the fixed ones and one per chart color. */
export function pairsFor(palette: Palette): Pair[] {
  return [
    ...themeSpec.pairs,
    ...(palette.chart ?? []).map((_, i) => ({
      foreground: `chart${i + 1}`,
      background: "surfaceRaised",
      minimum: nonTextContrast,
      purpose: `chart series ${i + 1}`,
    })),
  ];
}

const round2 = (v: number) => Math.round(v * 100) / 100;

function checkColors(
  mode: Exclude<ProblemPlace, "typography">,
  colors: Record<string, string>,
  pairs: Pair[],
): ThemeProblem[] {
  const problems: ThemeProblem[] = [];
  for (const name of Object.keys(colors).sort()) {
    const value = colors[name];
    if (value === undefined || value === "") {
      continue;
    }
    if (!isColor(value)) {
      problems.push({ mode, kind: "color", foreground: name, purpose: value });
    } else if (
      hasAlpha(value) &&
      (mode === "console" || !themeSpec.translucentTokens.includes(name))
    ) {
      problems.push({ mode, kind: "alpha", foreground: name });
    }
  }
  for (const pair of pairs) {
    const fg = colors[pair.foreground];
    const bg = colors[pair.background];
    // A translucent color is reported above; it has no contrast of its own.
    if ((fg !== undefined && hasAlpha(fg)) || (bg !== undefined && hasAlpha(bg))) {
      continue;
    }
    const ratio = fg && bg ? contrast(fg, bg) : null;
    if (ratio === null || ratio + 1e-9 < pair.minimum) {
      problems.push({
        mode,
        kind: "contrast",
        foreground: pair.foreground,
        background: pair.background,
        ratio: ratio === null ? 0 : round2(ratio),
        minimum: pair.minimum,
        purpose: pair.purpose,
      });
    }
  }
  for (const meaning of themeSpec.meanings) {
    const value = colors[meaning.token];
    const h = value ? hue(value) : null;
    if (!h) {
      continue;
    }
    const ok =
      h.saturation >= 0.25 && meaning.hues.some(([from, to]) => h.hue >= from && h.hue <= to);
    if (!ok) {
      problems.push({ mode, kind: "meaning", foreground: meaning.token, purpose: meaning.name });
    }
  }
  return problems;
}

/** Whether the panel serves a font family itself. */
export function isBundledFont(family: string): boolean {
  const wanted = family.toLowerCase();
  return themeSpec.bundledFonts.some((f) => f.toLowerCase() === wanted);
}

function checkFonts(theme: Theme): ThemeProblem[] {
  const problems: ThemeProblem[] = [];
  for (const name of ["sans", "mono"] as const) {
    const font = theme.tokens.typography?.[name];
    if (!font?.family || (font.files?.length ?? 0) > 0 || isBundledFont(font.family)) {
      continue;
    }
    problems.push({ mode: "typography", kind: "font", foreground: name, purpose: font.family });
  }
  return problems;
}

/**
 * Everything wrong with a theme's tokens: contrasts below WCAG AA in both
 * palettes and the console palette, colors that aren't colors or are
 * translucent where they can't be, status colors that lost their meaning
 * and fonts that come without their files. The server refuses a theme with
 * any of these, and reports them in the same order.
 */
export function checkTheme(theme: Theme): ThemeProblem[] {
  return [
    ...checkColors("light", paletteColors(theme.tokens.light), pairsFor(theme.tokens.light)),
    ...checkColors("dark", paletteColors(theme.tokens.dark), pairsFor(theme.tokens.dark)),
    ...checkColors("console", consoleColors(theme), themeSpec.consolePairs),
    ...checkFonts(theme),
  ];
}

/** One row of the contrast checker. */
export interface ContrastRow extends Pair {
  mode: "light" | "dark" | "console";
  ratio: number | null;
  pass: boolean;
}

/** Every checked pair of both palettes and the console palette, with its ratio. */
export function contrastTable(theme: Theme): ContrastRow[] {
  const rows: ContrastRow[] = [];
  const add = (mode: ContrastRow["mode"], colors: Record<string, string>, pairs: Pair[]) => {
    for (const pair of pairs) {
      const fg = colors[pair.foreground];
      const bg = colors[pair.background];
      const ratio = fg && bg ? contrast(fg, bg) : null;
      rows.push({
        ...pair,
        mode,
        ratio: ratio === null ? null : round2(ratio),
        pass: ratio !== null && ratio + 1e-9 >= pair.minimum,
      });
    }
  };
  for (const mode of ["light", "dark"] as const) {
    const palette = theme.tokens[mode];
    add(mode, paletteColors(palette), pairsFor(palette));
  }
  add("console", consoleColors(theme), themeSpec.consolePairs);
  return rows;
}
