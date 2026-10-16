import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { type ApiClient, type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";

export type SearchResult = Schemas["SearchResult"];

export interface SearchAnswer {
  results: SearchResult[];
  /** Kinds with more results than were asked for. */
  more: string[];
}

/** Resources of every kind the person can see whose names or indexed fields contain the text. */
export async function searchResources(
  api: ApiClient,
  q: string,
  options: { kinds?: string[]; limit?: number } = {},
): Promise<SearchAnswer> {
  const answer = await unwrap(
    api.GET("/api/v1/search", {
      params: {
        query: {
          q,
          ...(options.kinds?.length ? { kind: options.kinds } : {}),
          ...(options.limit ? { limit: options.limit } : {}),
        },
      },
    }),
  );
  return { results: answer.results ?? [], more: answer.more ?? [] };
}

/** The shortest text a search starts at; shorter would match nearly everything. */
export const minSearch = 2;

/** How long typing settles before a search runs, so a word is searched once rather than letter by letter. */
export const searchSettle = 250;

/** The text once it has stopped changing for a moment; empty text settles at once. */
export function useSettledText(text: string, delay = searchSettle): string {
  const [settled, setSettled] = useState(text);
  useEffect(() => {
    if (text.trim() === "") {
      setSettled(text);
      return;
    }
    const timer = setTimeout(() => setSettled(text), delay);
    return () => clearTimeout(timer);
  }, [text, delay]);
  return text.trim() === "" ? text : settled;
}

/** A search as the text settles, kept while the next one loads. */
export function useResourceSearch(q: string, limit?: number) {
  const api = useApi();
  const text = q.trim();
  return useQuery({
    queryKey: ["search", text, limit],
    queryFn: () => searchResources(api, text, limit ? { limit } : {}),
    enabled: text.length >= minSearch,
    placeholderData: (previous) => previous,
    staleTime: 10_000,
  });
}
