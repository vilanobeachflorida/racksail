import type { FieldDiff } from "@/lib/changes";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

function join(base: string, key: string | number): string {
  if (typeof key === "number") {
    return `${base}[${key}]`;
  }
  return base === "" ? key : `${base}.${key}`;
}

/** The path of a field without the places of list items: events[1].type is events[].type. */
function shape(path: string): string {
  return path.replace(/\[\d+\]/g, "[]");
}

/**
 * The fields that differ between two versions of a spec, in the form change
 * sets write them: objects field by field, lists of objects item by item,
 * and lists of plain values as a whole, which reads better. Secret fields
 * hold references to their values; they show as changed, never what to.
 */
export function diffSpecs(
  before: unknown,
  after: unknown,
  sensitive: readonly string[] = [],
  path = "",
): FieldDiff[] {
  if (same(before, after)) {
    return [];
  }
  const secret = path !== "" && sensitive.includes(shape(path));
  if (!secret && isRecord(before) && isRecord(after)) {
    const keys = [...new Set([...Object.keys(before), ...Object.keys(after)])].sort();
    return keys.flatMap((key) => diffSpecs(before[key], after[key], sensitive, join(path, key)));
  }
  if (
    !secret &&
    Array.isArray(before) &&
    Array.isArray(after) &&
    [...before, ...after].some((item) => typeof item === "object" && item !== null)
  ) {
    const out: FieldDiff[] = [];
    for (let i = 0; i < Math.max(before.length, after.length); i++) {
      out.push(...diffSpecs(before[i], after[i], sensitive, join(path, i)));
    }
    return out;
  }
  const op: FieldDiff["op"] =
    before === undefined ? "add" : after === undefined ? "remove" : "replace";
  if (secret) {
    return [{ path, op, sensitive: true }];
  }
  return [
    {
      path,
      op,
      ...(before === undefined ? {} : { before }),
      ...(after === undefined ? {} : { after }),
    },
  ];
}
