import { type QueryKey, useMutation, useQueryClient } from "@tanstack/react-query";

export interface OptimisticOptions<TData, TVariables, TResult> {
  /** The query whose cached data changes at once. */
  queryKey: QueryKey;
  mutationFn: (variables: TVariables) => Promise<TResult>;
  /** The data as it will be once the mutation succeeds. */
  update: (current: TData | undefined, variables: TVariables) => TData | undefined;
  onSuccess?: (result: TResult, variables: TVariables) => void;
  onError?: (error: Error, variables: TVariables) => void;
}

/**
 * A mutation that shows its result before the server confirms it. If the
 * server refuses, the data goes back to what it was; either way the query
 * is fetched again, so what's shown ends up matching the server. Use it
 * for safe, reversible changes; risky ones go through the change preview.
 */
export function useOptimisticMutation<TData, TVariables, TResult = unknown>({
  queryKey,
  mutationFn,
  update,
  onSuccess,
  onError,
}: OptimisticOptions<TData, TVariables, TResult>) {
  const queryClient = useQueryClient();
  return useMutation<TResult, Error, TVariables, { previous: TData | undefined }>({
    mutationFn,
    onMutate: async (variables) => {
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<TData>(queryKey);
      queryClient.setQueryData<TData>(queryKey, (current) => update(current, variables));
      return { previous };
    },
    onError: (error, variables, result) => {
      queryClient.setQueryData<TData>(queryKey, result?.previous);
      onError?.(error, variables);
    },
    onSuccess: (result, variables) => onSuccess?.(result, variables),
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  });
}
