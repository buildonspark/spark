import { describe, it, expect } from "@jest/globals";
import { newHasher } from "../utils/hashstructure.js";

describe("Hasher", () => {
  it("should produce different hashes for different tags (domain separation)", () => {
    const tag1 = ["spark", "token", "create"];
    const hash1 = newHasher(tag1)
      .addBytes(new Uint8Array([1, 2, 3]))
      .hash();

    const tag2 = ["spark", "token", "mint"];
    const hash2 = newHasher(tag2)
      .addBytes(new Uint8Array([1, 2, 3]))
      .hash();

    expect(hash1).not.toEqual(hash2);
  });

  it("should handle empty values correctly", () => {
    const tag = ["test"];

    const hashNone = newHasher(tag).hash();

    const hashOneEmpty = newHasher(tag).addBytes(new Uint8Array([])).hash();

    expect(hashNone).not.toEqual(hashOneEmpty);
  });

  it("should be sensitive to order", () => {
    const tag = ["test"];

    const hash1 = newHasher(tag).addUint32(1).addUint32(2).hash();

    const hash2 = newHasher(tag).addUint32(2).addUint32(1).hash();

    expect(hash1).not.toEqual(hash2);
  });

  it("should produce deterministic output", () => {
    const tag = ["spark", "operator", "sign"];

    const hashes: Uint8Array[] = [];

    for (let i = 0; i < 2; i++) {
      hashes.push(
        newHasher(tag)
          .addUint32(123)
          .addBytes(new Uint8Array([0x01, 0x02, 0x03, 0x04]))
          .addString("transaction-id")
          .addBytes(new Uint8Array([0xff, 0xfe]))
          .hash(),
      );
    }

    expect(hashes[0]).toEqual(hashes[1]);
  });

  it("should handle all uint types correctly", () => {
    const tag = ["test"];

    const hashUint8 = newHasher(tag).addUint8(255).hash();
    const hashUint16 = newHasher(tag).addUint16(65535).hash();
    const hashUint32 = newHasher(tag).addUint32(4294967295).hash();
    const hashUint64Number = newHasher(tag).addUint64(9007199254740991).hash(); // MAX_SAFE_INTEGER
    const hashUint64BigInt = newHasher(tag)
      .addUint64(18446744073709551615n)
      .hash(); // MAX_UINT64

    // All should be different
    expect(hashUint8).not.toEqual(hashUint16);
    expect(hashUint16).not.toEqual(hashUint32);
    expect(hashUint32).not.toEqual(hashUint64Number);
    expect(hashUint64Number).not.toEqual(hashUint64BigInt);
  });

  it("should handle strings correctly", () => {
    const tag = ["test"];

    const hash1 = newHasher(tag).addString("hello").hash();
    const hash2 = newHasher(tag).addString("world").hash();
    const hash3 = newHasher(tag).addString("hello").hash();

    expect(hash1).not.toEqual(hash2);
    expect(hash1).toEqual(hash3);
  });

  it("should handle bytes correctly", () => {
    const tag = ["test"];

    const hash1 = newHasher(tag)
      .addBytes(new Uint8Array([1, 2, 3]))
      .hash();
    const hash2 = newHasher(tag)
      .addBytes(new Uint8Array([4, 5, 6]))
      .hash();
    const hash3 = newHasher(tag)
      .addBytes(new Uint8Array([1, 2, 3]))
      .hash();

    expect(hash1).not.toEqual(hash2);
    expect(hash1).toEqual(hash3);
  });

  it("should validate uint64 number input", () => {
    const tag = ["test"];

    expect(() => newHasher(tag).addUint64(-1)).toThrow();
    expect(() => newHasher(tag).addUint64(1.5)).toThrow();
    expect(() =>
      newHasher(tag).addUint64(Number.MAX_SAFE_INTEGER + 1),
    ).toThrow();
  });

  it("should validate uint64 bigint input", () => {
    const tag = ["test"];

    expect(() => newHasher(tag).addUint64(-1n)).toThrow();
    expect(() => newHasher(tag).addUint64(18446744073709551615n)).not.toThrow(); // MAX_UINT64
    expect(() => newHasher(tag).addUint64(18446744073709551616n)).toThrow(); // MAX_UINT64 + 1
  });

  it("should validate uint32 input", () => {
    const tag = ["test"];

    expect(() => newHasher(tag).addUint32(-1)).toThrow();
    expect(() => newHasher(tag).addUint32(0xffffffff + 1)).toThrow();
    expect(() => newHasher(tag).addUint32(0xffffffff)).not.toThrow();
  });

  it("should validate uint16 input", () => {
    const tag = ["test"];

    expect(() => newHasher(tag).addUint16(-1)).toThrow();
    expect(() => newHasher(tag).addUint16(0xffff + 1)).toThrow();
    expect(() => newHasher(tag).addUint16(0xffff)).not.toThrow();
  });

  it("should validate uint8 input", () => {
    const tag = ["test"];

    expect(() => newHasher(tag).addUint8(-1)).toThrow();
    expect(() => newHasher(tag).addUint8(0xff + 1)).toThrow();
    expect(() => newHasher(tag).addUint8(0xff)).not.toThrow();
  });

  it("should handle chaining multiple operations", () => {
    const tag = ["test"];

    const hash = newHasher(tag)
      .addUint8(1)
      .addUint16(2)
      .addUint32(3)
      .addUint64(4)
      .addString("test")
      .addBytes(new Uint8Array([5, 6, 7]))
      .hash();

    expect(hash).toBeInstanceOf(Uint8Array);
    expect(hash.length).toBe(32); // SHA256 output is 32 bytes
  });

  it("should handle empty tag", () => {
    const hash = newHasher([]).addString("test").hash();

    expect(hash).toBeInstanceOf(Uint8Array);
    expect(hash.length).toBe(32);
  });

  it("should handle unicode strings correctly", () => {
    const tag = ["test"];

    const hash1 = newHasher(tag).addString("hello").hash();
    const hash2 = newHasher(tag).addString("héllo").hash(); // é is 2 bytes in UTF-8
    const hash3 = newHasher(tag).addString("🚀").hash(); // emoji is 4 bytes in UTF-8

    expect(hash1).not.toEqual(hash2);
    expect(hash2).not.toEqual(hash3);
  });
});
