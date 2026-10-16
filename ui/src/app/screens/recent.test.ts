import { afterEach, describe, expect, it } from "vitest";
import { clearRecent, loadRecent, recentKey, recordRecent } from "./recent";

afterEach(() => {
  window.localStorage.clear();
});

describe("what was opened recently", () => {
  it("keeps the latest few, newest first, each once", () => {
    for (let i = 0; i < 15; i++) {
      recordRecent({ path: `/jobs/job_${i}`, title: `Job ${i}`, what: "Job" });
    }
    recordRecent({ path: "/jobs/job_10", title: "Job 10", what: "Job" });
    const recent = loadRecent();
    expect(recent).toHaveLength(12);
    expect(recent.map((r) => r.path).slice(0, 3)).toEqual([
      "/jobs/job_10",
      "/jobs/job_14",
      "/jobs/job_13",
    ]);
    expect(new Set(recent.map((r) => r.path)).size).toBe(12);
  });

  it("ignores what doesn't look like an item of the panel's", () => {
    window.localStorage.setItem(
      recentKey,
      JSON.stringify([
        { path: "https://elsewhere.example.com", title: "Away", what: "Page", at: 1 },
        { path: "/events", title: 7, what: "Page", at: 1 },
        { path: "/changes", title: "Changes", what: "Page", at: 2 },
      ]),
    );
    expect(loadRecent()).toEqual([{ path: "/changes", title: "Changes", what: "Page", at: 2 }]);
    window.localStorage.setItem(recentKey, "not json");
    expect(loadRecent()).toEqual([]);
  });

  it("forgets everything when asked", () => {
    recordRecent({ path: "/jobs", title: "Jobs", what: "Page" });
    clearRecent();
    expect(loadRecent()).toEqual([]);
  });
});
