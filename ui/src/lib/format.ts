import { useMemo } from "react";
import { type FormatNumberOptions, type IntlShape, useIntl } from "react-intl";
import { formatMessages } from "./format-messages";

const decimalUnits = ["byte", "kilobyte", "megabyte", "gigabyte", "terabyte", "petabyte"] as const;
const binaryMessages = [
  formatMessages.bytes,
  formatMessages.kibibytes,
  formatMessages.mebibytes,
  formatMessages.gibibytes,
  formatMessages.tebibytes,
  formatMessages.pebibytes,
];

export interface BytesOptions {
  /** Powers of 1024 with KiB, MiB and so on, as memory is usually counted. */
  binary?: boolean;
  /** A rate, such as network or disk throughput. */
  perSecond?: boolean;
}

/** Picks the unit a size reads best in, and the size in that unit. */
export function scaleBytes(bytes: number, base: 1000 | 1024): { value: number; exponent: number } {
  let value = Math.abs(bytes);
  let exponent = 0;
  while (value >= base && exponent < decimalUnits.length - 1) {
    value /= base;
    exponent++;
  }
  return { value: Math.sign(bytes) * value, exponent };
}

/**
 * Formatters for numbers, sizes, durations, dates and times in the user's
 * locale. Dates and times show in the viewer's time zone; the server keeps
 * everything in UTC.
 */
export function createFormatters(intl: IntlShape) {
  const digits = (v: number) => (Math.abs(v) < 10 && v !== Math.trunc(v) ? 1 : 0);
  return {
    number: (value: number, options?: FormatNumberOptions) => intl.formatNumber(value, options),
    /** Short forms of large numbers: 1.2K, 3.4M, in the locale's words. */
    compact: (value: number) =>
      intl.formatNumber(value, { notation: "compact", maximumFractionDigits: 1 }),
    /** A share from 0 to 1, as a percentage. */
    percent: (share: number, maximumFractionDigits = 1) =>
      intl.formatNumber(share, { style: "percent", maximumFractionDigits }),
    bytes: (bytes: number, { binary = false, perSecond = false }: BytesOptions = {}) => {
      const { value, exponent } = scaleBytes(bytes, binary ? 1024 : 1000);
      const maximumFractionDigits = digits(value);
      if (binary) {
        const message = binaryMessages[exponent] ?? formatMessages.bytes;
        const text = intl.formatMessage(message, {
          value: intl.formatNumber(value, { maximumFractionDigits }),
        });
        return perSecond ? intl.formatMessage(formatMessages.perSecond, { value: text }) : text;
      }
      const unit = decimalUnits[exponent] ?? "byte";
      return intl.formatNumber(value, {
        style: "unit",
        unit: perSecond ? `${unit}-per-second` : unit,
        unitDisplay: "short",
        maximumFractionDigits,
      });
    },
    /** A duration in seconds, in its two largest units: 2 hr, 5 min. */
    duration: (seconds: number) => {
      const units: [Intl.NumberFormatOptions["unit"], number][] = [
        ["day", 86400],
        ["hour", 3600],
        ["minute", 60],
        ["second", 1],
      ];
      let rest = Math.max(0, Math.round(seconds));
      const parts: string[] = [];
      for (const [unit, size] of units) {
        const n = Math.floor(rest / size);
        if (n > 0 || (unit === "second" && parts.length === 0)) {
          parts.push(intl.formatNumber(n, { style: "unit", unit, unitDisplay: "short" }));
          rest -= n * size;
        }
        if (parts.length === 2) {
          break;
        }
      }
      return intl.formatList(parts, { type: "unit", style: "narrow" });
    },
    date: (date: Date | number) => intl.formatDate(date, { dateStyle: "medium" }),
    time: (date: Date | number) => intl.formatTime(date, { timeStyle: "short" }),
    dateTime: (date: Date | number) =>
      intl.formatDate(date, { dateStyle: "medium", timeStyle: "short" }),
    /** How long ago or how far ahead a moment is: "5 minutes ago", "tomorrow". */
    relative: (date: Date | number, now: Date | number = Date.now()) => {
      const seconds = (new Date(date).getTime() - new Date(now).getTime()) / 1000;
      const abs = Math.abs(seconds);
      const [value, unit]: [number, Intl.RelativeTimeFormatUnit] =
        abs < 45
          ? [seconds, "second"]
          : abs < 45 * 60
            ? [seconds / 60, "minute"]
            : abs < 22 * 3600
              ? [seconds / 3600, "hour"]
              : abs < 26 * 86400
                ? [seconds / 86400, "day"]
                : abs < 320 * 86400
                  ? [seconds / (30 * 86400), "month"]
                  : [seconds / (365 * 86400), "year"];
      return intl.formatRelativeTime(Math.round(value), unit, { numeric: "auto" });
    },
  };
}

export type Formatters = ReturnType<typeof createFormatters>;

/** The formatters for the current locale. */
export function useFormat(): Formatters {
  const intl = useIntl();
  return useMemo(() => createFormatters(intl), [intl]);
}
