import { filterTokenBalanceForTokenIdentifier } from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { SparkWalletTesting } from "@buildonspark/spark-sdk/test-utils";
import { TEST_CONFIGS } from "./test-configs.js";

describe.each(TEST_CONFIGS)(
  "token output tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    it("should consolidate token outputs using optimizeTokenOutputs", async () => {
      const totalAmount = 10000n;
      const smallTransferAmount = 10n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });

      await issuerWallet.createToken({
        tokenName: `${name}OPT`,
        tokenTicker: "OPT",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      await issuerWallet.mintTokens(totalAmount);
      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();
      expect(tokenIdentifier).toBeDefined();

      const userSparkAddress = await userWallet.getSparkAddress();
      const issuerSparkAddress = await issuerWallet.getSparkAddress();

      const transfersToUser = Array.from({ length: 60 }, () => ({
        tokenIdentifier: tokenIdentifier!,
        tokenAmount: smallTransferAmount,
        receiverSparkAddress: userSparkAddress,
      }));

      await issuerWallet.batchTransferTokens(transfersToUser);

      const transfersToIssuer = Array.from({ length: 60 }, () => ({
        tokenIdentifier: tokenIdentifier!,
        tokenAmount: smallTransferAmount,
        receiverSparkAddress: issuerSparkAddress,
      }));

      await userWallet.batchTransferTokens(transfersToIssuer);

      const balanceBeforeOptimization =
        await issuerWallet.getIssuerTokenBalance();
      expect(balanceBeforeOptimization.balance).toBe(totalAmount);

      const outputsBeforeOptimization =
        await issuerWallet.getTokenOutputStats(tokenIdentifier);
      expect(outputsBeforeOptimization).toBeDefined();
      expect(outputsBeforeOptimization.outputCount).toBe(61);

      await issuerWallet.optimizeTokenOutputs();

      await (issuerWallet as any).syncTokenOutputs();

      const balanceAfterOptimization =
        await issuerWallet.getIssuerTokenBalance();
      expect(balanceAfterOptimization.balance).toBe(totalAmount);

      const outputsAfterOptimization =
        await issuerWallet.getTokenOutputStats(tokenIdentifier);
      expect(outputsAfterOptimization).toBeDefined();
      expect(outputsAfterOptimization.outputCount).toBe(1);

      await issuerWallet.transferTokens({
        tokenAmount: 100n,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: userSparkAddress,
      });

      const userBalanceObj = await userWallet.getBalance();
      const userBalance = filterTokenBalanceForTokenIdentifier(
        userBalanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalance.ownedBalance).toBe(100n);
    });
  },
);
