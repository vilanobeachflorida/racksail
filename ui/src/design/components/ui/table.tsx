import {
  type ComponentProps,
  createContext,
  type RefObject,
  useContext,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { cn } from "@/lib/utils";

/** Whether an element's content is wider than it, so that it scrolls sideways. */
function useScrollsSideways(ref: RefObject<HTMLElement | null>): boolean {
  const [scrolls, setScrolls] = useState(false);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) {
      return;
    }
    const measure = () => setScrolls(el.scrollWidth > el.clientWidth);
    measure();
    if (typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    if (el.firstElementChild) {
      observer.observe(el.firstElementChild);
    }
    return () => observer.disconnect();
  }, [ref]);
  return scrolls;
}

/** How a table's caption tells the table which ID it has, or that it's gone. */
const CaptionContext = createContext<{
  id: string;
  setCaption: (id: string | null) => void;
} | null>(null);

/**
 * A plain table for short lists; DataTable handles long ones. One wider
 * than the screen scrolls sideways, and takes focus then, so the keyboard
 * can scroll it too. It's then a region named as the table is, by its
 * aria-labelledby, its aria-label or its caption, so a screen reader says
 * what took focus. Name every table: without a name the container can't be
 * a region, and takes focus without saying what it holds.
 */
export function Table({ className, ...props }: ComponentProps<"table">) {
  const container = useRef<HTMLDivElement>(null);
  const scrolls = useScrollsSideways(container);
  const captionId = useId();
  const [caption, setCaption] = useState<string | null>(null);
  const captions = useMemo(() => ({ id: captionId, setCaption }), [captionId]);
  // A table's name comes from its aria-labelledby, else its aria-label,
  // else its caption.
  const label = props["aria-labelledby"] ? undefined : props["aria-label"];
  const labelledBy = props["aria-labelledby"] ?? (label ? undefined : (caption ?? undefined));
  return (
    // The ring is drawn inside, since screens put tables in other scrolling
    // containers, which would clip one outside.
    // biome-ignore lint/a11y/useAriaPropsSupportedByRole: the container is named only while it scrolls, when it's a region
    <div
      ref={container}
      data-slot="table-container"
      className="relative w-full overflow-x-auto focus-visible:focus-ring-inset"
      role={scrolls && (label || labelledBy) ? "region" : undefined}
      aria-label={scrolls ? label : undefined}
      aria-labelledby={scrolls ? labelledBy : undefined}
      tabIndex={scrolls ? 0 : undefined}
    >
      <CaptionContext.Provider value={captions}>
        <table
          data-slot="table"
          className={cn("w-full caption-bottom border-collapse text-sm", className)}
          {...props}
        />
      </CaptionContext.Provider>
    </div>
  );
}

export function TableHeader({ className, ...props }: ComponentProps<"thead">) {
  return (
    <thead data-slot="table-header" className={cn("bg-surface-sunken", className)} {...props} />
  );
}

export function TableBody({ className, ...props }: ComponentProps<"tbody">) {
  return <tbody data-slot="table-body" className={className} {...props} />;
}

export function TableRow({ className, ...props }: ComponentProps<"tr">) {
  return (
    <tr
      data-slot="table-row"
      className={cn(
        "border-border border-b transition-colors hover:bg-muted/60 data-[state=selected]:bg-primary-subtle",
        className,
      )}
      {...props}
    />
  );
}

export function TableHead({ className, ...props }: ComponentProps<"th">) {
  return (
    <th
      data-slot="table-head"
      className={cn(
        "h-row whitespace-nowrap px-cell-x text-start align-middle font-medium text-muted-foreground text-xs",
        className,
      )}
      {...props}
    />
  );
}

export function TableCell({ className, ...props }: ComponentProps<"td">) {
  return (
    <td
      data-slot="table-cell"
      className={cn("px-cell-x py-cell-y align-middle", className)}
      {...props}
    />
  );
}

export function TableCaption({ className, id, ...props }: ComponentProps<"caption">) {
  const table = useContext(CaptionContext);
  const own = id ?? table?.id;
  useLayoutEffect(() => {
    if (!table || !own) {
      return;
    }
    table.setCaption(own);
    return () => table.setCaption(null);
  }, [table, own]);
  return (
    <caption
      data-slot="table-caption"
      id={own}
      className={cn("mt-3 text-muted-foreground text-sm", className)}
      {...props}
    />
  );
}
