import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { apiVersions, supports } from "./index.ts";

test("speaks the version of the add-on protocol the panel publishes", async () => {
  const doc = JSON.parse(await readFile(new URL("../../../spec/addon-protocol.json", import.meta.url), "utf8"));
  assert.equal(apiVersions[0], doc.info.version);
  assert.ok(supports(doc.info.version));
  assert.ok(!supports("v0"));
});
