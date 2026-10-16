import type { APIRequestContext } from "@playwright/test";

/**
 * Waits until the panel would take a burst of requests from the person the
 * suites act as. racksail dev signs everyone in as the same person, so the
 * browsers of every test share one rate limit; suites that open many
 * screens wait for it here rather than have the panel refuse a screen's
 * requests halfway. A panel that sends no RateLimit headers doesn't limit.
 */
export async function waitForQuota(request: APIRequestContext, wanted = 80): Promise<void> {
  for (;;) {
    const response = await request.get("/api/v1/version");
    const remaining = Number(response.headers()["ratelimit-remaining"]);
    const limit = Number(response.headers()["ratelimit-limit"]);
    // A small limit is waited on until half full, which it always gets to.
    const enough = Number.isNaN(limit) ? wanted : Math.min(wanted, limit / 2);
    if (Number.isNaN(remaining) || remaining >= enough) {
      return;
    }
    // The bucket fills at a steady rate; a second or two is usually enough.
    await new Promise((resolve) => setTimeout(resolve, 1_000));
  }
}
