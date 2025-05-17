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

  it("should fail when minting tokens without announcement", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await expect(wallet.mintTokens(tokenAmount)).rejects.toThrow();
  });

  it("should fail when announce decimal is greater than js MAX_SAFE_INTEGER", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await expect(
      fundAndAnnounce(
        wallet,
        tokenAmount,
        2 ** 53,
        "2Pow53Decimal",
        "2P53D",
        false,
      ),
    ).rejects.toThrow();
  });

  it("should fail when minting more than max supply", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await fundAndAnnounce(wallet, 2n, 0, "MaxSupply", "MST");
    await expect(wallet.mintTokens(tokenAmount)).rejects.toThrow();
  });

  it("should announce token and issue tokens successfully", async () => {
    const tokenAmount: bigint = 1000n;
    const tokenName = "AnnounceIssue";
    const tokenSymbol = "AIT";
    const { wallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await fundAndAnnounce(wallet, 100000n, 0, tokenName, tokenSymbol);

    const publicKeyInfo = await wallet.getIssuerTokenInfo();

    // Assert token public key info values
    const identityPublicKey = await wallet.getIdentityPublicKey();
    expect(publicKeyInfo?.tokenName).toEqual(tokenName);
    expect(publicKeyInfo?.tokenSymbol).toEqual(tokenSymbol);
    expect(publicKeyInfo?.tokenDecimals).toEqual(0);
    expect(publicKeyInfo?.maxSupply).toEqual(100000n);
    expect(publicKeyInfo?.isFreezable).toEqual(false);

    // Compare the public key using bytesToHex
    const pubKeyHex = publicKeyInfo?.tokenPublicKey;
    expect(pubKeyHex).toEqual(identityPublicKey);

    await wallet.mintTokens(tokenAmount);

    const tokenBalance = await wallet.getIssuerTokenBalance();
    expect(tokenBalance.balance).toEqual(tokenAmount);
  });

  it("should announce, mint, and transfer tokens with ECDSA", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    const { wallet: destinationWallet } = await SparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await fundAndAnnounce(issuerWallet, 100000n, 0, "ECDSATransfer", "ETT");

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
  });

  it("should track token operations in monitoring", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    const { wallet: destinationWallet } = await SparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await fundAndAnnounce(issuerWallet, 100000n, 0, "ECDSATransfer", "ETT");

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
  });

  // broken because LRC20 does not yet have ISSUER operation types.
  brokenTestFn("should track token operations in monitoring", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    const { wallet: destinationWallet } = await SparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await fundAndAnnounce(issuerWallet, 100000n, 0, "Monitoring", "MOT");

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
    let mint_operation = 0;
    let transfer_operation = 0;
    issuerOperations.transactions.forEach((transaction) => {
      if (transaction.transaction?.$case === "spark") {
        if (transaction.transaction.spark.operationType === "ISSUER_MINT") {
          mint_operation++;
        } else if (
          transaction.transaction.spark.operationType === "ISSUER_TRANSFER"
        ) {
          transfer_operation++;
        }
      }
    });
    expect(mint_operation).toBe(1);
    expect(transfer_operation).toBe(1);
  });

  it("should announce, mint, and transfer tokens with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    const { wallet: destinationWallet } = await SparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await fundAndAnnounce(issuerWallet, 100000n, 0, "SchnorrTransfer", "STT");

    await issuerWallet.mintTokens(tokenAmount);
    await issuerWallet.transferTokens({
      tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
      tokenAmount,
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
  });

  it("it should mint token with 1 max supply without issue", async () => {
    const tokenAmount: bigint = 1n;
    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    const { wallet: destinationWallet } = await SparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await fundAndAnnounce(issuerWallet, 1n, 0, "MaxSupply", "MST");
    await issuerWallet.mintTokens(tokenAmount);

    const tokenBalance = await issuerWallet.getIssuerTokenBalance();
    expect(tokenBalance.balance).toEqual(tokenAmount);
  });

  // freeze is hardcoded to mainnet
  brokenTestFn(
    "should announce, mint, freeze and unfreeze tokens with ECDSA",
    async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_ECDSA,
        });

      await fundAndAnnounce(issuerWallet, 100000n, 0, "ECDSAFreeze", "EFT");
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

  // freeze is hardcoded to mainnet
  brokenTestFn(
    "should announce, mint, freeze and unfreeze tokens with Schnorr",
    async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: LOCAL_WALLET_CONFIG_SCHNORR,
        });

      await fundAndAnnounce(issuerWallet, 100000n, 0, "SchnorrFreeze", "SFT");

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

  it("should announce, mint, and burn tokens with ECDSA", async () => {
    const tokenAmount: bigint = 200n;
    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await fundAndAnnounce(issuerWallet, 100000n, 0, "ECDSABurn", "EBT");
    await issuerWallet.mintTokens(tokenAmount);

    const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
      .balance;
    expect(issuerTokenBalance).toEqual(tokenAmount);

    await issuerWallet.burnTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn = (
      await issuerWallet.getIssuerTokenBalance()
    ).balance;
    expect(issuerTokenBalanceAfterBurn).toEqual(0n);
  });

  it("should announce, mint, and burn tokens with Schnorr", async () => {
    const tokenAmount: bigint = 200n;
    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await fundAndAnnounce(issuerWallet, 100000n, 0, "SchnorrBurn", "SBT");
    await issuerWallet.mintTokens(tokenAmount);

    const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
      .balance;
    expect(issuerTokenBalance).toEqual(tokenAmount);

    await issuerWallet.burnTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn = (
      await issuerWallet.getIssuerTokenBalance()
    ).balance;
    expect(issuerTokenBalanceAfterBurn).toEqual(0n);
  });

  it("should complete full token lifecycle with ECDSA: announce, mint, transfer, return, burn", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    const { wallet: userWallet } = await SparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await fundAndAnnounce(issuerWallet, 100000n, 0, "ECDSAFullCycle", "EFCT");
    await issuerWallet.mintTokens(tokenAmount);

    const issuerBalanceAfterMint = (await issuerWallet.getIssuerTokenBalance())
      .balance;
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
  });

  it("should complete full token lifecycle with Schnorr: announce, mint, transfer, return, burn", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    const { wallet: userWallet } = await SparkWalletTesting.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await fundAndAnnounce(issuerWallet, 100000n, 0, "SchnorrFullCycle", "SFCT");
    await issuerWallet.mintTokens(tokenAmount);

    const issuerBalanceAfterMint = (await issuerWallet.getIssuerTokenBalance())
      .balance;
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
  });
});

async function fundAndAnnounce(
  wallet: IssuerSparkWallet,
  maxSupply: bigint = 100000n,
  decimals: number = 0,
  tokenName: string = "TestToken1",
  tokenSymbol: string = "TT1",
  isFreezable: boolean = false,
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
      decimals,
      maxSupply,
      isFreezable,
    );
    console.log("Announce token response:", response);
  } catch (error: any) {
    console.error("Error when announcing token on L1:", error);
    throw error;
  }
  await faucet.mineBlocks(2);

  // Wait for LRC20 processing.
  const SECONDS = 1000;
  await new Promise((resolve) => setTimeout(resolve, 3 * SECONDS));
}
