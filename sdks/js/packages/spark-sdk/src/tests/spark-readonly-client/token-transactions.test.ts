/**
 * Integration tests for SparkReadonlyClient.getTokenTransactions.
 *
 * Tests the cursor-based pagination query for token transactions.
 * Since creating actual token transactions requires issuance/mint
 * infrastructure, these tests primarily verify that the method correctly
 * returns empty results for wallets with no token activity and handles
 * validation correctly.
 */
import { describe, it, expect, jest, beforeAll } from "@jest/globals";
import {
  createEmptyWallet,
  createPublicReadonlyClient,
  type FundedWallet,
} from "./helpers.js";
import { SparkReadonlyClient } from "../../spark-readonly-client/spark-readonly-client.node.js";
import { SparkValidationError } from "../../errors/types.js";

describe("getTokenTransactions", () => {
  jest.setTimeout(30_000);

  let emptyWallet: FundedWallet;
  let publicClient: SparkReadonlyClient;

  beforeAll(async () => {
    emptyWallet = await createEmptyWallet();
    publicClient = createPublicReadonlyClient();
  });

  // ── Happy Paths ──────────────────────────────────────────

  it("returns empty results when querying by a wallet with no token activity", async () => {
    const result = await publicClient.getTokenTransactions({
      sparkAddresses: [emptyWallet.sparkAddress],
    });
    expect(result.transactions).toHaveLength(0);
  });

  it("returns results with default params (no filters)", async () => {
    const result = await publicClient.getTokenTransactions();
    // Should succeed with an empty or non-empty result depending on state
    expect(result.transactions).toBeDefined();
    expect(result.pageResponse).toBeDefined();
  });

  it("respects pageSize parameter", async () => {
    const result = await publicClient.getTokenTransactions({
      pageSize: 1,
    });
    expect(result.transactions.length).toBeLessThanOrEqual(1);
  });

  it("supports direction parameter", async () => {
    const nextResult = await publicClient.getTokenTransactions({
      direction: "NEXT",
    });
    expect(nextResult.transactions).toBeDefined();

    const prevResult = await publicClient.getTokenTransactions({
      direction: "PREVIOUS",
    });
    expect(prevResult.transactions).toBeDefined();
  });

  // ── Unhappy Paths ────────────────────────────────────────

  it("rejects pageSize = 0", async () => {
    await expect(
      publicClient.getTokenTransactions({ pageSize: 0 }),
    ).rejects.toThrow(SparkValidationError);
  });

  it("rejects fractional pageSize", async () => {
    await expect(
      publicClient.getTokenTransactions({ pageSize: 3.14 }),
    ).rejects.toThrow(SparkValidationError);
  });

  it("rejects negative pageSize", async () => {
    await expect(
      publicClient.getTokenTransactions({ pageSize: -10 }),
    ).rejects.toThrow(SparkValidationError);
  });

  it("throws on invalid spark address in filter", async () => {
    await expect(
      publicClient.getTokenTransactions({
        sparkAddresses: ["not-a-valid-address"],
      }),
    ).rejects.toThrow();
  });

  it("throws on invalid token identifier in filter", async () => {
    await expect(
      publicClient.getTokenTransactions({
        tokenIdentifiers: ["not-a-valid-token-id"],
      }),
    ).rejects.toThrow();
  });
});
