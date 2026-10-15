import { useMemo, useRef } from "react";
import type uPlot from "uplot";
import { useOptionalTheme } from "@/design/theme/provider";
import { cn } from "@/lib/utils";
import { readChartColors, useUPlot, useWidth, withAlpha } from "./uplot";

export interface SparklineProps {
  /** Values in time order. */
  values: readonly (number | null)[];
  /** Which chart color to draw in, from 0. */
  series?: number;
  height?: number;
  /** What the line shows, with its latest value, for screen readers: "CPU, 23% now". */
  label: string;
  className?: string;
}

/**
 * A tiny line chart without axes, for a trend next to a number. It's an
 * image to screen readers, described by its label.
 */
export function Sparkline({ values, series = 0, height = 32, label, className }: SparklineProps) {
  const container = useRef<HTMLDivElement>(null);
  const width = useWidth(container, 120);
  const tokensKey = useOptionalTheme()?.tokensKey;
  const data = useMemo<uPlot.AlignedData>(
    () => [values.map((_, i) => i), values.map((v) => v ?? null)],
    [values],
  );
  // biome-ignore lint/correctness/useExhaustiveDependencies: tokensKey changes when the colors read here do
  const options = useMemo<uPlot.Options>(() => {
    const colors = readChartColors(container.current ?? undefined);
    const stroke = colors.series[series % colors.series.length] ?? colors.text;
    return {
      width,
      height,
      pxAlign: false,
      cursor: { show: false },
      legend: { show: false },
      select: { show: false, left: 0, top: 0, width: 0, height: 0 },
      scales: { x: { time: false } },
      axes: [{ show: false }, { show: false }],
      series: [{}, { stroke, width: 2, fill: withAlpha(stroke, 0.12), points: { show: false } }],
    };
  }, [width, height, series, tokensKey]);
  useUPlot(container, options, data);
  return (
    <div
      ref={container}
      role="img"
      aria-label={label}
      data-slot="sparkline"
      className={cn("w-full", className)}
      style={{ height }}
    />
  );
}
