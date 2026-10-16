// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { bundleBudget, compressedSize, firstLoad } from "./bundle-budget.mjs";

/**
 * @param {string} fileName
 * @param {Partial<import("./bundle-budget.mjs").Chunk>} [extra]
 * @returns {import("./bundle-budget.mjs").Chunk}
 */
const chunk = (fileName, extra = {}) => ({
  type: "chunk",
  fileName,
  code: `export const name = ${JSON.stringify(fileName)};`.repeat(20),
  imports: [],
  isEntry: false,
  isDynamicEntry: false,
  facadeModuleId: null,
  ...extra,
});

/** @type {import("./bundle-budget.mjs").Bundle} */
const bundle = {
  "index.js": chunk("index.js", {
    isEntry: true,
    facadeModuleId: "/ui/index.html",
    imports: ["shared.js"],
    viteMetadata: { importedCss: new Set(["index.css"]) },
  }),
  "shared.js": chunk("shared.js"),
  "index.css": { type: "asset", fileName: "index.css", source: ".a{color:var(--rs-text)}" },
  "web.js": chunk("web.js", {
    isDynamicEntry: true,
    facadeModuleId: "/ui/src/modules/web/index.ts",
    imports: ["globe.js", "shared.js"],
  }),
  "globe.js": chunk("globe.js"),
  "en.js": chunk("en.js", {
    isDynamicEntry: true,
    facadeModuleId: "C:\\ui\\src\\modules\\web\\messages\\en.json",
  }),
  "de.js": chunk("de.js", {
    isDynamicEntry: true,
    facadeModuleId: "/ui/src/modules/web/messages/de.json",
  }),
  "sites.js": chunk("sites.js", {
    isDynamicEntry: true,
    facadeModuleId: "/ui/src/modules/web/screens/sites.tsx",
    imports: ["table.js"],
    viteMetadata: { importedCss: new Set(["sites.css"]) },
  }),
  "table.js": chunk("table.js"),
  "sites.css": { type: "asset", fileName: "sites.css", source: ".b{}" },
  "inter.woff2": { type: "asset", fileName: "inter.woff2", source: new Uint8Array(100) },
};

const atStart = [
  /\/src\/modules\/[^/]+\/index\.tsx?$/,
  /\/src\/modules\/[^/]+\/messages\/en\.json$/,
];

describe("the bundle budget", () => {
  it("counts the entry, what it imports and what the shell loads before it renders", () => {
    expect(firstLoad(bundle, atStart)).toEqual([
      "en.js",
      "globe.js",
      "index.css",
      "index.js",
      "shared.js",
      "web.js",
    ]);
  });

  it("fails the build over budget and names the largest files", () => {
    const plugin = bundleBudget({ budget: 100, atStart });
    const context = {
      /** @param {string} message */
      error: (message) => {
        throw new Error(message);
      },
    };
    const writeBundle = /** @type {Function} */ (plugin.writeBundle);
    expect(() => writeBundle.call(context, {}, bundle)).toThrow(
      /first load is .* KB compressed, of a 0\.1 KB budget\. Load more of it lazily.*\n.*index\.js/s,
    );
    const log = vi.spyOn(console, "log").mockImplementation(() => {});
    const roomy = bundleBudget({ budget: 250_000, atStart });
    expect(() =>
      /** @type {Function} */ (roomy.writeBundle).call(context, {}, bundle),
    ).not.toThrow();
    expect(log).toHaveBeenCalledWith(expect.stringMatching(/of a 250\.0 KB budget\.$/));
    expect(compressedSize("a".repeat(1000))).toBeLessThan(100);
  });
});
