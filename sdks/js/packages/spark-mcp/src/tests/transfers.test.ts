import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import { handleSendTransfer } from "../tools/transfers.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;

describe("handleSendTransfer", () => {
  let mockWallet: {
    getBalance: jest.MockedFunction<() => Promise<{ balance: bigint }>>;
    transfer: jest.MockedFunction<
      (args: {
        receiverSparkAddress: string;
        amountSats: number;
      }) => Promise<{ id: string; status: string }>
    >;
  };
  let mockResolve: jest.MockedFunction<ResolveFn>;

  beforeEach(() => {
    mockWallet = {
      getBalance: jest.fn<() => Promise<{ balance: bigint }>>(),
      transfer:
        jest.fn<
          (args: {
            receiverSparkAddress: string;
            amountSats: number;
          }) => Promise<{ id: string; status: string }>
        >(),
    };
    mockResolve = jest
      .fn<ResolveFn>()
      .mockResolvedValue(mockWallet as unknown as SparkWallet);
  });

  it("returns insufficient balance error when wallet has less than transfer amount", async () => {
    mockWallet.getBalance.mockResolvedValue({ balance: 500n });

    const result = await handleSendTransfer(
      "sparkl1abc",
      1000,
      undefined,
      mockResolve,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0]!.text).toContain("Insufficient balance");
    expect(result.content[0]!.text).toContain("500 sats");
    expect(result.content[0]!.text).toContain("1,000 sats");
    expect(result.content[0]!.text).toContain("settling");
    expect(mockWallet.transfer).not.toHaveBeenCalled();
  });

  it("returns insufficient balance error when balance is zero", async () => {
    mockWallet.getBalance.mockResolvedValue({ balance: 0n });

    const result = await handleSendTransfer(
      "sparkl1abc",
      1000,
      undefined,
      mockResolve,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0]!.text).toContain("Insufficient balance");
    expect(mockWallet.transfer).not.toHaveBeenCalled();
  });

  it("proceeds with transfer when balance is sufficient", async () => {
    mockWallet.getBalance.mockResolvedValue({ balance: 5000n });
    mockWallet.transfer.mockResolvedValue({
      id: "txn-123",
      status: "COMPLETED",
    });

    const result = await handleSendTransfer(
      "sparkl1abc",
      1000,
      undefined,
      mockResolve,
    );

    expect(result.isError).toBeUndefined();
    expect(result.content[0]!.text).toContain("Transfer sent");
    expect(result.content[0]!.text).toContain("txn-123");
    expect(mockWallet.transfer).toHaveBeenCalledWith({
      receiverSparkAddress: "sparkl1abc",
      amountSats: 1000,
    });
  });

  it("proceeds when balance exactly equals amount", async () => {
    mockWallet.getBalance.mockResolvedValue({ balance: 1000n });
    mockWallet.transfer.mockResolvedValue({
      id: "txn-456",
      status: "COMPLETED",
    });

    const result = await handleSendTransfer(
      "sparkl1abc",
      1000,
      undefined,
      mockResolve,
    );

    expect(result.isError).toBeUndefined();
    expect(result.content[0]!.text).toContain("Transfer sent");
  });

  it("returns error when transfer SDK call fails", async () => {
    mockWallet.getBalance.mockResolvedValue({ balance: 5000n });
    mockWallet.transfer.mockRejectedValue(new Error("network timeout"));

    const result = await handleSendTransfer(
      "sparkl1abc",
      1000,
      undefined,
      mockResolve,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0]!.text).toContain("network timeout");
  });
});
