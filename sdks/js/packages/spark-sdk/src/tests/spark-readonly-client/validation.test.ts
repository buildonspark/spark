/**
 * Tests for input validation on SparkReadonlyClient methods.
 *
 * These tests exercise the validation helpers (assertPositiveInteger,
 * assertNonNegativeInteger, assertNonEmptyArray) indirectly through the
 * public API. They don't require a running backend because validation
 * rejects before any RPC call is made.
 */
import { describe, it, expect, jest } from "@jest/globals";
import { SparkValidationError } from "../../errors/types.js";
import {
  createPublicReadonlyClient,
  sparkAddressFromPubkey,
} from "./helpers.js";
import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex } from "@noble/curves/utils";

// Create a well-formed spark address for validation tests
const dummyPubkey = bytesToHex(
  secp256k1.getPublicKey(secp256k1.utils.randomPrivateKey(), true),
);
const VALID_SPARK_ADDRESS = sparkAddressFromPubkey(dummyPubkey);

describe("SparkReadonlyClient input validation", () => {
  jest.setTimeout(10_000);

  const client = createPublicReadonlyClient();

  // ── getTransfers ───────────────────────────────────────────

  describe("getTransfers", () => {
    it("rejects limit = 0", async () => {
      await expect(
        client.getTransfers({ sparkAddress: VALID_SPARK_ADDRESS, limit: 0 }),
      ).rejects.toThrow(SparkValidationError);
    });

    it("rejects negative limit", async () => {
      await expect(
        client.getTransfers({ sparkAddress: VALID_SPARK_ADDRESS, limit: -5 }),
      ).rejects.toThrow(SparkValidationError);
    });

    it("rejects fractional limit", async () => {
      await expect(
        client.getTransfers({ sparkAddress: VALID_SPARK_ADDRESS, limit: 1.5 }),
      ).rejects.toThrow(SparkValidationError);
    });

    it("rejects negative offset", async () => {
      await expect(
        client.getTransfers({ sparkAddress: VALID_SPARK_ADDRESS, offset: -1 }),
      ).rejects.toThrow(SparkValidationError);
    });

    it("rejects mutually exclusive createdAfter and createdBefore", async () => {
      const now = new Date();
      await expect(
        client.getTransfers({
          sparkAddress: VALID_SPARK_ADDRESS,
          createdAfter: now,
          createdBefore: now,
        }),
      ).rejects.toThrow(SparkValidationError);
    });
  });

  // ── getTransfersByIds ──────────────────────────────────────

  describe("getTransfersByIds", () => {
    it("rejects empty transferIds array", async () => {
      await expect(client.getTransfersByIds([])).rejects.toThrow(
        SparkValidationError,
      );
    });
  });

  // ── getUnusedDepositAddresses ──────────────────────────────

  describe("getUnusedDepositAddresses", () => {
    it("rejects limit = 0", async () => {
      await expect(
        client.getUnusedDepositAddresses({
          sparkAddress: VALID_SPARK_ADDRESS,
          limit: 0,
        }),
      ).rejects.toThrow(SparkValidationError);
    });

    it("rejects negative offset", async () => {
      await expect(
        client.getUnusedDepositAddresses({
          sparkAddress: VALID_SPARK_ADDRESS,
          offset: -1,
        }),
      ).rejects.toThrow(SparkValidationError);
    });
  });

  // ── getUtxosForDepositAddress ──────────────────────────────

  describe("getUtxosForDepositAddress", () => {
    it("rejects limit = 0", async () => {
      await expect(
        client.getUtxosForDepositAddress({
          depositAddress: "bcrt1qfakeaddress",
          limit: 0,
        }),
      ).rejects.toThrow(SparkValidationError);
    });

    it("rejects negative offset", async () => {
      await expect(
        client.getUtxosForDepositAddress({
          depositAddress: "bcrt1qfakeaddress",
          offset: -1,
        }),
      ).rejects.toThrow(SparkValidationError);
    });
  });

  // ── getSparkInvoices ───────────────────────────────────────

  describe("getSparkInvoices", () => {
    it("rejects empty invoices array", async () => {
      await expect(client.getSparkInvoices({ invoices: [] })).rejects.toThrow(
        SparkValidationError,
      );
    });

    it("rejects limit = 0", async () => {
      await expect(
        client.getSparkInvoices({
          invoices: ["some-invoice"],
          limit: 0,
        }),
      ).rejects.toThrow(SparkValidationError);
    });

    it("rejects negative offset", async () => {
      await expect(
        client.getSparkInvoices({
          invoices: ["some-invoice"],
          offset: -1,
        }),
      ).rejects.toThrow(SparkValidationError);
    });
  });

  // ── getTokenTransactions ───────────────────────────────────

  describe("getTokenTransactions", () => {
    it("rejects pageSize = 0", async () => {
      await expect(
        client.getTokenTransactions({ pageSize: 0 }),
      ).rejects.toThrow(SparkValidationError);
    });

    it("rejects fractional pageSize", async () => {
      await expect(
        client.getTokenTransactions({ pageSize: 2.5 }),
      ).rejects.toThrow(SparkValidationError);
    });
  });

  // ── Spark address validation ───────────────────────────────

  describe("spark address validation", () => {
    it("rejects an invalid spark address in getAvailableBalance", async () => {
      await expect(
        client.getAvailableBalance("not-a-valid-spark-address"),
      ).rejects.toThrow();
    });

    it("rejects an invalid spark address in getPendingTransfers", async () => {
      await expect(
        client.getPendingTransfers("not-a-valid-spark-address"),
      ).rejects.toThrow();
    });

    it("rejects an invalid spark address in getStaticDepositAddresses", async () => {
      await expect(
        client.getStaticDepositAddresses("not-a-valid-spark-address"),
      ).rejects.toThrow();
    });
  });
});
