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
  "token transfer tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    it("should create, mint, and transfer tokens", async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });
      await issuerWallet.createToken({
        tokenName: `${name}MTR`,
        tokenTicker: "MTR",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      await issuerWallet.mintTokens(tokenAmount);

      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();
      await issuerWallet.transferTokens({
        tokenAmount,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: await userWallet.getSparkAddress(),
      });

      const balanceObj = await userWallet.getBalance();
      const userBalance = filterTokenBalanceForTokenIdentifier(
        balanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalance.balance).toBeGreaterThanOrEqual(tokenAmount);
    });

    it("should create, mint, and batchtransfer tokens", async () => {
      const tokenAmount: bigint = 999n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}MBN`,
        tokenTicker: "MBN",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      const { wallet: destinationWallet } = await SparkWalletTesting.initialize(
        {
          options: config,
        },
      );

      const { wallet: destinationWallet2 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: destinationWallet3 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.mintTokens(tokenAmount);
      const sharedIssuerBalance = await issuerWallet.getIssuerTokenBalance();
      expect(sharedIssuerBalance).toBeDefined();
      expect(sharedIssuerBalance.tokenIdentifier).toBeDefined();

      const tokenIdentifier = sharedIssuerBalance.tokenIdentifier!;
      const sourceBalanceBefore = sharedIssuerBalance.balance;

      await issuerWallet.batchTransferTokens([
        {
          tokenAmount: tokenAmount / 3n,
          tokenIdentifier,
          receiverSparkAddress: await destinationWallet.getSparkAddress(),
        },
        {
          tokenAmount: tokenAmount / 3n,
          tokenIdentifier,
          receiverSparkAddress: await destinationWallet2.getSparkAddress(),
        },
        {
          tokenAmount: tokenAmount / 3n,
          tokenIdentifier,
          receiverSparkAddress: await destinationWallet3.getSparkAddress(),
        },
      ]);

      const sourceBalanceAfter = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(sourceBalanceAfter).toEqual(sourceBalanceBefore - tokenAmount);

      const balanceObj = await destinationWallet.getBalance();
      const destinationBalance = filterTokenBalanceForTokenIdentifier(
        balanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(destinationBalance.balance).toEqual(tokenAmount / 3n);
      const balanceObj2 = await destinationWallet2.getBalance();
      const destinationBalance2 = filterTokenBalanceForTokenIdentifier(
        balanceObj2?.tokenBalances,
        tokenIdentifier!,
      );
      expect(destinationBalance2.balance).toEqual(tokenAmount / 3n);
      const balanceObj3 = await destinationWallet3.getBalance();
      const destinationBalance3 = filterTokenBalanceForTokenIdentifier(
        balanceObj3?.tokenBalances,
        tokenIdentifier!,
      );
      expect(destinationBalance3.balance).toEqual(tokenAmount / 3n);
    });
  },
);
