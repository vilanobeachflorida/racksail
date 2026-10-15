/**
 * The theme tokens, as the CSS custom properties theme.css defines. Classes
 * cover almost everything; these are for code that needs a value, such as
 * charts drawn on a canvas.
 */
export const colorTokens = [
  "surface",
  "surface-raised",
  "surface-sunken",
  "text",
  "text-muted",
  "border",
  "border-strong",
  "brand",
  "brand-text",
  "accent",
  "accent-text",
  "success",
  "success-text",
  "warning",
  "warning-text",
  "danger",
  "danger-text",
  "info",
  "info-text",
  "focus",
  "overlay",
  "chart1",
  "chart2",
  "chart3",
  "chart4",
  "chart5",
  "chart6",
  "chart7",
  "chart8",
  "accent-subtle",
  "success-subtle",
  "warning-subtle",
  "danger-subtle",
  "info-subtle",
] as const;

/** The protected palette of safety-critical areas, which themes can't change. */
export const protectedTokens = [
  "critical-surface",
  "critical-text",
  "critical-border",
  "critical-danger",
  "critical-danger-text",
  "critical-warning",
  "critical-warning-text",
  "critical-focus",
] as const;

export const typeSteps = ["xs", "sm", "base", "lg", "xl", "2xl", "3xl", "4xl"] as const;

export const sharedTokens = [
  "font-sans",
  "font-mono",
  "font-size-base",
  "weight-regular",
  "weight-medium",
  "weight-semibold",
  "weight-bold",
  ...typeSteps.flatMap((step) => [`text-${step}`, `leading-${step}`] as const),
  "radius-sm",
  "radius-md",
  "radius-lg",
  "radius-xl",
  "shadow-sm",
  "shadow-md",
  "shadow-lg",
  "motion-fast",
  "motion-normal",
  "motion-slow",
  "focus-width",
  "focus-offset",
  "control-sm",
  "control-md",
  "control-lg",
  "row",
  "cell-x",
  "cell-y",
  "gap",
  "card-pad",
] as const;

export type ColorToken = (typeof colorTokens)[number];
export type ProtectedToken = (typeof protectedTokens)[number];
export type Token = ColorToken | ProtectedToken | (typeof sharedTokens)[number];

/** The CSS custom property that holds a token. */
export function tokenVar(token: Token): string {
  return `--rs-${token}`;
}

/** A CSS value that refers to a token, for inline styles. */
export function tokenRef(token: Token): string {
  return `var(${tokenVar(token)})`;
}

/**
 * The current value of a token, which depends on the light or dark mode in
 * effect. Canvas code reads it again when the mode changes.
 */
export function readToken(token: Token, element: Element = document.documentElement): string {
  return getComputedStyle(element).getPropertyValue(tokenVar(token)).trim();
}

/** The most chart colors a theme can have. */
export const maxChartColors = 12;

/**
 * The chart colors of the theme in effect, in series order. Themes have six
 * to twelve; the list ends at the first one not set.
 */
export function readChartPalette(element: Element = document.documentElement): string[] {
  const style = getComputedStyle(element);
  const colors: string[] = [];
  for (let i = 1; i <= maxChartColors; i++) {
    const value = style.getPropertyValue(`--rs-chart${i}`).trim();
    if (value === "") {
      break;
    }
    colors.push(value);
  }
  return colors;
}
