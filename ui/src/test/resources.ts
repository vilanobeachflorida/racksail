import type { Kind, Resource } from "@/app/screens/resources/kinds";
import type { ChangeSet } from "@/lib/changes";

/**
 * Kinds, resources and change sets as the panel describes them, for tests
 * of the generic screens. The webhook kind is the core's, as racksail dev
 * serves it.
 */

const webhookSpec = {
  $defs: {
    Scope: {
      additionalProperties: false,
      properties: {
        id: { description: "The owning reseller or account", type: "string" },
        level: {
          description: "Who owns the resource",
          enum: ["server", "reseller", "account"],
          type: "string",
        },
      },
      required: ["level"],
      type: "object",
    },
  },
  additionalProperties: false,
  properties: {
    description: { description: "What the webhook is for", maxLength: 200, type: "string" },
    disabled: {
      description: "Stops sending events until the webhook is enabled again",
      type: "boolean",
    },
    events: {
      description: "Event types to send",
      items: { type: "string" },
      maxItems: 64,
      minItems: 1,
      type: ["array", "null"],
    },
    minSeverity: {
      description: "Only send events at least this urgent",
      enum: ["info", "notice", "warning", "error", "critical"],
      type: "string",
    },
    owner: {
      $ref: "#/$defs/Scope",
      description: "Only send events about this reseller's or account's resources",
    },
    secret: { description: "The signing secret", type: "string" },
    url: {
      description: "Where events are sent",
      format: "uri",
      maxLength: 2048,
      minLength: 1,
      type: "string",
    },
  },
  required: ["url", "events"],
  type: "object",
};

const webhookStatus = {
  properties: {
    phase: {
      enum: ["pending", "applying", "ready", "degraded", "failed", "deleting"],
      type: "string",
    },
    message: { type: "string" },
    observedGeneration: { type: "integer" },
    conditions: { type: ["array", "null"] },
    lastDelivery: {
      description: "When an event was last sent",
      format: "date-time",
      type: "string",
    },
    consecutiveFailures: { description: "Attempts in a row that failed", type: "integer" },
  },
  type: "object",
};

export const webhookKind: Kind = {
  name: "core.webhook",
  module: "core",
  title: "Webhook",
  plural: "webhooks",
  description: "Sends events to a URL of your choice as they happen.",
  prefix: "whk",
  scope: "server",
  path: "/api/v1/core/webhooks",
  risks: { create: "medium", update: "medium", delete: "medium" },
  actions: [
    {
      name: "test",
      title: "Send a test",
      summary: "Send a test event to the webhook",
      description: "Sends a test event right away.",
      risk: "low",
    },
    {
      name: "redeliver",
      title: "Send again",
      summary: "Send an earlier delivery again",
      description: "Sends an earlier delivery once more.",
      risk: "low",
      hasInput: true,
    },
  ],
  indexes: [{ name: "url", path: "url" }],
  events: ["core.webhook.created", "core.webhook.updated", "core.webhook.deleted"],
  ui: {
    icon: "webhook",
    titleField: "url",
    subtitleField: "description",
    columns: [
      { field: "spec.url", format: "link" },
      { field: "spec.events", format: "badge" },
      { field: "status.phase", format: "status" },
      { field: "status.lastDelivery", format: "date" },
    ],
    fields: {
      url: { order: 1 },
      events: { order: 2 },
      minSeverity: { order: 3 },
      secret: { order: 4, widget: "password", sensitive: true },
      description: { order: 5 },
      disabled: { order: 6 },
      owner: { order: 7, advanced: true },
    },
  },
  backup: { covered: true },
  spec: webhookSpec,
  status: webhookStatus,
  inputs: {
    redeliver: {
      properties: { delivery: { description: "The delivery to send again", type: "string" } },
      required: ["delivery"],
      type: "object",
    },
  },
  outputs: {},
} as unknown as Kind;

/** A kind the panel keeps for itself, which has no screens. */
export const hiddenKind: Kind = {
  ...webhookKind,
  name: "core.session",
  title: "Session",
  plural: "sessions",
  prefix: "ses",
  path: "/api/v1/core/sessions",
  actions: [],
  ui: { ...webhookKind.ui, hidden: true },
} as Kind;

