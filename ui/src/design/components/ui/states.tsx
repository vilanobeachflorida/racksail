import { CircleAlert, type LucideIcon } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { useIntl } from "react-intl";
import { errorMessage } from "@/lib/errors";
import { cn } from "@/lib/utils";
import { designMessages } from "../../messages";
import { Button } from "./button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "./display";

export interface EmptyStateProps extends Omit<ComponentProps<"div">, "title"> {
  icon?: LucideIcon;
  /** What isn't here yet, in a few words: "No sites yet". */
  title: ReactNode;
  /** What this place is for and how to fill it. */
  description?: ReactNode;
  /** The first thing to do, usually one primary button. */
  action?: ReactNode;
}

/** What a list or screen shows when there's nothing in it yet. */
export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
  className,
  ...props
}: EmptyStateProps) {
  return (
    <div
      data-slot="empty-state"
      className={cn(
        "flex flex-col items-center justify-center gap-3 rounded-lg border border-border border-dashed px-6 py-10 text-center",
        className,
      )}
      {...props}
    >
      {Icon ? (
        <div className="flex size-12 items-center justify-center rounded-full bg-muted text-muted-foreground">
          <Icon className="size-6" aria-hidden="true" />
        </div>
      ) : null}
      <div className="flex max-w-md flex-col gap-1">
        <p className="font-semibold text-base">{title}</p>
        {description ? <p className="text-muted-foreground text-sm">{description}</p> : null}
      </div>
      {action ? <div className="mt-1 flex flex-wrap justify-center gap-2">{action}</div> : null}
    </div>
  );
}

export interface ErrorStateProps extends Omit<ComponentProps<"div">, "title"> {
  /** What failed, in a few words: "The sites couldn't load". */
  title: ReactNode;
  /** The error; its message says what happened and what to do. */
  error?: unknown;
  /** Tries again, such as refetching a query. */
  onRetry?: () => void;
  /** Technical details for people who report the problem, hidden at first. */
  details?: ReactNode;
}

/** What a list or screen shows when loading it failed. */
export function ErrorState({
  title,
  error,
  onRetry,
  details,
  className,
  ...props
}: ErrorStateProps) {
  const intl = useIntl();
  return (
    <div
      data-slot="error-state"
      role="alert"
      className={cn(
        "flex flex-col items-start gap-3 rounded-lg border border-danger/40 bg-danger-subtle p-card",
        className,
      )}
      {...props}
    >
      <p className="flex items-center gap-2 font-semibold">
        <CircleAlert className="size-4 shrink-0 text-danger" aria-hidden="true" />
        {title}
      </p>
      {error !== undefined ? <p className="text-sm">{errorMessage(intl, error)}</p> : null}
      {details ? (
        <Collapsible>
          <CollapsibleTrigger className="text-muted-foreground text-xs">
            {intl.formatMessage(designMessages.details)}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <pre className="mt-2 max-h-48 overflow-auto rounded-md bg-surface-sunken p-3 font-mono text-xs">
              {details}
            </pre>
          </CollapsibleContent>
        </Collapsible>
      ) : null}
      {onRetry ? (
        <Button variant="outline" size="sm" onClick={onRetry}>
          {intl.formatMessage(designMessages.retry)}
        </Button>
      ) : null}
    </div>
  );
}

export type Status = "ok" | "warning" | "error" | "pending" | "off" | "unknown";

const dotColors: Record<Status, string> = {
  ok: "bg-success",
  warning: "bg-warning",
  error: "bg-danger",
  pending: "bg-info animate-pulse",
  off: "bg-border-strong",
  unknown: "bg-transparent border border-border-strong",
};

/**
 * A small colored dot with a label, for the state of a service, a site or
 * a check. The label is always there, so the state never rests on color.
 */
export function StatusDot({
  status,
  label,
  className,
  hideLabel,
}: {
  status: Status;
  label: ReactNode;
  className?: string;
  /** Keeps the label for screen readers only, where space is short. */
  hideLabel?: boolean;
}) {
  return (
    <span
      data-slot="status-dot"
      data-status={status}
      className={cn("inline-flex items-center gap-2 text-sm", className)}
    >
      <span aria-hidden="true" className={cn("size-2 shrink-0 rounded-full", dotColors[status])} />
      <span className={hideLabel ? "sr-only" : undefined}>{label}</span>
    </span>
  );
}

export interface StatTileProps extends Omit<ComponentProps<"div">, "title"> {
  label: ReactNode;
  /** The number, already formatted for the locale. */
  value: ReactNode;
  /** A comparison, such as "+12% since last week". */
  delta?: ReactNode;
  /** Whether the change is good, bad or neither, shown with an icon and color. */
  trend?: "up-good" | "up-bad" | "down-good" | "down-bad" | "flat";
  /** A sparkline or other small chart under the value. */
  chart?: ReactNode;
  icon?: LucideIcon;
}

/** One number that matters, with its label and how it's changing. */
export function StatTile({
  label,
  value,
  delta,
  trend = "flat",
  chart,
  icon: Icon,
  className,
  ...props
}: StatTileProps) {
  const tone =
    trend === "up-good" || trend === "down-good"
      ? "text-success"
      : trend === "up-bad" || trend === "down-bad"
        ? "text-danger"
        : "text-muted-foreground";
  const arrow = trend.startsWith("up") ? "↑" : trend.startsWith("down") ? "↓" : "→";
  return (
    <div
      data-slot="stat-tile"
      className={cn(
        "flex min-w-0 flex-col gap-1 rounded-lg border border-border bg-card p-card shadow-sm",
        className,
      )}
      {...props}
    >
      <p className="flex items-center gap-2 text-muted-foreground text-sm">
        {Icon ? <Icon className="size-4" aria-hidden="true" /> : null}
        {label}
      </p>
      <p className="font-semibold text-2xl tabular-nums leading-tight">{value}</p>
      {delta ? (
        <p className={cn("flex items-center gap-1 text-xs", tone)}>
          <span aria-hidden="true">{arrow}</span>
          {delta}
        </p>
      ) : null}
      {chart ? <div className="mt-2">{chart}</div> : null}
    </div>
  );
}
