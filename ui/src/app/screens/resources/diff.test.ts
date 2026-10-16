import { describe, expect, it } from "vitest";
import { diffSpecs } from "./diff";

describe("the fields two versions of a spec differ in", () => {
  it("are none when nothing changed, whatever the order of the keys", () => {
    expect(
      diffSpecs({ url: "https://a.example", on: true }, { on: true, url: "https://a.example" }),
    ).toEqual([]);
  });

  it("go field by field through objects, in order of their names", () => {
    expect(
      diffSpecs(
        { url: "https://a.example", owner: { level: "account", id: "acct_1" }, old: 1 },
        { url: "https://b.example", owner: { level: "account", id: "acct_2" }, added: "x" },
      ),
    ).toEqual([
      { path: "added", op: "add", after: "x" },
      { path: "old", op: "remove", before: 1 },
      { path: "owner.id", op: "replace", before: "acct_1", after: "acct_2" },
      { path: "url", op: "replace", before: "https://a.example", after: "https://b.example" },
    ]);
  });

  it("show a list of plain values as a whole, which reads better", () => {
    expect(diffSpecs({ events: ["a", "b"] }, { events: ["a", "b", "c"] })).toEqual([
      { path: "events", op: "replace", before: ["a", "b"], after: ["a", "b", "c"] },
    ]);
  });

  it("go item by item through lists of objects", () => {
    expect(
      diffSpecs(
        { records: [{ name: "www", ttl: 300 }] },
        {
          records: [
            { name: "www", ttl: 600 },
            { name: "mail", ttl: 300 },
          ],
        },
      ),
    ).toEqual([
      { path: "records[0].ttl", op: "replace", before: 300, after: 600 },
      { path: "records[1]", op: "add", after: { name: "mail", ttl: 300 } },
    ]);
  });

  it("say a secret changed, never what to, items of lists included", () => {
    expect(
      diffSpecs(
        { secret: "whk_1/secret.chg_1", users: [{ name: "a", password: "ref-1" }] },
        { secret: "whk_1/secret.chg_2", users: [{ name: "a", password: "ref-2" }] },
        ["secret", "users[].password"],
      ),
    ).toEqual([
      { path: "secret", op: "replace", sensitive: true },
      { path: "users[0].password", op: "replace", sensitive: true },
    ]);
    expect(diffSpecs({}, { secret: "whk_1/secret.chg_1" }, ["secret"])).toEqual([
      { path: "secret", op: "add", sensitive: true },
    ]);
  });

  it("show every field of a first version", () => {
    expect(diffSpecs({}, { url: "https://a.example", events: ["*"] })).toEqual([
      { path: "events", op: "add", after: ["*"] },
      { path: "url", op: "add", after: "https://a.example" },
    ]);
  });
});
