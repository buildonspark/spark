/**
 * Integration tests for SparkReadonlyClient balance methods:
 *   - getAvailableBalance
 *   - getTokenBalance
 *
 * Uses a real funded wallet and queries it via both an owner-authenticated
 * readonly client and an unauthenticated public readonly client.
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
import { SparkRequestError } from "../../errors/types.js";

describe("getAvailableBalance", () => {
  jest.setTimeout(30_000);

  let funded: FundedWallet;
  let publicClient: SparkReadonlyClient;
  let ownerClient: SparkReadonlyClient;

  beforeAll(async () => {
    funded = await createFundedWallet(10_000n);
    publicClient = createPublicReadonlyClient();
    ownerClient = await createOwnerReadonlyClient(funded.mnemonic);
  });

  // ── Happy Paths ──────────────────────────────────────────

  it("owner client returns the correct funded balance", async () => {
    const balance = await ownerClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("public client returns the correct funded balance (non-private wallet)", async () => {
    // By default wallets are non-private, so public queries should work
    const balance = await publicClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("returns 0n for an empty wallet", async () => {
    const empty = await createEmptyWallet();
    const balance = await publicClient.getAvailableBalance(empty.sparkAddress);
    expect(balance).toBe(0n);
  });

  // ── Unhappy Paths ────────────────────────────────────────

  it("throws on an invalid spark address", async () => {
    await expect(
      publicClient.getAvailableBalance("invalid-address"),
    ).rejects.toThrow();
  });
});

describe("getTokenBalance", () => {
  jest.setTimeout(30_000);

  let emptyWallet: FundedWallet;
  let publicClient: SparkReadonlyClient;

  beforeAll(async () => {
    emptyWallet = await createEmptyWallet();
    publicClient = createPublicReadonlyClient();
  });

  // ── Happy Paths ──────────────────────────────────────────

  it("returns an empty map when the wallet has no tokens", async () => {
    const balances = await publicClient.getTokenBalance(
      emptyWallet.sparkAddress,
    );
    expect(balances).toBeInstanceOf(Map);
    expect(balances.size).toBe(0);
  });

  // ── Unhappy Paths ────────────────────────────────────────

  it("throws on an invalid spark address", async () => {
    await expect(
      publicClient.getTokenBalance("invalid-address"),
    ).rejects.toThrow();
  });

  it("throws on an invalid token identifier", async () => {
    await expect(
      publicClient.getTokenBalance(emptyWallet.sparkAddress, [
        "not-a-bech32m-token-id",
      ]),
    ).rejects.toThrow();
  });
});
