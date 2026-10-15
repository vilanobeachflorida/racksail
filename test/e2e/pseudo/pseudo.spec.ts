import { expect, type Page, test } from "@playwright/test";

// Every screen of a panel built with the pseudo-locales in pseudo.mjs, shown
// in each of them on a desktop and on a phone. In en-XA every word that
// came from a message catalog is accented, so unaccented words are either
// the server's data or text that skipped the catalogs; text there is a
// third longer than in English, and must neither push the page sideways nor
// get cut off. In ar-XB the panel lays itself out right to left, and every
// part of the page must sit where it sits left to right, mirrored.

/** The screens to check: home, the ones the navigation offers, and a missing page. */
async function screens(page: Page): Promise<string[]> {
  await page.goto("/");
  await page.waitForLoadState("networkidle");
  const linked = await page
    .getByRole("navigation")
    .locator("a[href^='/']")
    .evaluateAll((links) => links.map((a) => new URL((a as HTMLAnchorElement).href).pathname));
  return [...new Set(["/", ...linked, "/no-such-page"])];
}

/** Records every string and number the API sends, which the panel shows as it is. */
function recordData(page: Page): Set<string> {
  const data = new Set<string>();
  const collect = (value: unknown): void => {
    if (typeof value === "string" || typeof value === "number") {
      data.add(String(value));
    } else if (Array.isArray(value)) {
      value.forEach(collect);
    } else if (value && typeof value === "object") {
      Object.values(value).forEach(collect);
    }
  };
  page.on("response", async (response) => {
    const json = (response.headers()["content-type"] ?? "").includes("json");
    if (json && new URL(response.url()).pathname.startsWith("/api/")) {
      // A navigation can discard the body of a response it interrupted.
      collect(await response.json().catch(() => null));
    }
  });
  return data;
}

async function show(page: Page, path: string): Promise<void> {
  await page.goto(path);
  await page.waitForLoadState("networkidle");
  await page.evaluate(() => document.fonts.ready);
  await expect(page.getByRole("main")).toBeVisible();
}

/** Text outside code and translate="no" with letters no catalog accented. */
async function untranslated(page: Page, data: Set<string>): Promise<string[]> {
  return page.evaluate((data) => {
    const skip = "code, pre, kbd, samp, time, script, style, [translate='no']";
    const english = /[A-Za-z]{2,}/;
    // Data longest first, so a value inside another goes with it.
    const values = data.filter((d) => /[A-Za-z]{2,}/.test(d)).sort((a, b) => b.length - a.length);
    const withoutData = (text: string) => values.reduce((t, d) => t.split(d).join(" "), text);
    const found = new Set<string>();
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const el = node.parentElement;
      const text = node.textContent ?? "";
      if (el && !el.closest(skip) && el.checkVisibility() && english.test(withoutData(text))) {
        found.add(text.trim());
      }
    }
    for (const el of document.body.querySelectorAll("[aria-label], [title], [placeholder], [alt]")) {
      for (const attr of ["aria-label", "title", "placeholder", "alt"]) {
        const value = el.getAttribute(attr);
        if (value && !el.closest(skip) && english.test(withoutData(value))) {
          found.add(`${attr}="${value}"`);
        }
      }
    }
    return [...found];
  }, [...data]);
}

/** A page wider than the window, and text cut off without an ellipsis. */
async function overflow(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const problems: string[] = [];
    const page = document.documentElement;
    if (page.scrollWidth > window.innerWidth + 1) {
      problems.push(`the page is ${page.scrollWidth}px wide in a ${window.innerWidth}px window, so it scrolls sideways`);
    }
    const describe = (el: Element) => `<${el.tagName.toLowerCase()}> "${(el.textContent ?? "").trim().slice(0, 60)}"`;
    for (const el of document.body.querySelectorAll<HTMLElement>("*")) {
      const style = getComputedStyle(el);
      const clips = style.overflowX === "hidden" || style.overflowX === "clip";
      // Visually hidden text, such as a skip link, is clipped on purpose.
      if (!clips || style.textOverflow === "ellipsis" || el.clientWidth <= 1 || !el.checkVisibility()) {
        continue;
      }
      const hasText = [...el.childNodes].some((n) => n.nodeType === Node.TEXT_NODE && (n.textContent ?? "").trim() !== "");
      if (hasText && el.scrollWidth > el.clientWidth + 1) {
        problems.push(`${describe(el)} is cut off: its text needs ${el.scrollWidth}px and gets ${el.clientWidth}px`);
      }
    }
    return problems;
  });
}

type Box = { name: string; x: number; width: number };

/** Where the page's landmarks sit across the window. */
async function landmarks(page: Page): Promise<Box[]> {
  return page.evaluate(() => {
    const selector = "header, nav, main, aside, footer, [role=banner], [role=navigation], [role=main], [role=complementary], [role=contentinfo]";
    return [...document.querySelectorAll<HTMLElement>(selector)]
      .filter((el) => el.checkVisibility() && el.getBoundingClientRect().width > 0)
      .map((el, i) => {
        const r = el.getBoundingClientRect();
        // Without the direction marks ar-XB puts in labels, for the report.
        const label = (el.getAttribute("aria-label") ?? el.getAttribute("role") ?? "").replace(/[\u200e\u200f\u202a-\u202e]/g, "");
        return { name: `${i + 1}. <${el.tagName.toLowerCase()}> ${label}`.trim(), x: r.x, width: r.width };
      });
  });
}

test("every screen reads and fits in the pseudo-locale", async ({ page }, testInfo) => {
  const locale = testInfo.project.use.locale ?? "";
  const rightToLeft = locale.startsWith("ar");
  const data = recordData(page);
  for (const path of await screens(page)) {
    await test.step(path, async () => {
      await show(page, path);
      const root = page.locator("html");
      await expect(root).toHaveAttribute("lang", locale);
      await expect(root).toHaveAttribute("dir", rightToLeft ? "rtl" : "ltr");
      await testInfo.attach(`${path} in ${locale}`, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });

      expect.soft(await overflow(page), `${path} doesn't fit`).toEqual([]);
      if (!rightToLeft) {
        expect.soft(await untranslated(page, data), `${path} shows text that isn't from a message catalog`).toEqual([]);
        return;
      }

      // The same page laid out left to right must be the mirror image.
      const width = await page.evaluate(() => document.documentElement.clientWidth);
      const rtl = await landmarks(page);
      await page.evaluate(() => document.documentElement.setAttribute("dir", "ltr"));
      const ltr = await landmarks(page);
      await page.evaluate(() => document.documentElement.setAttribute("dir", "rtl"));
      expect(rtl.map((b) => b.name), `${path} has other landmarks right to left`).toEqual(ltr.map((b) => b.name));
      const unmirrored = rtl.flatMap((box, i) => {
        const other = ltr[i];
        if (!other) {
          return [];
        }
        const mirrored = width - other.x - other.width;
        return Math.abs(box.x - mirrored) > 2 || Math.abs(box.width - other.width) > 2
          ? [`${box.name} spans ${Math.round(box.x)}px to ${Math.round(box.x + box.width)}px right to left, not ${Math.round(mirrored)}px to ${Math.round(mirrored + other.width)}px`]
          : [];
      });
      expect.soft(unmirrored, `${path} doesn't mirror right to left`).toEqual([]);
    });
  }
});