/** A kind as a module would describe it, with only what the generic screens need. */
function kind(
  fields: Omit<Partial<Kind>, "ui" | "spec"> & {
    name: string;
    ui: Record<string, unknown>;
    spec: Record<string, unknown>;
  },
): Kind {
  const [module = ""] = fields.name.split(".");
  return {
    module,
    description: `${fields.title ?? ""} for tests.`,
    risks: { create: "low", update: "medium", delete: "medium" },
    actions: [],
    indexes: [],
    events: [],
    backup: { covered: true },
    status: { type: "object", properties: { phase: { type: "string" } } },
    inputs: {},
    outputs: {},
    ...fields,
  } as unknown as Kind;
}

/** The core's accounts, which own themselves, as the panel's identity keeps them. */
export const accountKind = kind({
  name: "core.account",
  title: "Account",
  plural: "accounts",
  prefix: "acct",
  scope: "account",
  path: "/api/v1/core/accounts",
  ui: { titleField: "name", fields: { name: { order: 0 } } },
  spec: {
    type: "object",
    properties: { name: { type: "string", title: "Name", minLength: 1 } },
    required: ["name"],
  },
});

/** A module's kind of resource that sites refer to. */
export const databaseKind = kind({
  name: "databases.database",
  title: "Database",
  plural: "databases",
  prefix: "db",
  scope: "account",
  path: "/api/v1/databases/databases",
  ui: { titleField: "name" },
  spec: { type: "object", properties: { name: { type: "string" } } },
});

/**
 * A module's kind that belongs to accounts, with a field naming a database
 * its hints say the kind of, and one naming a reseller they don't.
 */
export const siteKind = kind({
  name: "web.site",
  title: "Site",
  plural: "sites",
  prefix: "site",
  scope: "account",
  path: "/api/v1/web/sites",
  ui: {
    titleField: "domain",
    fields: {
      domain: { order: 0 },
      database: { order: 1, widget: "resource", kind: "databases.database" },
      reseller: { order: 2, widget: "resource" },
    },
  },
  spec: {
    type: "object",
    properties: {
      domain: { type: "string", title: "Domain", minLength: 1 },
      database: { type: "string", title: "Database" },
      reseller: { type: "string", title: "Reseller" },
    },
    required: ["domain"],
  },
});

/** A webhook resource, with what a test changes. */
export function webhook(
  overrides: Partial<Resource> & { spec?: Record<string, unknown> } = {},
): Resource {
  const { spec, ...rest } = overrides;
  return {
    id: "whk_01jq0000000000000000000001",
    kind: "core.webhook",
    scope: { level: "server" },
    generation: 2,
    revision: 7,
    createdAt: "2026-10-01T10:00:00Z",
    updatedAt: "2026-10-14T09:30:00Z",
    labels: { team: "ops" },
    spec: {
      url: "https://hooks.example.com/racksail",
      events: ["web.site.*", "*.failed"],
      secret: "ref",
      ...spec,
    },
    status: {
      phase: "ready",
      observedGeneration: 2,
      lastDelivery: "2026-10-14T09:31:00Z",
      conditions: [
        {
          type: "Ready",
          status: "true",
          reason: "Delivered",
          message: "The last event was delivered.",
          lastTransitionTime: "2026-10-14T09:31:00Z",
        },
      ],
    },
    ...rest,
  } as Resource;
}

/** A change set of one resource change, in a given state. */
export function changeSet(overrides: Partial<ChangeSet> = {}): ChangeSet {
  return {
    id: "chg_01jq0000000000000000000001",
    title: "Update webhook",
    state: "planned",
    dryRun: true,
    risk: "medium",
    effects: [],
    estimate: { downloadBytes: 0, diskBytes: 0, memoryBytes: 0 },
    createdAt: "2026-10-15T12:00:00Z",
    actor: { type: "user", name: "developer", source: "ui" },
    changes: [
      {
        op: "update",
        kind: "core.webhook",
        id: "whk_01jq0000000000000000000001",
        scope: { level: "server" },
        source: { type: "request" },
        before: { url: "https://hooks.example.com/racksail", events: ["web.site.*"] },
        after: {
          url: "https://hooks.example.com/racksail",
          events: ["web.site.*"],
          description: "Ops",
        },
        diff: [{ path: "description", op: "add", after: "Ops" }],
      },
    ],
    ...overrides,
  } as ChangeSet;
}
