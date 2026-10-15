import "./chart.css";
import { type RefObject, useEffect, useLayoutEffect, useRef, useState } from "react";
import uPlot from "uplot";
import { readChartPalette, readToken } from "@/design/tokens";

/** A color with an alpha, as #RRGGBBAA, which canvases read. */
export function withAlpha(hex: string, alpha: number): string {
  const a = Math.round(Math.max(0, Math.min(1, alpha)) * 255)
    .toString(16)
    .padStart(2, "0");
  return /^#[0-9a-fA-F]{6}$/.test(hex) ? `${hex}${a}` : hex;
}

/** The token values a chart draws with, read when the theme or mode changes. */
export interface ChartColors {
  series: string[];
  text: string;
  muted: string;
  grid: string;
  axis: string;
  surface: string;
  info: string;
  warning: string;
  danger: string;
}

export function readChartColors(element?: Element): ChartColors {
  const at = element ?? document.documentElement;
  const series = readChartPalette(at);
  return {
    series: series.length > 0 ? series : [readToken("accent", at)],
    text: readToken("text", at),
    muted: readToken("text-muted", at),
    grid: readToken("border", at),
    axis: readToken("border-strong", at),
    surface: readToken("surface-raised", at),
    info: readToken("info", at),
    warning: readToken("warning", at),
    danger: readToken("danger", at),
  };
}

/** Tracks an element's width, for charts that fill their container. */
export function useWidth(ref: RefObject<HTMLElement | null>, fallback = 600): number {
  const [width, setWidth] = useState(fallback);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) {
      return;
    }
    const measure = () => {
      const w = el.getBoundingClientRect().width;
      if (w > 0) {
        setWidth(Math.floor(w));
      }
    };
    measure();
    if (typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [ref]);
  return width;
}

/**
 * Draws a uPlot chart into a container and returns it once drawn. The chart
 * is built again when the options change, and only gets new data otherwise.
 */
export function useUPlot(
  container: RefObject<HTMLElement | null>,
  options: uPlot.Options | null,
  data: uPlot.AlignedData,
): uPlot | null {
  const [chart, setChart] = useState<uPlot | null>(null);
  const latestData = useRef(data);
  useLayoutEffect(() => {
    latestData.current = data;
  });

  useEffect(() => {
    const el = container.current;
    if (!el || !options) {
      return;
    }
    const created = new uPlot(options, latestData.current, el);
    setChart(created);
    return () => {
      created.destroy();
      setChart(null);
    };
  }, [container, options]);

  useEffect(() => {
    chart?.setData(data);
  }, [chart, data]);

  return chart;
}
