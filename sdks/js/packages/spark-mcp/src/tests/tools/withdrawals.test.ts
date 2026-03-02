import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import {
  handleGetWithdrawalFeeQuote,
  handleWithdraw,
} from "../../tools/withdrawals.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type CurrencyAmount = { originalValue: number; originalUnit: string };
type CoopExitFeeQuote = {
  id: string;
  expiresAt: string;
  userFeeFast?: CurrencyAmount;
  userFeeMedium?: CurrencyAmount;
  userFeeSlow?: CurrencyAmount;
  l1BroadcastFeeFast?: CurrencyAmount;
  l1BroadcastFeeMedium?: CurrencyAmount;
  l1BroadcastFeeSlow?: CurrencyAmount;
};
type CoopExitRequest = { id: string; status: string };

const getWithdrawalFeeQuoteMock =
  jest.fn<
    (params: {
      amountSats: number;
      withdrawalAddress: string;
    }) => Promise<CoopExitFeeQuote | null>
  >();
const withdrawMock =
  jest.fn<
    (params: {
      onchainAddress: string;
      exitSpeed: string;
      amountSats?: number;
      feeQuoteId?: string;
    }) => Promise<CoopExitRequest | null>
  >();

const mockWallet = {
  getWithdrawalFeeQuote: getWithdrawalFeeQuoteMock,
  withdraw: withdrawMock,
};

const mockResolve = jest
  .fn<(mnemonic?: string) => Promise<SparkWallet>>()
  .mockResolvedValue(mockWallet as unknown as SparkWallet);

beforeEach(() => {
  jest.clearAllMocks();
  mockResolve.mockResolvedValue(mockWallet as unknown as SparkWallet);
});

describe("handleGetWithdrawalFeeQuote", () => {
  it("returns fee quote details", async () => {
    getWithdrawalFeeQuoteMock.mockResolvedValue({
      id: "quote-xyz",
      expiresAt: "2026-02-25T20:05:00Z",
      userFeeFast: { originalValue: 300, originalUnit: "SATOSHI" },
      l1BroadcastFeeFast: { originalValue: 200, originalUnit: "SATOSHI" },
      userFeeMedium: { originalValue: 150, originalUnit: "SATOSHI" },
      l1BroadcastFeeMedium: { originalValue: 100, originalUnit: "SATOSHI" },
      userFeeSlow: { originalValue: 50, originalUnit: "SATOSHI" },
      l1BroadcastFeeSlow: { originalValue: 50, originalUnit: "SATOSHI" },
    });
    const result = await handleGetWithdrawalFeeQuote(
      50000,
      "bc1q...",
      undefined,
      mockResolve,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("500");
    expect(result.content[0]?.text).toContain("quote-xyz");
  });

  it("returns error on null quote", async () => {
    getWithdrawalFeeQuoteMock.mockResolvedValue(null);
    const result = await handleGetWithdrawalFeeQuote(
      50000,
      "bc1q...",
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
  });

  it("returns error on failure", async () => {
    getWithdrawalFeeQuoteMock.mockRejectedValue(new Error("invalid address"));
    const result = await handleGetWithdrawalFeeQuote(
      50000,
      "badaddr",
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("invalid address");
  });
});

describe("handleWithdraw", () => {
  const feeQuote = {
    id: "quote-abc",
    expiresAt: "2026-02-25T20:05:00Z",
    userFeeFast: { originalValue: 300, originalUnit: "SATOSHI" },
    l1BroadcastFeeFast: { originalValue: 200, originalUnit: "SATOSHI" },
    userFeeMedium: { originalValue: 150, originalUnit: "SATOSHI" },
    l1BroadcastFeeMedium: { originalValue: 100, originalUnit: "SATOSHI" },
    userFeeSlow: { originalValue: 50, originalUnit: "SATOSHI" },
    l1BroadcastFeeSlow: { originalValue: 50, originalUnit: "SATOSHI" },
  };

  it("returns withdrawal request details", async () => {
    getWithdrawalFeeQuoteMock.mockResolvedValue(feeQuote);
    withdrawMock.mockResolvedValue({
      id: "withdraw-123",
      status: "PENDING",
    });
    const result = await handleWithdraw(
      "bc1q...",
      "FAST",
      undefined,
      undefined,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("withdraw-123");
    expect(result.content[0]?.text).toContain("PENDING");
  });

  it("returns error when fee quote is unavailable", async () => {
    getWithdrawalFeeQuoteMock.mockResolvedValue(null);
    const result = await handleWithdraw(
      "bc1q...",
      "FAST",
      undefined,
      undefined,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("No fee quote available");
    expect(withdrawMock).not.toHaveBeenCalled();
  });

  it("returns error on failure", async () => {
    getWithdrawalFeeQuoteMock.mockResolvedValue(feeQuote);
    withdrawMock.mockRejectedValue(new Error("insufficient funds"));
    const result = await handleWithdraw(
      "bc1q...",
      "FAST",
      undefined,
      undefined,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("insufficient funds");
  });
});
