// @vitest-environment node
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { filesToCheck, findHardCodedColors } from "./check-tokens.mjs";

/** @param {string} text @param {string} file */
const rules = (text, file) => findHardCodedColors(text, file).map((p) => `${p.rule}: ${p.text}`);

describe("findHardCodedColors", () => {
  it("accepts token-based styles", () => {
    expect(
      rules(
        `<div className="bg-surface-raised text-muted-foreground border-border ring-ring/50 bg-transparent" />`,
        "a.tsx",
      ),
    ).toEqual([]);
    expect(
      rules(
        ".a { color: var(--rs-text); background: color-mix(in oklab, var(--rs-brand) 40%, transparent); }",
        "a.css",
      ),
    ).toEqual([]);
    expect(rules(`<a href="#content">skip</a>`, "a.tsx")).toEqual([]);
  });

  it("reports hex colors and color functions", () => {
    expect(rules(".a { color: #fff; border-color: #0062CCAA; }", "a.css")).toEqual([
      "hex color: #fff",
      "hex color: #0062CCAA",
    ]);
    expect(
      rules(`const style = { color: "rgb(1, 2, 3)", fill: "oklch(0.5 0.1 200)" };`, "a.ts"),
    ).toEqual(["color function: rgb(", "color function: oklch("]);
  });

  it("reports Tailwind palette and arbitrary colors", () => {
    expect(
      rules(`cn("bg-red-500 hover:text-slate-700/50 border-white", "bg-[#123456]")`, "a.tsx"),
    ).toEqual([
      "Tailwind palette color: bg-red-500",
      "Tailwind palette color: text-slate-700/50",
      "Tailwind palette color: border-white",
      "hex color: #123456",
    ]);
  });

  it("reports named colors in declarations", () => {
    expect(rules(".a { background: white; outline: 1px solid red; }", "a.css")).toEqual([
      "named color: background: white",
      "named color: outline: 1px solid red",
    ]);
    expect(rules(`<p style={{ color: "red" }}>`, "a.tsx")).toEqual([`named color: color: "red`]);
    expect(rules(`const label = "The red team";`, "a.ts")).toEqual([]);
  });

  it("ignores colors in CSS comments and reports positions", () => {
    expect(rules("/* #fff was the old color */\n.a { color: var(--rs-text); }", "a.css")).toEqual(
      [],
    );
    const [problem] = findHardCodedColors("a\n  .b { color: #abc; }", "a.css");
    expect(problem).toMatchObject({ line: 2, column: 15 });
  });
});

describe("filesToCheck", () => {
  it("skips hidden directories and generated files", () => {
    const dir = mkdtempSync(join(tmpdir(), "tokens-"));
    for (const sub of [".hidden", "design", "lib/api", "app"]) {
      mkdirSync(join(dir, sub), { recursive: true });
    }
    for (const file of [
      ".hidden/a.tsx",
      "design/theme.css",
      "design/globals.css",
      "lib/api/schema.d.ts",
      "app/shell.tsx",
      "app/logo.png",
    ]) {
      writeFileSync(join(dir, file), "");
    }
    expect(filesToCheck(dir).map((f) => f.slice(dir.length + 1))).toEqual([
      "app/shell.tsx",
      "design/globals.css",
    ]);
  });
});
