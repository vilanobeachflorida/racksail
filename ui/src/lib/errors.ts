import type { IntlShape, PrimitiveType } from "react-intl";
import { ApiError, type Problem } from "./api/client";

/** The message id that translates an error code. */
export function errorMessageId(code: string): string {
  return `error.${code}`;
}

function formatValues(params: Problem["params"]): Record<string, PrimitiveType> {
  const values: Record<string, PrimitiveType> = {};
  for (const [key, value] of Object.entries(params ?? {})) {
    values[key] =
      typeof value === "string" || typeof value === "number" || typeof value === "boolean"
        ? value
        : JSON.stringify(value);
  }
  return values;
}

/**
 * The message for a problem in the user's language: the translation of its
 * code when the catalogs have one, and the server's English text otherwise.
 */
export function problemMessage(intl: IntlShape, problem: Problem): string {
  const id = errorMessageId(problem.code);
  if (Object.hasOwn(intl.messages, id)) {
    return intl.formatMessage({ id }, formatValues(problem.params));
  }
  return (
    problem.detail ?? problem.title ?? intl.formatMessage({ id: errorMessageId("core.internal") })
  );
}

/**
 * What an error says for a bug report, as it is rather than translated:
 * a problem's code, status, English explanation, parameters and where it
 * happened, or an exception's name, message and stack.
 */
export function errorDetails(error: unknown): string {
  if (error instanceof ApiError) {
    const p = error.problem;
    const params = p.params && Object.keys(p.params).length > 0 ? JSON.stringify(p.params) : "";
    return [`${p.code} (${error.status})`, p.detail ?? p.title ?? "", params, p.instance ?? ""]
      .filter((line) => line !== "")
      .join("\n");
  }
  if (error instanceof Error) {
    const head = `${error.name}: ${error.message}`;
    const stack = error.stack ?? "";
    // V8 starts the stack with the name and message; other engines don't.
    return stack.startsWith(head) ? stack : [head, stack].filter((s) => s !== "").join("\n");
  }
  return String(error);
}

/** The message for any error: problems from the API, or what an exception says. */
export function errorMessage(intl: IntlShape, error: unknown): string {
  if (error instanceof ApiError) {
    return problemMessage(intl, error.problem);
  }
  if (error instanceof Error && error.message !== "") {
    return error.message;
  }
  return intl.formatMessage({ id: errorMessageId("core.internal") });
}
