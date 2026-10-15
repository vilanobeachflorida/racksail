import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { colorTokens, readToken, sharedTokens, tokenRef, tokenVar } from "./tokens";

const here = dirname(fileURLToPath(import.meta.url));
const read = (name: string) => readFileSync(join(here, name), "utf8");
const themeCSS = read("theme.css");

function block(selector: string): string {
  const start = themeCSS.indexOf(`${selector} {`);
  expect(start, selector).toBeGreaterThanOrEqual(0);
  return themeCSS.slice(start, themeCSS.indexOf("}", start));
}

describe("theme tokens", () => {
  it("are all defined by theme.css, the light and dark palettes alike", () => {
    const light = block(":root");
    const dark = block(':root[data-theme="dark"]');
    for (const token of [...colorTokens, ...sharedTokens]) {
      expect(light).toContain(`${tokenVar(token)}:`);
    }
    for (const token of colorTokens) {
      expect(dark).toContain(`${tokenVar(token)}:`);
    }
  });

  it("cover every token the stylesheet uses", () => {
    const used = new Set(
      [...read("globals.css").matchAll(/var\((--rs-[a-z0-9-]+)\)/g)].map((m) => m[1]),
    );
    expect(used.size).toBeGreaterThan(20);
    for (const name of used) {
      expect(themeCSS, name).toContain(`${name}:`);
    }
  });

  it("are referenced and read through their custom properties", () => {
    expect(tokenVar("surface-raised")).toBe("--rs-surface-raised");
    expect(tokenRef("chart1")).toBe("var(--rs-chart1)");
    const element = document.createElement("div");
    element.style.setProperty("--rs-radius-md", " 6px ");
    document.body.append(element);
    expect(readToken("radius-md", element)).toBe("6px");
    element.remove();
  });
});
