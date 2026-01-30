import { sha256 } from "@noble/hashes/sha2";

/**
 * Compares two Uint8Arrays using byte-wise lexicographic comparison
 * for consistent cross-platform string ordering.
 */
function compareBytes(a: Uint8Array, b: Uint8Array): number {
  const minLength = Math.min(a.length, b.length);
  for (let i = 0; i < minLength; i++) {
    if (a[i] !== b[i]) {
      return a[i]! - b[i]!;
    }
  }

  return a.length - b.length;
}

/**
 * Hasher provides a type-safe API for securely hashing a sequence of values with SHA-256.
 * It preserves collision resistance when tags differ.
 * To preserve collision resistance across schema changes (such as data type, order, or meaning)
 * add a version to the tag.
 *
 * Example:
 *
 * ```typescript
 * const hash = newHasher(["spark", "token", "version 5"])
 *   .addBytes(new Uint8Array([1, 2, 3]))
 *   .addString("value")
 *   .addUint64(123)
 *   .hash();
 * ```
 */
export class Hasher {
  private hasher: ReturnType<typeof sha256.create>;

  private constructor(hasher: ReturnType<typeof sha256.create>) {
    this.hasher = hasher;
  }

  /**
   * Creates a new Hasher with the given hierarchical domain tag.
   * The tag is a hierarchical path, such as ["spark", "token", "create"].
   *
   * The hash is computed using the BIP-340 tagged hash pattern:
   * - tagHash = SHA256(serializeTag(tag))
   * - result = SHA256(tagHash || tagHash || serialized values)
   */
  static newHasher(tag: string[]): Hasher {
    const tagHash = sha256(serializeTag(tag));

    const hasher = sha256.create();
    // Write tagHash || tagHash as per BIP-340 tagged hash pattern
    hasher.update(tagHash);
    hasher.update(tagHash);

    return new Hasher(hasher);
  }

  /**
   * Adds a Uint8Array value to the hash computation.
   */
  addBytes(b: Uint8Array): Hasher {
    this.addValue(b);
    return this;
  }

  /**
   * Adds a string value to the hash computation.
   * The string is encoded as UTF-8 bytes.
   */
  addString(s: string): Hasher {
    const encoder = new TextEncoder();
    this.addValue(encoder.encode(s));
    return this;
  }

  /**
   * Adds a uint64 value to the hash computation.
   * Accepts both number (for convenience) and bigint (for full uint64 range).
   * Numbers must be safe integers (within Number.MAX_SAFE_INTEGER).
   * Bigints must be within the uint64 range (0 to 2^64-1).
   */
  addUint64(v: number | bigint): Hasher {
    let value: bigint;
    if (typeof v === "number") {
      if (!Number.isSafeInteger(v) || v < 0) {
        throw new Error(
          `addUint64: number must be a non-negative safe integer, got ${v}`,
        );
      }
      value = BigInt(v);
    } else {
      if (v < 0n) {
        throw new Error(`addUint64: bigint must be non-negative, got ${v}`);
      }
      // Prevent silent truncation by setBigUint64
      const MAX_UINT64 = 18446744073709551615n; // 2^64 - 1
      if (v > MAX_UINT64) {
        throw new Error(
          `addUint64: bigint must be within uint64 range (0 to 2^64-1), got ${v}`,
        );
      }
      value = v;
    }

    const valueBytes = new Uint8Array(8);
    const view = new DataView(valueBytes.buffer);
    view.setBigUint64(0, value, false); // false = big-endian
    this.addValue(valueBytes);
    return this;
  }

  /**
   * Adds a uint32 value to the hash computation.
   */
  addUint32(v: number): Hasher {
    if (!Number.isSafeInteger(v) || v < 0 || v > 0xffffffff) {
      throw new Error(`addUint32: value must be a valid uint32, got ${v}`);
    }
    return this.addUint64(BigInt(v));
  }

  /**
   * Adds a uint16 value to the hash computation.
   */
  addUint16(v: number): Hasher {
    if (!Number.isSafeInteger(v) || v < 0 || v > 0xffff) {
      throw new Error(`addUint16: value must be a valid uint16, got ${v}`);
    }
    return this.addUint64(BigInt(v));
  }

  /**
   * Adds a uint8 value to the hash computation.
   */
  addUint8(v: number): Hasher {
    if (!Number.isSafeInteger(v) || v < 0 || v > 0xff) {
      throw new Error(`addUint8: value must be a valid uint8, got ${v}`);
    }
    return this.addUint64(BigInt(v));
  }

  /**
   * Adds a map<string, Uint8Array> to the hash computation.
   * The map is hashed in a deterministic order: first the count of entries,
   * then each key-value pair sorted by key.
   *
   * Format: [count (uint64)] [key1 (string)] [value1 (bytes)] [key2 (string)] [value2 (bytes)] ...
   */
  addMapStringToBytes(m: Record<string, Uint8Array>): Hasher {
    this.addUint64(Object.keys(m).length);

    // For determinism, convert map to array of key-value pairs and sort by key
    const encoder = new TextEncoder();
    const pairs: { key: string; value: Uint8Array; keyBytes: Uint8Array }[] =
      Object.entries(m).map(([key, value]) => ({
        key,
        value,
        keyBytes: encoder.encode(key),
      }));
    pairs.sort((a, b) => compareBytes(a.keyBytes, b.keyBytes));

    for (const pair of pairs) {
      this.addString(pair.key);
      this.addBytes(pair.value);
    }

    return this;
  }

  /**
   * Writes a value directly to the hash state.
   * Format: [8-byte length (big-endian uint64)] [value bytes]
   */
  private addValue(valueBytes: Uint8Array): void {
    const lengthBytes = new Uint8Array(8);
    const view = new DataView(lengthBytes.buffer);
    view.setBigUint64(0, BigInt(valueBytes.length), false); // false = big-endian
    this.hasher.update(lengthBytes);
    this.hasher.update(valueBytes);
  }

  /**
   * Computes and returns the final SHA256 hash.
   * The hash is computed using the BIP-340 tagged hash pattern.
   *
   * Values are serialized incrementally as they are added via the Add* methods.
   * Each value is serialized as [8-byte length (big-endian uint64)] [value bytes].
   */
  hash(): Uint8Array {
    return this.hasher.digest();
  }
}

/**
 * Creates a new Hasher with the given hierarchical domain tag.
 * The tag is a hierarchical path, such as ["spark", "token", "create"].
 */
export function newHasher(tag: string[]): Hasher {
  return Hasher.newHasher(tag);
}

/**
 * Serializes a hierarchical tag into bytes.
 * Format: For each component, [8-byte length (big-endian uint64)] [UTF-8 bytes]
 */
function serializeTag(tag: string[]): Uint8Array {
  const encoder = new TextEncoder();
  const parts: Uint8Array[] = [];

  for (const component of tag) {
    const componentBytes = encoder.encode(component);
    const lengthBytes = new Uint8Array(8);
    const view = new DataView(lengthBytes.buffer);
    view.setBigUint64(0, BigInt(componentBytes.length), false); // false = big-endian
    parts.push(lengthBytes);
    parts.push(componentBytes);
  }

  // Concatenate all parts
  const totalLength = parts.reduce((sum, part) => sum + part.length, 0);
  const result = new Uint8Array(totalLength);
  let offset = 0;
  for (const part of parts) {
    result.set(part, offset);
    offset += part.length;
  }

  return result;
}
