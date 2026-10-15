import { QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { createIntl } from "react-intl";
import { describe, expect, it } from "vitest";
import { fakeLive, testQueryClient } from "@/test/render";
import { appendLogLine, type LogLine } from "./changes";
import { eventText, messageArguments, type TimelineEvent } from "./events";
import { createFormatters, scaleBytes } from "./format";
import { shellMessages } from "./i18n";
import { LiveProvider, upsertById, useLiveQuery } from "./live";
import { useOptimisticMutation } from "./optimistic";
import { validate } from "./schema";
import { withKeys } from "./utils";

describe("formatters", () => {
  const en = createFormatters(createIntl({ locale: "en", messages: shellMessages }));
  const de = createFormatters(createIntl({ locale: "de", messages: shellMessages }));

  it("format sizes in decimal units, or binary ones for memory", () => {
    expect(en.bytes(0)).toBe("0 byte");
    expect(en.bytes(1_500_000)).toBe("1.5 MB");
    expect(en.bytes(26_000_000_000)).toBe("26 GB");
    expect(en.bytes(64 * 1024 * 1024, { binary: true })).toBe("64 MiB");
    expect(en.bytes(1536, { binary: true })).toBe("1.5 KiB");
    expect(en.bytes(2_000_000, { perSecond: true })).toBe("2 MB/s");
    expect(en.bytes(2 * 1024 * 1024, { binary: true, perSecond: true })).toBe("2 MiB/s");
    expect(de.bytes(1_500_000)).toBe("1,5 MB");
    expect(scaleBytes(1024 ** 3, 1024)).toEqual({ value: 1, exponent: 3 });
  });

  it("format numbers, shares and durations for the locale", () => {
    expect(en.number(12345.6)).toBe("12,345.6");
    expect(de.number(12345.6)).toBe("12.345,6");
    expect(en.percent(0.256)).toBe("25.6%");
    expect(en.compact(1_234_000)).toBe("1.2M");
    expect(en.duration(0)).toBe("0 sec");
    expect(en.duration(3725)).toBe("1 hr 2 min");
    expect(en.duration(90061)).toBe("1 day 1 hr");
  });

  it("format dates in the viewer's time zone and relative to now", () => {
    const now = Date.UTC(2026, 9, 15, 12, 0, 0);
    expect(en.relative(now - 5 * 60_000, now)).toBe("5 minutes ago");
    expect(en.relative(now + 86_400_000, now)).toBe("tomorrow");
    expect(en.relative(now - 2 * 3600_000, now)).toBe("2 hours ago");
    expect(en.date(now)).toMatch(/2026/);
    expect(en.dateTime(now)).toMatch(/2026/);
  });
});

describe("events", () => {
  const intl = createIntl({ locale: "en", messages: shellMessages });
  const event: TimelineEvent = {
    id: "evt_1",
    type: "core.changeset.applied",
    time: "2026-10-15T12:00:00Z",
    severity: "info",
    scope: { level: "server" },
    message: "Applied the change set",
  };

  it("show in the user's language when the catalogs have their type", () => {
    expect(eventText(intl, { ...event, data: { title: "Create example.com" } })).toBe(
      "Applied: Create example.com",
    );
  });

  it("fall back to the panel's words when the type or its data is unknown", () => {
    expect(eventText(intl, event)).toBe("Applied the change set");
    expect(eventText(intl, { ...event, type: "web.site.created" })).toBe("Applied the change set");
  });

  it("know which values a message needs", () => {
    expect(
      messageArguments("{n, plural, one {# site of {owner}} other {# sites}} on {when, date}"),
    ).toEqual(["n", "owner", "when"]);
    expect(messageArguments("No values")).toEqual([]);
  });
});

describe("job logs", () => {
  const line = (seq: number): LogLine => ({
    seq,
    time: "2026-10-15T12:00:00Z",
    level: "INFO",
    message: `line ${seq}`,
  });

  it("keep each line once and only the latest", () => {
    let lines: LogLine[] = [];
    for (const seq of [1, 2, 2, 1, 3]) {
      lines = appendLogLine(lines, line(seq));
    }
    expect(lines.map((l) => l.seq)).toEqual([1, 2, 3]);
    expect(appendLogLine(lines, line(4), 2).map((l) => l.seq)).toEqual([3, 4]);
  });
});

describe("withKeys", () => {
  it("makes unique keys from content", () => {
    expect(withKeys(["a", "b", "a", "a"], (x) => x).map(([k]) => k)).toEqual([
      "a",
      "b",
      "a#2",
      "a#3",
    ]);
  });
});

describe("JSON Schema validation", () => {
  const schema = {
    $defs: { port: { type: "integer", minimum: 1, maximum: 65535 } },
    type: "object",
    additionalProperties: false,
    required: ["name", "port"],
    properties: {
      name: { type: "string", minLength: 3, maxLength: 8, pattern: "^[a-z]+$" },
      port: { $ref: "#/$defs/port" },
      email: { type: "string", format: "email" },
      tags: { type: "array", items: { type: "string" }, maxItems: 2, uniqueItems: true },
      mode: { enum: ["fast", "safe"] },
      ratio: { type: ["number", "null"], multipleOf: 0.5 },
    },
  } as const;

  it("accepts a matching value", () => {
    expect(
      validate(schema, { name: "web", port: 443, tags: ["a"], mode: "safe", ratio: null }),
    ).toEqual([]);
    expect(validate(schema, { name: "web", port: 8080, ratio: 1.5 })).toEqual([]);
  });

  it("reports every problem with its path", () => {
    const issues = validate(schema, {
      name: "Web Site!",
      port: 0,
      email: "nope",
      tags: ["a", "a", "b"],
      mode: "slow",
      ratio: 0.3,
      extra: true,
    });
    const found = issues.map((i) => `${i.path}:${i.keyword}`);
    expect(found).toEqual(
      expect.arrayContaining([
        "name:maxLength",
        "name:pattern",
        "port:minimum",
        "email:format",
        "tags:maxItems",
        "tags:uniqueItems",
        "mode:enum",
        "ratio:multipleOf",
        "extra:additionalProperties",
      ]),
    );
    expect(validate(schema, {}).map((i) => i.path)).toEqual(["name", "port"]);
    expect(validate(schema, { name: 3, port: "x" }).map((i) => i.keyword)).toEqual([
      "type",
      "type",
    ]);
  });
});

describe("live data", () => {
  function wrapper(live = fakeLive()) {
    const client = testQueryClient();
    return {
      client,
      ...live,
      Wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>
          <LiveProvider client={live.live}>{children}</LiveProvider>
        </QueryClientProvider>
      ),
    };
  }

  it("keeps a query current from server-sent events", async () => {
    const { Wrapper, sources } = wrapper();
    const { result } = renderHook(
      () =>
        useLiveQuery<{ id: string; state: string }[], { id: string; state: string }>({
          queryKey: ["jobs"],
          queryFn: async () => [{ id: "job_1", state: "running" }],
          stream: { url: "/api/v1/jobs/events", types: ["job"] },
          apply: (jobs, event) => upsertById(jobs, event.data),
        }),
      { wrapper: Wrapper },
    );
    await waitFor(() => expect(result.current.data).toHaveLength(1));
    expect(sources).toHaveLength(1);
    act(() => sources[0]?.open());
    await waitFor(() => expect(result.current.liveStatus).toBe("open"));
    act(() => sources[0]?.emit("job", { id: "job_1", state: "succeeded" }, "7"));
    act(() => sources[0]?.emit("job", { id: "job_2", state: "queued" }, "8"));
    await waitFor(() =>
      expect(result.current.data).toEqual([
        { id: "job_2", state: "queued" },
        { id: "job_1", state: "succeeded" },
      ]),
    );
  });

  it("shares one connection per stream and reconnects when the server refuses it", async () => {
    const { live, sources } = fakeLive();
    const seen: string[] = [];
    const resets: number[] = [];
    const off1 = live.subscribe("/s", ["a"], {
      onEvent: (e) => seen.push(`1:${e.type}:${String(e.data)}`),
      onReset: () => resets.push(1),
    });
    const off2 = live.subscribe("/s", ["a", "b"], {
      onEvent: (e) => seen.push(`2:${e.type}:${String(e.data)}`),
    });
    expect(sources).toHaveLength(1);
    sources[0]?.open();
    sources[0]?.emit("a", "x", "41");
    sources[0]?.emit("b", "y", "42");
    expect(seen).toEqual(["1:a:x", "2:a:x", "2:b:y"]);
    expect(resets).toEqual([]);
    // The server refused the stream. A new connection can't say where it
    // left off, so once it's open, subscribers read their data again.
    sources[0]?.fail(true);
    await waitFor(() => expect(sources).toHaveLength(2));
    expect(sources[1]?.url).toBe("/s");
    sources[1]?.open();
    expect(resets).toEqual([1]);
    // The server says the client fell behind.
    sources[1]?.emit("resync", { reason: "lagging" });
    expect(resets).toEqual([1, 1]);
    expect(seen).toHaveLength(3);
    off1();
    off2();
    expect(sources[1]?.closed).toBe(true);
  });

  it("reads a live query again after a reconnect", async () => {
    const { Wrapper, sources } = wrapper();
    let calls = 0;
    const { result } = renderHook(
      () =>
        useLiveQuery<string[], string>({
          queryKey: ["names"],
          queryFn: async () => {
            calls++;
            return [`load ${calls}`];
          },
          stream: { url: "/api/v1/events/stream", types: ["event"] },
          apply: (names, event) => [...(names ?? []), event.data],
        }),
      { wrapper: Wrapper },
    );
    await waitFor(() => expect(result.current.data).toEqual(["load 1"]));
    act(() => sources[0]?.open());
    act(() => sources[0]?.emit("event", "a"));
    await waitFor(() => expect(result.current.data).toEqual(["load 1", "a"]));
    // The connection dropped and the browser reconnected by itself; what
    // happened in between may be lost.
    act(() => sources[0]?.fail(false));
    act(() => sources[0]?.open());
    await waitFor(() => expect(result.current.data).toEqual(["load 2"]));
    expect(calls).toBe(2);
  });
});

