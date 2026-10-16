import type { Client } from "openapi-fetch";
import { type ApiClient, type Problem, unwrap } from "@/lib/api/client";
import type { ChangeSet } from "@/lib/changes";
import type { Kind, Resource } from "./kinds";

/*
 * Every kind has the same operations under its own path, which the OpenAPI
 * document lists kind by kind. Screens built for any kind call them by the
 * path the kind's description gives, through the same client and error
 * handling as every other call; the answers are checked by the kind's own
 * schemas, not by the compiler.
 */

type Json = { "application/json": unknown };
type Answers<Status extends number> = {
  [S in Status]: { headers: Record<string, unknown>; content: Json };
} & {
  default: { headers: Record<string, unknown>; content: { "application/problem+json": Problem } };
};
type Query = Record<string, string | number | boolean | readonly string[] | undefined>;
interface Params {
  query?: Query;
  header?: { "Idempotency-Key"?: string };
  path?: never;
  cookie?: never;
}
interface KindPathItem {
  parameters: Params;
  get: { parameters: Params; requestBody?: never; responses: Answers<200> };
  post: {
    parameters: Params;
    requestBody?: { content: Json };
    responses: Answers<200 | 202>;
  };
  put: { parameters: Params; requestBody: { content: Json }; responses: Answers<200 | 202> };
  delete: { parameters: Params; requestBody?: never; responses: Answers<200 | 202> };
}
type KindPaths = Record<`/api/v1/${string}`, KindPathItem>;

const kindClient = (api: ApiClient) => api as unknown as Client<KindPaths>;

/** A page of resources. */
export interface ResourcePage {
  items: Resource[];
  next?: string;
}

/** The filters a kind's list takes; indexes are filters of their own, by name. */
export interface ResourceQuery {
  scope?: string;
  label?: string[];
  phase?: string[];
  q?: string;
  sort?: string;
  cursor?: string;
  limit?: number;
  includeDeleted?: boolean;
  [index: string]: string | number | boolean | string[] | undefined;
}

/** How a change is sent: as a dry run that returns its preview, or applied. */
export interface ChangeOptions {
  dryRun?: boolean;
  /** Makes a repeated request return the first change set instead of applying again. */
  idempotencyKey?: string;
}

function changeParams(options: ChangeOptions): Params {
  return {
    ...(options.dryRun ? { query: { dryRun: true } } : {}),
    ...(options.idempotencyKey ? { header: { "Idempotency-Key": options.idempotencyKey } } : {}),
  };
}

const at = (kind: Kind, id?: string): `/api/v1/${string}` =>
  (id === undefined ? kind.path : `${kind.path}/${encodeURIComponent(id)}`) as `/api/v1/${string}`;

export async function listResources(
  api: ApiClient,
  kind: Kind,
  query: ResourceQuery = {},
): Promise<ResourcePage> {
  const page = (await unwrap(
    kindClient(api).GET(at(kind), { params: { query } }),
  )) as Partial<ResourcePage>;
  return { items: page.items ?? [], ...(page.next ? { next: page.next } : {}) };
}

export async function getResource(api: ApiClient, kind: Kind, id: string): Promise<Resource> {
  return (await unwrap(kindClient(api).GET(at(kind, id)))) as Resource;
}

/** One recorded version of a resource's spec. */
export interface Version {
  generation: number;
  spec: unknown;
  time: string;
  actor?: string;
  changeSet?: string;
  deleted?: boolean;
  labels?: Record<string, string>;
  scope?: { level: string; id?: string };
}

export async function getHistory(api: ApiClient, kind: Kind, id: string): Promise<Version[]> {
  const body = (await unwrap(kindClient(api).GET(`${at(kind, id)}/history`))) as {
    versions?: Version[] | null;
  };
  return body.versions ?? [];
}

/** A new resource: its settings, owner, labels and the values of its secret fields. */
export interface CreateBody {
  spec: unknown;
  scope?: { level: string; id?: string };
  labels?: Record<string, string>;
  secrets?: Record<string, string>;
  id?: string;
}

export async function createResource(
  api: ApiClient,
  kind: Kind,
  body: CreateBody,
  options: ChangeOptions = {},
): Promise<ChangeSet> {
  return (await unwrap(
    kindClient(api).POST(at(kind), { params: changeParams(options), body }),
  )) as ChangeSet;
}

/** New settings for a resource, against the revision they were read at. */
export interface UpdateBody {
  spec: unknown;
  labels?: Record<string, string>;
  secrets?: Record<string, string>;
  revision?: number;
}

export async function updateResource(
  api: ApiClient,
  kind: Kind,
  id: string,
  body: UpdateBody,
  options: ChangeOptions = {},
): Promise<ChangeSet> {
  return (await unwrap(
    kindClient(api).PUT(at(kind, id), { params: changeParams(options), body }),
  )) as ChangeSet;
}

export async function deleteResource(
  api: ApiClient,
  kind: Kind,
  id: string,
  revision: number | undefined,
  options: ChangeOptions = {},
): Promise<ChangeSet> {
  const params = changeParams(options);
  if (revision) {
    params.query = { ...params.query, revision };
  }
  return (await unwrap(kindClient(api).DELETE(at(kind, id), { params }))) as ChangeSet;
}

export async function runAction(
  api: ApiClient,
  kind: Kind,
  id: string,
  action: string,
  input: unknown,
  options: ChangeOptions = {},
): Promise<ChangeSet> {
  return (await unwrap(
    kindClient(api).POST(`${at(kind, id)}/actions/${encodeURIComponent(action)}`, {
      params: changeParams(options),
      ...(input === undefined ? {} : { body: input }),
    }),
  )) as ChangeSet;
}
