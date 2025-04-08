import { IssuerSparkWallet } from "../../issuer-spark-wallet.js";
import { jest } from "@jest/globals";
import { LOCAL_WALLET_CONFIG_ECDSA } from "../../../../spark-sdk/src/services/wallet-config.js";
import { SparkWallet } from "@buildonspark/spark-sdk";
import { filterTokenBalanceForTokenPublicKey } from "@buildonspark/spark-sdk/utils";

const TEST_TIMEOUT = 80000; // 80 seconds

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

  it("[Spark] wallets should successfully complete multiple token transactions in rapid succession (5 TPS)", async () => {
    // Iteration: IssuerSparkWallet -> SparkWallet -> IssuerSparkWallet
    // 2 transactions per iteration
    // (2 transactions * 200 iterations) / 80 seconds = 5 TPS
    const MAX_ITERATIONS = 200;
    const TOKEN_AMOUNT: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });
    const { wallet: userWallet } = await SparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await issuerWallet.mintTokens(TOKEN_AMOUNT);
    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const userWalletSparkAddress = await userWallet.getSparkAddress();
    const issuerWalletSparkAddress = await issuerWallet.getSparkAddress();

    for (let i = 0; i < MAX_ITERATIONS; i++) {
      if (timeoutReached) {
        console.log(
          "Timeout reached, stopping iterations at idx: " +
            i +
            " of " +
            MAX_ITERATIONS,
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
        throw new Error(`Test failed on iteration ${i}: ${error}`);
      }
    }
  });
});
