/**
 * Integration tests for SparkReadonlyClient.getSparkInvoices.
 *
 * Creates wallets, generates spark invoices, and queries their status
 * through the readonly client.
 */
import { beforeAll, describe, expect, it, jest } from "@jest/globals";
import { SparkValidationError } from "../../errors/types.js";
import { SparkReadonlyClient } from "../../spark-readonly-client/spark-readonly-client.node.js";
import {
  createEmptyWallet,
  createPublicReadonlyClient,
  type FundedWallet,
} from "./helpers.js";

describe("getSparkInvoices", () => {
  jest.setTimeout(30_000);

  let walletInfo: FundedWallet;
  let invoice: string;
  let publicClient: SparkReadonlyClient;

  beforeAll(async () => {
    walletInfo = await createEmptyWallet();

    const tomorrow = new Date(Date.now() + 1000 * 60 * 60 * 24);
    invoice = await walletInfo.wallet.createSatsInvoice({
      amount: 1_000,
      memo: "readonly-client-test",
      expiryTime: tomorrow,
    });

    publicClient = createPublicReadonlyClient();
  });

  // ── Happy Paths ──────────────────────────────────────────

  it("returns status for a known invoice", async () => {
    const result = await publicClient.getSparkInvoices({
      invoices: [invoice],
    });
    expect(result.invoiceStatuses.length).toBe(1);
    expect(result.invoiceStatuses[0]!.invoice).toBe(invoice);
  });

  it("returns statuses for multiple invoices", async () => {
    const tomorrow = new Date(Date.now() + 1000 * 60 * 60 * 24);
    const invoice2 = await walletInfo.wallet.createSatsInvoice({
      amount: 2_000,
      memo: "test-2",
      expiryTime: tomorrow,
    });

    const result = await publicClient.getSparkInvoices({
      invoices: [invoice, invoice2],
    });
    expect(result.invoiceStatuses.length).toBe(2);
  });

  it("respects limit parameter", async () => {
    const tomorrow = new Date(Date.now() + 1000 * 60 * 60 * 24);
    const invoices: string[] = [];
    for (let i = 0; i < 3; i++) {
      invoices.push(
        await walletInfo.wallet.createSatsInvoice({
          amount: 500,
          memo: `limit-test-${i}`,
          expiryTime: tomorrow,
        }),
      );
    }

    const result = await publicClient.getSparkInvoices({
      invoices,
      limit: 1,
    });
    expect(result.invoiceStatuses.length).toBeLessThanOrEqual(1);
  });

  // ── Unhappy Paths ────────────────────────────────────────

  it("rejects empty invoices array", async () => {
    await expect(
      publicClient.getSparkInvoices({ invoices: [] }),
    ).rejects.toThrow(SparkValidationError);
  });

  it("rejects limit = 0", async () => {
    await expect(
      publicClient.getSparkInvoices({ invoices: [invoice], limit: 0 }),
    ).rejects.toThrow(SparkValidationError);
  });

  it("rejects negative offset", async () => {
    await expect(
      publicClient.getSparkInvoices({ invoices: [invoice], offset: -1 }),
    ).rejects.toThrow(SparkValidationError);
  });
});
