import {
  type QueryKey,
  type UseQueryOptions,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

/** One server-sent event: its type, its data decoded from JSON, and its id. */
export interface LiveEvent<T = unknown> {
  type: string;
  data: T;
  id: string;
}

export type LiveStatus = "connecting" | "open" | "retrying";

type EventSourceFactory = (url: string) => EventSource;

/** What a subscriber hears from a stream. */
export interface LiveHandlers {
  onEvent: (event: LiveEvent) => void;
  onStatus?: (status: LiveStatus) => void;
  /**
   * Events may have been missed: the stream connected again after a break,
   * or the server said the client fell behind. Data kept current by the
   * stream should be read again.
   */
  onReset?: () => void;
}

interface Subscriber extends LiveHandlers {
  types: ReadonlySet<string>;
}

interface Stream {
  source: EventSource | null;
  subscribers: Set<Subscriber>;
  /** Event types the stream listens for. */
  types: Set<string>;
  /** Whether the stream has been open before, so opening now is a reconnect. */
  opened: boolean;
  attempt: number;
  status: LiveStatus;
  retry: ReturnType<typeof setTimeout> | null;
}

/** The event the panel sends when a client fell behind and missed events. */
export const resyncEvent = "resync";

/**
 * Live data from the panel over server-sent events. Every URL gets one
 * connection however many components listen to it, and the connection
 * comes back by itself: the browser retries dropped connections, and when
 * the server refuses one, the client tries again with growing pauses. What
 * happened while it was away may be lost, so every reconnect, and every
 * resync event from the server, tells subscribers to read their data again.
 */
export class LiveClient {
  private streams = new Map<string, Stream>();
  private readonly open: EventSourceFactory;
  private readonly maxDelay: number;

  constructor(options: { open?: EventSourceFactory; maxDelay?: number } = {}) {
    this.open = options.open ?? ((url) => new EventSource(url, { withCredentials: true }));
    this.maxDelay = options.maxDelay ?? 30_000;
  }

  /**
   * Listens for events of the given types at a URL until the returned
   * function is called.
   */
  subscribe(url: string, types: readonly string[], handlers: LiveHandlers): () => void {
    let stream = this.streams.get(url);
    if (!stream) {
      stream = {
        source: null,
        subscribers: new Set(),
        types: new Set([resyncEvent]),
        opened: false,
        attempt: 0,
        status: "connecting",
        retry: null,
      };
      this.streams.set(url, stream);
    }
    const subscriber: Subscriber = { ...handlers, types: new Set(types) };
    const { onStatus } = handlers;
    stream.subscribers.add(subscriber);
    for (const t of types) {
      if (!stream.types.has(t)) {
        stream.types.add(t);
        if (stream.source) {
          this.listen(stream, stream.source, t);
        }
      }
    }
    if (!stream.source && !stream.retry) {
      this.connect(url, stream);
    }
    onStatus?.(stream.status);
    return () => {
      const current = this.streams.get(url);
      if (!current) {
        return;
      }
      current.subscribers.delete(subscriber);
      if (current.subscribers.size === 0) {
        current.source?.close();
        if (current.retry) {
          clearTimeout(current.retry);
        }
        this.streams.delete(url);
      }
    };
  }

  /** Closes every connection. */
  close(): void {
    for (const stream of this.streams.values()) {
      stream.source?.close();
      if (stream.retry) {
        clearTimeout(stream.retry);
      }
    }
    this.streams.clear();
  }

  private setStatus(stream: Stream, status: LiveStatus) {
    stream.status = status;
    for (const s of stream.subscribers) {
      s.onStatus?.(status);
    }
  }

  private reset(stream: Stream) {
    for (const s of stream.subscribers) {
      s.onReset?.();
    }
  }

  private connect(url: string, stream: Stream) {
    stream.source?.close();
    // A new connection can't send Last-Event-ID, so it starts from now.
    const source = this.open(url);
    stream.source = source;
    this.setStatus(stream, "connecting");
    source.addEventListener("open", () => {
      stream.attempt = 0;
      this.setStatus(stream, "open");
      if (stream.opened) {
        this.reset(stream);
      }
      stream.opened = true;
    });
    source.addEventListener("error", () => {
      if (source.readyState === closed) {
        // The server refused the stream; the browser won't retry on its own.
        this.setStatus(stream, "retrying");
        const delay = Math.min(this.maxDelay, 1000 * 2 ** stream.attempt);
        stream.attempt++;
        stream.retry = setTimeout(() => {
          stream.retry = null;
          if (this.streams.get(url) === stream) {
            this.connect(url, stream);
          }
        }, delay);
      } else {
        this.setStatus(stream, "connecting");
      }
    });
    for (const type of stream.types) {
      this.listen(stream, source, type);
    }
  }

  private listen(stream: Stream, source: EventSource, type: string) {
    source.addEventListener(type, (raw) => {
      if (type === resyncEvent) {
        this.reset(stream);
        return;
      }
      const message = raw as MessageEvent<string>;
      let data: unknown = message.data;
      try {
        data = JSON.parse(message.data);
      } catch {
        // Plain text stays text.
      }
      const event: LiveEvent = { type, data, id: message.lastEventId };
      for (const s of stream.subscribers) {
        if (s.types.has(type)) {
          s.onEvent(event);
        }
      }
    });
  }
}

// EventSource.CLOSED, written out so the client also runs where there is no EventSource.
const closed = 2;

const LiveContext = createContext<LiveClient | null>(null);

/** Makes a live client available; tests give one that opens fake event sources. */
export function LiveProvider({ client, children }: { client?: LiveClient; children: ReactNode }) {
  const [own] = useState(() => client ?? new LiveClient());
  useEffect(() => () => own.close(), [own]);
  return <LiveContext value={client ?? own}>{children}</LiveContext>;
}

/** The live client of the surrounding LiveProvider. */
export function useLiveClient(): LiveClient {
  const client = useContext(LiveContext);
  if (!client) {
    throw new Error("useLiveClient needs a LiveProvider above it");
  }
  return client;
}

/**
 * Calls onEvent for every event of the given types from a stream while the
 * component is mounted, and onReset when events may have been missed, and
 * returns the connection's status. A null URL listens to nothing.
 */
export function useLiveEvents<T = unknown>(
  url: string | null,
  types: readonly string[],
  onEvent: (event: LiveEvent<T>) => void,
  onReset?: () => void,
): LiveStatus {
  const client = useLiveClient();
  const [status, setStatus] = useState<LiveStatus>("connecting");
  const handlers = useRef({ onEvent, onReset });
  useEffect(() => {
    handlers.current = { onEvent, onReset };
  });
  const typeKey = types.join("\n");
  useEffect(() => {
    if (url === null) {
      return;
    }
    return client.subscribe(url, typeKey.split("\n"), {
      onEvent: (event) => handlers.current.onEvent(event as LiveEvent<T>),
      onStatus: setStatus,
      onReset: () => handlers.current.onReset?.(),
    });
  }, [client, url, typeKey]);
  return status;
}

export interface LiveQueryOptions<TData, TEvent> {
  queryKey: QueryKey;
  queryFn: () => Promise<TData>;
  /** Where updates to the data stream from, and which event types carry them. */
  stream: { url: string; types: readonly string[] } | null;
  /**
   * How an event changes the cached data. Return undefined to fetch the
   * data again instead, for events too broad to apply.
   */
  apply: (current: TData | undefined, event: LiveEvent<TEvent>) => TData | undefined;
  enabled?: boolean;
  select?: UseQueryOptions<TData>["select"];
}

/**
 * A query kept current by server-sent events instead of polling: the data
 * loads once, then each event updates the cache in place. When events may
 * have been missed, after a reconnect or a resync, it loads again.
 */
export function useLiveQuery<TData, TEvent = unknown>({
  queryKey,
  queryFn,
  stream,
  apply,
  enabled = true,
}: LiveQueryOptions<TData, TEvent>) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey, queryFn, enabled, staleTime: Number.POSITIVE_INFINITY });
  const keyRef = useRef(queryKey);
  const applyRef = useRef(apply);
  useEffect(() => {
    keyRef.current = queryKey;
    applyRef.current = apply;
  });
  const status = useLiveEvents<TEvent>(
    enabled && stream ? stream.url : null,
    stream?.types ?? [],
    (event) => {
      const current = queryClient.getQueryData<TData>(keyRef.current);
      const next = applyRef.current(current, event);
      if (next === undefined) {
        void queryClient.invalidateQueries({ queryKey: keyRef.current });
      } else {
        queryClient.setQueryData<TData>(keyRef.current, next);
      }
    },
    () => void queryClient.invalidateQueries({ queryKey: keyRef.current }),
  );
  return useMemo(() => ({ ...query, liveStatus: status }), [query, status]);
}

/** Puts an item into a list by id, replacing an older copy or adding it at the start. */
export function upsertById<T extends { id: string }>(list: readonly T[] | undefined, item: T): T[] {
  const items = list ?? [];
  const index = items.findIndex((x) => x.id === item.id);
  if (index === -1) {
    return [item, ...items];
  }
  const next = [...items];
  next[index] = item;
  return next;
}
