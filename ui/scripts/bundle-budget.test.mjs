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
  // A chunk the build put several lazily loaded modules in has no facade.
  "entries.js": chunk("entries.js", {
    moduleIds: [
      "/ui/src/modules/dns/index.ts",
      "/ui/src/modules/mail/index.ts",
      "/ui/icons/mail.js",
    ],
    imports: ["shared.js"],
  }),
  "inbox.js": chunk("inbox.js", {
    isDynamicEntry: true,
    facadeModuleId: "/ui/src/app/screens/inbox/card.tsx",
    imports: ["kinds.js"],
  }),
  "kinds.js": chunk("kinds.js"),
  "inter.woff2": { type: "asset", fileName: "inter.woff2", source: new Uint8Array(100) },
};

const atStart = [
  /\/src\/modules\/[^/]+\/index\.tsx?$/,
  /\/src\/modules\/[^/]+\/messages\/en\.json$/,
];
const firstScreen = [/\/src\/app\/screens\/inbox\/card\.tsx$/];

describe("the bundle budget", () => {
  it("counts the entry, what it imports and what the shell loads before it renders", () => {
    expect(firstLoad(bundle, atStart)).toEqual([
      "en.js",
      "entries.js",
      "globe.js",
      "index.css",
      "index.js",
      "shared.js",
      "web.js",
    ]);
  });

  it("counts what the home screen loads as it shows on top", () => {
    expect(firstLoad(bundle, [...atStart, ...firstScreen])).toEqual([
      "en.js",
      "entries.js",
      "globe.js",
      "inbox.js",
      "index.css",
      "index.js",
      "kinds.js",
      "shared.js",
      "web.js",
    ]);
  });

  it("fails the build over budget and names the largest files", () => {
    const plugin = bundleBudget({ budget: 100, atStart, firstScreen });
    const context = {
      /** @param {string} message */
      error: (message) => {
        throw new Error(message);
      },
    };
    const writeBundle = /** @type {Function} */ (plugin.writeBundle);
    expect(() => writeBundle.call(context, {}, bundle)).toThrow(
      /first screen loads .* KB compressed, of a 0\.1 KB budget: .* for the shell and .* for the home screen\. Load more of it lazily.*\n.*index\.js/s,
    );
    const log = vi.spyOn(console, "log").mockImplementation(() => {});
    const roomy = bundleBudget({ budget: 250_000, atStart, firstScreen });
    expect(() =>
      /** @type {Function} */ (roomy.writeBundle).call(context, {}, bundle),
    ).not.toThrow();
    const home = ["inbox.js", "kinds.js"].reduce((sum, file) => {
      const item = /** @type {import("./bundle-budget.mjs").Chunk} */ (bundle[file]);
      return sum + compressedSize(item.code);
    }, 0);
    expect(log).toHaveBeenCalledWith(
      expect.stringMatching(
        new RegExp(
          `of a 250\\.0 KB budget: .* for the shell and ${(home / 1000).toFixed(1)} KB for the home screen\\.$`,
        ),
      ),
    );
    expect(compressedSize("a".repeat(1000))).toBeLessThan(100);
  });
});
