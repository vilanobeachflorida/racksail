import { useQuery } from "@tanstack/react-query";
import { useApi } from "@/lib/api/context";
import { getChangeSet } from "./api";

/**
 * A change set as the panel last told it. Applying a change refreshes it
 * once the change's job has ended, as it does every query of change sets.
 */
export function useChangeSet(id: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: ["changesets", id],
    queryFn: () => getChangeSet(api, id ?? ""),
    enabled: id !== undefined,
  });
}
