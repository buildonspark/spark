/**
 * Integration tests for SparkReadonlyClient transfer query methods:
 *   - getTransfers
 *   - getTransfersByIds
 *   - getPendingTransfers
 *
 * Creates two funded wallets, performs a transfer between them,
 * and then queries the resulting transfers via the readonly client.
 */
import { describe, it, expect, jest, beforeAll } from "@jest/globals";
import {
  createFundedWallet,
  createEmptyWallet,
  createPublicReadonlyClient,
  createOwnerReadonlyClient,
  type FundedWallet,
} from "./helpers.js";
import { SparkReadonlyClient } from "../../spark-readonly-client/spark-readonly-client.node.js";
import { SparkValidationError } from "../../errors/types.js";

describe("getTransfers", () => {
  jest.setTimeout(60_000);

  let sender: FundedWallet;
  let receiver: FundedWallet;
  let publicClient: SparkReadonlyClient;
  let senderOwnerClient: SparkReadonlyClient;

  beforeAll(async () => {
    sender = await createFundedWallet(10_000n);
    receiver = await createEmptyWallet();

    // Send a transfer from sender → receiver
    await sender.wallet.transfer({
      amountSats: 5_000,
      receiverSparkAddress: receiver.sparkAddress,
    });

    publicClient = createPublicReadonlyClient();
    senderOwnerClient = await createOwnerReadonlyClient(sender.mnemonic);
  });

  // ── Happy Paths ──────────────────────────────────────────

  it("returns transfers for the sender wallet", async () => {
    const result = await publicClient.getTransfers({
      sparkAddress: sender.sparkAddress,
    });
    expect(result.transfers.length).toBeGreaterThanOrEqual(1);
  });

  it("returns transfers for the sender via owner client", async () => {
    const result = await senderOwnerClient.getTransfers({
      sparkAddress: sender.sparkAddress,
    });
    expect(result.transfers.length).toBeGreaterThanOrEqual(1);
  });

  it("respects limit parameter", async () => {
    const result = await publicClient.getTransfers({
      sparkAddress: sender.sparkAddress,
      limit: 1,
    });
    expect(result.transfers.length).toBeLessThanOrEqual(1);
  });

  it("returns empty list for a wallet with no transfers", async () => {
    const empty = await createEmptyWallet();
    const result = await publicClient.getTransfers({
      sparkAddress: empty.sparkAddress,
    });
    expect(result.transfers).toHaveLength(0);
  });

  it("supports createdAfter time filter", async () => {
    const longAgo = new Date("2020-01-01");
    const result = await publicClient.getTransfers({
      sparkAddress: sender.sparkAddress,
      createdAfter: longAgo,
    });
    expect(result.transfers.length).toBeGreaterThanOrEqual(1);
  });

  it("supports createdBefore time filter", async () => {
    const future = new Date("2099-01-01");
    const result = await publicClient.getTransfers({
      sparkAddress: sender.sparkAddress,
      createdBefore: future,
    });
    expect(result.transfers.length).toBeGreaterThanOrEqual(1);
  });

  // ── Edge Cases ─────────────────────────────────────────

  it("createdAfter far in the future returns empty", async () => {
    const future = new Date("2099-01-01");
    const result = await publicClient.getTransfers({
      sparkAddress: sender.sparkAddress,
      createdAfter: future,
    });
    expect(result.transfers).toHaveLength(0);
  });

  it("createdBefore far in the past returns empty", async () => {
    const past = new Date("2000-01-01");
    const result = await publicClient.getTransfers({
      sparkAddress: sender.sparkAddress,
      createdBefore: past,
    });
    expect(result.transfers).toHaveLength(0);
  });
});

describe("getTransfersByIds", () => {
  jest.setTimeout(60_000);

  let sender: FundedWallet;
  let receiver: FundedWallet;
  let publicClient: SparkReadonlyClient;
  let transferId: string;

  beforeAll(async () => {
    sender = await createFundedWallet(10_000n);
    receiver = await createEmptyWallet();

    const transferResult = await sender.wallet.transfer({
      amountSats: 3_000,
      receiverSparkAddress: receiver.sparkAddress,
    });
    transferId = transferResult.id;

    publicClient = createPublicReadonlyClient();
  });

  // ── Happy Paths ──────────────────────────────────────────

  it("returns the correct transfer by ID", async () => {
    const transfers = await publicClient.getTransfersByIds([transferId]);
    expect(transfers.length).toBeGreaterThanOrEqual(1);

    const found = transfers.find((t) => t.id === transferId);
    expect(found).toBeDefined();
  });

  it("returns results for multiple IDs", async () => {
    // Query with the known ID plus a non-existent one
    const transfers = await publicClient.getTransfersByIds([
      transferId,
      "00000000-0000-0000-0000-000000000000",
    ]);
    // At least the known transfer should be present
    const found = transfers.find((t) => t.id === transferId);
    expect(found).toBeDefined();
  });

  // ── Unhappy Paths ────────────────────────────────────────

  it("rejects empty array", async () => {
    await expect(publicClient.getTransfersByIds([])).rejects.toThrow(
      SparkValidationError,
    );
  });
});

describe("getPendingTransfers", () => {
  jest.setTimeout(30_000);

  let publicClient: SparkReadonlyClient;

  beforeAll(() => {
    publicClient = createPublicReadonlyClient();
  });

  it("returns empty array for a wallet with no pending transfers", async () => {
    const empty = await createEmptyWallet();
    const transfers = await publicClient.getPendingTransfers(
      empty.sparkAddress,
    );
    expect(transfers).toEqual([]);
  });

  it("throws on an invalid spark address", async () => {
    await expect(
      publicClient.getPendingTransfers("bad-address"),
    ).rejects.toThrow();
  });
});
