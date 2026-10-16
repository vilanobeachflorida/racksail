import { afterEach, describe, expect, it } from "vitest";
import { untranslatedText } from "./pseudo";

afterEach(() => {
  document.body.innerHTML = "";
});

describe("the untranslated text sweep", () => {
  it("passes messages, data and the punctuation of numbers", () => {
    document.body.innerHTML =
      "<p>[Ţóţáļ]: 12:30</p><p>Άλφα, Βήτα</p><button aria-label='[Ĵóóbš]'>K</button>";
    expect(untranslatedText()).toEqual([]);
  });

  it("finds words and punctuation that join two messages", () => {
    document.body.innerHTML =
      "<p><span>[Ĵóóbš]</span>, <span>[2 ŕúúññíñĝ]</span></p>" +
      "<button aria-label='[Ĵóóbš]: [2 ŕúúññíñĝ]'></button>" +
      "<p>[Ďóññé] · [Šííţéš]</p><p>Saved</p>";
    expect(untranslatedText()).toEqual([
      '"," joins two messages in <p>',
      '"·" joins two messages in <p>',
      'text "·" in <p>',
      'text "Saved" in <p>',
      `aria-label="[Ĵóóbš]: [2 ŕúúññíñĝ]" on <button>`,
    ]);
  });
});
