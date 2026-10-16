import { type ComponentProps, type RefObject, useLayoutEffect, useRef, useState } from "react";
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

/**
 * A plain table for short lists; DataTable handles long ones. One wider
 * than the screen scrolls sideways, and takes focus then, so the keyboard
 * can scroll it too.
 */
export function Table({ className, ...props }: ComponentProps<"table">) {
  const container = useRef<HTMLDivElement>(null);
  const scrolls = useScrollsSideways(container);
  return (
    <div
      ref={container}
      data-slot="table-container"
      className="relative w-full overflow-x-auto focus-visible:focus-ring"
      tabIndex={scrolls ? 0 : undefined}
    >
      <table
        data-slot="table"
        className={cn("w-full caption-bottom border-collapse text-sm", className)}
        {...props}
      />
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

export function TableCaption({ className, ...props }: ComponentProps<"caption">) {
  return (
    <caption
      data-slot="table-caption"
      className={cn("mt-3 text-muted-foreground text-sm", className)}
      {...props}
    />
  );
}
