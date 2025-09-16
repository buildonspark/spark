import { SparkWalletTesting } from "./spark-testing-wallet.js";
import { SparkWalletEvent } from "../../index.js";

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
