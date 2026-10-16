import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Input } from "@/design/components/ui/input";
import { Skeleton } from "@/design/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { searchMessages as m } from "../messages";
import {
  type Kind,
  kindNouns,
  kindPath,
  kindPlural,
  resourcePath,
  useKinds,
} from "../resources/kinds";
import { PhaseBadge, When } from "../resources/values";
import { minSearch, type SearchResult, useResourceSearch } from "./api";

/** How long typing settles before the search runs. */
const settle = 250;

/** One kind's results: the resources found, and a way to its list when there are more. */
function KindResults({
  kind,
  results,
  more,
}: {
  kind: Kind | undefined;
  results: SearchResult[];
  more: boolean;
}) {
  const intl = useIntl();
  const name = kind ? kindPlural(intl, kind) : (results[0]?.kind ?? "");
  const id = useId();
  return (
    <section aria-labelledby={id} className="flex flex-col gap-2">
      <h2 id={id} className="font-semibold text-lg">
        {name}
      </h2>
      <ul className="flex flex-col divide-y divide-border rounded-lg border border-border bg-card">
        {results.map((r) => (
          <li key={r.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3">
            <div className="flex min-w-0 flex-1 flex-col">
              {kind ? (
                <Link
                  to={resourcePath(kind, r.id)}
                  className="truncate rounded-sm font-medium text-primary underline-offset-4 hover:underline focus-visible:focus-ring"
                >
                  {r.title}
                </Link>
              ) : (
                <span className="truncate font-medium">{r.title}</span>
              )}
              {r.subtitle ? (
                <span className="truncate text-muted-foreground text-sm">{r.subtitle}</span>
              ) : null}
            </div>
            <PhaseBadge phase={r.phase} />
            <span className="text-muted-foreground text-sm">
              <When time={r.updatedAt} />
            </span>
          </li>
        ))}
      </ul>
      {more && kind ? (
        <Button asChild variant="link" className="self-start">
          <Link to={kindPath(kind)}>
            {intl.formatMessage(m.allOfKind, { kinds: kindNouns(intl, kind) })}
          </Link>
        </Button>
      ) : null}
    </section>
  );
}

/**
 * Searching every kind of resource the person can see at once, by ID,
 * name or what the kind indexes, results grouped by kind; the text stays in
 * the address, so a search can be shared and comes back.
 */
export default function SearchScreen() {
  const intl = useIntl();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { q?: string };
  const q = typeof search.q === "string" ? search.q : "";
  const [text, setText] = useState(q);
  const kinds = useKinds().data ?? [];
  const found = useResourceSearch(q, 20);
  // The address follows the text once typing settles.
  useEffect(() => {
    if (text.trim() === q) {
      return;
    }
    const timer = setTimeout(
      () =>
        void navigate({
          to: "/search",
          search: text.trim() ? { q: text.trim() } : {},
          replace: true,
        }),
      settle,
    );
    return () => clearTimeout(timer);
  }, [text, q, navigate]);
  const groups = new Map<string, SearchResult[]>();
  for (const r of found.data?.results ?? []) {
    groups.set(r.kind, [...(groups.get(r.kind) ?? []), r]);
  }
  const short = q.trim().length < minSearch;
  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-6">
      <h1 className="font-semibold text-2xl">{intl.formatMessage(m.title)}</h1>
      <div className="relative max-w-xl">
        <Search
          className="pointer-events-none absolute start-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
          aria-hidden="true"
        />
        <Input
          type="search"
          value={text}
          autoFocus
          aria-label={intl.formatMessage(m.label)}
          placeholder={intl.formatMessage(m.placeholder)}
          className="ps-9"
          onChange={(e) => setText(e.target.value)}
        />
      </div>
      {short ? (
        <p className="text-muted-foreground">{intl.formatMessage(m.hint, { min: minSearch })}</p>
      ) : found.isPending ? (
        <div className="flex flex-col gap-3" aria-busy="true">
          <Skeleton className="h-6 w-40" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : found.isError ? (
        <ErrorState
          title={intl.formatMessage(m.failed)}
          error={found.error}
          onRetry={() => void found.refetch()}
        />
      ) : groups.size === 0 ? (
        <EmptyState
          icon={Search}
          title={intl.formatMessage(m.none, { q })}
          description={intl.formatMessage(m.noneHelp)}
        />
      ) : (
        <div className="flex flex-col gap-6" aria-live="polite">
          {[...groups.entries()].map(([kindName, results]) => (
            <KindResults
              key={kindName}
              kind={kinds.find((k) => k.name === kindName)}
              results={results}
              more={(found.data?.more ?? []).includes(kindName)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
