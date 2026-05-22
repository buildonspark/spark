import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import { handleDeposit } from "../tools/deposit-flow.js";
import { type handleFundAddress } from "../tools/funding.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;
type FundFn = typeof handleFundAddress;

function clearEnv() {
  delete process.env["BITCOIN_NETWORK"];
}

describe("handleDeposit", () => {
  let mockWallet: {
    getSingleUseDepositAddress: jest.MockedFunction<() => Promise<string>>;
    claimDeposit: jest.MockedFunction<
      (txid: string) => Promise<{ value: number }[]>
    >;
    getBalance: jest.MockedFunction<() => Promise<{ balance: bigint }>>;
  };
  let mockResolve: jest.MockedFunction<ResolveFn>;
  let mockFund: jest.MockedFunction<FundFn>;

  beforeEach(() => {
    clearEnv();
    mockWallet = {
      getSingleUseDepositAddress: jest.fn<() => Promise<string>>(),
      claimDeposit: jest.fn<(txid: string) => Promise<{ value: number }[]>>(),
      getBalance: jest.fn<() => Promise<{ balance: bigint }>>(),
    };
    mockResolve = jest
      .fn<ResolveFn>()
      .mockResolvedValue(mockWallet as unknown as SparkWallet);
    mockFund = jest.fn<FundFn>();
  });

  it("rejects on MAINNET network", async () => {
    process.env["BITCOIN_NETWORK"] = "MAINNET";

    const result = await handleDeposit(
      50_000,
      undefined,
      mockResolve,
      mockFund,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0].text).toContain("only works on the LOCAL");
    expect(mockResolve).not.toHaveBeenCalled();
  });

  it("rejects on REGTEST network", async () => {
    process.env["BITCOIN_NETWORK"] = "REGTEST";

    const result = await handleDeposit(
      50_000,
      undefined,
      mockResolve,
      mockFund,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0].text).toContain("only works on the LOCAL");
    expect(mockResolve).not.toHaveBeenCalled();
  });

  it("rejects with MAINNET override on LOCAL default", async () => {
    process.env["BITCOIN_NETWORK"] = "LOCAL";

    const result = await handleDeposit(
      50_000,
      undefined,
      mockResolve,
      mockFund,
      "normal",
      "MAINNET",
    );

    expect(result.isError).toBe(true);
    expect(result.content[0].text).toContain("LOCAL");
    expect(mockResolve).not.toHaveBeenCalled();
  });

  it("completes full deposit flow on LOCAL network", async () => {
    process.env["BITCOIN_NETWORK"] = "LOCAL";
    mockWallet.getSingleUseDepositAddress.mockResolvedValue("bcrt1pabc123");
    mockFund.mockResolvedValue({
      content: [
        {
          type: "text",
          text: "Funded 50,000 sats to bcrt1pabc123\nTransaction ID: txid123\nMined 1 blocks to confirm.",
        },
      ],
    });
    mockWallet.claimDeposit.mockResolvedValue([{ value: 50_000 }]);
    mockWallet.getBalance.mockResolvedValue({ balance: 50_000n });

    const result = await handleDeposit(
      50_000,
      undefined,
      mockResolve,
      mockFund,
    );

    expect(result.isError).toBeUndefined();
    expect(result.content[0].text).toContain("Deposit complete");
    expect(result.content[0].text).toContain("50,000 sats");
    expect(result.content[0].text).toContain("txid123");
    expect(mockWallet.getSingleUseDepositAddress).toHaveBeenCalled();
    expect(mockFund).toHaveBeenCalledWith("bcrt1pabc123", 50_000);
    expect(mockWallet.claimDeposit).toHaveBeenCalledWith("txid123");
  });

  it("propagates fund errors", async () => {
    process.env["BITCOIN_NETWORK"] = "LOCAL";
    mockWallet.getSingleUseDepositAddress.mockResolvedValue("bcrt1pabc");
    mockFund.mockResolvedValue({
      content: [{ type: "text", text: "Bitcoin RPC HTTP error: 500" }],
      isError: true,
    });

    const result = await handleDeposit(
      50_000,
      undefined,
      mockResolve,
      mockFund,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0].text).toContain("500");
    expect(mockWallet.claimDeposit).not.toHaveBeenCalled();
  });

  it("returns error when wallet resolution fails", async () => {
    process.env["BITCOIN_NETWORK"] = "LOCAL";
    mockResolve.mockRejectedValue(new Error("No wallet specified"));

    const result = await handleDeposit(
      50_000,
      undefined,
      mockResolve,
      mockFund,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0].text).toContain("No wallet specified");
  });

  it("handles claim failure", async () => {
    process.env["BITCOIN_NETWORK"] = "LOCAL";
    mockWallet.getSingleUseDepositAddress.mockResolvedValue("bcrt1pabc");
    mockFund.mockResolvedValue({
      content: [
        {
          type: "text",
          text: "Funded 50,000 sats to bcrt1pabc\nTransaction ID: txid789\nMined 1 blocks to confirm.",
        },
      ],
    });
    mockWallet.claimDeposit.mockRejectedValue(
      new Error("Deposit not confirmed"),
    );

    const result = await handleDeposit(
      50_000,
      undefined,
      mockResolve,
      mockFund,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0].text).toContain("Deposit not confirmed");
  });

  it("uses default amount of 50,000 sats", async () => {
    process.env["BITCOIN_NETWORK"] = "LOCAL";
    mockWallet.getSingleUseDepositAddress.mockResolvedValue("bcrt1pabc");
    mockFund.mockResolvedValue({
      content: [
        {
          type: "text",
          text: "Funded 50,000 sats to bcrt1pabc\nTransaction ID: txidABC\nMined 1 blocks to confirm.",
        },
      ],
    });
    mockWallet.claimDeposit.mockResolvedValue([{ value: 50_000 }]);
    mockWallet.getBalance.mockResolvedValue({ balance: 50_000n });

    const result = await handleDeposit(
      undefined,
      undefined,
      mockResolve,
      mockFund,
    );

    expect(result.isError).toBeUndefined();
    expect(mockFund).toHaveBeenCalledWith("bcrt1pabc", 50_000);
  });
});
