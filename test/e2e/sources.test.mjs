// Checks the end-to-end suites' own sources. Bidirectional controls and
// marks change the order code reads in without showing in it, so sources
// write them as escapes, such as "\u202E", which reviewers can see.
//
//   node --test sources.test.mjs
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";

/** The bidirectional controls and marks: ALM, LRM and RLM, embeddings, overrides and isolates. */
export const bidiControls = /[\u061C\u200E\u200F\u202A-\u202E\u2066-\u2069]/;

const text = /\.(?:[cm]?[jt]s|json|go|sh|md|ya?ml|html|css)$/;

/**
 * The text files git tracks in dir, as paths relative to it. Other
 * checkouts and worktrees can sit inside the repository, untracked and in
 * hidden directories, so neither counts; a file deleted but not yet
 * committed doesn't either.
 * @param {string} dir
 * @returns {string[]}
 */
export function trackedSources(dir) {
  return execFileSync("git", ["ls-files", "-z", "--cached", "--", "."], { cwd: dir, encoding: "utf8" })
    .split("\0")
    .filter((file) => file !== "" && text.test(file))
    .filter((file) => !file.split("/").some((part) => part.startsWith(".")))
    .filter((file) => existsSync(path.join(dir, file)))
    .sort();
}

test("the sources have no hidden bidirectional controls", () => {
  const dir = import.meta.dirname;
  const files = trackedSources(dir);
  assert.ok(files.includes("pseudo/pseudo.mjs"), "the check reads the pseudo-locales' sources");
  const found = files.filter((file) => bidiControls.test(readFileSync(path.join(dir, file), "utf8")));
  assert.deepEqual(found, []);
});

test("every bidirectional control and mark counts", () => {
  for (const code of [0x061c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2069]) {
    assert.ok(bidiControls.test(`a${String.fromCodePoint(code)}b`), code.toString(16));
  }
  assert.ok(!bidiControls.test("Plain text · with punctuation, \\u202E as an escape"));
});
