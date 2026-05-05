/**
 * Integration tests for SparkReadonlyClient balance methods:
 *   - getAvailableBalance
 *   - getOwnedBalance
 *   - getTokenBalance
 *
 * Uses a real funded wallet and queries it via both an owner-authenticated
 * readonly client and an unauthenticated public readonly client.
 */
import { beforeAll, describe, expect, it, jest } from "@jest/globals";
import { type SparkReadonlyClient } from "../../../spark-readonly-client/spark-readonly-client.node.js";
import {
  createEmptyWallet,
  createFundedWallet,
  createOwnerReadonlyClient,
  createPublicReadonlyClient,
  type FundedWallet,
} from "../../spark-readonly-client/helpers.js";

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

  it("owner client returns the correct funded balance", async () => {
    const balance = await ownerClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("public client returns the correct funded balance (non-private wallet)", async () => {
    const balance = await publicClient.getAvailableBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("returns 0n for an empty wallet", async () => {
    const empty = await createEmptyWallet();
    const balance = await publicClient.getAvailableBalance(empty.sparkAddress);
    expect(balance).toBe(0n);
  });

  it("throws on an invalid spark address", async () => {
    await expect(
      publicClient.getAvailableBalance("invalid-address"),
    ).rejects.toThrow();
  });
});

describe("getOwnedBalance", () => {
  jest.setTimeout(30_000);

  let funded: FundedWallet;
  let publicClient: SparkReadonlyClient;
  let ownerClient: SparkReadonlyClient;

  beforeAll(async () => {
    funded = await createFundedWallet(10_000n);
    publicClient = createPublicReadonlyClient();
    ownerClient = await createOwnerReadonlyClient(funded.mnemonic);
  });

  it("owner client returns the correct funded owned balance", async () => {
    const balance = await ownerClient.getOwnedBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("public client returns the correct funded owned balance", async () => {
    const balance = await publicClient.getOwnedBalance(funded.sparkAddress);
    expect(balance).toBe(10_000n);
  });

  it("returns 0n for an empty wallet", async () => {
    const empty = await createEmptyWallet();
    const balance = await publicClient.getOwnedBalance(empty.sparkAddress);
    expect(balance).toBe(0n);
  });

  it("throws on an invalid spark address", async () => {
    await expect(
      publicClient.getOwnedBalance("invalid-address"),
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

  it("returns an empty map when the wallet has no tokens", async () => {
    const balances = await publicClient.getTokenBalance(
      emptyWallet.sparkAddress,
    );
    expect(balances).toBeInstanceOf(Map);
    expect(balances.size).toBe(0);
  });

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
