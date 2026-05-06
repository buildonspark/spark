import { describe, expect, it } from "@jest/globals";
import fs from "fs";
import { SparkInvoiceFields } from "../proto/spark.js";
import { HashSparkInvoice } from "../utils/invoice-hashing.js";
import type { NetworkType } from "../utils/network.js";

type SigningHashTestCase = {
  expectedHash: string;
  name: string;
  network: NetworkType;
  receiverPublicKey: string;
  sparkInvoiceFields: unknown;
};

type SigningHashDataset = {
  testCases?: SigningHashTestCase[];
};

function toHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

describe("Cross-Language Token Invoice Signing Hash", () => {
  const candidates = [
    new URL(
      "../../../../../../spark/testdata/token_invoice_signing_hash_cases.json",
      import.meta.url,
    ),
  ];

  let jsonData: SigningHashDataset | null = null;
  for (const u of candidates) {
    try {
      const raw = fs.readFileSync(u, "utf8");
      jsonData = JSON.parse(raw) as SigningHashDataset;
      break;
    } catch {
      // try next
    }
  }

  if (!jsonData) {
    it("skips when token invoice signing hash dataset is absent", () => {
      expect(true).toBe(true);
    });
    return;
  }

  const allCases = jsonData.testCases ?? [];

  for (const tc of allCases) {
    it(`matches expected signing hash for ${tc.name}`, () => {
      const sparkInvoiceFields = SparkInvoiceFields.fromJSON(
        tc.sparkInvoiceFields,
      );
      const receiverPublicKey = Buffer.from(tc.receiverPublicKey, "base64");

      const hash = HashSparkInvoice(
        sparkInvoiceFields,
        receiverPublicKey,
        tc.network,
      );

      expect(hash).toHaveLength(32);
      expect(toHex(hash).toLowerCase()).toBe(
        String(tc.expectedHash).toLowerCase(),
      );
    });
  }
});
