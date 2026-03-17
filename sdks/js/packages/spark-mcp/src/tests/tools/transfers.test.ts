import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import {
  handleSendTransfer,
  handleSendMultiTransfer,
  handleGetTransfer,
  handleListTransfers,
} from "../../tools/transfers.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type WalletTransfer = {
  id: string;
  status: string;
  totalValue: number;
  createdTime: Date | undefined;
  updatedTime: Date | undefined;
  expiryTime: Date | undefined;
  type: string;
  transferDirection: string;
  senderIdentityPublicKey: string;
  receiverIdentityPublicKey: string;
  sparkInvoice: string | undefined;
  leaves: unknown[];
};

const getBalanceMock = jest.fn<() => Promise<{ balance: bigint }>>();
const transferMock =
  jest.fn<
    (p: {
      receiverSparkAddress: string;
      amountSats: number;
    }) => Promise<WalletTransfer>
  >();
const transferV2Mock =
  jest.fn<
    (p: {
      receivers: Array<{ receiverSparkAddress: string; amountSats: number }>;
    }) => Promise<WalletTransfer>
  >();
const getTransferMock =
  jest.fn<(id: string) => Promise<WalletTransfer | undefined>>();
const getTransfersMock =
  jest.fn<
    (
      limit?: number,
      offset?: number,
    ) => Promise<{ transfers: WalletTransfer[]; offset: number }>
  >();
const mockWallet = {
  getBalance: getBalanceMock,
  transfer: transferMock,
  transferV2: transferV2Mock,
  getTransfer: getTransferMock,
  getTransfers: getTransfersMock,
};

const mockResolve = jest
  .fn<(mnemonic?: string) => Promise<SparkWallet>>()
  .mockResolvedValue(mockWallet as unknown as SparkWallet);

const sampleTransfer: WalletTransfer = {
  id: "txn-123",
  status: "COMPLETED",
  totalValue: 1000,
  createdTime: new Date("2024-01-01"),
  updatedTime: new Date("2024-01-01"),
  expiryTime: undefined,
  type: "TRANSFER",
  transferDirection: "OUTGOING",
  senderIdentityPublicKey: "sender-pub-key-abc",
  receiverIdentityPublicKey: "receiver-pub-key-xyz",
  sparkInvoice: undefined,
  leaves: [],
};

beforeEach(() => {
  jest.clearAllMocks();
  mockResolve.mockResolvedValue(mockWallet as unknown as SparkWallet);
});

