// @vitest-environment node
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { trackedFiles } from "./tracked.mjs";

/** @type {string[]} */
const made = [];
afterEach(() => {
  for (const dir of made.splice(0)) {
    rmSync(dir, { recursive: true, force: true });
  }
});

describe("trackedFiles", () => {
  it("lists the files git tracks that are still there, leaving out hidden ones", () => {
    const dir = mkdtempSync(join(tmpdir(), "tracked-"));
    made.push(dir);
    for (const sub of ["src/app", "src/.wip", ".worktrees/other/src"]) {
      mkdirSync(join(dir, sub), { recursive: true });
    }
    for (const file of [
      "package.json",
      "src/app/shell.tsx",
      "src/app/gone.tsx",
      "src/.wip/draft.tsx",
      ".worktrees/other/src/shell.tsx",
      ".editorconfig",
    ]) {
      writeFileSync(join(dir, file), "");
    }
    execFileSync("git", ["init", "--quiet"], { cwd: dir });
    execFileSync("git", ["add", "--", "."], { cwd: dir });
    // Deleted but still tracked until committed, and never added.
    rmSync(join(dir, "src/app/gone.tsx"));
    writeFileSync(join(dir, "src/app/new.tsx"), "");
    mkdirSync(join(dir, "checkout/src"), { recursive: true });
    writeFileSync(join(dir, "checkout/src/shell.tsx"), "");

    expect(trackedFiles(dir)).toEqual(["package.json", "src/app/shell.tsx"]);
    expect(trackedFiles(join(dir, "src"))).toEqual(["app/shell.tsx"]);
  });
});
