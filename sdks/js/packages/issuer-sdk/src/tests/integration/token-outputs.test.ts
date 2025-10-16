import {
  ConfigOptions,
  filterTokenBalanceForTokenIdentifier,
  WalletConfig,
} from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { SparkWalletTesting } from "@buildonspark/spark-sdk/test-utils";

export const TOKENS_SCHNORR_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "SCHNORR",
};

export const TOKENS_ECDSA_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "ECDSA",
};

const TEST_CONFIGS = [
  { name: "TE", config: TOKENS_ECDSA_CONFIG },
  { name: "TS", config: TOKENS_SCHNORR_CONFIG },
];

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

      const outputsBeforeOptimization = (issuerWallet as any).tokenOutputs.get(
        tokenIdentifier,
      );
      expect(outputsBeforeOptimization).toBeDefined();
      expect(outputsBeforeOptimization.length).toBe(61);

      await issuerWallet.optimizeTokenOutputs();

      await (issuerWallet as any).syncTokenOutputs();

      const balanceAfterOptimization =
        await issuerWallet.getIssuerTokenBalance();
      expect(balanceAfterOptimization.balance).toBe(totalAmount);

      const outputsAfterOptimization = (issuerWallet as any).tokenOutputs.get(
        tokenIdentifier,
      );
      expect(outputsAfterOptimization).toBeDefined();
      expect(outputsAfterOptimization.length).toBe(1);

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
      expect(userBalance.balance).toBe(100n);
    });

    it("should prevent concurrent transactions from spending the same outputs using tokenOutputsLocks", async () => {
      const tokenAmount: bigint = 1000n;
      const transferAmount: bigint = 100n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: userWallet1 } = await SparkWalletTesting.initialize({
        options: config,
      });

      const { wallet: userWallet2 } = await SparkWalletTesting.initialize({
        options: config,
      });

      await issuerWallet.createToken({
        tokenName: `${name}LOCK`,
        tokenTicker: "LCK",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      await issuerWallet.mintTokens(tokenAmount);
      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();
      expect(tokenIdentifier).toBeDefined();

      const user1Address = await userWallet1.getSparkAddress();
      const user2Address = await userWallet2.getSparkAddress();

      const balanceBefore = await issuerWallet.getIssuerTokenBalance();
      expect(balanceBefore.balance).toBe(tokenAmount);

      const transfer1Promise = issuerWallet.transferTokens({
        tokenAmount: transferAmount,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: user1Address,
      });

      const transfer2Promise = issuerWallet.transferTokens({
        tokenAmount: transferAmount,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: user2Address,
      });

      const results = await Promise.allSettled([
        transfer1Promise,
        transfer2Promise,
      ]);

      const successCount = results.filter(
        (result) => result.status === "fulfilled",
      ).length;
      const failureCount = results.filter(
        (result) => result.status === "rejected",
      ).length;

      expect(successCount).toBe(1);
      expect(failureCount).toBe(1);

      const balanceAfter = await issuerWallet.getIssuerTokenBalance();
      expect(balanceAfter.balance).toBe(tokenAmount - transferAmount);

      const user1BalanceObj = await userWallet1.getBalance();
      const user1Balance = filterTokenBalanceForTokenIdentifier(
        user1BalanceObj?.tokenBalances,
        tokenIdentifier!,
      );

      const user2BalanceObj = await userWallet2.getBalance();
      const user2Balance = filterTokenBalanceForTokenIdentifier(
        user2BalanceObj?.tokenBalances,
        tokenIdentifier!,
      );

      const totalReceived = user1Balance.balance + user2Balance.balance;
      expect(totalReceived).toBe(transferAmount);
      expect(
        user1Balance.balance === transferAmount ||
          user2Balance.balance === transferAmount,
      ).toBe(true);
    });
  },
);
