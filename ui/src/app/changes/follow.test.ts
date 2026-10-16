import { QueryClient, type QueryKey } from "@tanstack/react-query";
import { createIntl } from "react-intl";
import { describe, expect, it, vi } from "vitest";
import type { ChangeSet } from "@/lib/changes";
import { shellMessages } from "@/lib/i18n";
import { LiveClient } from "@/lib/live";
import { fakeApi, json } from "@/test/render";
import { changeSet } from "@/test/resources";
import { addonEntriesKey } from "../addons";
import type { ShellState } from "../shell-state";
import { track } from "./follow";
import type { Followed, TrackerHost } from "./tracker";

/** Follows a change set that is done already, and returns the queries it read again. */
async function finished(cs: ChangeSet): Promise<QueryKey[]> {
  const queryClient = new QueryClient();
  const invalidated: QueryKey[] = [];
  vi.spyOn(queryClient, "invalidateQueries").mockImplementation(async (filters) => {
    invalidated.push(filters?.queryKey ?? []);
  });
  const fetched = vi.fn(() => json(cs));
  const host: TrackerHost = {
    api: fakeApi({ [`GET /api/v1/changesets/${cs.id}`]: fetched }),
    live: new LiveClient(),
    queryClient,
    followed: new Map<string, Followed>(),
    current: () => ({
      intl: createIntl({ locale: "en", messages: shellMessages }),
      shell: { showJob: () => {} } as unknown as ShellState,
      allowed: () => true,
    }),
    setLast: () => {},
    confirm: () => {},
    track: () => {},
    undo: async () => {},
  };
  track(host, cs, { quiet: true });
  await vi.waitFor(() => expect(fetched).toHaveBeenCalled());
  return invalidated;
}

const applied = (kind: string) =>
  changeSet({
    state: "applied",
    dryRun: false,
    changes: [
      {
        op: "update",
        kind,
        id: "addon_01jq0000000000000000000001",
        scope: { level: "server" },
        source: { type: "request" },
      },
    ],
  });

describe("following a change to the end", () => {
  it("reads again what any change makes out of date", async () => {
    const keys = await finished(applied("core.webhook"));
    expect(keys).toEqual([["resources"], ["changesets"]]);
  });

  it("reads add-ons' kinds and entries again once an add-on changed", async () => {
    const keys = await finished(applied("core.addon"));
    expect(keys).toContainEqual(["kinds"]);
    expect(keys).toContainEqual([...addonEntriesKey]);
  });

  it("reads add-ons' entries again once an account's add-on changed", async () => {
    const keys = await finished(applied("core.account_addon"));
    expect(keys).toContainEqual([...addonEntriesKey]);
    expect(keys).not.toContainEqual(["kinds"]);
  });
});
