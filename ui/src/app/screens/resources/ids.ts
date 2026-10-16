/** The letters of a TypeID's suffix: Crockford's base32, in small letters. */
const alphabet = "0123456789abcdefghjkmnpqrstvwxyz";

/**
 * A new ID for a resource, made as the panel makes them (store.NewID): the
 * kind's prefix, an underscore and a UUIDv7 in base32, so IDs still sort by
 * when they were made. Screens choose one up front when a request has to
 * name the resource it creates, as an account does in its own owner.
 */
export function newResourceID(
  prefix: string,
  now: number = Date.now(),
  random: Uint8Array = crypto.getRandomValues(new Uint8Array(10)),
): string {
  const bytes = new Uint8Array(16);
  // 48 bits of milliseconds, then random bits around the version and variant.
  let ms = Math.max(0, Math.floor(now));
  for (let i = 5; i >= 0; i--) {
    bytes[i] = ms % 256;
    ms = Math.floor(ms / 256);
  }
  bytes.set(random.subarray(0, 10), 6);
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x70;
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
  let value = 0n;
  for (const b of bytes) {
    value = (value << 8n) | BigInt(b);
  }
  // 26 characters of five bits, most significant first; the first holds three.
  let suffix = "";
  for (let i = 25; i >= 0; i--) {
    suffix += alphabet.charAt(Number((value >> BigInt(5 * i)) & 31n));
  }
  return `${prefix}_${suffix}`;
}
