import { lazy, Suspense } from "react";
import { Skeleton } from "../ui/skeleton";
import type { TerminalProps } from "./terminal";

export type { TerminalProps } from "./terminal";
export { type TerminalTransport, webSocketTransport } from "./transport";

const XTermTerminal = lazy(() => import("./terminal"));

/** The web terminal, loaded the first time one opens so xterm.js stays out of the first download. */
export function Terminal(props: TerminalProps) {
  return (
    <Suspense fallback={<Skeleton className={props.className ?? "h-64 w-full"} />}>
      <XTermTerminal {...props} />
    </Suspense>
  );
}
