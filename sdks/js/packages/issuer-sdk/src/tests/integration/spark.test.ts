import { SparkWallet } from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { hexToBytes } from "@noble/curves/abstract/utils";
import {
  LOCAL_WALLET_CONFIG_ECDSA,
  LOCAL_WALLET_CONFIG_SCHNORR,
} from "../../../../spark-sdk/src/services/wallet-config.js";
import { BitcoinFaucet } from "../../../../spark-sdk/src/tests/utils/test-faucet.js";
import { IssuerSparkWallet } from "../../issuer-spark-wallet.js";
import { filterTokenBalanceForTokenPublicKey } from "@buildonspark/spark-sdk/utils";

const brokenTestFn = process.env.GITHUB_ACTIONS ? it.skip : it;
describe("token integration test", () => {
  jest.setTimeout(80000);

  it("should issue a single token with ECDSA", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await wallet.mintTokens(tokenAmount);

    const tokenBalance = (await wallet.getIssuerTokenBalance()).balance;
    expect(tokenBalance).toEqual(tokenAmount);
  });

  it("should issue a single token with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await wallet.mintTokens(tokenAmount);

    const tokenBalance = (await wallet.getIssuerTokenBalance()).balance;
    expect(tokenBalance).toEqual(tokenAmount);
  });

  brokenTestFn("should announce and issue a single token", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    // Faucet funds to the Issuer wallet because announcing a token
    // requires ownership of an L1 UTXO.
    const faucet = BitcoinFaucet.getInstance();
    const l1WalletPubKey = await wallet.getIdentityPublicKey();
    await faucet.sendFaucetCoinToP2WPKHAddress(hexToBytes(l1WalletPubKey));
    await faucet.mineBlocks(6);

    await new Promise((resolve) => setTimeout(resolve, 5000));

    try {
      const response = await wallet.announceTokenL1({
        tokenName: "TestToken1",
        tokenTicker: "TT1",
        decimals: 0,
        maxSupply: 0,
        isFreezable: false,
      });
      console.log("Announce token response:", response);
    } catch (error: any) {
      console.error("Error when announcing token on L1:", error);
      expect(error).toBeUndefined();
    }
    await faucet.mineBlocks(6);

    // Wait for LRC20 processing.
    await new Promise((resolve) => setTimeout(resolve, 50000));

    const publicKeyInfo = await wallet.getIssuerTokenInfo();

    // Assert token public key info values
    const identityPublicKey = await wallet.getIdentityPublicKey();
    expect(publicKeyInfo?.tokenName).toEqual("TestToken1");
    expect(publicKeyInfo?.tokenSymbol).toEqual("TT1");
    expect(publicKeyInfo?.tokenDecimals).toEqual(0);
    expect(publicKeyInfo?.maxSupply).toEqual(0);
    expect(publicKeyInfo?.isFreezable).toEqual(false);

    // Compare the public key using bytesToHex
    const pubKeyHex = publicKeyInfo?.tokenPublicKey;
    expect(pubKeyHex).toEqual(identityPublicKey);

    await wallet.mintTokens(tokenAmount);

    const sourceBalance = (await wallet.getIssuerTokenBalance()).balance;
    expect(sourceBalance).toEqual(tokenAmount);

    const tokenInfo = await wallet.getTokenInfo();
    expect(tokenInfo[0].tokenName).toEqual("TestToken1");
    expect(tokenInfo[0].tokenSymbol).toEqual("TT1");
    expect(tokenInfo[0].tokenDecimals).toEqual(0);
    expect(tokenInfo[0].maxSupply).toEqual(tokenAmount);
  });

  it("should issue a single token and transfer it with ECDSA", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    const { wallet: destinationWallet } = await SparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

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

  brokenTestFn("monitoring operations", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    const { wallet: destinationWallet } = await SparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

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

  it("should issue a single token and transfer it with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    const { wallet: destinationWallet } = await SparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

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

  it("should freeze tokens with ECDSA", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await issuerWallet.mintTokens(tokenAmount);

    // Check issuer balance after minting
    const issuerBalanceAfterMint = (await issuerWallet.getIssuerTokenBalance())
      .balance;
    expect(issuerBalanceAfterMint).toEqual(tokenAmount);

    const { wallet: userWallet } = await SparkWallet.initialize({
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
    const freezeResponse = await issuerWallet.freezeTokens(userWalletPublicKey);
    expect(freezeResponse.impactedOutputIds.length).toBeGreaterThan(0);
    expect(freezeResponse.impactedTokenAmount).toEqual(tokenAmount);

    // Unfreeze tokens
    const unfreezeResponse =
      await issuerWallet.unfreezeTokens(userWalletPublicKey);
    expect(unfreezeResponse.impactedOutputIds.length).toBeGreaterThan(0);
    expect(unfreezeResponse.impactedTokenAmount).toEqual(tokenAmount);
  });

  it("should freeze tokens with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await issuerWallet.mintTokens(tokenAmount);

    // Check issuer balance after minting
    const issuerBalanceAfterMint = (await issuerWallet.getIssuerTokenBalance())
      .balance;
    expect(issuerBalanceAfterMint).toEqual(tokenAmount);

    const { wallet: userWallet } = await SparkWallet.initialize({
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
  });

  it("should burn tokens with ECDSA", async () => {
    const tokenAmount: bigint = 200n;
    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });
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

  it("should burn tokens with Schnorr", async () => {
    const tokenAmount: bigint = 200n;
    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });
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

  it("mint, transfer to user, user transfer to issuer, burn with ECDSA", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    const { wallet: userWallet } = await SparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

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

  it("mint, transfer to user, user transfer to issuer, burn with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;

    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    const { wallet: userWallet } = await SparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    await issuerWallet.mintTokens(tokenAmount);

    const issuerBalanceAfterMint = (await issuerWallet.getIssuerTokenBalance())
      .balance;
    expect(issuerBalanceAfterMint).toEqual(tokenAmount);

    const userWalletPublicKey = await userWallet.getSparkAddress();

    await issuerWallet.transferTokens({
      tokenAmount,
      tokenPublicKey,
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
