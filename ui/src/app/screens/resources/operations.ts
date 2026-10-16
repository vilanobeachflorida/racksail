import { useQuery } from "@tanstack/react-query";
import { usePermission } from "@/app/modules";
import { type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { type Kind, useKinds } from "./kinds";

export type Operation = Schemas["Operation"];

/** The API's operations, which only change with a new release of the panel. */
export function useOperations() {
  const api = useApi();
  return useQuery({
    queryKey: ["operations"],
    queryFn: async () => (await unwrap(api.GET("/api/v1/operations"))).operations ?? [],
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/**
 * The kinds with screens the person may use for something: list for those
 * whose resources they may read, create for those they may add to, going
 * by the permission each operation needs.
 */
export function useKindsFor(operation: "list" | "create"): Kind[] {
  const kinds = useKinds().data ?? [];
  const operations = useOperations().data ?? [];
  const allowed = usePermission();
  const names = new Set(
    operations
      .filter((o) => o.operation === operation && o.kind && allowed(o.permission))
      .map((o) => o.kind),
  );
  return kinds.filter((k) => !k.ui.hidden && names.has(k.name));
}
