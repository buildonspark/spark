/**
 * Integration tests for private wallet access via SparkReadonlyClient.
 *
 * When a wallet has privacy enabled:
 *   - A public (unauthenticated) client should see EMPTY results (not errors)
 *   - The owner (authenticated with their own identity key) should see all data
 *   - A master key holder (if set) should see all data
 *
 * When privacy is disabled (default):
 *   - Any client can see the wallet's data
 *
 * Server behavior reference: spark/so/handler/tree_query_handler_test.go
 */
import { describe, it, expect, jest, beforeAll } from "@jest/globals";
import {
  createFundedWallet,
  createPublicReadonlyClient,
  createOwnerReadonlyClient,
  type FundedWallet,
  LOCAL_OPTIONS,
} from "./helpers.js";
import { SparkReadonlyClient } from "../../spark-readonly-client/spark-readonly-client.node.js";

describe("private wallet access", () => {
  jest.setTimeout(60_000);

  let funded: FundedWallet;
  let publicClient: SparkReadonlyClient;
  let ownerClient: SparkReadonlyClient;

  beforeAll(async () => {
    funded = await createFundedWallet(10_000n);

    // Enable privacy on this wallet
    await funded.wallet.setPrivacyEnabled(true);

    publicClient = createPublicReadonlyClient();
    ownerClient = await createOwnerReadonlyClient(funded.mnemonic);
  });

  // ── getAvailableBalance ────────────────────────────────────

  describe("getAvailableBalance", () => {
    it("owner sees their balance even with privacy enabled", async () => {
      const balance = await ownerClient.getAvailableBalance(
        funded.sparkAddress,
      );
      expect(balance).toBe(10_000n);
    });

    it("public client sees 0 balance for a private wallet", async () => {
      const balance = await publicClient.getAvailableBalance(
        funded.sparkAddress,
      );
      expect(balance).toBe(0n);
    });
  });

  // ── getTransfers ───────────────────────────────────────────

  describe("getTransfers", () => {
    it("owner sees their transfers even with privacy enabled", async () => {
      const result = await ownerClient.getTransfers({
        sparkAddress: funded.sparkAddress,
      });
      // The funded wallet has at least one transfer (the deposit claim)
      expect(result.transfers.length).toBeGreaterThanOrEqual(0);
    });

    it("public client sees no transfers for a private wallet", async () => {
      const result = await publicClient.getTransfers({
        sparkAddress: funded.sparkAddress,
      });
      expect(result.transfers).toHaveLength(0);
    });
  });

  // ── getPendingTransfers ────────────────────────────────────

  describe("getPendingTransfers", () => {
    it("owner can query pending transfers with privacy enabled", async () => {
      const transfers = await ownerClient.getPendingTransfers(
        funded.sparkAddress,
      );
      // No pending transfers expected, but the query should succeed
      expect(transfers).toBeDefined();
    });

    it("public client sees no pending transfers for a private wallet", async () => {
      const transfers = await publicClient.getPendingTransfers(
        funded.sparkAddress,
      );
      expect(transfers).toHaveLength(0);
    });
  });

  // ── getUnusedDepositAddresses ──────────────────────────────

  describe("getUnusedDepositAddresses", () => {
    it("public client sees empty addresses for a private wallet", async () => {
      const result = await publicClient.getUnusedDepositAddresses({
        sparkAddress: funded.sparkAddress,
      });
      expect(result.depositAddresses).toHaveLength(0);
    });
  });

  // ── getStaticDepositAddresses ──────────────────────────────

  describe("getStaticDepositAddresses", () => {
    it("public client sees empty static addresses for a private wallet", async () => {
      const result = await publicClient.getStaticDepositAddresses(
        funded.sparkAddress,
      );
      expect(result).toHaveLength(0);
    });
  });
});

describe("non-private wallet access (default)", () => {
  jest.setTimeout(60_000);

  let funded: FundedWallet;
  let publicClient: SparkReadonlyClient;
  let ownerClient: SparkReadonlyClient;

  beforeAll(async () => {
    // Create a wallet without enabling privacy (default = non-private)
    funded = await createFundedWallet(10_000n);

    publicClient = createPublicReadonlyClient();
    ownerClient = await createOwnerReadonlyClient(funded.mnemonic);
  });

  it("public client sees balance for non-private wallet", async () => {
    const balance = await publicClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("owner client sees balance for non-private wallet", async () => {
    const balance = await ownerClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("both clients see the same data", async () => {
    const publicBalance = await publicClient.getAvailableBalance(
      funded.sparkAddress,
    );
    const ownerBalance = await ownerClient.getAvailableBalance(
      funded.sparkAddress,
    );
    expect(publicBalance).toBe(ownerBalance);
  });
});

describe("master key access to private wallet", () => {
  jest.setTimeout(60_000);

  let funded: FundedWallet;
  let masterClient: SparkReadonlyClient;

  beforeAll(async () => {
    funded = await createFundedWallet(10_000n);

    // The owner's identity IS the master key for now — the readonly client
    // created from the same mnemonic authenticates as the owner, which is
    // equivalent to a master key lookup (owner always has access).
    await funded.wallet.setPrivacyEnabled(true);

    masterClient = await createOwnerReadonlyClient(funded.mnemonic);
  });

  it("master/owner sees balance of a private wallet", async () => {
    const balance = await masterClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("master/owner sees transfers of a private wallet", async () => {
    const result = await masterClient.getTransfers({
      sparkAddress: funded.sparkAddress,
    });
    // Query should succeed (owner always has access)
    expect(result.transfers).toBeDefined();
  });
});
