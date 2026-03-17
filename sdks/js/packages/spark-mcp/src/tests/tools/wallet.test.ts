import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import {
  handleGetBalance,
  handleGetSparkAddress,
  handleDisconnectWallet,
} from "../../tools/wallet.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

const getBalanceMock = jest.fn<() => Promise<{ balance: bigint }>>();
const getSparkAddressMock = jest.fn<() => Promise<string>>();
const claimPendingTransfersMock = jest
  .fn<() => Promise<unknown[]>>()
  .mockResolvedValue([]);

const mockWallet = {
  getBalance: getBalanceMock,
  getSparkAddress: getSparkAddressMock,
  claimPendingTransfers: claimPendingTransfersMock,
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

describe("handleDisconnectWallet", () => {
  it("reports wallet disconnected when eviction succeeds", async () => {
    const mockEvict = jest.fn<() => Promise<boolean>>().mockResolvedValue(true);
    const result = await handleDisconnectWallet(
      "some mnemonic",
      undefined,
      mockEvict,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("Wallet disconnected");
    expect(mockEvict).toHaveBeenCalledWith("some mnemonic", undefined);
  });

  it("reports no cached wallet when eviction returns false", async () => {
    const mockEvict = jest
      .fn<() => Promise<boolean>>()
      .mockResolvedValue(false);
    const result = await handleDisconnectWallet(
      "some mnemonic",
      undefined,
      mockEvict,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("No cached wallet");
  });

  it("returns raw output when requested", async () => {
    const mockEvict = jest.fn<() => Promise<boolean>>().mockResolvedValue(true);
    const result = await handleDisconnectWallet(
      "some mnemonic",
      undefined,
      mockEvict,
      "raw",
    );
    const parsed = JSON.parse(result.content[0]!.text);
    expect(parsed.evicted).toBe(true);
  });

  it("returns error on failure", async () => {
    const mockEvict = jest
      .fn<() => Promise<boolean>>()
      .mockRejectedValue(new Error("cleanup failed"));
    const result = await handleDisconnectWallet(
      "some mnemonic",
      undefined,
      mockEvict,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("cleanup failed");
  });
});
