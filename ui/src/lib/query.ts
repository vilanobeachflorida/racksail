import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "./api/client";

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
        // apart from asking too often, which passes in a moment.
        retry: (failures, error) =>
          failures < 2 &&
          !(error instanceof ApiError && error.status < 500 && error.status !== 429),
      },
    },
  });
}
