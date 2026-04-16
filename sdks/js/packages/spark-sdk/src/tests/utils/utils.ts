import { SparkWalletTesting } from "./spark-testing-wallet.js";
import { SparkWalletEvent } from "../../spark-wallet/types.js";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/**
 * Retry a function until it succeeds.
 * @param fn - The function to retry.
 * @param maxAttempts - The maximum number of attempts.
 * @param delayMs - The delay between attempts.
 * @returns The result of the function.
 */
export async function retryUntilSuccess<T>(
  fn: () => Promise<T>,
  { maxAttempts = 20, delayMs = 2000 } = {},
): Promise<T> {
  let err: unknown;
  for (let i = 1; i <= maxAttempts; i++) {
    try {
      return await fn();
    } catch (e) {
      err = e;
    }
    await sleep(delayMs);
  }
  throw err;
}

/**
 * Wait for a claim to be made on a wallet.
 * @param wallet - The wallet to wait for a claim on.
 * @param timeoutMs - The timeout in milliseconds.
 * @param throwOnTimeout - Whether to throw an error if the timeout is reached.
 * @returns A promise that resolves when the claim is made.
 */
export async function waitForClaim({
  wallet,
  timeoutMs = 30000,
  throwOnTimeout = false,
}: {
  wallet: SparkWalletTesting;
  timeoutMs?: number;
  throwOnTimeout?: boolean;
}): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const onClaim = () => {
      cleanup();
      resolve();
    };
    const timer = setTimeout(() => {
      cleanup();
      if (throwOnTimeout) {
        reject(new Error("claim timeout"));
      } else {
        resolve();
      }
    }, timeoutMs);
    const cleanup = () => {
      clearTimeout(timer);
    };
    wallet.once(SparkWalletEvent.TransferClaimed, onClaim);
  });
}

/**
 * Polls getBalance() until it reaches the expected value.
 *
 * After a static deposit claim, the leaves are TRANSFER_LOCKED on the SO
 * until the wallet's background stream receives the transfer event and
 * completes the auto-claim (tweak keys → sign refunds → finalize).
 * This polls until that pipeline finishes and the balance is visible.
 */
export async function waitForBalance(
  wallet: SparkWalletTesting,
  expectedBalance: bigint,
  timeoutMs: number = 30000,
): Promise<void> {
  const start = Date.now();
  let lastBalance: bigint = 0n;
  while (Date.now() - start < timeoutMs) {
    const { balance } = await wallet.getBalance();
    lastBalance = balance;
    if (balance === expectedBalance) return;
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(
    `waitForBalance timed out after ${timeoutMs}ms: expected ${expectedBalance}, last seen ${lastBalance}`,
  );
}
