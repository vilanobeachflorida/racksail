// @vitest-environment node
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const ui = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

// Bidirectional controls and marks change the order code reads in without
// showing up in it, so sources write them as escapes, such as "\u202E":
// ALM, LRM and RLM, embeddings, overrides and isolates.
const hidden = /[\u061C\u200E\u200F\u202A-\u202E\u2066-\u2069]/;

/**
 * The UI's sources that git tracks. Other checkouts and worktrees can sit
 * inside the repository, untracked and in hidden directories, so neither
 * counts; a file deleted but not yet committed doesn't either.
 */
function trackedSources(): string[] {
  return execFileSync("git", ["ls-files", "-z", "--cached", "--", "."], {
    cwd: ui,
    encoding: "utf8",
  })
    .split("\0")
    .filter((file) => /\.(?:[cm]?[jt]sx?|json|css|html|svg|webmanifest)$/.test(file))
    .filter((file) => !file.split("/").some((part) => part.startsWith(".")))
    .filter((file) => existsSync(join(ui, file)));
}

describe("the UI's sources", () => {
  it("have no hidden bidirectional controls", () => {
    const files = trackedSources();
    expect(files).toContain("src/lib/pseudo.ts");
    expect(files.filter((file) => hidden.test(readFileSync(join(ui, file), "utf8")))).toEqual([]);
  });

  it("count every bidirectional control and mark as hidden", () => {
    for (const code of [0x061c, 0x200e, 0x200f, 0x202a, 0x202c, 0x202e, 0x2066, 0x2069]) {
      expect(hidden.test(`a${String.fromCodePoint(code)}b`), code.toString(16)).toBe(true);
    }
    expect(hidden.test("Plain text · with punctuation")).toBe(false);
  });
});
