/**
 * Change sets and jobs as the panel's API returns them: the JSON forms of
 * internal/changeset and internal/jobs. Every change, whatever started it,
 * becomes a change set that is previewed, applied as a job and can be
 * undone.
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
  diff?: FieldDiff[];
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
  changes?: number[];
  effects?: number[];
}

/** Two contributors changing the same field; the change set can't be applied until it's resolved. */
export interface Conflict {
  resource: string;
  path: string;
  sources: ChangeSource[];
  message: string;
}

export interface Estimate {
  downloadBytes: number;
  diskBytes: number;
  memoryBytes: number;
}

export type ChangeSetState = "planned" | "applying" | "applied" | "failed" | "undone";

export interface Actor {
  type: "user" | "token" | "automation" | "addon" | "assistant" | "system";
  id?: string;
  name: string;
  source: "ui" | "cli" | "api" | "automation" | "addon" | "assistant" | "system";
}

export interface ChangeSet {
  id: string;
  title: string;
  actor?: Actor;
  changes: ResourceChange[];
  effects: Effect[];
  contributions?: Contribution[];
  conflicts?: Conflict[];
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
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface LogLine {
  time: string;
  level: string;
  message: string;
  attrs?: Record<string, unknown>;
}

/** Whether a job has finished, one way or another. */
export function jobDone(state: JobState): boolean {
  return state === "succeeded" || state === "failed" || state === "canceled";
}

/** Whether a change set removes data or is risky enough to ask before applying. */
export function needsConfirmation(cs: ChangeSet): boolean {
  return cs.risk === "high" || cs.risk === "critical" || cs.effects.some((e) => e.destructive);
}

/** A contributor's label, as the preview shows it: "addon:cdn 1.2" or the hook point. */
export function sourceName(source: ChangeSource): string {
  return source.name || source.hook || source.type;
}
