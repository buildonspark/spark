import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import {
  handleGetDepositAddress,
  handleClaimDeposit,
} from "../../tools/deposits.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

const getSingleUseDepositAddressMock = jest.fn<() => Promise<string>>();
const claimDepositMock =
  jest.fn<(txid: string) => Promise<{ value: number }[]>>();
const getBalanceMock = jest.fn<() => Promise<{ balance: bigint }>>();

const mockWallet = {
  getSingleUseDepositAddress: getSingleUseDepositAddressMock,
  claimDeposit: claimDepositMock,
  getBalance: getBalanceMock,
};

const mockResolve = jest
  .fn<(mnemonic?: string) => Promise<SparkWallet>>()
  .mockResolvedValue(mockWallet as unknown as SparkWallet);

beforeEach(() => {
  jest.clearAllMocks();
  mockResolve.mockResolvedValue(mockWallet as unknown as SparkWallet);
});

describe("handleGetDepositAddress", () => {
  it("returns deposit address", async () => {
    getSingleUseDepositAddressMock.mockResolvedValue("bcrt1qtest");
    const result = await handleGetDepositAddress(undefined, mockResolve);
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("bcrt1qtest");
  });

  it("returns error on failure", async () => {
    getSingleUseDepositAddressMock.mockRejectedValue(
      new Error("network error"),
    );
    const result = await handleGetDepositAddress(undefined, mockResolve);
    expect(result.isError).toBe(true);
  });
});

describe("handleClaimDeposit", () => {
  it("returns claimed sats after balance settles", async () => {
    getBalanceMock
      .mockResolvedValueOnce({ balance: 0n }) // prior balance
      .mockResolvedValueOnce({ balance: 50_000n }); // settled
    claimDepositMock.mockResolvedValue([{ value: 50_000 }]);
    const result = await handleClaimDeposit(
      "abc123",
      undefined,
      mockResolve,
      5_000,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("50,000");
  });

  it("returns error on failure", async () => {
    getBalanceMock.mockResolvedValue({ balance: 0n });
    claimDepositMock.mockRejectedValue(new Error("already claimed"));
    const result = await handleClaimDeposit(
      "abc123",
      undefined,
      mockResolve,
      5_000,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("already claimed");
  });
});
