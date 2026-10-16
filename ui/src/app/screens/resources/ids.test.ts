import { describe, expect, it } from "vitest";
import { newResourceID } from "./ids";

describe("IDs chosen up front", () => {
  it("are made as the panel makes them", () => {
    // The panel's store.FromUUID gives this for the same moment and bits.
    const random = Uint8Array.from([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    expect(newResourceID("acct", Date.UTC(2026, 9, 15, 12), random)).toBe(
      "acct_01m4zpxyg0e41861050r3gg28a",
    );
  });

  it("are new each time and sort by when they were made", () => {
    const earlier = newResourceID("acct", Date.UTC(2026, 9, 15, 12));
    const later = newResourceID("acct", Date.UTC(2026, 9, 15, 12, 0, 1));
    expect(earlier).toMatch(/^acct_[0-7][0-9a-hjkmnp-tv-z]{25}$/);
    expect(later > earlier).toBe(true);
    expect(newResourceID("acct")).not.toBe(newResourceID("acct"));
  });
});
