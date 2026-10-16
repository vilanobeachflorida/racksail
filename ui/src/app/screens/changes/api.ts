import { type ApiClient, unwrap } from "@/lib/api/client";
import type { ChangeSet } from "@/lib/changes";

/** One change of a request that holds several, as the change pipeline takes it. */
export interface ChangeRequest {
  op: "create" | "update" | "delete" | "action";
  kind: string;
  id?: string;
  action?: string;
  input?: unknown;
  spec?: unknown;
  labels?: Record<string, string>;
  scope?: { level: "server" | "reseller" | "account"; id?: string };
  revision?: number;
}

/** Plans several changes as one change set: one preview, one apply and one undo. */
export async function submitChanges(
  api: ApiClient,
  body: { title?: string; changes: ChangeRequest[] },
  options: { dryRun?: boolean; idempotencyKey?: string } = {},
): Promise<ChangeSet> {
  return (await unwrap(
    api.POST("/api/v1/changesets", {
      params: {
        query: options.dryRun ? { dryRun: true } : {},
        header: options.idempotencyKey ? { "Idempotency-Key": options.idempotencyKey } : {},
      },
      body,
    }),
  )) as unknown as ChangeSet;
}

/** Applies a preview as it was shown. */
export async function applyPreview(
  api: ApiClient,
  id: string,
  idempotencyKey?: string,
): Promise<ChangeSet> {
  return (await unwrap(
    api.POST("/api/v1/changesets/{id}/apply", {
      params: {
        path: { id },
        header: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {},
      },
    }),
  )) as unknown as ChangeSet;
}

/** Previews, or applies, the change set that undoes another. */
export async function undoChangeSet(
  api: ApiClient,
  id: string,
  options: { dryRun?: boolean; idempotencyKey?: string } = {},
): Promise<ChangeSet> {
  return (await unwrap(
    api.POST("/api/v1/changesets/{id}/undo", {
      params: {
        path: { id },
        query: options.dryRun ? { dryRun: true } : {},
        header: options.idempotencyKey ? { "Idempotency-Key": options.idempotencyKey } : {},
      },
    }),
  )) as unknown as ChangeSet;
}

export async function getChangeSet(api: ApiClient, id: string): Promise<ChangeSet> {
  return (await unwrap(
    api.GET("/api/v1/changesets/{id}", { params: { path: { id } } }),
  )) as unknown as ChangeSet;
}

/** Filters of the list of change sets. */
export interface ChangeSetQuery {
  state?: "planned" | "applying" | "applied" | "failed" | "undone";
  resource?: string;
  actor?: string;
  previews?: boolean;
  cursor?: string;
  limit?: number;
}

export async function listChangeSets(
  api: ApiClient,
  query: ChangeSetQuery = {},
): Promise<{ changeSets: ChangeSet[]; next?: string }> {
  const page = await unwrap(api.GET("/api/v1/changesets", { params: { query } }));
  return {
    changeSets: (page.changeSets ?? []) as unknown as ChangeSet[],
    ...(page.next ? { next: page.next } : {}),
  };
}

/** A fresh key, so a request sent again after a dropped connection isn't applied twice. */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}
