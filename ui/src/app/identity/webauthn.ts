/*
 * Passkeys in the browser. The panel sends the options of a WebAuthn
 * ceremony as JSON, with binary values in base64url, and takes the new or
 * used passkey back in the JSON form PublicKeyCredential.toJSON() gives.
 * Browsers that can't parse or write that form themselves get it done here.
 */

/** Why a passkey ceremony didn't finish, for the message people see. */
export type PasskeyFailure = "cancelled" | "exists" | "unavailable" | "unsupported";

/** A passkey ceremony that didn't finish, and why. */
export class PasskeyError extends Error {
  readonly reason: PasskeyFailure;

  constructor(reason: PasskeyFailure, cause?: unknown) {
    super(`passkey ${reason}`, { cause });
    this.name = "PasskeyError";
    this.reason = reason;
  }
}

/** Whether this browser can use passkeys at all. */
export function passkeysSupported(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.PublicKeyCredential === "function" &&
    typeof navigator.credentials?.create === "function"
  );
}

function fromBase64url(text: string): ArrayBuffer {
  const base64 = text.replace(/-/g, "+").replace(/_/g, "/");
  const padded = base64 + "=".repeat((4 - (base64.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes.buffer;
}

function toBase64url(buffer: ArrayBuffer | ArrayBufferView): string {
  const bytes =
    buffer instanceof ArrayBuffer
      ? new Uint8Array(buffer)
      : new Uint8Array(buffer.buffer, buffer.byteOffset, buffer.byteLength);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

interface DescriptorJSON {
  id: string;
  type: PublicKeyCredentialType;
  transports?: AuthenticatorTransport[];
}

interface CreationJSON
  extends Omit<PublicKeyCredentialCreationOptions, "challenge" | "user" | "excludeCredentials"> {
  challenge: string;
  user: { id: string; name: string; displayName: string };
  excludeCredentials?: DescriptorJSON[];
}

interface RequestJSON
  extends Omit<PublicKeyCredentialRequestOptions, "challenge" | "allowCredentials"> {
  challenge: string;
  allowCredentials?: DescriptorJSON[];
}

const descriptor = (d: DescriptorJSON): PublicKeyCredentialDescriptor => ({
  ...d,
  id: fromBase64url(d.id),
});

function creationOptions(json: CreationJSON): PublicKeyCredentialCreationOptions {
  const { challenge, user, excludeCredentials, ...rest } = json;
  return {
    ...rest,
    challenge: fromBase64url(challenge),
    user: { ...user, id: fromBase64url(user.id) },
    ...(excludeCredentials ? { excludeCredentials: excludeCredentials.map(descriptor) } : {}),
  };
}

function requestOptions(json: RequestJSON): PublicKeyCredentialRequestOptions {
  const { challenge, allowCredentials, ...rest } = json;
  return {
    ...rest,
    challenge: fromBase64url(challenge),
    ...(allowCredentials ? { allowCredentials: allowCredentials.map(descriptor) } : {}),
  };
}

/** The passkey as the panel takes it back. */
function credentialJSON(credential: PublicKeyCredential): unknown {
  const native = (credential as { toJSON?: () => unknown }).toJSON;
  if (typeof native === "function") {
    return native.call(credential);
  }
  const response = credential.response;
  const common = {
    id: credential.id,
    rawId: toBase64url(credential.rawId),
    type: credential.type,
    clientExtensionResults: credential.getClientExtensionResults(),
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
  };
  if ("attestationObject" in response) {
    const attestation = response as AuthenticatorAttestationResponse;
    return {
      ...common,
      response: {
        clientDataJSON: toBase64url(attestation.clientDataJSON),
        attestationObject: toBase64url(attestation.attestationObject),
        transports: attestation.getTransports?.() ?? [],
      },
    };
  }
  const assertion = response as AuthenticatorAssertionResponse;
  return {
    ...common,
    response: {
      clientDataJSON: toBase64url(assertion.clientDataJSON),
      authenticatorData: toBase64url(assertion.authenticatorData),
      signature: toBase64url(assertion.signature),
      userHandle: assertion.userHandle ? toBase64url(assertion.userHandle) : undefined,
    },
  };
}

function failure(error: unknown): PasskeyError {
  const name = error instanceof DOMException ? error.name : "";
  switch (name) {
    case "InvalidStateError":
      return new PasskeyError("exists", error);
    case "SecurityError":
      return new PasskeyError("unavailable", error);
    case "NotSupportedError":
      return new PasskeyError("unsupported", error);
  }
  // NotAllowedError covers both a cancelled prompt and one that timed out,
  // and browsers don't say which.
  return new PasskeyError("cancelled", error);
}

function unwrapOptions<T>(options: unknown): T {
  const outer = options as { publicKey?: T } | null;
  return (outer?.publicKey ?? options) as T;
}

/** Makes a new passkey with the options of a registration ceremony. */
export async function createPasskey(options: unknown): Promise<unknown> {
  if (!passkeysSupported()) {
    throw new PasskeyError("unsupported");
  }
  let credential: Credential | null;
  try {
    credential = await navigator.credentials.create({
      publicKey: creationOptions(unwrapOptions<CreationJSON>(options)),
    });
  } catch (error) {
    throw failure(error);
  }
  if (!(credential instanceof PublicKeyCredential)) {
    throw new PasskeyError("cancelled");
  }
  return credentialJSON(credential);
}

/** Uses a passkey with the options of a sign-in or confirmation ceremony. */
export async function askForPasskey(options: unknown): Promise<unknown> {
  if (!passkeysSupported()) {
    throw new PasskeyError("unsupported");
  }
  let credential: Credential | null;
  try {
    credential = await navigator.credentials.get({
      publicKey: requestOptions(unwrapOptions<RequestJSON>(options)),
    });
  } catch (error) {
    throw failure(error);
  }
  if (!(credential instanceof PublicKeyCredential)) {
    throw new PasskeyError("cancelled");
  }
  return credentialJSON(credential);
}
