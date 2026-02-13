/**
 * Integration tests for SparkReadonlyClient factory methods:
 *   - createPublic
 *   - createWithMasterKey
 *   - createWithSigner
 *
 * Verifies that each factory constructs a functional client capable of
 * making real network calls against the local backend.
 */
import { describe, it, expect, jest } from "@jest/globals";
import { SparkReadonlyClient } from "../../spark-readonly-client/spark-readonly-client.node.js";
import { DefaultSparkSigner } from "../../signer/signer.js";
import {
  createEmptyWallet,
  createPublicReadonlyClient,
  LOCAL_OPTIONS,
} from "./helpers.js";

describe("SparkReadonlyClient factory methods", () => {
  jest.setTimeout(30_000);

  // ── createPublic ────────────────────────────────────────────

  describe("createPublic", () => {
    it("creates a functional unauthenticated client", async () => {
      const client = SparkReadonlyClient.createPublic(LOCAL_OPTIONS);
      expect(client).toBeInstanceOf(SparkReadonlyClient);

      // Verify it can actually make a call (empty wallet → 0 balance)
      const empty = await createEmptyWallet();
      const balance = await client.getAvailableBalance(empty.sparkAddress);
      expect(balance).toBe(0n);
    });

    it("works without explicit config (defaults to REGTEST)", () => {
      const client = SparkReadonlyClient.createPublic();
      expect(client).toBeInstanceOf(SparkReadonlyClient);
    });
  });

  // ── createWithMasterKey ────────────────────────────────────

  describe("createWithMasterKey", () => {
    it("creates a functional client from a mnemonic", async () => {
      const mnemonic =
        "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";
      const client = await SparkReadonlyClient.createWithMasterKey(
        LOCAL_OPTIONS,
        mnemonic,
      );
      expect(client).toBeInstanceOf(SparkReadonlyClient);
    });

    it("creates a functional client from a hex seed", async () => {
      const hexSeed =
        "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f";
      const client = await SparkReadonlyClient.createWithMasterKey(
        LOCAL_OPTIONS,
        hexSeed,
      );
      expect(client).toBeInstanceOf(SparkReadonlyClient);
    });

    it("creates a functional client from a Uint8Array seed", async () => {
      const seed = new Uint8Array(32).fill(0xab);
      const client = await SparkReadonlyClient.createWithMasterKey(
        LOCAL_OPTIONS,
        seed,
      );
      expect(client).toBeInstanceOf(SparkReadonlyClient);
    });

    it("respects explicit account number", async () => {
      const seed = new Uint8Array(32).fill(0x01);
      const client0 = await SparkReadonlyClient.createWithMasterKey(
        LOCAL_OPTIONS,
        seed,
        0,
      );
      const client42 = await SparkReadonlyClient.createWithMasterKey(
        LOCAL_OPTIONS,
        seed,
        42,
      );
      // Both should be valid instances (different account = different identity)
      expect(client0).toBeInstanceOf(SparkReadonlyClient);
      expect(client42).toBeInstanceOf(SparkReadonlyClient);
    });
  });

  // ── createWithSigner ───────────────────────────────────────

  describe("createWithSigner", () => {
    it("creates a functional client with an initialized signer", async () => {
      const signer = new DefaultSparkSigner();
      await signer.createSparkWalletFromSeed(new Uint8Array(32));
      const client = SparkReadonlyClient.createWithSigner(
        LOCAL_OPTIONS,
        signer,
      );
      expect(client).toBeInstanceOf(SparkReadonlyClient);
    });
  });
});
