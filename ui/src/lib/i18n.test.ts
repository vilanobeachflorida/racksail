import { parse } from "@formatjs/icu-messageformat-parser";
import { createIntl } from "react-intl";
import { describe, expect, it } from "vitest";
import {
  applyLocale,
  availableLocales,
  isRightToLeft,
  loadMessages,
  loadScreenMessages,
  locales,
  pickLocale,
  shellMessages,
  storedLocale,
} from "./i18n";
import { isPseudoLocale, pseudoMessage, pseudoMessages } from "./pseudo";

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

  it("load the screens' own catalogs apart from the shell's, sharing no ids", async () => {
    const screens = await loadScreenMessages("en");
    const inbox = await loadScreenMessages("en", "inbox");
    const kinds = await loadScreenMessages("en", "kinds");
    expect(screens["app.resource.create"]).toBe("New {kind}");
    expect(inbox["app.inbox.title"]).toBe("Needs your attention");
    // The core's kinds' words come with the screens' too.
    expect(kinds["core.kinds.webhook.title"]).toBe("Webhook");
    expect(screens["core.kinds.webhook.title"]).toBe("Webhook");
    expect(kinds["app.resource.create"]).toBeUndefined();
    const files = import.meta.glob<Record<string, string>>(
      ["../app/screens/messages/en.json", "../app/screens/kinds/messages/en.json"],
      { eager: true, import: "default" },
    );
    const ids = [shellMessages, inbox, ...Object.values(files)].flatMap((catalog) =>
      Object.keys(catalog),
    );
    expect(ids.filter((id, i) => ids.indexOf(id) !== i)).toEqual([]);
    const pseudo = await loadScreenMessages("en-XA", "inbox");
    expect(pseudo["app.inbox.title"]).toBe(pseudoMessage("Needs your attention", "en-XA"));
    expect(await loadScreenMessages("eo")).toEqual(screens);
  });

  it("are all valid messages", () => {
    const catalogs = import.meta.glob<Record<string, string>>(
      [
        "../app/messages/*.json",
        "../app/screens/messages/*.json",
        "../app/screens/inbox/messages/*.json",
        "../app/screens/kinds/messages/*.json",
        "../modules/*/messages/*.json",
      ],
      { eager: true, import: "default" },
    );
    const broken: string[] = [];
    for (const [path, catalog] of Object.entries(catalogs)) {
      for (const [id, text] of Object.entries(catalog)) {
        try {
          parse(text, { requiresOtherClause: true });
        } catch (e) {
          broken.push(`${path} ${id}: ${(e as Error).message}`);
        }
      }
    }
    expect(Object.keys(catalogs)).toContain("../app/messages/en.json");
    expect(Object.keys(catalogs)).toContain("../app/screens/messages/en.json");
    expect(broken).toEqual([]);
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

describe("pseudo-locales", () => {
  it("accent and lengthen text but keep placeholders, plurals and selects", () => {
    expect(pseudoMessage("Close", "en-XA")).toBe("[Çļóóšéé]");
    const plural = pseudoMessage("{count, plural, one {# row} other {# rows}}", "en-XA");
    const intl = createIntl({ locale: "en", messages: { m: plural } });
    expect(intl.formatMessage({ id: "m" }, { count: 1 })).toBe("[1 ŕóóŵ]");
    expect(intl.formatMessage({ id: "m" }, { count: 3 })).toBe("[3 ŕóóŵš]");
    const select = pseudoMessage(
      "Hello {name}, {mode, select, dark {Dark} other {Light}}",
      "en-XA",
    );
    expect(
      createIntl({ locale: "en", messages: { m: select } }).formatMessage(
        { id: "m" },
        { name: "Ana", mode: "dark" },
      ),
    ).toBe("[Ĥééļļóó Ana, Ðááŕķ]");
  });

  it("mark mirrored text to run right to left", () => {
    expect(pseudoMessage("Save", "ar-XB")).toBe("\u202ESave\u202C");
    expect(isRightToLeft("ar-XB")).toBe(true);
  });

  it("make every message of the shell, which stays a valid message", () => {
    const pseudo = pseudoMessages(shellMessages, "en-XA");
    expect(Object.keys(pseudo)).toEqual(Object.keys(shellMessages));
    const intl = createIntl({
      locale: "en",
      messages: pseudo,
      onError: (e) => {
        throw e;
      },
    });
    expect(intl.formatMessage({ id: "app.jobs.running" }, { count: 2 })).toBe(
      "[2 ĵóóƀš ŕúúññííñĝ]",
    );
  });

  it("load from the English catalogs and are offered in development only", async () => {
    const messages = await loadMessages("en-XA", ["web"]);
    expect(messages["web.name"]).toBe("[Ŵééƀ ĥóóšţííñĝ]");
    expect(isPseudoLocale("en-XA")).toBe(true);
    expect(isPseudoLocale("en")).toBe(false);
    expect(availableLocales(true).map((l) => l.tag)).toEqual(["en", "en-XA", "ar-XB"]);
    expect(availableLocales(false).map((l) => l.tag)).toEqual(["en"]);
  });

  it("can be chosen and remembered in the browser", () => {
    expect(storedLocale()).toBeNull();
    window.localStorage.setItem("racksail.locale", "en-XA");
    expect(storedLocale()).toBe("en-XA");
    window.localStorage.setItem("racksail.locale", "tlh");
    expect(storedLocale()).toBeNull();
  });
});
