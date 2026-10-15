import { createContext, type ReactNode, useContext } from "react";
import type { ApiClient } from "./client";

const ApiContext = createContext<ApiClient | null>(null);

/** Makes an API client available to the components below it. */
export function ApiProvider({ client, children }: { client: ApiClient; children: ReactNode }) {
  return <ApiContext value={client}>{children}</ApiContext>;
}

/** The API client of the surrounding ApiProvider. */
export function useApi(): ApiClient {
  const client = useContext(ApiContext);
  if (!client) {
    throw new Error("useApi needs an ApiProvider above it");
  }
  return client;
}
