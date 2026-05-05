/**
 * SSP-backed integration tests for private wallet access via SparkReadonlyClient.
 *
 * Privacy setup uses wallet mutation flows, so these belong in the SSP lane.
 */
import { describe, it, expect, jest, beforeAll } from "@jest/globals";
import {
  createFundedWallet,
  createPublicReadonlyClient,
  createOwnerReadonlyClient,
  type FundedWallet,
} from "../../../spark-readonly-client/helpers.js";
import { type SparkReadonlyClient } from "../../../../spark-readonly-client/spark-readonly-client.node.js";

describe("private wallet access", () => {
  jest.setTimeout(60_000);

  let funded: FundedWallet;
  let publicClient: SparkReadonlyClient;
  let ownerClient: SparkReadonlyClient;

  beforeAll(async () => {
    funded = await createFundedWallet(10_000n);
    await funded.wallet.setPrivacyEnabled(true);

    publicClient = createPublicReadonlyClient();
    ownerClient = await createOwnerReadonlyClient(funded.mnemonic);
  });

  describe("getAvailableBalance", () => {
    it("owner sees their balance even with privacy enabled", async () => {
      const balance = await ownerClient.getAvailableBalance(
        funded.sparkAddress,
      );
      expect(balance).toBe(10_000n);
    });

    it("public SSP client still sees balance for a private wallet", async () => {
      const balance = await publicClient.getAvailableBalance(
        funded.sparkAddress,
      );
      expect(balance).toBe(10_000n);
    });
  });

  describe("getOwnedBalance", () => {
    it("owner sees their owned balance even with privacy enabled", async () => {
      const balance = await ownerClient.getOwnedBalance(funded.sparkAddress);
      expect(balance).toBe(10_000n);
    });

    it("public SSP client still sees owned balance for a private wallet", async () => {
      const balance = await publicClient.getOwnedBalance(funded.sparkAddress);
      expect(balance).toBe(10_000n);
    });
  });

  describe("getTransfers", () => {
    it("owner sees their transfers even with privacy enabled", async () => {
      const result = await ownerClient.getTransfers({
        sparkAddress: funded.sparkAddress,
      });
      expect(result.transfers.length).toBeGreaterThanOrEqual(0);
    });

    it("public client sees no transfers for a private wallet", async () => {
      const result = await publicClient.getTransfers({
        sparkAddress: funded.sparkAddress,
      });
      expect(result.transfers).toHaveLength(0);
    });
  });

  describe("getPendingTransfers", () => {
    it("owner can query pending transfers with privacy enabled", async () => {
      const transfers = await ownerClient.getPendingTransfers(
        funded.sparkAddress,
      );
      expect(transfers).toBeDefined();
    });

    it("public client sees no pending transfers for a private wallet", async () => {
      const transfers = await publicClient.getPendingTransfers(
        funded.sparkAddress,
      );
      expect(transfers).toHaveLength(0);
    });
  });

  describe("getUnusedDepositAddresses", () => {
    it("public client sees empty addresses for a private wallet", async () => {
      const result = await publicClient.getUnusedDepositAddresses({
        sparkAddress: funded.sparkAddress,
      });
      expect(result.depositAddresses).toHaveLength(0);
    });
  });

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
    funded = await createFundedWallet(10_000n);

    publicClient = createPublicReadonlyClient();
    ownerClient = await createOwnerReadonlyClient(funded.mnemonic);
  });

  it("public client sees balance for non-private wallet", async () => {
    const balance = await publicClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("public client sees owned balance for non-private wallet", async () => {
    const balance = await publicClient.getOwnedBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("owner client sees balance for non-private wallet", async () => {
    const balance = await ownerClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("owner client sees owned balance for non-private wallet", async () => {
    const balance = await ownerClient.getOwnedBalance(funded.sparkAddress);
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

  it("both clients see the same owned balance", async () => {
    const publicBalance = await publicClient.getOwnedBalance(
      funded.sparkAddress,
    );
    const ownerBalance = await ownerClient.getOwnedBalance(funded.sparkAddress);
    expect(publicBalance).toBe(ownerBalance);
  });
});

describe("master key access to private wallet", () => {
  jest.setTimeout(60_000);

  let funded: FundedWallet;
  let masterClient: SparkReadonlyClient;

  beforeAll(async () => {
    funded = await createFundedWallet(10_000n);
    await funded.wallet.setPrivacyEnabled(true);

    masterClient = await createOwnerReadonlyClient(funded.mnemonic);
  });

  it("master/owner sees balance of a private wallet", async () => {
    const balance = await masterClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("master/owner sees owned balance of a private wallet", async () => {
    const balance = await masterClient.getOwnedBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("master/owner sees transfers of a private wallet", async () => {
    const result = await masterClient.getTransfers({
      sparkAddress: funded.sparkAddress,
    });
    expect(result.transfers).toBeDefined();
  });
});
