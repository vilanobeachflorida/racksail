import { Popover as PopoverPrimitive } from "radix-ui";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";
import { floatingClasses } from "./overlay";

// Popovers are only used by screens that load later, such as the data
// table's filters, so they live apart from the tooltips and menus the shell
// shows itself, which keeps Radix's popover out of the shell's first load.

export const Popover = PopoverPrimitive.Root;
export const PopoverTrigger = PopoverPrimitive.Trigger;
export const PopoverAnchor = PopoverPrimitive.Anchor;
export const PopoverClose = PopoverPrimitive.Close;

export function PopoverContent({
  className,
  align = "center",
  sideOffset = 6,
  ...props
}: ComponentProps<typeof PopoverPrimitive.Content>) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        data-slot="popover-content"
        align={align}
        sideOffset={sideOffset}
        className={cn(floatingClasses, "w-72 p-4 outline-none", className)}
        {...props}
      />
    </PopoverPrimitive.Portal>
  );
}
