import { IssuerSparkWallet } from "../../services/spark/wallet.js";
import { Network } from "@buildonspark/spark-sdk/utils";
import { wordlist } from "@scure/bip39/wordlists/english";
import { generateMnemonic } from "@scure/bip39";
import { SparkWallet } from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { LOCAL_WALLET_CONFIG_ECDSA, LOCAL_WALLET_CONFIG_SCHNORR } from "@buildonspark/spark-sdk/test-util";

describe("token integration test", () => {
  // Skip all tests if running in GitHub Actions
  process.env.GITHUB_ACTIONS ? it.skip : it;

  // Increase timeout for all tests in this suite
  jest.setTimeout(15000);

  it("should issue a single token with ECDSA", async () => {
    const tokenAmount: bigint = 1000n;
    const wallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_ECDSA)
    const mnemonic = generateMnemonic(wordlist);
    await wallet.initWallet(mnemonic);

    await wallet.mintIssuerTokens(tokenAmount);

    const tokenBalance = await wallet.getIssuerTokenBalance();
    expect(tokenBalance.balance).toEqual(tokenAmount);
  });

  it("should issue a single token with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;
    const wallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_SCHNORR)
    const mnemonic = generateMnemonic(wordlist);
    await wallet.initWallet(mnemonic);

    await wallet.mintIssuerTokens(tokenAmount);

    const tokenBalance = await wallet.getIssuerTokenBalance();
    expect(tokenBalance.balance).toEqual(tokenAmount);
  });

  it("should issue a single token and transfer it with ECDSA", async () => {
    const tokenAmount: bigint = 1000n;

    const issuerWallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_ECDSA);
    const mnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWallet(mnemonic);

    const destinationWallet = new SparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_ECDSA);
    const destinationMnemonic = generateMnemonic(wordlist);
    await destinationWallet.initWallet(destinationMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);
    await issuerWallet.transferIssuerTokens(
      tokenAmount,
      await destinationWallet.getIdentityPublicKey(),
    );
    const sourceBalance = await issuerWallet.getIssuerTokenBalance();
    expect(sourceBalance.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const destinationBalance = await getSparkWalletTokenBalanceOrZero(
      destinationWallet,
      tokenPublicKey,
    );
    expect(destinationBalance.balance).toEqual(tokenAmount);
  });

  it("should issue a single token and transfer it with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;

    const issuerWallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_SCHNORR);
    const mnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWallet(mnemonic);

    const destinationWallet = new SparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_SCHNORR);
    const destinationMnemonic = generateMnemonic(wordlist);
    await destinationWallet.initWallet(destinationMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);
    await issuerWallet.transferIssuerTokens(
      tokenAmount,
      await destinationWallet.getIdentityPublicKey(),
    );
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
    const issuerWallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_ECDSA);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWallet(issuerMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);

    // Check issuer balance after minting
    const issuerBalanceAfterMint = await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const userWallet = new SparkWallet(Network.LOCAL);
    const userMnemonic = generateMnemonic(wordlist);
    await userWallet.initWallet(userMnemonic);
    const userWalletPublicKey = await userWallet.getIdentityPublicKey();

    await issuerWallet.transferIssuerTokens(tokenAmount, userWalletPublicKey);

    const issuerBalanceAfterTransfer =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const userBalanceAfterTransfer = await getSparkWalletTokenBalanceOrZero(userWallet, tokenPublicKey);
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    const freezeResult =
      await issuerWallet.freezeIssuerTokens(userWalletPublicKey);
    expect(freezeResult.impactedLeafIds.length).toBe(1);
    expect(freezeResult.impactedTokenAmount).toBe(1000n);

    const unfreezeResult =
      await issuerWallet.unfreezeIssuerTokens(userWalletPublicKey);
    expect(unfreezeResult.impactedLeafIds.length).toBe(1);
    expect(unfreezeResult.impactedTokenAmount).toBe(1000n);
  });

  it("should freeze tokens with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;
    const issuerWallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_SCHNORR);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWallet(issuerMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);

    // Check issuer balance after minting
    const issuerBalanceAfterMint = await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const userWallet = new SparkWallet(Network.LOCAL);
    const userMnemonic = generateMnemonic(wordlist);
    await userWallet.initWallet(userMnemonic);
    const userWalletPublicKey = await userWallet.getIdentityPublicKey();

    await issuerWallet.transferIssuerTokens(tokenAmount, userWalletPublicKey);

    const issuerBalanceAfterTransfer =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const userBalanceAfterTransfer = await getSparkWalletTokenBalanceOrZero(userWallet, tokenPublicKey);
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    const freezeResult =
      await issuerWallet.freezeIssuerTokens(userWalletPublicKey);
    expect(freezeResult.impactedLeafIds.length).toBe(1);
    expect(freezeResult.impactedTokenAmount).toBe(1000n);

    const unfreezeResult =
      await issuerWallet.unfreezeIssuerTokens(userWalletPublicKey);
    expect(unfreezeResult.impactedLeafIds.length).toBe(1);
    expect(unfreezeResult.impactedTokenAmount).toBe(1000n);
  });

  it("should burn tokens with ECDSA", async () => {
    const tokenAmount: bigint = 200n;
    const issuerWallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_ECDSA);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWallet(issuerMnemonic);
    await issuerWallet.mintIssuerTokens(tokenAmount);

    const issuerTokenBalance = await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnIssuerTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
  });

  it("should burn tokens with Schnorr", async () => {
    const tokenAmount: bigint = 200n;
    const issuerWallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_SCHNORR);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWallet(issuerMnemonic);
    await issuerWallet.mintIssuerTokens(tokenAmount);

    const issuerTokenBalance = await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnIssuerTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
  });

  it("mint, transfer to user, user transfer to issuer, burn with ECDSA", async () => {
    const tokenAmount: bigint = 1000n;

    const issuerWallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_ECDSA);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWallet(issuerMnemonic);

    const userWallet = new SparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_ECDSA);
    const userMnemonic = generateMnemonic(wordlist);
    await userWallet.initWallet(userMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);

    const issuerBalanceAfterMint = await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const userWalletPublicKey = await userWallet.getIdentityPublicKey();

    await issuerWallet.transferIssuerTokens(tokenAmount, userWalletPublicKey);

    const issuerBalanceAfterTransfer =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKeyHex = await issuerWallet.getIdentityPublicKey();
    const userBalanceAfterTransfer = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKeyHex,
    );
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    await userWallet.sendSparkTokenTransfer({
      tokenPublicKey: tokenPublicKeyHex,
      tokenAmount,
      receiverSparkAddress: tokenPublicKeyHex,
    });

    const userBalanceAfterTransferBack = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKeyHex,
    );
    expect(userBalanceAfterTransferBack.balance).toEqual(0n);

    const issuerTokenBalance = await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnIssuerTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
  });

  it("mint, transfer to user, user transfer to issuer, burn with Schnorr", async () => {
    const tokenAmount: bigint = 1000n;

    const issuerWallet = new IssuerSparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_SCHNORR);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWallet(issuerMnemonic);

    const userWallet = new SparkWallet(Network.LOCAL, undefined, LOCAL_WALLET_CONFIG_SCHNORR);
    const userMnemonic = generateMnemonic(wordlist);
    await userWallet.initWallet(userMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);

    const issuerBalanceAfterMint = await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const userWalletPublicKey = await userWallet.getIdentityPublicKey();

    await issuerWallet.transferIssuerTokens(tokenAmount, userWalletPublicKey);

    const issuerBalanceAfterTransfer =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKeyHex = await issuerWallet.getIdentityPublicKey();
    const userBalanceAfterTransfer = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKeyHex,
    );
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    await userWallet.sendSparkTokenTransfer({
      tokenPublicKey: tokenPublicKeyHex,
      tokenAmount,
      receiverSparkAddress: tokenPublicKeyHex,
    });

    const userBalanceAfterTransferBack = await getSparkWalletTokenBalanceOrZero(
      userWallet,
      tokenPublicKeyHex,
    );
    expect(userBalanceAfterTransferBack.balance).toEqual(0n);

    const issuerTokenBalance = await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnIssuerTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn =
      await issuerWallet.getIssuerTokenBalance();
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
  });
});

async function getSparkWalletTokenBalanceOrZero(sparkWallet: SparkWallet, publicKey: string): Promise<{ balance: bigint }> {
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
