import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "./api/client";

/** The longest the panel may ask a query to wait before it's asked again. */
const longestWait = 30_000;

/**
 * How long a query the panel refused for coming too often waits before it
 * asks again, as the refusal says; undefined when it doesn't say, or asks
 * for longer than is worth waiting.
 */
export function rateLimitWait(error: unknown): number | undefined {
  if (!(error instanceof ApiError) || error.status !== 429) {
    return undefined;
  }
  const seconds = Number(error.problem.params?.retryAfter);
  if (!Number.isFinite(seconds) || seconds < 0 || seconds * 1000 > longestWait) {
    return undefined;
  }
  return seconds * 1000;
}

/**
 * Creates the query client. Data stays fresh until the server says
 * otherwise: live updates arrive over server-sent events, so nothing
 * refetches on a timer or on window focus.
 */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        refetchOnWindowFocus: false,
        // Errors the request itself caused won't go away by asking again,
        // apart from asking too often, which passes once the panel says it
        // has.
        retry: (failures, error) => {
          if (error instanceof ApiError && error.status < 500) {
            return failures < 2 && rateLimitWait(error) !== undefined;
          }
          return failures < 2;
        },
        retryDelay: (failures, error) =>
          rateLimitWait(error) ?? Math.min(1000 * 2 ** failures, longestWait),
      },
    },
  });
}
