import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Bidirectional controls change the order code reads in without showing up
// in it, so sources write them as escapes, such as "\u202E".
const hidden = /[\u202A-\u202E\u2066-\u2069]/;

function files(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      return entry.name.startsWith(".") || entry.name === "node_modules" ? [] : files(path);
    }
    return /\.(?:[cm]?[jt]sx?|json|css|html)$/.test(entry.name) ? [path] : [];
  });
}

describe("the UI's sources", () => {
  it("have no hidden bidirectional controls", () => {
    const found = ["src", "scripts", "public"]
      .flatMap(files)
      .filter((file) => hidden.test(readFileSync(file, "utf8")));
    expect(found).toEqual([]);
  });
});
