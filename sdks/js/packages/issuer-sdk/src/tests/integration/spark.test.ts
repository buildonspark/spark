import { SparkWallet } from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { hexToBytes } from "@noble/curves/abstract/utils";
import {
  LOCAL_WALLET_CONFIG_ECDSA,
  LOCAL_WALLET_CONFIG_SCHNORR,
} from "../../../../spark-sdk/src/services/wallet-config.js";
import { BitcoinFaucet } from "../../../../spark-sdk/src/tests/utils/test-faucet.js";
import { IssuerSparkWallet } from "../../issuer-spark-wallet.js";

describe("token integration test", () => {
  // Skip all tests if running in GitHub Actions
  process.env.GITHUB_ACTIONS ? it.skip : it;

  // Increase timeout for all tests in this suite
  jest.setTimeout(60000);

  it("should issue a single token with ECDSA", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });

    await wallet.mintTokens(tokenAmount);

    const tokenBalance = await wallet.getIssuerTokenBalance();
    expect(tokenBalance.balance).toEqual(tokenAmount);
  });

  it("should issue a single token with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await wallet.mintTokens(tokenAmount);

    const tokenBalance = await wallet.getIssuerTokenBalance();
    expect(tokenBalance.balance).toEqual(tokenAmount);
  });

  it("should announce and issue a single token", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    // Faucet funds to the Issuer wallet because announcing a token
    // requires ownership of an L1 UTXO.
    const faucet = new BitcoinFaucet();
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
      console.warn("Announce token response: " + response);
    } catch (error: any) {
      fail(
        "Expected announceTokenL1() to succeed with fauceted funds: " + error,
      );
    }
    await faucet.mineBlocks(6);

    // Wait for LRC20 processing.
    await new Promise((resolve) => setTimeout(resolve, 50000));

    const publicKeyInfo = await wallet.getIssuerTokenInfo();

    // Assert token public key info values
    const identityPublicKey = await wallet.getIdentityPublicKey();
    expect(publicKeyInfo?.announcement?.name).toEqual("TestToken1");
    expect(publicKeyInfo?.announcement?.symbol).toEqual("TT1");
    expect(publicKeyInfo?.announcement?.decimal).toEqual(0);
    expect(publicKeyInfo?.announcement?.maxSupply).toEqual(0);
    expect(publicKeyInfo?.announcement?.isFreezable).toEqual(false);

    // Compare the public key using bytesToHex
    const pubKeyHex = publicKeyInfo?.announcement?.tokenPubkey.pubkey;
    expect(pubKeyHex).toEqual(identityPublicKey);

    await wallet.mintTokens(tokenAmount);

    const sourceBalance = await wallet.getIssuerTokenBalance();
    expect(sourceBalance.balance).toEqual(tokenAmount);

    const tokenInfo = await wallet.getTokenInfo();
    expect(tokenInfo[0].tokenName).toEqual("TestToken1");
    expect(tokenInfo[0].tokenSymbol).toEqual("TT1");
    expect(tokenInfo[0].tokenDecimals).toEqual(0);
    expect(tokenInfo[0].tokenSupply).toEqual(tokenAmount);
  });

  it("should announce, issue, and withdraw a single token", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    // Faucet funds to the Issuer wallet because announcing a token
    // requires ownership of an L1 UTXO.
    const faucet = new BitcoinFaucet();
    const l1WalletPubKey = await wallet.getIdentityPublicKey();
    await faucet.sendFaucetCoinToP2WPKHAddress(hexToBytes(l1WalletPubKey));
    await faucet.mineBlocks(6);

    await new Promise((resolve) => setTimeout(resolve, 5000));

    try {
      const response = await wallet.announceTokenL1({
        tokenName: "TestToken2",
        tokenTicker: "TT2",
        decimals: 0,
        maxSupply: 0,
        isFreezable: false,
      });
      console.log("Announce token response: " + response);
    } catch (error: any) {
      console.error(
        "Expected announceTokenL1() to succeed with fauceted funds: " + error,
      );
      fail();
    }
    await faucet.mineBlocks(6);
    await wallet.mintTokens(tokenAmount);
    // Mint a second time to ensure that multiple leaves are handled correctly
    // (a self transfer should first be broadcast to enable withdrawal in a single TX).
    await wallet.mintTokens(tokenAmount);

    const sourceBalance = await wallet.getIssuerTokenBalance();
    expect(sourceBalance.balance).toEqual(tokenAmount * 2n);

    await new Promise((resolve) => setTimeout(resolve, 5000));

    try {
      const response = await wallet.withdrawTokens(l1WalletPubKey);
      console.log("Withdraw token txid: " + response?.txid);
    } catch (error: any) {
      fail("Expected withdrawTokens() to succeed: " + error);
    }
    // Wallet should update balance immediately by marking the leafs as withdrawn in memory in the wallet.
    // (note that if re-initializing the wallet this will currently revert balance until confirmation).
    const sourceBalanceImmediatelyAfterWithdrawal =
      await wallet.getIssuerTokenBalance();
    expect(sourceBalanceImmediatelyAfterWithdrawal.balance).toEqual(0n);

    // Mine blocks to confirm the transaction and  make LRC20 aware
    // of the withdrawal.
    await faucet.mineBlocks(6);

    // Wait for LRC20 processing.
    await new Promise((resolve) => setTimeout(resolve, 30000));

    // Mine more blocks to trigger SO fetching of withdrawals from LRC.
    await faucet.mineBlocks(6);

    // Wait for SO processing.
    await new Promise((resolve) => setTimeout(resolve, 3000));

    // Ensure that after LRC20 processing that balance is still 0.
    const sourceBalanceAfterWithdrawal = await wallet.getIssuerTokenBalance();
    expect(sourceBalanceAfterWithdrawal.balance).toEqual(0n);
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
    const sourceBalance = await issuerWallet.getIssuerTokenBalance();
    expect(sourceBalance.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const destinationBalance = await getSparkWalletTokenBalanceOrZero(
      destinationWallet,
      tokenPublicKey,
    );
    expect(destinationBalance.balance).toEqual(tokenAmount);
  });

  it("monitoring operations", async () => {
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
    const sourceBalance = await issuerWallet.getIssuerTokenBalance();
    expect(sourceBalance.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const destinationBalance = await getSparkWalletTokenBalanceOrZero(
      destinationWallet,
      tokenPublicKey,
    );
    expect(destinationBalance.balance).toEqual(tokenAmount);

    const issuerOperations = await issuerWallet.getIssuerTokenActivity();
    expect(issuerOperations.transactions.length).toBe(1);
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
    const sourceBalance = await issuerWallet.getIssuerTokenBalance();
    expect(sourceBalance.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const destinationBalance = await getSparkWalletTokenBalanceOrZero(
      destinationWallet,
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
    const issuerBalanceAfterMint = await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const { wallet: userWallet } = await SparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });
    const userWalletPublicKey = await userWallet.getSparkAddress();

    await issuerWallet.transferTokens({
      tokenAmount,
      tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
      receiverSparkAddress: userWalletPublicKey,
    });

    const issuerBalanceAfterTransfer =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const userBalanceAfterTransfer = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKey,
    );
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    // Freeze tokens
    const freezeResponse = await issuerWallet.freezeTokens(userWalletPublicKey);
    expect(freezeResponse.impactedLeafIds.length).toBeGreaterThan(0);
    expect(freezeResponse.impactedTokenAmount).toEqual(tokenAmount);

    // Unfreeze tokens
    const unfreezeResponse =
      await issuerWallet.unfreezeTokens(userWalletPublicKey);
    expect(unfreezeResponse.impactedLeafIds.length).toBeGreaterThan(0);
    expect(unfreezeResponse.impactedTokenAmount).toEqual(tokenAmount);
  });

  it("should freeze tokens with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;
    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });

    await issuerWallet.mintTokens(tokenAmount);

    // Check issuer balance after minting
    const issuerBalanceAfterMint = await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const { wallet: userWallet } = await SparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });
    const userWalletPublicKey = await userWallet.getSparkAddress();

    await issuerWallet.transferTokens({
      tokenAmount,
      tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
      receiverSparkAddress: userWalletPublicKey,
    });

    const issuerBalanceAfterTransfer =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const userBalanceAfterTransfer = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKey,
    );
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    const freezeResult = await issuerWallet.freezeTokens(userWalletPublicKey);
    expect(freezeResult.impactedLeafIds.length).toBe(1);
    expect(freezeResult.impactedTokenAmount).toBe(1000n);

    const unfreezeResult =
      await issuerWallet.unfreezeTokens(userWalletPublicKey);
    expect(unfreezeResult.impactedLeafIds.length).toBe(1);
    expect(unfreezeResult.impactedTokenAmount).toBe(1000n);
  });

  it("should burn tokens with ECDSA", async () => {
    const tokenAmount: bigint = 200n;
    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_ECDSA,
    });
    await issuerWallet.mintTokens(tokenAmount);

    const issuerTokenBalance = await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
  });

  it("should burn tokens with Schnorr", async () => {
    const tokenAmount: bigint = 200n;
    const { wallet: issuerWallet } = await IssuerSparkWallet.initialize({
      options: LOCAL_WALLET_CONFIG_SCHNORR,
    });
    await issuerWallet.mintTokens(tokenAmount);

    const issuerTokenBalance = await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
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

    const issuerBalanceAfterMint = await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const userWalletPublicKey = await userWallet.getSparkAddress();

    await issuerWallet.transferTokens({
      tokenAmount,
      tokenPublicKey: await issuerWallet.getIdentityPublicKey(),
      receiverSparkAddress: userWalletPublicKey,
    });

    const issuerBalanceAfterTransfer =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKeyHex = await issuerWallet.getIdentityPublicKey();
    const userWalletPublicKeyHex = await userWallet.getSparkAddress();
    const userBalanceAfterTransfer = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKeyHex,
    );
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    await userWallet.transferTokens({
      tokenPublicKey: tokenPublicKeyHex,
      tokenAmount,
      receiverSparkAddress: userWalletPublicKeyHex,
    });

    const userBalanceAfterTransferBack = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKeyHex,
    );
    expect(userBalanceAfterTransferBack.balance).toEqual(0n);

    const issuerTokenBalance = await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
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

    const issuerBalanceAfterMint = await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const userWalletPublicKey = await userWallet.getSparkAddress();

    await issuerWallet.transferTokens({
      tokenAmount,
      tokenPublicKey,
      receiverSparkAddress: userWalletPublicKey,
    });

    const issuerBalanceAfterTransfer =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKeyHex = await issuerWallet.getIdentityPublicKey();
    const userWalletPublicKeyHex = await userWallet.getSparkAddress();
    const userBalanceAfterTransfer = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKeyHex,
    );
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    await userWallet.transferTokens({
      tokenPublicKey: tokenPublicKeyHex,
      tokenAmount,
      receiverSparkAddress: userWalletPublicKeyHex,
    });

    const userBalanceAfterTransferBack = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKeyHex,
    );
    expect(userBalanceAfterTransferBack.balance).toEqual(0n);

    const issuerTokenBalance = await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
  });
});

async function getSparkWalletTokenBalanceOrZero(
  sparkWallet: SparkWallet,
  publicKey: string,
): Promise<{ balance: bigint }> {
  const balanceObj = await sparkWallet.getBalance();
  if (!balanceObj.tokenBalances || !balanceObj.tokenBalances.has(publicKey)) {
    return {
      balance: 0n,
    };
  }
  return {
    balance: balanceObj.tokenBalances.get(publicKey)!.balance,
  };
}