describe("handleSendTransfer", () => {
  it("returns transfer id and status", async () => {
    getBalanceMock.mockResolvedValue({ balance: 5000n });
    transferMock.mockResolvedValue(sampleTransfer);
    const result = await handleSendTransfer(
      "sparkl1abc",
      1000,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("txn-123");
  });

  it("returns insufficient balance error before calling transfer", async () => {
    getBalanceMock.mockResolvedValue({ balance: 500n });
    const result = await handleSendTransfer(
      "sparkl1abc",
      1000,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("Insufficient balance");
    expect(transferMock).not.toHaveBeenCalled();
  });

  it("returns error on transfer failure", async () => {
    getBalanceMock.mockResolvedValue({ balance: 5000n });
    transferMock.mockRejectedValue(new Error("insufficient funds"));
    const result = await handleSendTransfer(
      "sparkl1abc",
      1000,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("insufficient funds");
  });
});

describe("handleSendMultiTransfer", () => {
  const receivers = [
    { receiverSparkAddress: "sparkl1abc", amountSats: 500 },
    { receiverSparkAddress: "sparkl1def", amountSats: 300 },
  ];

  it("returns transfer id and receiver count", async () => {
    getBalanceMock.mockResolvedValue({ balance: 5000n });
    transferV2Mock.mockResolvedValue(sampleTransfer);
    const result = await handleSendMultiTransfer(
      receivers,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("txn-123");
    expect(result.content[0]?.text).toContain("Receivers: 2");
  });

  it("returns insufficient balance error when total exceeds balance", async () => {
    getBalanceMock.mockResolvedValue({ balance: 500n });
    const result = await handleSendMultiTransfer(
      receivers,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("Insufficient balance");
    expect(transferV2Mock).not.toHaveBeenCalled();
  });

  it("returns error on transfer failure", async () => {
    getBalanceMock.mockResolvedValue({ balance: 5000n });
    transferV2Mock.mockRejectedValue(new Error("network timeout"));
    const result = await handleSendMultiTransfer(
      receivers,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("network timeout");
  });

  it("returns raw JSON when output is raw", async () => {
    getBalanceMock.mockResolvedValue({ balance: 5000n });
    transferV2Mock.mockResolvedValue(sampleTransfer);
    const result = await handleSendMultiTransfer(
      receivers,
      undefined,
      mockResolve,
      "raw",
    );
    expect(result.isError).toBeFalsy();
    const parsed = JSON.parse(result.content[0]!.text);
    expect(parsed.id).toBe("txn-123");
    expect(parsed.status).toBe("COMPLETED");
  });

  it("returns verbose output with receiver breakdown", async () => {
    getBalanceMock.mockResolvedValue({ balance: 5000n });
    transferV2Mock.mockResolvedValue(sampleTransfer);
    const result = await handleSendMultiTransfer(
      receivers,
      undefined,
      mockResolve,
      "verbose",
    );
    expect(result.isError).toBeFalsy();
    const text = result.content[0]!.text;
    expect(text).toContain("Direction: OUTGOING");
    expect(text).toContain("Receivers:");
    expect(text).toContain("sparkl1abc: 500 sats");
    expect(text).toContain("sparkl1def: 300 sats");
  });
});

describe("handleGetTransfer", () => {
  it("returns transfer details", async () => {
    getTransferMock.mockResolvedValue(sampleTransfer);
    const result = await handleGetTransfer("txn-123", undefined, mockResolve);
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("txn-123");
  });

  it("returns error when transfer not found", async () => {
    getTransferMock.mockResolvedValue(undefined);
    const result = await handleGetTransfer("missing", undefined, mockResolve);
    expect(result.isError).toBe(true);
  });
});

describe("handleListTransfers", () => {
  it("returns formatted list", async () => {
    getTransfersMock.mockResolvedValue({
      transfers: [sampleTransfer],
      offset: 0,
    });
    const result = await handleListTransfers(undefined, mockResolve);
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("txn-123");
  });

  it("returns empty message when no transfers", async () => {
    getTransfersMock.mockResolvedValue({ transfers: [], offset: 0 });
    const result = await handleListTransfers(undefined, mockResolve);
    expect(result.content[0]?.text).toContain("No transfers");
  });

  it("returns raw JSON when output is raw", async () => {
    getTransfersMock.mockResolvedValue({
      transfers: [sampleTransfer],
      offset: 0,
    });
    const result = await handleListTransfers(undefined, mockResolve, "raw");
    const parsed = JSON.parse(result.content[0]!.text);
    expect(parsed).toHaveLength(1);
    expect(parsed[0].id).toBe("txn-123");
    expect(parsed[0].senderIdentityPublicKey).toBe("sender-pub-key-abc");
  });

  it("returns verbose output with all fields", async () => {
    getTransfersMock.mockResolvedValue({
      transfers: [sampleTransfer],
      offset: 0,
    });
    const result = await handleListTransfers(undefined, mockResolve, "verbose");
    const text = result.content[0]!.text;
    expect(text).toContain("Direction: OUTGOING");
    expect(text).toContain("Sender: sender-pub-key-abc");
    expect(text).toContain("Receiver: receiver-pub-key-xyz");
    expect(text).toContain("Type: TRANSFER");
  });
});

describe("output modes for handleGetTransfer", () => {
  it("returns raw JSON", async () => {
    getTransferMock.mockResolvedValue(sampleTransfer);
    const result = await handleGetTransfer(
      "txn-123",
      undefined,
      mockResolve,
      "raw",
    );
    const parsed = JSON.parse(result.content[0]!.text);
    expect(parsed.id).toBe("txn-123");
    expect(parsed.receiverIdentityPublicKey).toBe("receiver-pub-key-xyz");
  });

  it("returns verbose output", async () => {
    getTransferMock.mockResolvedValue(sampleTransfer);
    const result = await handleGetTransfer(
      "txn-123",
      undefined,
      mockResolve,
      "verbose",
    );
    const text = result.content[0]!.text;
    expect(text).toContain("Direction: OUTGOING");
    expect(text).toContain("Sender: sender-pub-key-abc");
  });
});
