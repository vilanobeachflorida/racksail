/**
 * The TypeScript SDK for Racksail add-ons, on Node.js.
 *
 * An add-on runs as its own sandboxed systemd service and talks to the panel
 * over a Unix socket: the panel calls the add-on's hooks, and the add-on
 * calls the panel's API with a token limited to the permissions it was
 * granted. The protocol is spec/addon-protocol.json in the Racksail
 * repository.
 */

/** The hook API versions this SDK speaks, newest first. */
export const apiVersions = ["v1"] as const;

/** A hook API version this SDK speaks. */
export type ApiVersion = (typeof apiVersions)[number];

/** Reports whether the SDK speaks a hook API version. */
export function supports(version: string): version is ApiVersion {
  return (apiVersions as readonly string[]).includes(version);
}
