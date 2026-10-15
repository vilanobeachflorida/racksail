import { type JSONSchema, type SchemaIssue, validate } from "@/lib/schema";
import { checkTheme } from "./check";
import { themeSpec } from "./spec";
import type { Theme, ThemeProblem } from "./types";

/** The largest theme file the panel reads, as internal/theme.MaxThemeBytes. */
export const maxThemeBytes = 256 * 1024;

export const themeSchema = (themeSpec as unknown as { schema: JSONSchema }).schema;

/** What reading a theme file found. */
export interface ThemeReading {
  /** The theme, when the file matched the schema. */
  theme: Theme | null;
  /** Why the file couldn't be read at all, such as broken YAML. */
  error: "too-large" | "unreadable" | "protected" | null;
  /** Where the file doesn't match the schema. */
  issues: SchemaIssue[];
  /** Problems with the palettes: contrast and status colors. */
  problems: ThemeProblem[];
  /** Protected tokens the file tried to set. */
  protectedKeys: string[];
}

function findProtectedKeys(value: unknown, found: string[] = []): string[] {
  if (Array.isArray(value)) {
    for (const item of value) {
      findProtectedKeys(item, found);
    }
  } else if (typeof value === "object" && value !== null) {
    for (const [key, child] of Object.entries(value)) {
      if (key.toLowerCase().startsWith("critical")) {
        found.push(key);
      }
      findProtectedKeys(child, found);
    }
  }
  return found;
}

/**
 * Checks a theme document already decoded from YAML or JSON, the way the
 * server does when the theme is saved.
 */
export function readThemeDocument(doc: unknown): ThemeReading {
  const protectedKeys = findProtectedKeys(doc);
  if (protectedKeys.length > 0) {
    return { theme: null, error: "protected", issues: [], problems: [], protectedKeys };
  }
  const issues = validate(themeSchema, doc);
  if (issues.length > 0) {
    return { theme: null, error: null, issues, problems: [], protectedKeys };
  }
  const theme = doc as Theme;
  return { theme, error: null, issues, problems: checkTheme(theme), protectedKeys };
}

/** Reads a theme.yaml or JSON file's text and checks it. */
export async function readThemeFile(text: string): Promise<ThemeReading> {
  const empty = { theme: null, issues: [], problems: [], protectedKeys: [] };
  if (new TextEncoder().encode(text).length > maxThemeBytes) {
    return { ...empty, error: "too-large" };
  }
  let doc: unknown;
  try {
    // JSON is YAML too, so one parser reads both.
    const { parse } = await import("yaml");
    doc = parse(text, { maxAliasCount: 0, uniqueKeys: true });
  } catch {
    return { ...empty, error: "unreadable" };
  }
  return readThemeDocument(doc);
}

/** Writes a theme as theme.yaml, the format theme packages use. */
export async function themeToYAML(theme: Theme): Promise<string> {
  const { stringify } = await import("yaml");
  return stringify(theme, { indent: 2, lineWidth: 0 });
}

/** Writes a theme as JSON, which the panel reads like YAML. */
export function themeToJSON(theme: Theme): string {
  return `${JSON.stringify(theme, null, 2)}\n`;
}
