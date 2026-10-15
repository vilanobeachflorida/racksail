import { Flag } from "lucide-react";
import { useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useIntl } from "react-intl";
import type uPlot from "uplot";
import { useOptionalTheme } from "@/design/theme/provider";
import { useFormat } from "@/lib/format";
import { cn, withKeys } from "@/lib/utils";
import { designMessages as m } from "../../messages";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../ui/display";
import { Tooltip } from "../ui/overlay";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { readChartColors, useUPlot, useWidth, withAlpha } from "./uplot";

export interface Series {
  /** The series' name, translated: "Requests". */
  label: string;
  /** One value per time, null where there's no data. */
  values: readonly (number | null)[];
  /**
   * Which chart color to draw in, from 0; by default the series' place in
   * the list. Give it when series can be filtered, so each keeps its color.
   * Charts have as many series as the theme has chart colors at most: fold
   * the rest into one "Other" series.
   */
  color?: number;
}

/** Something that happened, drawn over the chart where it happened: a deploy, a restart. */
export interface ChartEvent {
  time: Date | number;
  label: string;
  severity?: "info" | "warning" | "error";
}

export interface TimeSeriesProps {
  /** What the chart shows, translated; it titles the chart for everyone. */
  title: string;
  /** Times, in milliseconds since the epoch, one per value. */
  times: readonly number[];
  series: readonly Series[];
  /** How values read on the axis and in the tooltip. */
  format?: (value: number) => string;
  events?: readonly ChartEvent[];
  height?: number;
  /** Fills the area under a single series. */
  area?: boolean;
  className?: string;
}

interface Hover {
  index: number;
  left: number;
  top: number;
}

// One empty list for every chart without events: a new one per render would
// rebuild the chart on every render.
const noEvents: readonly ChartEvent[] = [];

function sameHover(a: Hover | null, b: Hover | null): boolean {
  return (
    a === b ||
    (a !== null && b !== null && a.index === b.index && a.left === b.left && a.top === b.top)
  );
}

/**
 * A chart of values over time with one y-axis, a crosshair tooltip, a
 * legend that names every series, the event timeline drawn over it, and a
 * table of the same data for screen readers and people who prefer one.
 */
