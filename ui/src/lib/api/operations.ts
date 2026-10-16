import type { Client } from "openapi-fetch";
import type { Job, JobList, JobState, LogLine } from "../changes";
import type { Severity, TimelineEventList } from "../events";
import { type ApiClient, type Problem, unwrap } from "./client";
import type { paths } from "./schema";
import { normalizeSession, type PanelSession, type SessionPreferences } from "./session";

/*
 * Operations the UI calls that spec/openapi.json doesn't describe yet: the
 * job queue and the event timeline (listJobs, getJobLogs, cancelJob,
 * retryJob and listEvents). They're declared here in the form openapi-typescript generates, and go through
 * the same client and error handling as generated ones. When the OpenAPI
 * document gains an operation, `make gen` types it in schema.d.ts and the
 * generated types take over at once: the functions below are checked
 * against them, so any difference from what the UI expects is a type error
 * here. Its declaration can then go.
 */

type NoParameters = { query?: never; header?: never; path?: never; cookie?: never };

interface Answer<T, Status extends number = 200> {
  responses: {
    [S in Status]: { headers: Record<string, unknown>; content: { "application/json": T } };
  } & {
    default: { headers: Record<string, unknown>; content: { "application/problem+json": Problem } };
  };
}

interface IdPath {
  query?: never;
  header?: never;
  path: { id: string };
  cookie?: never;
}

/** Filters of the job list. */
export interface JobQuery {
  state?: JobState;
  type?: string;
  changeSet?: string;
  resource?: string;
  cursor?: string;
  limit?: number;
}

/** Filters of the event timeline. */
export interface EventQuery {
  scope?: string;
  subject?: string;
  kind?: string;
  changeSet?: string;
  job?: string;
  since?: string;
  until?: string;
  type?: string[];
  minSeverity?: Severity;
  cursor?: string;
  limit?: number;
}

/** The operations, as openapi-typescript writes paths. */
export interface PanelPaths {
  "/api/v1/jobs": {
    parameters: NoParameters;
    get: {
      parameters: { query?: JobQuery; header?: never; path?: never; cookie?: never };
      requestBody?: never;
    } & Answer<JobList>;
  };
  "/api/v1/jobs/{id}/logs": {
    parameters: NoParameters;
    get: {
      parameters: {
        query?: { after?: number; limit?: number };
        header?: never;
        path: { id: string };
        cookie?: never;
      };
      requestBody?: never;
    } & Answer<{ lines: LogLine[] | null }>;
  };
  "/api/v1/jobs/{id}/cancel": {
    parameters: NoParameters;
    post: { parameters: IdPath; requestBody?: never } & Answer<Job>;
  };
  "/api/v1/jobs/{id}/retry": {
    parameters: NoParameters;
    post: { parameters: IdPath; requestBody?: never } & Answer<Job, 202>;
  };
  "/api/v1/events": {
    parameters: NoParameters;
    get: {
      parameters: { query?: EventQuery; header?: never; path?: never; cookie?: never };
      requestBody?: never;
    } & Answer<TimelineEventList>;
  };
}

/** The generated paths, and the declared ones the OpenAPI document doesn't have yet. */
type KnownPaths = paths & Omit<PanelPaths, keyof paths>;

// The same client: the declared paths only change what TypeScript knows.
const panel = (api: ApiClient) => api as unknown as Client<KnownPaths>;

/** Where the job list and each job's progress and log stream from. */
export const streams = {
  events: "/api/v1/events/stream",
  job: (id: string) => `/api/v1/jobs/${encodeURIComponent(id)}/stream`,
};

/** The jobs the panel runs and ran recently, newest first. */
export function listJobs(api: ApiClient, query?: JobQuery): Promise<JobList> {
  return unwrap(panel(api).GET("/api/v1/jobs", { params: { query } }));
}

/** A job's log lines. */
export function getJobLogs(api: ApiClient, id: string): Promise<{ lines: LogLine[] | null }> {
  return unwrap(panel(api).GET("/api/v1/jobs/{id}/logs", { params: { path: { id } } }));
}

/** Cancels a queued or running job; the answer is the job. */
export function cancelJob(api: ApiClient, id: string): Promise<Job> {
  return unwrap(panel(api).POST("/api/v1/jobs/{id}/cancel", { params: { path: { id } } }));
}

/** Runs a failed or canceled job again; the answer is the new job. */
export function retryJob(api: ApiClient, id: string): Promise<Job> {
  return unwrap(panel(api).POST("/api/v1/jobs/{id}/retry", { params: { path: { id } } }));
}

/** Events from the timeline, newest first. */
export function listEvents(api: ApiClient, query?: EventQuery): Promise<TimelineEventList> {
  return unwrap(panel(api).GET("/api/v1/events", { params: { query } }));
}

/** Who is signed in and how the panel looks and behaves for them. */
export async function getSession(api: ApiClient): Promise<PanelSession> {
  return normalizeSession(await unwrap(api.GET("/api/v1/session")));
}

/** Keeps the person's display preferences with their account. */
export async function savePreferences(
  api: ApiClient,
  preferences: SessionPreferences,
): Promise<void> {
  await unwrap(api.PUT("/api/v1/session/preferences", { body: preferences }));
}
