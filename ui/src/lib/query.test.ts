import { describe, expect, it } from "vitest";
import { ApiError } from "./api/client";
import { createQueryClient, rateLimitWait } from "./query";

const refusal = (status: number, params?: Record<string, unknown>) =>
  new ApiError(status, {
    type: "about:blank",
    status,
    code: status === 429 ? "core.rate_limited" : "core.not_found",
    ...(params ? { params } : {}),
  });

describe("the query client", () => {
  const { retry, retryDelay } = createQueryClient().getDefaultOptions().queries ?? {};
  const retries = retry as (failures: number, error: unknown) => boolean;
  const delay = retryDelay as (failures: number, error: unknown) => number;

  it("asks again after a refusal for coming too often, as long as the panel says", () => {
    expect(rateLimitWait(refusal(429, { retryAfter: 3 }))).toBe(3000);
    expect(retries(0, refusal(429, { retryAfter: 3 }))).toBe(true);
    expect(delay(0, refusal(429, { retryAfter: 3 }))).toBe(3000);
    // Twice at most, like any query.
    expect(retries(2, refusal(429, { retryAfter: 3 }))).toBe(false);
  });

  it("doesn't wait for a refusal that says nothing, or asks to wait long", () => {
    expect(rateLimitWait(refusal(429))).toBeUndefined();
    expect(retries(0, refusal(429))).toBe(false);
    expect(rateLimitWait(refusal(429, { retryAfter: 120 }))).toBeUndefined();
    expect(retries(0, refusal(429, { retryAfter: 120 }))).toBe(false);
  });

  it("leaves other errors the request caused alone, and asks again after the panel's own", () => {
    expect(retries(0, refusal(404))).toBe(false);
    expect(retries(0, refusal(503))).toBe(true);
    expect(retries(0, new TypeError("Failed to fetch"))).toBe(true);
    expect(delay(1, refusal(503))).toBe(2000);
  });
});
