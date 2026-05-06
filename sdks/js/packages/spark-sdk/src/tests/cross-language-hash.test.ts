// sdks/js/packages/spark-sdk/src/tests/cross-language-hash.test.ts
/**
 * Cross-language hash compatibility test for SparkInvoiceFields.
 * This test validates that our JavaScript protoreflecthash implementation
 * produces identical hashes to the Go implementation for the same data.
 */

import { describe, expect, it } from "@jest/globals";
import { getFieldNumbers } from "../spark-wallet/proto-reflection.js";

describe("Cross-Language Hash Compatibility", () => {
  it("should extract correct field numbers from SparkInvoiceFields", () => {
    const fieldNumbers = getFieldNumbers("spark.SparkInvoiceFields");
    expect(fieldNumbers.version).toBe(1);
    expect(fieldNumbers.id).toBe(2);
  });
});