export function TimeSeries({
  title,
  times,
  series,
  format,
  events = noEvents,
  height = 220,
  area = series.length === 1,
  className,
}: TimeSeriesProps) {
  const intl = useIntl();
  const titleId = useId();
  const f = useFormat();
  const container = useRef<HTMLDivElement>(null);
  const width = useWidth(container);
  const tokensKey = useOptionalTheme()?.tokensKey;
  const [hover, setHover] = useState<Hover | null>(null);
  const formatValue = format ?? ((v: number) => f.compact(v));
  const formatRef = useRef(formatValue);
  useLayoutEffect(() => {
    formatRef.current = formatValue;
  });

  const data = useMemo<uPlot.AlignedData>(
    () => [times.map((t) => t / 1000), ...series.map((s) => s.values.map((v) => v ?? null))],
    [times, series],
  );
  const eventTimes = useMemo(() => events.map((e) => new Date(e.time).getTime() / 1000), [events]);
  const eventKeys = useMemo(
    () => withKeys(events, (e) => `${new Date(e.time).getTime()}:${e.label}`).map(([key]) => key),
    [events],
  );

  // The chart is built again when its series' colors or names change, not
  // for every new array of the same series.
  const seriesKey = series.map((s, i) => `${s.color ?? i}:${s.label}`).join("\n");
  // biome-ignore lint/correctness/useExhaustiveDependencies: tokensKey changes when the colors read here do, and seriesKey when the series do
  const options = useMemo<uPlot.Options>(() => {
    const colors = readChartColors(container.current ?? undefined);
    const font = `12px ${getComputedStyle(document.documentElement).getPropertyValue("--rs-font-sans") || "sans-serif"}`;
    const axis = {
      stroke: colors.muted,
      font,
      grid: { stroke: colors.grid, width: 1 },
      ticks: { stroke: colors.axis, width: 1, size: 4 },
    };
    const severityColor = { info: colors.info, warning: colors.warning, error: colors.danger };
    return {
      width,
      height,
      legend: { show: false },
      cursor: { points: { size: 8, width: 2 }, drag: { x: false, y: false } },
      scales: { x: { time: true } },
      axes: [
        {
          ...axis,
          values: (_u: uPlot, ticks: number[]) => ticks.map((t) => f.time(t * 1000)),
        },
        {
          ...axis,
          size: 56,
          values: (_u: uPlot, ticks: number[]) => ticks.map((t) => formatRef.current(t)),
        },
      ],
      series: [
        {},
        ...series.map((s, i) => {
          const stroke = colors.series[(s.color ?? i) % colors.series.length] ?? colors.text;
          return {
            label: s.label,
            stroke,
            width: 2,
            fill: area ? withAlpha(stroke, 0.12) : undefined,
            points: { show: false },
            spanGaps: false,
          };
        }),
      ],
      hooks: {
        draw: [
          (u: uPlot) => {
            const ctx = u.ctx;
            ctx.save();
            ctx.lineWidth = Math.max(1, devicePixelRatio);
            ctx.setLineDash([4 * devicePixelRatio, 3 * devicePixelRatio]);
            events.forEach((e, i) => {
              const x = u.valToPos(eventTimes[i] ?? 0, "x", true);
              if (x < u.bbox.left || x > u.bbox.left + u.bbox.width) {
                return;
              }
              ctx.strokeStyle = severityColor[e.severity ?? "info"];
              ctx.beginPath();
              ctx.moveTo(x, u.bbox.top);
              ctx.lineTo(x, u.bbox.top + u.bbox.height);
              ctx.stroke();
            });
            ctx.restore();
          },
        ],
        setCursor: [
          (u: uPlot) => {
            const { idx, left, top } = u.cursor;
            const next =
              idx === null || idx === undefined || left === undefined || left < 0
                ? null
                : { index: idx, left, top: top ?? 0 };
            // Redraws report the cursor too; only a move should render again.
            setHover((prev) => (sameHover(prev, next) ? prev : next));
          },
        ],
      },
    };
  }, [width, height, seriesKey, area, eventTimes, events, f, tokensKey]);
  const plot = useUPlot(container, options, data);
  // biome-ignore lint/correctness/useExhaustiveDependencies: tokensKey changes when the colors read here do
  const colors = useMemo(() => readChartColors(container.current ?? undefined), [tokensKey, plot]);

  const hoverTime = hover ? times[hover.index] : undefined;
  return (
    <figure
      data-slot="time-series"
      aria-labelledby={titleId}
      className={cn("flex min-w-0 flex-col gap-2", className)}
    >
      <figcaption className="flex flex-wrap items-center justify-between gap-2">
        <span id={titleId} className="font-medium text-sm">
          {title}
        </span>
        {series.length > 1 ? (
          <ul
            className="flex flex-wrap gap-3 text-muted-foreground text-xs"
            aria-label={intl.formatMessage(m.chartLegend)}
          >
            {series.map((s, i) => (
              <li key={s.label} className="flex items-center gap-1.5">
                <span
                  aria-hidden="true"
                  className="h-0.5 w-3 rounded-full"
                  style={{ backgroundColor: colors.series[(s.color ?? i) % colors.series.length] }}
                />
                {s.label}
              </li>
            ))}
          </ul>
        ) : null}
      </figcaption>
      <div className="relative">
        <div ref={container} aria-hidden="true" style={{ height }} />
        {events.map((e, i) => {
          const x = plot ? plot.valToPos(eventTimes[i] ?? 0, "x") + plot.over.offsetLeft : null;
          if (
            x === null ||
            Number.isNaN(x) ||
            (plot &&
              (x < plot.over.offsetLeft || x > plot.over.offsetLeft + plot.over.clientWidth + 1))
          ) {
            return null;
          }
          return (
            <Tooltip key={eventKeys[i]} content={`${f.dateTime(e.time)}: ${e.label}`}>
              <button
                type="button"
                className={cn(
                  "absolute top-0 flex size-5 -translate-x-1/2 items-center justify-center rounded-full border bg-card focus-visible:focus-ring",
                  e.severity === "error"
                    ? "border-danger text-danger"
                    : e.severity === "warning"
                      ? "border-warning text-warning"
                      : "border-info text-info",
                )}
                // Time runs left to right on the canvas in every direction.
                style={{ left: x }}
                aria-label={intl.formatMessage(m.chartEvent, {
                  time: f.dateTime(e.time),
                  event: e.label,
                })}
              >
                <Flag className="size-3" aria-hidden="true" />
              </button>
            </Tooltip>
          );
        })}
        {hover && hoverTime !== undefined ? (
          <div
            role="presentation"
            className="pointer-events-none absolute z-10 min-w-36 rounded-md border border-border bg-popover px-3 py-2 text-xs shadow-md"
            style={{
              left: Math.min(
                hover.left + (plot?.over.offsetLeft ?? 0) + 12,
                Math.max(0, width - 180),
              ),
              top: Math.max(0, hover.top - 16),
            }}
          >
            <p className="mb-1 font-medium">
              <time dateTime={new Date(hoverTime).toISOString()}>{f.dateTime(hoverTime)}</time>
            </p>
            {series.map((s, i) => {
              const v = s.values[hover.index];
              return (
                <p key={s.label} className="flex items-center justify-between gap-3">
                  <span className="flex items-center gap-1.5 text-muted-foreground">
                    <span
                      aria-hidden="true"
                      className="size-2 rounded-full"
                      style={{
                        backgroundColor: colors.series[(s.color ?? i) % colors.series.length],
                      }}
                    />
                    {s.label}
                  </span>
                  <span className="tabular-nums">
                    {v === null || v === undefined
                      ? intl.formatMessage(m.chartNoValue)
                      : formatValue(v)}
                  </span>
                </p>
              );
            })}
          </div>
        ) : null}
      </div>
      <Collapsible>
        <CollapsibleTrigger className="text-muted-foreground text-xs">
          {intl.formatMessage(m.chartTable)}
        </CollapsibleTrigger>
        <CollapsibleContent className="mt-2 max-h-72 overflow-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{intl.formatMessage(m.chartTime)}</TableHead>
                {series.map((s) => (
                  <TableHead key={s.label} className="text-end">
                    {s.label}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {times.map((t, row) => (
                <TableRow key={t}>
                  <TableCell>
                    <time dateTime={new Date(t).toISOString()}>{f.dateTime(t)}</time>
                  </TableCell>
                  {series.map((s) => {
                    const v = s.values[row];
                    return (
                      <TableCell key={s.label} className="text-end tabular-nums">
                        {v === null || v === undefined
                          ? intl.formatMessage(m.chartNoValue)
                          : formatValue(v)}
                      </TableCell>
                    );
                  })}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CollapsibleContent>
      </Collapsible>
    </figure>
  );
}
