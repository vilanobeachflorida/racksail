// @vitest-environment node
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { runBiome } from "./biome.mjs";

/** @type {string[]} */
const made = [];
afterEach(() => {
  for (const dir of made.splice(0)) {
    rmSync(dir, { recursive: true, force: true });
  }
});

/** A checkout with a tidy tracked file and messy ones that aren't the project's. */
function checkout() {
  const dir = mkdtempSync(join(tmpdir(), "biome-"));
  made.push(dir);
  writeFileSync(join(dir, "biome.json"), '{ "root": true }\n');
  writeFileSync(join(dir, "tidy.ts"), "export const tidy = 1;\n");
  // Tracked, but in a hidden directory, as another worktree's files are.
  mkdirSync(join(dir, ".worktrees/other"), { recursive: true });
  writeFileSync(join(dir, ".worktrees/other/messy.ts"), "export const   messy=1\n");
  execFileSync("git", ["init", "--quiet"], { cwd: dir });
  execFileSync("git", ["add", "--", "."], { cwd: dir });
  writeFileSync(join(dir, "draft.ts"), "export const   draft=1\n");
  return dir;
}

describe("runBiome", () => {
  it("checks only the files git tracks", () => {
    const dir = checkout();
    expect(runBiome(["check"], dir, "pipe").status).toBe(0);

    execFileSync("git", ["add", "--", "draft.ts"], { cwd: dir });
    const messy = runBiome(["check"], dir, "pipe");
    expect(messy.stdout + messy.stderr).toContain("draft.ts");
    expect(messy.status).not.toBe(0);
  });

  it("formats only the files git tracks", () => {
    const dir = checkout();
    writeFileSync(join(dir, "tidy.ts"), "export const   tidy=1\n");
    const result = runBiome(["format", "--write"], dir, "pipe");
    expect(result.status).toBe(0);
    expect(readFileSync(join(dir, "tidy.ts"), "utf8")).toBe("export const tidy = 1;\n");
    expect(readFileSync(join(dir, "draft.ts"), "utf8")).toBe("export const   draft=1\n");
    expect(readFileSync(join(dir, ".worktrees/other/messy.ts"), "utf8")).toBe(
      "export const   messy=1\n",
    );
  });

  it("is how npm run lint and npm run format run Biome", () => {
    const { scripts } = JSON.parse(
      readFileSync(new URL("../package.json", import.meta.url), "utf8"),
    );
    expect(scripts.lint).toContain("node scripts/biome.mjs check");
    expect(scripts.format).toBe("node scripts/biome.mjs format --write");
    /** @type {string[]} */
    const commands = Object.values(scripts).flatMap((s) => String(s).split(/&&|\|\||;/));
    expect(commands.filter((c) => /^\s*(?:npx\s+)?biome\b/.test(c))).toEqual([]);
  });

  it("refuses a directory git tracks nothing in, rather than read all of it", () => {
    const dir = mkdtempSync(join(tmpdir(), "biome-empty-"));
    made.push(dir);
    execFileSync("git", ["init", "--quiet"], { cwd: dir });
    writeFileSync(join(dir, "draft.ts"), "export const   draft=1\n");
    expect(() => runBiome(["check"], dir, "pipe")).toThrow("git tracks no files");
  });
});
