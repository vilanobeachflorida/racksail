/**
 * Change sets and jobs as the panel's API returns them: the JSON forms of
 * internal/changeset and internal/jobs. Every change, whatever started it,
 * becomes a change set that is previewed, applied as a job and can be
 * undone. Lists the API sends as null when they're empty are typed so.
 */

export type Risk = "read" | "low" | "medium" | "high" | "critical";

export interface Scope {
  level: "server" | "reseller" | "account";
  id?: string;
}

/** Who contributed part of a change set. */
export interface ChangeSource {
  type: "request" | "hook" | "rule" | "addon" | "module";
  /** Such as an add-on id and version. */
  name?: string;
  /** The hook point the contribution came through. */
  hook?: string;
}

export interface FieldDiff {
  /** The field, with dots and indexes: domains[0]. */
  path: string;
  op: "add" | "remove" | "replace";
  before?: unknown;
  after?: unknown;
  /** Secret fields are left out of diffs. */
  sensitive?: boolean;
}

export interface ResourceChange {
  op: "create" | "update" | "delete" | "action";
  action?: string;
  kind: string;
  id: string;
  scope: Scope;
  server?: string;
  labels?: Record<string, string>;
  /** The spec before the change; secret fields hold references, never values. */
  before?: unknown;
  /** The spec after the change. */
  after?: unknown;
  diff?: FieldDiff[] | null;
  source: ChangeSource;
}

export type EffectType =
  | "download"
  | "package"
  | "port"
  | "service"
  | "file"
  | "user"
  | "storage"
  | "network"
  | "firewall"
  | "kernel"
  | "certificate"
  | "process";

/** One planned change to the machine. */
export interface Effect {
  type: EffectType;
  /** A verb: install, remove, open, close, start, stop, write, delete. */
  action: string;
  target: string;
  detail?: string;
  /** Where a download comes from. */
  source?: string;
  downloadBytes?: number;
  diskBytes?: number;
  memoryBytes?: number;
  /** Removes data. */
  destructive?: boolean;
  /** Who added the effect when it wasn't the requested change: "addon:cdn". */
  contributor?: string;
}

/** What a hook or add-on added to the change set. */
export interface Contribution {
  source: ChangeSource;
  summary: string;
  /** Indexes into the change set's changes and effects. */
  changes?: number[] | null;
  effects?: number[] | null;
}

/** Two contributors changing the same field; the change set can't be applied until it's resolved. */
export interface Conflict {
  resource: string;
  path: string;
  sources: ChangeSource[] | null;
  message: string;
}

export interface Estimate {
  downloadBytes: number;
  diskBytes: number;
  memoryBytes: number;
}

export type ChangeSetState = "planned" | "applying" | "applied" | "failed" | "undone";

/**
 * Who did something. The panel names the kind of actor and where the
 * request came from with open sets of words, so a newer panel can add one.
 */
export interface Actor {
  /** user, token, automation, addon, assistant or system. */
  type: string;
  id?: string;
  name: string;
  /** ui, cli, api, automation, addon, assistant or system. */
  source: string;
  /** The person responsible when something acts for them, such as a token's owner. */
  user?: string;
  /** The admin or reseller viewing the panel as this customer, when one is. */
  impersonator?: string;
  /** The address the request came from. */
  ip?: string;
}

export interface ChangeSet {
  id: string;
  title: string;
  actor?: Actor;
  changes: ResourceChange[] | null;
  effects: Effect[] | null;
  contributions?: Contribution[] | null;
  conflicts?: Conflict[] | null;
  risk: Risk;
  estimate: Estimate;
  /** Applying it takes snapshots first, because it removes data. */
  snapshot?: boolean;
  dryRun?: boolean;
  state: ChangeSetState;
  job?: string;
  undoOf?: string;
  error?: string;
  createdAt: string;
  appliedAt?: string;
}

export type JobState = "queued" | "running" | "succeeded" | "failed" | "canceled";

export interface Job {
  id: string;
  type: string;
  title: string;
  state: JobState;
  progress: { done: number; total: number; message?: string };
  actor?: Actor;
  changeSet?: string;
  resource?: string;
  attempt: number;
  error?: { code: string; message: string };
  /** Who owns the work; jobs show to the people who can see their scope. */
  scope?: Scope;
  /** The job this one retries. */
  retryOf?: string;
  /** When a queued job may start, for retries that wait. */
  runAfter?: string;
  /** What the job returned. */
  result?: unknown;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
}

/** A page of jobs, newest first. */
export interface JobList {
  jobs: Job[] | null;
  /** The cursor of the next page; empty on the last one. */
  next?: string;
}

/** One line of a job's log. */
export interface LogLine {
  /** The line's number, from 1. */
  seq: number;
  time: string;
  level: "DEBUG" | "INFO" | "WARN" | "ERROR";
  message: string;
  attrs?: Record<string, unknown>;
}

/**
 * Adds a log line unless it's one already there, as when a stream starts
 * over, keeping the last limit lines.
 */
export function appendLogLine(lines: readonly LogLine[], line: LogLine, limit = 2000): LogLine[] {
  const last = lines.at(-1);
  if (last && line.seq <= last.seq) {
    return lines as LogLine[];
  }
  const next = [...lines, line];
  return next.length > limit ? next.slice(next.length - limit) : next;
}

/** Whether a job has finished, one way or another. */
export function jobDone(state: JobState): boolean {
  return state === "succeeded" || state === "failed" || state === "canceled";
}

/** Whether a change set removes data or is risky enough to ask before applying. */
export function needsConfirmation(cs: ChangeSet): boolean {
  return (
    cs.risk === "high" || cs.risk === "critical" || (cs.effects ?? []).some((e) => e.destructive)
  );
}

/**
 * The resource a change set is mostly about, whose name critical changes
 * ask to be typed: the first one it deletes, or else the first one the
 * request itself changes, or else the first change.
 */
export function confirmationTarget(cs: ChangeSet): ResourceChange | undefined {
  const changes = cs.changes ?? [];
  return (
    changes.find((c) => c.op === "delete") ??
    changes.find((c) => c.source.type === "request") ??
    changes[0]
  );
}

/**
 * A resource's name for people: the value at the kind's title field (its UI
 * hints' titleField, such as a site's domain) in the spec before the change,
 * or after it for a new resource, and its id otherwise.
 */
export function resourceTitle(change: ResourceChange, titleField?: string): string {
  if (titleField) {
    for (const spec of [change.before, change.after]) {
      let value: unknown = spec;
      for (const key of titleField.split(".")) {
        value =
          value !== null && typeof value === "object"
            ? (value as Record<string, unknown>)[key]
            : undefined;
      }
      if (typeof value === "string" && value !== "") {
        return value;
      }
    }
  }
  return change.id;
}

/** A contributor's label, as the preview shows it: "addon:cdn 1.2" or the hook point. */
export function sourceName(source: ChangeSource): string {
  return source.name || source.hook || source.type;
}
