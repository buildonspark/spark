import { filterTokenBalanceForTokenIdentifier } from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { SparkWalletTesting } from "@buildonspark/spark-sdk/test-utils";
import {
  burnSingleIssuerToken,
  freezeSingleIssuerToken,
  getSingleIssuerTokenBalance,
  mintSingleIssuerToken,
  unfreezeSingleIssuerToken,
} from "../utils/multi-token-utils.js";
import { TEST_CONFIGS } from "./test-configs.js";

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
      await mintSingleIssuerToken(issuerWallet, tokenAmount);

      const issuerBalanceObjAfterMint =
        await getSingleIssuerTokenBalance(issuerWallet);
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
        await getSingleIssuerTokenBalance(issuerWallet)
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(0n);

      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenIdentifier(
        userBalanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalanceAfterTransfer.ownedBalance).toEqual(tokenAmount);

      const freezeResponse = await freezeSingleIssuerToken(
        issuerWallet,
        userSparkAddress,
      );
      expect(freezeResponse.impactedTokenOutputs.length).toBeGreaterThan(0);
      expect(freezeResponse.impactedTokenAmount).toEqual(tokenAmount);

      const unfreezeResponse = await unfreezeSingleIssuerToken(
        issuerWallet,
        userSparkAddress,
      );
      expect(unfreezeResponse.impactedTokenOutputs.length).toBeGreaterThan(0);
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

      await mintSingleIssuerToken(issuerWallet, tokenAmount);
      const issuerTokenBalance = (
        await getSingleIssuerTokenBalance(issuerWallet)
      ).balance;
      expect(issuerTokenBalance).toBeGreaterThanOrEqual(tokenAmount);

      await burnSingleIssuerToken(issuerWallet, tokenAmount);

      const issuerTokenBalanceAfterBurn = (
        await getSingleIssuerTokenBalance(issuerWallet)
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

      const initialBalance = (await getSingleIssuerTokenBalance(issuerWallet))
        .balance;

      await mintSingleIssuerToken(issuerWallet, tokenAmount);
      const issuerBalanceObjAfterMint =
        await getSingleIssuerTokenBalance(issuerWallet);
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
        await getSingleIssuerTokenBalance(issuerWallet)
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(initialBalance);

      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenIdentifier(
        userBalanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalanceAfterTransfer.ownedBalance).toEqual(tokenAmount);

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

      expect(userBalanceAfterTransferBack.ownedBalance).toEqual(0n);

      const issuerTokenBalance = (
        await getSingleIssuerTokenBalance(issuerWallet)
      ).balance;
      expect(issuerTokenBalance).toEqual(initialBalance + tokenAmount);

      await burnSingleIssuerToken(issuerWallet, tokenAmount);

      const issuerTokenBalanceAfterBurn = (
        await getSingleIssuerTokenBalance(issuerWallet)
      ).balance;
      expect(issuerTokenBalanceAfterBurn).toEqual(initialBalance);
    });
  },
);
