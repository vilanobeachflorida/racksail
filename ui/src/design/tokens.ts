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
  "chart1",
  "chart2",
  "chart3",
  "chart4",
  "chart5",
  "chart6",
  "chart7",
  "chart8",
] as const;

export const sharedTokens = [
  "font-sans",
  "font-mono",
  "font-size-base",
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
  "weight-regular",
  "weight-medium",
  "weight-semibold",
  "weight-bold",
] as const;

export type ColorToken = (typeof colorTokens)[number];
export type Token = ColorToken | (typeof sharedTokens)[number];

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
