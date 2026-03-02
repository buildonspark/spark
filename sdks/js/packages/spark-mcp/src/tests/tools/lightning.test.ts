import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import {
  handleCreateInvoice,
  handlePayInvoice,
  handleGetLightningFeeEstimate,
} from "../../tools/lightning.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type Invoice = { encodedInvoice: string };
type LightningReceiveRequest = { invoice: Invoice };
type LightningSendRequest = { id: string };
type WalletTransfer = { id?: string; status?: string; totalValue?: number };

const createLightningInvoiceMock =
  jest.fn<
    (params: {
      amountSats: number;
      memo?: string;
    }) => Promise<LightningReceiveRequest>
  >();
const payLightningInvoiceMock =
  jest.fn<
    (params: {
      invoice: string;
      maxFeeSats: number;
    }) => Promise<LightningSendRequest | WalletTransfer>
  >();
const getLightningSendFeeEstimateMock =
  jest.fn<(params: { encodedInvoice: string }) => Promise<number>>();

const mockWallet = {
  createLightningInvoice: createLightningInvoiceMock,
  payLightningInvoice: payLightningInvoiceMock,
  getLightningSendFeeEstimate: getLightningSendFeeEstimateMock,
};

const mockResolve = jest
  .fn<(mnemonic?: string) => Promise<SparkWallet>>()
  .mockResolvedValue(mockWallet as unknown as SparkWallet);

beforeEach(() => {
  jest.clearAllMocks();
  mockResolve.mockResolvedValue(mockWallet as unknown as SparkWallet);
});

describe("handleCreateInvoice", () => {
  it("returns BOLT11 invoice string", async () => {
    createLightningInvoiceMock.mockResolvedValue({
      invoice: { encodedInvoice: "lnbc500n1ptest..." },
    });
    const result = await handleCreateInvoice(
      500,
      "Coffee",
      undefined,
      mockResolve,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("lnbc500n1ptest");
    expect(result.content[0]?.text).toContain("500 sats");
  });

  it("returns error on failure", async () => {
    createLightningInvoiceMock.mockRejectedValue(new Error("node offline"));
    const result = await handleCreateInvoice(
      100,
      undefined,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("node offline");
  });
});

describe("handlePayInvoice", () => {
  it("returns payment success with ID", async () => {
    payLightningInvoiceMock.mockResolvedValue({
      id: "pay-abc",
    });
    const result = await handlePayInvoice(
      "lnbc...",
      10,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("pay-abc");
  });

  it("returns error on payment failure", async () => {
    payLightningInvoiceMock.mockRejectedValue(new Error("no route"));
    const result = await handlePayInvoice(
      "lnbc...",
      10,
      undefined,
      mockResolve,
    );
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("no route");
  });
});

describe("handleGetLightningFeeEstimate", () => {
  it("returns estimated fee", async () => {
    getLightningSendFeeEstimateMock.mockResolvedValue(3);
    const result = await handleGetLightningFeeEstimate(
      "lnbc...",
      undefined,
      mockResolve,
    );
    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("3 sats");
  });
});
