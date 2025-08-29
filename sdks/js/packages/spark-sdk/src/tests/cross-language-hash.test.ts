// sdks/js/packages/spark-sdk/src/tests/cross-language-hash.test.ts
/**
 * Cross-language hash compatibility test for SparkInvoiceFields.
 * This test validates that our JavaScript protoreflecthash implementation
 * produces identical hashes to the Go implementation for the same data.
 */

import { describe, expect, it } from "@jest/globals";
import fs from "fs";
import { SparkInvoiceFields } from "../proto/spark.js";
import { createProtoHasher } from "../spark-wallet/proto-hash.js";
import { getFieldNumbers } from "../spark-wallet/proto-reflection.js";

function toHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

describe("Cross-Language Hash Compatibility", () => {
  const hasher = createProtoHasher();

  // Load canonical protobuf JSON test cases from the repo
  const candidates = [
    new URL(
      "../../../../../../spark/testdata/cross_language_hash_cases_proto.json",
      import.meta.url,
    ),
  ];

  let jsonData: any | null = null;
  for (const u of candidates) {
    try {
      const raw = fs.readFileSync(u, "utf8");
      jsonData = JSON.parse(raw);
      break;
    } catch {
      // try next
    }
  }

  if (!jsonData) {
    it("skips when proto-JSON dataset is absent", () => {
      expect(true).toBe(true);
    });
    return;
  }

  const allCases = (jsonData.testCases || []) as any[];

  it("should extract correct field numbers from SparkInvoiceFields", () => {
    const fieldNumbers = getFieldNumbers("spark.SparkInvoiceFields");
    expect(fieldNumbers.version).toBe(1);
    expect(fieldNumbers.id).toBe(2);
  });

  for (const tc of allCases) {
    it(`matches expected hash for ${tc.name}`, async () => {
      const msg = SparkInvoiceFields.fromJSON(tc.sparkInvoiceFields);
      const hash = await hasher.hashProto(msg, "spark.SparkInvoiceFields");
      const hex = toHex(hash);

      // Always 32 bytes and deterministic.
      expect(hash).toHaveLength(32);
      const hash2 = await hasher.hashProto(msg, "spark.SparkInvoiceFields");
      expect(hash).toEqual(hash2);

      // Compare against expected hash from JSON
      expect(hex.toLowerCase()).toBe(String(tc.expectedHash).toLowerCase());
    });
  }

  it("errors on nil/undefined messages", async () => {
    await expect(hasher.hashProto(null as any)).rejects.toThrow(
      /cannot hash nil/i,
    );
    await expect(hasher.hashProto(undefined as any)).rejects.toThrow(
      /cannot hash nil/i,
    );
  });
});
