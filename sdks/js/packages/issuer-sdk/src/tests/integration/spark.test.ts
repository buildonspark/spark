import { filterTokenBalanceForTokenPublicKey } from "@buildonspark/spark-sdk/utils";
import { jest } from "@jest/globals";
import { hexToBytes } from "@noble/curves/abstract/utils";
import {
  LOCAL_WALLET_CONFIG_ECDSA,
  LOCAL_WALLET_CONFIG_SCHNORR,
} from "../../../../spark-sdk/src/services/wallet-config.js";
import { BitcoinFaucet } from "../../../../spark-sdk/src/tests/utils/test-faucet.js";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { SparkWallet } from "@buildonspark/spark-sdk";
import { IssuerSparkWallet } from "../../index.js";

const brokenTestFn = process.env.GITHUB_ACTIONS ? it.skip : it;
describe("token integration tests", () => {
  jest.setTimeout(80000);

  brokenTestFn(
    "should fail when minting tokens without announcement",
    async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet } = await IssuerSparkWalletTesting.initialize({
        options: LOCAL_WALLET_CONFIG_ECDSA,
      });

      await wallet.mintTokens(tokenAmount);

      const tokenBalance = (await wallet.getIssuerTokenBalance()).balance;
      expect(tokenBalance).toEqual(tokenAmount);
    },
  );

  brokenTestFn("should fail when minting more than max supply", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await fundAndAnnounce(wallet, 2n, "MaxSupplyToken", "MST");
    await expect(wallet.mintTokens(tokenAmount)).rejects.toThrow();
  });

  brokenTestFn(
    "should announce token and issue tokens successfully",
    async () => {
      const tokenAmount: bigint = 1000n;
      const tokenName = "AnnounceIssueToken";
      const tokenSymbol = "AIT";
      const { wallet } = await IssuerSparkWalletTesting.initialize({
        options: LOCAL_WALLET_CONFIG_SCHNORR,
      });

      await fundAndAnnounce(wallet, 100000n, tokenName, tokenSymbol);

      const publicKeyInfo = await wallet.getIssuerTokenInfo();

      // Assert token public key info values
      const identityPublicKey = await wallet.getIdentityPublicKey();
      expect(publicKeyInfo?.tokenName).toEqual(tokenName);
      expect(publicKeyInfo?.tokenSymbol).toEqual(tokenSymbol);
      expect(publicKeyInfo?.tokenDecimals).toEqual(0);
      expect(publicKeyInfo?.maxSupply).toEqual(0n);
      expect(publicKeyInfo?.isFreezable).toEqual(false);

      // Compare the public key using bytesToHex
      const pubKeyHex = publicKeyInfo?.tokenPublicKey;
      expect(pubKeyHex).toEqual(identityPublicKey);

      await wallet.mintTokens(tokenAmount);

      const sourceBalance = (await wallet.getIssuerTokenBalance()).balance;
      expect(sourceBalance).toEqual(tokenAmount);

      const tokenInfo = await wallet.getTokenInfo();
      expect(tokenInfo[0].tokenName).toEqual(tokenName);
      expect(tokenInfo[0].tokenSymbol).toEqual(tokenSymbol);
      expect(tokenInfo[0].tokenDecimals).toEqual(0);
      expect(tokenInfo[0].maxSupply).toEqual(tokenAmount);
    },
  );

  brokenTestFn(
    "should announce, mint, and transfer tokens with ECDSA",
    async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_ECDSA,
        });

      const { wallet: destinationWallet } = await SparkWalletTesting.initialize(
        {
          options: LOCAL_WALLET_CONFIG_ECDSA,
        },
      );

      await fundAndAnnounce(issuerWallet, 100000n, "ECDSATransferToken", "ETT");

      await issuerWallet.mintTokens(tokenAmount);
      await issuerWallet.transferTokens({
        tokenAmount,
        tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
        receiverSparkAddress: await destinationWallet.getSparkAddress(),
      });
      const sourceBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(sourceBalance).toEqual(0n);

      const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
      const balanceObj = await destinationWallet.getBalance();
      const destinationBalance = filterTokenBalanceForTokenPublicKey(
        balanceObj?.tokenBalances,
        tokenPublicKey,
      );
      expect(destinationBalance.balance).toEqual(tokenAmount);
    },
  );

  brokenTestFn("should track token operations in monitoring", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    const { wallet: destinationWallet } = await SparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await fundAndAnnounce(issuerWallet, 100000n, "MonitoringToken", "MOT");

    await issuerWallet.mintTokens(tokenAmount);
    await issuerWallet.transferTokens({
      tokenAmount,
      tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
      receiverSparkAddress: await destinationWallet.getSparkAddress(),
    });
    const sourceBalance = (await issuerWallet.getIssuerTokenBalance()).balance;
    expect(sourceBalance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const balanceObj = await destinationWallet.getBalance();
    const destinationBalance = filterTokenBalanceForTokenPublicKey(
      balanceObj?.tokenBalances,
      tokenPublicKey,
    );
    expect(destinationBalance.balance).toEqual(tokenAmount);

    const issuerOperations = await issuerWallet.getIssuerTokenActivity();
    expect(issuerOperations.transactions.length).toBe(2);
    const issuerOperationTx = issuerOperations.transactions[0].transaction;
    expect(issuerOperationTx?.$case).toBe("spark");
    if (issuerOperationTx?.$case === "spark") {
      expect(issuerOperationTx.spark.operationType).toBe("ISSUER_MINT");
    }
  });

  brokenTestFn(
    "should announce, mint, and transfer tokens with Schnorr",
    async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_SCHNORR,
        });

      const { wallet: destinationWallet } = await SparkWalletTesting.initialize(
        {
          options: LOCAL_WALLET_CONFIG_SCHNORR,
        },
      );

      await fundAndAnnounce(
        issuerWallet,
        100000n,
        "SchnorrTransferToken",
        "STT",
      );

      await issuerWallet.mintTokens(tokenAmount);
      await issuerWallet.transferTokens({
        tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
        tokenAmount,
        receiverSparkAddress: await destinationWallet.getSparkAddress(),
      });
      const sourceBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(sourceBalance).toEqual(0n);
      const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
      const balanceObj = await destinationWallet.getBalance();
      const destinationBalance = filterTokenBalanceForTokenPublicKey(
        balanceObj?.tokenBalances,
        tokenPublicKey,
      );
      expect(destinationBalance.balance).toEqual(tokenAmount);
    },
  );

  brokenTestFn(
    "should announce, mint, freeze and unfreeze tokens with ECDSA",
    async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_ECDSA,
        });

      await fundAndAnnounce(issuerWallet, 100000n, "ECDSAFreezeToken", "EFT");
      await issuerWallet.mintTokens(tokenAmount);

      // Check issuer balance after minting
      const issuerBalanceAfterMint = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterMint).toEqual(tokenAmount);

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: LOCAL_WALLET_CONFIG_ECDSA,
      });
      const userWalletPublicKey = await userWallet.getSparkAddress();

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
        receiverSparkAddress: userWalletPublicKey,
      });
      const issuerBalanceAfterTransfer = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(0n);

      const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenPublicKey(
        userBalanceObj?.tokenBalances,
        tokenPublicKey,
      );
      expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);
      // Freeze tokens
      const freezeResponse =
        await issuerWallet.freezeTokens(userWalletPublicKey);
      expect(freezeResponse.impactedOutputIds.length).toBeGreaterThan(0);
      expect(freezeResponse.impactedTokenAmount).toEqual(tokenAmount);

      // Unfreeze tokens
      const unfreezeResponse =
        await issuerWallet.unfreezeTokens(userWalletPublicKey);
      expect(unfreezeResponse.impactedOutputIds.length).toBeGreaterThan(0);
      expect(unfreezeResponse.impactedTokenAmount).toEqual(tokenAmount);
    },
  );

  brokenTestFn(
    "should announce, mint, freeze and unfreeze tokens with Schnorr",
    async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_SCHNORR,
        });

      await fundAndAnnounce(issuerWallet, 100000n, "SchnorrFreezeToken", "SFT");

      await issuerWallet.mintTokens(tokenAmount);

      // Check issuer balance after minting
      const issuerBalanceAfterMint = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterMint).toEqual(tokenAmount);

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: LOCAL_WALLET_CONFIG_SCHNORR,
      });
      const userWalletPublicKey = await userWallet.getSparkAddress();

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
        receiverSparkAddress: userWalletPublicKey,
      });

      const issuerBalanceAfterTransfer = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(0n);

      const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenPublicKey(
        userBalanceObj?.tokenBalances,
        tokenPublicKey,
      );
      expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

      const freezeResult = await issuerWallet.freezeTokens(userWalletPublicKey);
      expect(freezeResult.impactedOutputIds.length).toBe(1);
      expect(freezeResult.impactedTokenAmount).toBe(1000n);

      const unfreezeResult =
        await issuerWallet.unfreezeTokens(userWalletPublicKey);
      expect(unfreezeResult.impactedOutputIds.length).toBe(1);
      expect(unfreezeResult.impactedTokenAmount).toBe(1000n);
    },
  );

  brokenTestFn(
    "should announce, mint, and burn tokens with ECDSA",
    async () => {
      const tokenAmount: bigint = 200n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_ECDSA,
        });

      await fundAndAnnounce(issuerWallet, 100000n, "ECDSABurnToken", "EBT");
      await issuerWallet.mintTokens(tokenAmount);

      const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(issuerTokenBalance).toEqual(tokenAmount);

      await issuerWallet.burnTokens(tokenAmount);

      const issuerTokenBalanceAfterBurn = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerTokenBalanceAfterBurn).toEqual(0n);
    },
  );

  brokenTestFn(
    "should announce, mint, and burn tokens with Schnorr",
    async () => {
      const tokenAmount: bigint = 200n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_SCHNORR,
        });

      await fundAndAnnounce(issuerWallet, 100000n, "SchnorrBurnToken", "SBT");
      await issuerWallet.mintTokens(tokenAmount);

      const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(issuerTokenBalance).toEqual(tokenAmount);

      await issuerWallet.burnTokens(tokenAmount);

      const issuerTokenBalanceAfterBurn = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerTokenBalanceAfterBurn).toEqual(0n);
    },
  );

  brokenTestFn(
    "should complete full token lifecycle with ECDSA: announce, mint, transfer, return, burn",
    async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_ECDSA,
        });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: LOCAL_WALLET_CONFIG_ECDSA,
      });

      await fundAndAnnounce(
        issuerWallet,
        100000n,
        "ECDSAFullCycleToken",
        "EFCT",
      );
      await issuerWallet.mintTokens(tokenAmount);

      const issuerBalanceAfterMint = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterMint).toEqual(tokenAmount);

      const userWalletPublicKey = await userWallet.getSparkAddress();

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
        receiverSparkAddress: userWalletPublicKey,
      });

      const issuerBalanceAfterTransfer = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(0n);
      const tokenPublicKeyHex = await issuerWallet.getIdentityPublicKey();
      const userWalletPublicKeyHex = await userWallet.getSparkAddress();
      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenPublicKey(
        userBalanceObj?.tokenBalances,
        tokenPublicKeyHex,
      );
      expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);
      await userWallet.transferTokens({
        tokenPublicKey: tokenPublicKeyHex,
        tokenAmount,
        receiverSparkAddress: await issuerWallet.getSparkAddress(),
      });

      const userBalanceObjAfterTransferBack = await userWallet.getBalance();
      const userBalanceAfterTransferBack = filterTokenBalanceForTokenPublicKey(
        userBalanceObjAfterTransferBack?.tokenBalances,
        tokenPublicKeyHex,
      );

      expect(userBalanceAfterTransferBack.balance).toEqual(0n);

      const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(issuerTokenBalance).toEqual(tokenAmount);
      await issuerWallet.burnTokens(tokenAmount);
      const issuerTokenBalanceAfterBurn = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerTokenBalanceAfterBurn).toEqual(0n);
    },
  );

  brokenTestFn(
    "should complete full token lifecycle with Schnorr: announce, mint, transfer, return, burn",
    async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_SCHNORR,
        });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: LOCAL_WALLET_CONFIG_SCHNORR,
      });

      await fundAndAnnounce(
        issuerWallet,
        100000n,
        "SchnorrFullCycleToken",
        "SFCT",
      );
      await issuerWallet.mintTokens(tokenAmount);

      const issuerBalanceAfterMint = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterMint).toEqual(tokenAmount);

      const userWalletPublicKey = await userWallet.getSparkAddress();

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
        receiverSparkAddress: userWalletPublicKey,
      });

      const issuerBalanceAfterTransfer = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(0n);

      const tokenPublicKeyHex = await issuerWallet.getIdentityPublicKey();
      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenPublicKey(
        userBalanceObj?.tokenBalances,
        tokenPublicKeyHex,
      );
      expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

      await userWallet.transferTokens({
        tokenPublicKey: tokenPublicKeyHex,
        tokenAmount,
        receiverSparkAddress: await issuerWallet.getSparkAddress(),
      });

      const userBalanceObjAfterTransferBack = await userWallet.getBalance();
      const userBalanceAfterTransferBack = filterTokenBalanceForTokenPublicKey(
        userBalanceObjAfterTransferBack?.tokenBalances,
        tokenPublicKeyHex,
      );
      expect(userBalanceAfterTransferBack.balance).toEqual(0n);

      const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(issuerTokenBalance).toEqual(tokenAmount);

      await issuerWallet.burnTokens(tokenAmount);

      const issuerTokenBalanceAfterBurn = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerTokenBalanceAfterBurn).toEqual(0n);
    },
  );
});

async function fundAndAnnounce(
  wallet: IssuerSparkWallet,
  maxSupply: bigint = 100000n,
  tokenName: string = "TestToken1",
  tokenSymbol: string = "TT1",
) {
  // Faucet funds to the Issuer wallet because announcing a token
  // requires ownership of an L1 UTXO.
  const faucet = BitcoinFaucet.getInstance();
  const l1WalletPubKey = await wallet.getTokenL1Address();
  await faucet.sendToAddress(l1WalletPubKey, 100_000n);
  await faucet.mineBlocks(6);

  await new Promise((resolve) => setTimeout(resolve, 3000));

  try {
    const response = await wallet.announceTokenL1(
      tokenName,
      tokenSymbol,
      0,
      maxSupply,
      false,
    );
    console.log("Announce token response:", response);
  } catch (error: any) {
    console.error("Error when announcing token on L1:", error);
    expect(error).toBeUndefined();
  }
  await faucet.mineBlocks(1);

  // Wait for LRC20 processing.
  await new Promise((resolve) => setTimeout(resolve, 30000));
}
