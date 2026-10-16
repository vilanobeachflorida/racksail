import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * Pairs list items with React keys made from their content. Items that
 * look the same get a count after their key, so lists from the server,
 * which have no ids of their own, still render each item once.
 */
export function withKeys<T>(items: readonly T[], key: (item: T) => string): [string, T][] {
  const seen = new Map<string, number>();
  return items.map((item) => {
    const k = key(item);
    const n = (seen.get(k) ?? 0) + 1;
    seen.set(k, n);
    return [n > 1 ? `${k}#${n}` : k, item];
  });
}

/** Joins class names, letting a later Tailwind class override an earlier one. */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
