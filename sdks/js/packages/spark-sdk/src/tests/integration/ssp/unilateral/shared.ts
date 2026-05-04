import { expect } from "@jest/globals";

import { SparkError } from "../../../../errors/index.js";
import { SparkWalletEvent } from "../../../../spark-wallet/types.js";
import {
  SparkWalletTestingIntegration,
  SparkWalletTestingIntegrationWithStream,
} from "../../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../../utils/test-faucet.js";
import { broadcastUnilateralExit } from "../../../utils/unilateral-exit-helpers.js";
import { retryUntilSuccess } from "../../../utils/utils.js";

export const DEPOSIT_AMOUNT = 10_000n;
export const TRANSFER_AMOUNT = 1_000;
export const EXTERNAL_FUNDING_AMOUNT = 100_000n;

export const closeWallets = async (
  ...wallets: SparkWalletTestingIntegration[]
): Promise<void> => {
  await Promise.allSettled(
    wallets.map((wallet) => wallet.getConnectionManager().closeConnections()),
  );
};

export const waitForWalletBalance = async (
  wallet: SparkWalletTestingIntegration,
  expectedBalance: bigint,
): Promise<void> => {
  await retryUntilSuccess(
    async () => {
      const { balance } = await wallet.getBalance();
      expect(balance).toBe(expectedBalance);
      return balance;
    },
    { maxAttempts: 40, delayMs: 500 },
  );
};

export const waitForWalletLeaves = async (
  wallet: SparkWalletTestingIntegration,
  expectedBalance: bigint,
) => {
  return await retryUntilSuccess(
    async () => {
      const { balance } = await wallet.getBalance();
      expect(balance).toBe(expectedBalance);

      const leaves = await wallet.getLeaves();
      expect(leaves.length).toBeGreaterThan(0);

      const leavesBalance = leaves.reduce(
        (sum, leaf) => sum + BigInt(leaf.value),
        0n,
      );
      expect(leavesBalance).toBe(expectedBalance);

      return leaves;
    },
    { maxAttempts: 40, delayMs: 500 },
  );
};

export const initializeWalletWithConnectedStream = async () => {
  let resolveStreamConnected!: () => void;
  const streamConnectedPromise = new Promise<void>((resolve) => {
    resolveStreamConnected = resolve;
  });

  const { wallet } = await SparkWalletTestingIntegrationWithStream.initialize({
    options: {
      network: "LOCAL",
      events: {
        [SparkWalletEvent.StreamConnected]: () => {
          resolveStreamConnected();
        },
      },
    },
  });

  await streamConnectedPromise;
  return wallet;
};

export const createClaimedWallet = async (
  faucet: BitcoinFaucet,
  amount: bigint,
) => {
  const wallet = await initializeWalletWithConnectedStream();
  const depositAddress = await wallet.getSingleUseDepositAddress();

  if (!depositAddress) {
    throw new SparkError("Deposit address not found");
  }

  const signedTx = await faucet.sendToAddress(depositAddress, amount);
  await faucet.mineBlocksAndWaitForMiningToComplete(6);
  await wallet.claimDeposit(signedTx.id);
  await waitForWalletLeaves(wallet, amount);

  return wallet;
};

/**
 * Wait for `expectedBalance`'s worth of leaves to settle on `wallet`, pick the
 * largest one, and run it through the full unilateral-exit broadcast pipeline.
 * Used by both SSP unilateral-exit integration tests, which differ only in how
 * the wallet acquired its leaves (transfer vs lightning).
 */
export const unilateralExitLargestLeaf = async (
  faucet: BitcoinFaucet,
  wallet: SparkWalletTestingIntegration,
  expectedBalance: bigint,
): Promise<void> => {
  const leaves = await waitForWalletLeaves(wallet, expectedBalance);
  const largestLeaf = [...leaves].sort(
    (left, right) =>
      Number(BigInt(right.value) - BigInt(left.value)) ||
      left.id.localeCompare(right.id),
  )[0]!;

  await broadcastUnilateralExit(
    faucet,
    wallet,
    [largestLeaf],
    EXTERNAL_FUNDING_AMOUNT,
  );
};
