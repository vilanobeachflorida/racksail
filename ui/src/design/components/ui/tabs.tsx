import { Tabs as TabsPrimitive } from "radix-ui";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export const Tabs = TabsPrimitive.Root;

export function TabsList({ className, ...props }: ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      // The baseline is a shadow rather than a border, so the active tab's
      // underline can cover it without overflowing the scrolling list.
      className={cn(
        "inline-flex max-w-full items-center gap-1 self-start overflow-x-auto shadow-[inset_0_-1px_0_var(--color-border)]",
        className,
      )}
      {...props}
    />
  );
}

export function TabsTrigger({ className, ...props }: ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      className={cn(
        "inline-flex h-control-md items-center gap-2 whitespace-nowrap border-transparent border-b-2 px-3 font-medium text-muted-foreground text-sm transition-colors hover:text-foreground focus-visible:focus-ring-inset disabled:pointer-events-none disabled:opacity-50 data-[state=active]:border-primary data-[state=active]:text-foreground [&_svg]:size-4",
        className,
      )}
      {...props}
    />
  );
}

export function TabsContent({ className, ...props }: ComponentProps<typeof TabsPrimitive.Content>) {
  return (
    <TabsPrimitive.Content
      data-slot="tabs-content"
      className={cn("pt-4 outline-none focus-visible:focus-ring", className)}
      {...props}
    />
  );
}
