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
  "token lifecycle tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    it("should create, mint, freeze, and unfreeze tokens", async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.createToken({
        tokenName: `${name}FRZ`,
        tokenTicker: "FRZ",
        decimals: 0,
        isFreezable: true,
        maxSupply: 100000n,
      });
      await issuerWallet.mintTokens(tokenAmount);

      const issuerBalanceObjAfterMint =
        await issuerWallet.getIssuerTokenBalance();
      expect(issuerBalanceObjAfterMint).toBeDefined();
      expect(issuerBalanceObjAfterMint.tokenIdentifier).toBeDefined();

      const issuerBalanceAfterMint = issuerBalanceObjAfterMint.balance;
      const tokenIdentifier = issuerBalanceObjAfterMint.tokenIdentifier!;

      expect(issuerBalanceAfterMint).toEqual(tokenAmount);

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });
      const userSparkAddress = await userWallet.getSparkAddress();

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenIdentifier,
        receiverSparkAddress: userSparkAddress,
      });
      const issuerBalanceAfterTransfer = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(0n);

      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenIdentifier(
        userBalanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

      const freezeResponse = await issuerWallet.freezeTokens(userSparkAddress);
      expect(freezeResponse.impactedOutputIds.length).toBeGreaterThan(0);
      expect(freezeResponse.impactedTokenAmount).toEqual(tokenAmount);

      const unfreezeResponse =
        await issuerWallet.unfreezeTokens(userSparkAddress);
      expect(unfreezeResponse.impactedOutputIds.length).toBeGreaterThan(0);
      expect(unfreezeResponse.impactedTokenAmount).toEqual(tokenAmount);
    });

    it("should create, mint and burn tokens", async () => {
      const tokenAmount: bigint = 200n;

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

      await issuerWallet.mintTokens(tokenAmount);
      const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(issuerTokenBalance).toBeGreaterThanOrEqual(tokenAmount);

      await issuerWallet.burnTokens(tokenAmount);

      const issuerTokenBalanceAfterBurn = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerTokenBalanceAfterBurn).toEqual(
        issuerTokenBalance - tokenAmount,
      );
    });

    it("should complete a full token lifecycle - create, mint, transfer, return, burn", async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}LFC`,
        tokenTicker: "LFC",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });

      const initialBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;

      await issuerWallet.mintTokens(tokenAmount);
      const issuerBalanceObjAfterMint =
        await issuerWallet.getIssuerTokenBalance();
      expect(issuerBalanceObjAfterMint).toBeDefined();
      const issuerBalanceAfterMint = issuerBalanceObjAfterMint.balance;
      expect(issuerBalanceAfterMint).toEqual(initialBalance + tokenAmount);
      expect(issuerBalanceObjAfterMint.tokenIdentifier).toBeDefined();
      const tokenIdentifier = issuerBalanceObjAfterMint.tokenIdentifier!;
      const userSparkAddress = await userWallet.getSparkAddress();

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenIdentifier,
        receiverSparkAddress: userSparkAddress,
      });

      const issuerBalanceAfterTransfer = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(initialBalance);

      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenIdentifier(
        userBalanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

      await userWallet.transferTokens({
        tokenIdentifier,
        tokenAmount,
        receiverSparkAddress: await issuerWallet.getSparkAddress(),
      });

      const userBalanceObjAfterTransferBack = await userWallet.getBalance();
      const userBalanceAfterTransferBack = filterTokenBalanceForTokenIdentifier(
        userBalanceObjAfterTransferBack?.tokenBalances,
        tokenIdentifier!,
      );

      expect(userBalanceAfterTransferBack.balance).toEqual(0n);

      const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(issuerTokenBalance).toEqual(initialBalance + tokenAmount);

      await issuerWallet.burnTokens(tokenAmount);

      const issuerTokenBalanceAfterBurn = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerTokenBalanceAfterBurn).toEqual(initialBalance);
    });
  },
);
