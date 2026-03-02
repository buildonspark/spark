import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import { handleGetBalance, handleGetSparkAddress } from "../../tools/wallet.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

const getBalanceMock = jest.fn<() => Promise<{ balance: bigint }>>();
const getSparkAddressMock = jest.fn<() => Promise<string>>();

const mockWallet = {
  getBalance: getBalanceMock,
  getSparkAddress: getSparkAddressMock,
};

const mockResolve = jest
  .fn<(mnemonic?: string) => Promise<SparkWallet>>()
  .mockResolvedValue(mockWallet as unknown as SparkWallet);

beforeEach(() => {
  jest.clearAllMocks();
  mockResolve.mockResolvedValue(mockWallet as unknown as SparkWallet);
});

describe("handleGetBalance", () => {
  it("returns formatted balance", async () => {
    getBalanceMock.mockResolvedValue({ balance: 1250n });
    const result = await handleGetBalance(undefined, mockResolve);
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toBe("Balance: 1,250 sats");
  });

  it("returns error on SDK failure", async () => {
    getBalanceMock.mockRejectedValue(new Error("network error"));
    const result = await handleGetBalance(undefined, mockResolve);
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("network error");
  });

  it("returns error when resolve fails (no wallet configured)", async () => {
    mockResolve.mockRejectedValueOnce(new Error("No wallet specified"));
    const result = await handleGetBalance(undefined, mockResolve);
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("No wallet specified");
  });
});

describe("handleGetSparkAddress", () => {
  it("returns the spark address", async () => {
    getSparkAddressMock.mockResolvedValue(
      "spark1qpzry9x8gf2tvdw0s3jn54khce6mua7lt",
    );
    const result = await handleGetSparkAddress(undefined, mockResolve);
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain(
      "spark1qpzry9x8gf2tvdw0s3jn54khce6mua7lt",
    );
  });

  it("returns error on SDK failure", async () => {
    getSparkAddressMock.mockRejectedValue(new Error("disconnected"));
    const result = await handleGetSparkAddress(undefined, mockResolve);
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("disconnected");
  });
});