describe("optimistic updates", () => {
  it("show the change at once and roll it back when the server refuses", async () => {
    const client = testQueryClient();
    client.setQueryData(["name"], "old");
    let fail = true;
    function Probe() {
      useQueryClient();
      const mutation = useOptimisticMutation<string, string>({
        queryKey: ["name"],
        mutationFn: async (next) => {
          await new Promise((r) => setTimeout(r, 10));
          if (fail) {
            throw new Error("refused");
          }
          return next;
        },
        update: (_current, next) => next,
      });
      return (
        <button type="button" onClick={() => mutation.mutate("new")}>
          change
        </button>
      );
    }
    render(
      <QueryClientProvider client={client}>
        <Probe />
      </QueryClientProvider>,
    );
    const seen: unknown[] = [];
    const unsubscribe = client
      .getQueryCache()
      .subscribe(() => seen.push(client.getQueryData(["name"])));
    act(() => screen.getByRole("button").click());
    // The new value shows while the request runs, then the old one comes back.
    await waitFor(() => expect(seen).toContain("new"));
    await waitFor(() => expect(client.getQueryData(["name"])).toBe("old"));
    unsubscribe();
    fail = false;
    act(() => screen.getByRole("button").click());
    await waitFor(() => expect(client.getQueryData(["name"])).toBe("new"));
  });
});
