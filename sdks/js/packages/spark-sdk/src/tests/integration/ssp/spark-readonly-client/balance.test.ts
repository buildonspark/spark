/**
 * SSP-backed integration tests for readonly balance methods.
 *
 * These cases perform an actual transfer during setup, which exercises
 * SSP GraphQL-backed swap flows before the readonly client queries the
 * coordinator for the resulting balance state.
 */
import { beforeAll, describe, expect, it, jest } from "@jest/globals";
import { type SparkReadonlyClient } from "../../../../spark-readonly-client/spark-readonly-client.node.js";
import {
  createEmptyWallet,
  createFundedWallet,
  createOwnerReadonlyClient,
  createPublicReadonlyClient,
  type FundedWallet,
} from "../../../spark-readonly-client/helpers.js";
import { retryUntilSuccess } from "../../../utils/utils.js";

describe("getOwnedBalance after a transfer", () => {
  jest.setTimeout(60_000);

  let sender: FundedWallet;
  let receiver: FundedWallet;
  let publicClient: SparkReadonlyClient;
  let ownerClient: SparkReadonlyClient;

  beforeAll(async () => {
    sender = await createFundedWallet(10_000n);
    receiver = await createEmptyWallet();
    publicClient = createPublicReadonlyClient();
    ownerClient = await createOwnerReadonlyClient(sender.mnemonic);

    await sender.wallet.transfer({
      amountSats: 5_000,
      receiverSparkAddress: receiver.sparkAddress,
    });
  });

  it("matches the sender wallet balance state after a transfer", async () => {
    const senderWalletBalance = await retryUntilSuccess(async () => {
      const balance = await sender.wallet.getBalance();
      expect(balance.satsBalance.available).toBeLessThanOrEqual(
        balance.satsBalance.owned,
      );
      return balance;
    });

    await retryUntilSuccess(async () => {
      const available = await publicClient.getAvailableBalance(
        sender.sparkAddress,
      );
      const publicOwned = await publicClient.getOwnedBalance(
        sender.sparkAddress,
      );
      const ownerOwned = await ownerClient.getOwnedBalance(sender.sparkAddress);

      expect(available).toBe(senderWalletBalance.satsBalance.available);
      expect(publicOwned).toBe(senderWalletBalance.satsBalance.owned);
      expect(ownerOwned).toBe(senderWalletBalance.satsBalance.owned);
      expect(publicOwned).toBeGreaterThanOrEqual(available);
    });
  });
});
