import { IssuerSparkWallet } from "../../issuer-spark-wallet.js";
import { jest } from "@jest/globals";
import { LOCAL_WALLET_CONFIG_ECDSA } from "../../../../spark-sdk/src/services/wallet-config.js";
import { SparkWallet } from "@buildonspark/spark-sdk";
import { filterTokenBalanceForTokenPublicKey } from "@buildonspark/spark-sdk/utils";

const TEST_TIMEOUT = 600_000; // 10 minutes
const TOKEN_AMOUNT: bigint = 1000n;

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

describe("Stress test for token transfers", () => {
  jest.setTimeout(TEST_TIMEOUT);

  let timeoutReached = false;
  let timeoutId: NodeJS.Timeout;

  beforeEach(() => {
    timeoutReached = false;
    timeoutId = setTimeout(() => {
      timeoutReached = true;
    }, TEST_TIMEOUT);
  });

  afterEach(() => {
    clearTimeout(timeoutId);
  });

  it("[Spark] wallets should maintain state consistency through repeated token transfer cycles", async () => {
    const maxTransactionCycles = 50;

    const start_time = Date.now();
    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });
    const { wallet: userWallet } = await SparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await issuerWallet.mintTokens(TOKEN_AMOUNT);
    await sleep(1000);
    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const userWalletSparkAddress = await userWallet.getSparkAddress();
    const issuerWalletSparkAddress = await issuerWallet.getSparkAddress();

    for (let i = 0; i < maxTransactionCycles; i++) {
      if (timeoutReached) {
        console.log(
          "Timeout reached, stopping iterations at idx: " +
            i +
            " of " +
            maxTransactionCycles,
        );
        break;
      }
      try {
        // Transfer tokens from issuer to user
        await issuerWallet.transferTokens({
          tokenPublicKey,
          tokenAmount: TOKEN_AMOUNT,
          receiverSparkAddress: userWalletSparkAddress,
        });
        await sleep(1000);
        const issuerBalance = await issuerWallet.getIssuerTokenBalance();
        const userBalanceObj = await userWallet.getBalance();
        const userBalance = filterTokenBalanceForTokenPublicKey(
          userBalanceObj?.tokenBalances,
          tokenPublicKey,
        );
        expect(issuerBalance.balance).toEqual(0n);
        expect(userBalance.balance).toEqual(TOKEN_AMOUNT);

        // Transfer tokens from user to issuer
        await userWallet.transferTokens({
          tokenPublicKey,
          tokenAmount: TOKEN_AMOUNT,
          receiverSparkAddress: issuerWalletSparkAddress,
        });
        await sleep(1000);
        const userBalanceObjAfterTransferBack = await userWallet.getBalance();
        const userBalanceAfterTransferBack =
          filterTokenBalanceForTokenPublicKey(
            userBalanceObjAfterTransferBack?.tokenBalances,
            tokenPublicKey,
          );
        const issuerBalanceAfterTransferBack =
          await issuerWallet.getIssuerTokenBalance();
        expect(userBalanceAfterTransferBack.balance).toEqual(0n);
        expect(issuerBalanceAfterTransferBack.balance).toEqual(TOKEN_AMOUNT);
      } catch (error: any) {
        const end_time = Date.now();
        const duration_ms = end_time - start_time;
        const minutes = Math.floor(duration_ms / 60000);
        const seconds = ((duration_ms % 60000) / 1000).toFixed(2);
        throw new Error(
          `Test failed on iteration ${i}: ${error} in ${duration_ms}ms (${minutes}m ${seconds}s)`,
        );
      }
    }
    const end_time = Date.now();
    const duration_ms = end_time - start_time;
    const minutes = Math.floor(duration_ms / 60000);
    const seconds = ((duration_ms % 60000) / 1000).toFixed(2);
    console.log(
      `Time taken to process ${maxTransactionCycles} transaction cycles: ${duration_ms}ms (${minutes}m ${seconds}s)`,
    );
  });

  it("should correctly process a small batch of concurrent transfer requests", async () => {
    const minTargetTps = 1;
    const minTargetSuccessPercentage = 50;
    const concurrentWalletPairs = 30;

    const walletPairs = await Promise.all(
      Array(concurrentWalletPairs)
        .fill(0)
        .map(async () => {
          const issuer = await IssuerSparkWallet.initialize({
            options: LOCAL_WALLET_CONFIG_ECDSA,
          });
          const user = await SparkWallet.initialize({
            options: LOCAL_WALLET_CONFIG_ECDSA,
          });

          await issuer.wallet.mintTokens(TOKEN_AMOUNT);
          const tokenPublicKey = await issuer.wallet.getIdentityPublicKey();
          const userAddress = await user.wallet.getSparkAddress();

          return { issuer, tokenPublicKey, userAddress };
        }),
    );

    const transactions = walletPairs.map(
      ({ issuer, tokenPublicKey, userAddress }) =>
        async () => {
          const start_time = Date.now();
          try {
            await issuer.wallet.transferTokens({
              tokenPublicKey,
              tokenAmount: TOKEN_AMOUNT,
              receiverSparkAddress: userAddress,
            });
            const end_time = Date.now();
            return { success: true, duration: end_time - start_time };
          } catch (error) {
            const end_time = Date.now();
            return { success: false, error, duration: end_time - start_time };
          }
        },
    );

    const startTime = Date.now();
    const results = await Promise.all(transactions.map((tx) => tx()));
    const duration = (Date.now() - startTime) / 1000;

    const errors = results.filter((r: { success: boolean }) => !r.success);
    errors.forEach((e, i) => {
      console.log(`Error ${i}: ${e.error} occurred in ${e.duration}ms`);
    });

    const numSuccessfulTransfers = results.filter(
      (r: { success: boolean }) => r.success,
    ).length;
    const averageSuccessDuration =
      results
        .filter((r: { success: boolean }) => r.success)
        .reduce((acc, r) => acc + r.duration, 0) /
      (numSuccessfulTransfers || 1);
    const successRate = (numSuccessfulTransfers / transactions.length) * 100;
    const transfersPerSecond = numSuccessfulTransfers / duration;

    console.log(`Total transfers: ${transactions.length}`);
    console.log(`Duration: ${duration.toFixed(2)}s`);
    console.log(`Actual transfers/second: ${transfersPerSecond.toFixed(2)}`);
    console.log(
      `Average successful transfer duration: ${averageSuccessDuration}ms`,
    );
    console.log(`Success rate: ${successRate.toFixed(2)}%`);

    expect(transfersPerSecond).toBeGreaterThanOrEqual(minTargetTps);
    expect(successRate).toBeGreaterThanOrEqual(minTargetSuccessPercentage);
  });
});
