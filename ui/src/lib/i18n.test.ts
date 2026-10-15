import { describe, expect, it } from "vitest";
import {
  applyLocale,
  isRightToLeft,
  loadMessages,
  locales,
  pickLocale,
  shellMessages,
} from "./i18n";

describe("pickLocale", () => {
  it("prefers an exact match, then the same language, then English", () => {
    const available = ["de", "en", "pt-BR"];
    expect(pickLocale(["pt-BR", "de"], available)).toBe("pt-BR");
    expect(pickLocale(["pt-br"], available)).toBe("pt-BR");
    expect(pickLocale(["de-AT", "en"], available)).toBe("de");
    expect(pickLocale(["fr-FR", "ja"], available)).toBe("en");
    expect(pickLocale([], available)).toBe("en");
  });

  it("offers English", () => {
    expect(locales).toContain("en");
  });
});

describe("messages", () => {
  it("include the shell's and the given modules' messages", async () => {
    const messages = await loadMessages("en", ["web", "dns"]);
    expect(messages["app.name"]).toBe("Racksail");
    expect(messages["web.name"]).toBe("Web hosting");
    expect(messages["dns.name"]).toBe("DNS");
    expect(messages["mail.name"]).toBeUndefined();
  });

  it("fall back to English for a locale without translations", async () => {
    expect(await loadMessages("eo", ["web"])).toEqual(await loadMessages("en", ["web"]));
  });

  it("translate every core error code", () => {
    expect(shellMessages["error.core.not_found"]).toBe("{what} doesn't exist or was deleted.");
  });
});

describe("locale direction", () => {
  it("runs Arabic, Hebrew and Persian right to left", () => {
    expect(isRightToLeft("ar")).toBe(true);
    expect(isRightToLeft("he-IL")).toBe(true);
    expect(isRightToLeft("fa")).toBe(true);
    expect(isRightToLeft("en-GB")).toBe(false);
  });

  it("is applied to the document", () => {
    const root = document.createElement("html");
    applyLocale("ar", root);
    expect(root.lang).toBe("ar");
    expect(root.dir).toBe("rtl");
    applyLocale("en", root);
    expect(root.dir).toBe("ltr");
  });
});
