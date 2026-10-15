import createClient, { type Client, type ClientOptions } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];
export type Problem = Schemas["Problem"];
export type ApiClient = Client<paths>;

/**
 * Creates a client for the panel API. The panel serves the UI, so by default
 * the client talks to the page's own origin.
 */
export function createApiClient(options: ClientOptions = {}): ApiClient {
  return createClient<paths>({ baseUrl: window.location.origin, ...options });
}

/** An error answer from the API, with its RFC 9457 problem details. */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: Problem;

  constructor(status: number, problem: Problem) {
    super(problem.detail ?? problem.title ?? `The panel answered with status ${status}.`);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
  }

  /** The stable error code, such as "core.not_found". */
  get code(): string {
    return this.problem.code;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Whether a response body is problem details from the panel. */
export function isProblem(body: unknown): body is Problem {
  return isRecord(body) && typeof body.code === "string" && body.code !== "";
}

/**
 * Turns an error response into problem details. Something other than the
 * panel, such as a proxy in front of it, can answer with HTML or plain text;
 * that becomes a problem with the code for its status.
 */
export function toProblem(status: number, body: unknown): Problem {
  if (isProblem(body)) {
    return body;
  }
  if (status === 503 || status === 502 || status === 504) {
    return { type: "about:blank", status, code: "core.unavailable", params: { what: "The panel" } };
  }
  if (status >= 500) {
    return { type: "about:blank", status, code: "core.internal" };
  }
  const reason = typeof body === "string" && body.trim() !== "" ? body.trim() : `status ${status}`;
  return { type: "about:blank", status, code: "core.bad_request", params: { reason } };
}

interface Answer<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/**
 * Returns the data of a successful answer and throws an ApiError for an
 * error, so callers like TanStack Query see failures as exceptions.
 *
 *   const version = await unwrap(api.GET("/api/v1/version"));
 */
export async function unwrap<T>(request: Promise<Answer<T>>): Promise<T> {
  const { data, error, response } = await request;
  if (!response.ok || error !== undefined) {
    throw new ApiError(response.status, toProblem(response.status, error));
  }
  return data as T;
}
