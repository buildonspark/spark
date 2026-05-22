import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import {
  handleGetDepositAddress,
  handleClaimDeposit,
} from "../tools/deposits.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;

describe("handleClaimDeposit", () => {
  let mockWallet: {
    claimDeposit: jest.MockedFunction<
      (txid: string) => Promise<{ value: number }[]>
    >;
    getBalance: jest.MockedFunction<() => Promise<{ balance: bigint }>>;
  };
  let mockResolve: jest.MockedFunction<ResolveFn>;

  beforeEach(() => {
    mockWallet = {
      claimDeposit: jest.fn<(txid: string) => Promise<{ value: number }[]>>(),
      getBalance: jest.fn<() => Promise<{ balance: bigint }>>(),
    };
    mockResolve = jest
      .fn<ResolveFn>()
      .mockResolvedValue(mockWallet as unknown as SparkWallet);
  });

  it("waits for balance to settle before returning", async () => {
    // Prior balance is 0, claimed 50k, balance settles on second poll.
    mockWallet.getBalance
      .mockResolvedValueOnce({ balance: 0n }) // prior balance snapshot
      .mockResolvedValueOnce({ balance: 0n }) // first poll — stale
      .mockResolvedValueOnce({ balance: 50_000n }); // second poll — settled
    mockWallet.claimDeposit.mockResolvedValue([{ value: 50_000 }]);

    const result = await handleClaimDeposit(
      "txid123",
      undefined,
      mockResolve,
      5_000,
    );

    expect(result.isError).toBeUndefined();
    expect(result.content[0].text).toContain("Deposit claimed successfully");
    expect(result.content[0].text).toContain("50,000 sats");
    expect(result.content[0].text).toContain("Wallet balance");
    // Prior balance + 2 poll calls = 3 total getBalance calls
    expect(mockWallet.getBalance).toHaveBeenCalledTimes(3);
  });

  it("returns confirmed balance when it settles immediately", async () => {
    mockWallet.getBalance
      .mockResolvedValueOnce({ balance: 10_000n }) // prior balance
      .mockResolvedValueOnce({ balance: 60_000n }); // first poll — already settled
    mockWallet.claimDeposit.mockResolvedValue([{ value: 50_000 }]);

    const result = await handleClaimDeposit(
      "txidABC",
      undefined,
      mockResolve,
      5_000,
    );

    expect(result.isError).toBeUndefined();
    expect(result.content[0].text).toContain("60,000 sats");
    expect(mockWallet.getBalance).toHaveBeenCalledTimes(2);
  });

  it("returns warning when balance does not settle within timeout", async () => {
    mockWallet.getBalance.mockResolvedValue({ balance: 0n });
    mockWallet.claimDeposit.mockResolvedValue([{ value: 50_000 }]);

    // Use a very short timeout so the test doesn't take long.
    const result = await handleClaimDeposit(
      "txid-timeout",
      undefined,
      mockResolve,
      100, // 100ms timeout — will expire before any poll succeeds
    );

    expect(result.isError).toBeUndefined();
    expect(result.content[0].text).toContain("has not settled yet");
    expect(result.content[0].text).toContain("txid-timeout");
  });

  it("returns error when claim fails", async () => {
    mockWallet.getBalance.mockResolvedValue({ balance: 0n });
    mockWallet.claimDeposit.mockRejectedValue(
      new Error("Deposit not confirmed"),
    );

    const result = await handleClaimDeposit(
      "txid-bad",
      undefined,
      mockResolve,
      5_000,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0].text).toContain("Deposit not confirmed");
  });

  it("returns error when wallet resolution fails", async () => {
    mockResolve.mockRejectedValue(new Error("No wallet specified"));

    const result = await handleClaimDeposit(
      "txid-no-wallet",
      undefined,
      mockResolve,
      5_000,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0].text).toContain("No wallet specified");
  });

  it("accounts for existing balance when detecting settlement", async () => {
    // Wallet already has 100k, claiming 25k more. Should wait for 125k+.
    mockWallet.getBalance
      .mockResolvedValueOnce({ balance: 100_000n }) // prior balance
      .mockResolvedValueOnce({ balance: 100_000n }) // first poll — stale
      .mockResolvedValueOnce({ balance: 125_000n }); // second poll — settled
    mockWallet.claimDeposit.mockResolvedValue([{ value: 25_000 }]);

    const result = await handleClaimDeposit(
      "txid-existing",
      undefined,
      mockResolve,
      5_000,
    );

    expect(result.isError).toBeUndefined();
    expect(result.content[0].text).toContain("125,000 sats");
    expect(result.content[0].text).toContain("25,000 sats");
  });
});

describe("handleGetDepositAddress", () => {
  it("returns deposit address", async () => {
    const mockWallet = {
      getSingleUseDepositAddress: jest
        .fn<() => Promise<string>>()
        .mockResolvedValue("bcrt1pabc123"),
    };
    const mockResolve = jest
      .fn<ResolveFn>()
      .mockResolvedValue(mockWallet as unknown as SparkWallet);

    const result = await handleGetDepositAddress(undefined, mockResolve);

    expect(result.isError).toBeUndefined();
    expect(result.content[0].text).toContain("bcrt1pabc123");
    expect(result.content[0].text).toContain("single-use");
  });

  it("returns error when wallet resolution fails", async () => {
    const mockResolve = jest
      .fn<ResolveFn>()
      .mockRejectedValue(new Error("No wallet specified"));

    const result = await handleGetDepositAddress(undefined, mockResolve);

    expect(result.isError).toBe(true);
    expect(result.content[0].text).toContain("No wallet specified");
  });
});
