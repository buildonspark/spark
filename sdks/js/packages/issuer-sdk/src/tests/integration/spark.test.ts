import { IssuerSparkWallet } from "../../services/spark/wallet.js";
import { Network } from "@buildonspark/spark-sdk/utils";
import { wordlist } from "@scure/bip39/wordlists/english";
import { generateMnemonic } from "@scure/bip39";
import { SparkWallet } from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";

describe("token integration test", () => {
  // Skip all tests if running in GitHub Actions
  process.env.GITHUB_ACTIONS ? it.skip : it;

  // Increase timeout for all tests in this suite
  jest.setTimeout(15000);

  it("should issue a single token", async () => {
    const tokenAmount: bigint = 1000n;

    const wallet = new IssuerSparkWallet(Network.LOCAL);
    const mnemonic = generateMnemonic(wordlist);
    await wallet.initWalletFromMnemonic(mnemonic);

    await wallet.mintIssuerTokens(tokenAmount);

    const tokenBalance = await wallet.getTokenBalance(
      await wallet.getIdentityPublicKey()
    );
    expect(tokenBalance.balance).toEqual(tokenAmount);
  });

  it("should issue a single token and transfer it", async () => {
    const tokenAmount: bigint = 1000n;

    const issuerWallet = new IssuerSparkWallet(Network.LOCAL);
    const mnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWalletFromMnemonic(mnemonic);

    const destinationWallet = new SparkWallet(Network.LOCAL);
    const destinationMnemonic = generateMnemonic(wordlist);
    await destinationWallet.initWalletFromMnemonic(destinationMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);
    await issuerWallet.transferIssuerTokens(
      tokenAmount,
      await destinationWallet.getIdentityPublicKey()
    );
    await new Promise((resolve) => setTimeout(resolve, 3000));
    const sourceBalance = await issuerWallet.getTokenBalance(
      await issuerWallet.getIdentityPublicKey()
    );
    expect(sourceBalance.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const destinationBalance = await destinationWallet.getTokenBalance(
      tokenPublicKey
    );
    expect(destinationBalance.balance).toEqual(tokenAmount);
  });

  it("should consolidate three token leaves", async () => {
    const tokenAmount: bigint = 1000n;
    const wallet = new IssuerSparkWallet(Network.LOCAL);
    const mnemonic = generateMnemonic(wordlist);
    await wallet.initWalletFromMnemonic(mnemonic);

    await wallet.mintIssuerTokens(tokenAmount);
    await wallet.mintIssuerTokens(tokenAmount);
    await wallet.mintIssuerTokens(tokenAmount);

    const identityPublicKey = await wallet.getIdentityPublicKey();
    const balanceBeforeConsolidation = await wallet.getTokenBalance(
      await wallet.getIdentityPublicKey()
    );
    const leavesBeforeConsolidation = await wallet.getAllTokenLeaves();
    expect(balanceBeforeConsolidation.balance).toEqual(tokenAmount * 3n);
    expect(
      leavesBeforeConsolidation.get(identityPublicKey)?.length || 0
    ).toEqual(3);

    await wallet.consolidateIssuerTokenLeaves();

    const balanceAfterConsolidation = await wallet.getTokenBalance(
      await wallet.getIdentityPublicKey()
    );
    const leavesAfterConsolidation = await wallet.getAllTokenLeaves();
    expect(balanceAfterConsolidation.balance).toEqual(tokenAmount * 3n);
    expect(
      leavesAfterConsolidation.get(identityPublicKey)?.length || 0
    ).toEqual(1);
  });

  it("should freeze tokens", async () => {
    const tokenAmount: bigint = 1000n;
    const issuerWallet = new IssuerSparkWallet(Network.LOCAL);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWalletFromMnemonic(issuerMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);

    // Check issuer balance after minting
    const issuerBalanceAfterMint = await issuerWallet.getTokenBalance(
      await issuerWallet.getIdentityPublicKey()
    );
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const userWallet = new IssuerSparkWallet(Network.LOCAL);
    const userMnemonic = generateMnemonic(wordlist);
    await userWallet.initWalletFromMnemonic(userMnemonic);
    const userWalletPublicKey = await userWallet.getIdentityPublicKey();

    await issuerWallet.transferIssuerTokens(tokenAmount, userWalletPublicKey);

    const issuerBalanceAfterTransfer = await issuerWallet.getTokenBalance(
      await issuerWallet.getIdentityPublicKey()
    );
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKey = await issuerWallet.getIdentityPublicKey();
    const userBalanceAfterTransfer = await userWallet.getTokenBalance(
      tokenPublicKey
    );
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    const freezeResult = await issuerWallet.freezeIssuerTokens(
      userWalletPublicKey
    );
    expect(freezeResult.impactedLeafIds.length).toBe(1);
    expect(freezeResult.impactedTokenAmount).toBe(1000n);

    const unfreezeResult = await issuerWallet.unfreezeIssuerTokens(
      userWalletPublicKey
    );
    expect(unfreezeResult.impactedLeafIds.length).toBe(1);
    expect(unfreezeResult.impactedTokenAmount).toBe(1000n);
  });

  it("should burn tokens", async () => {
    const tokenAmount: bigint = 200n;
    const issuerWallet = new IssuerSparkWallet(Network.LOCAL);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWalletFromMnemonic(issuerMnemonic);
    await issuerWallet.mintIssuerTokens(tokenAmount);

    const issuerTokenBalance = await issuerWallet.getTokenBalance(
      await issuerWallet.getIdentityPublicKey()
    );
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnIssuerTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn = await issuerWallet.getTokenBalance(
      await issuerWallet.getIdentityPublicKey()
    );
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
  });

  it("mint, transfer to user, user transfer to issuer, burn", async () => {
    const tokenAmount: bigint = 1000n;

    const issuerWallet = new IssuerSparkWallet(Network.LOCAL);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWalletFromMnemonic(issuerMnemonic);

    const userWallet = new SparkWallet(Network.LOCAL);
    const userMnemonic = generateMnemonic(wordlist);
    await userWallet.initWalletFromMnemonic(userMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);

    const issuerBalanceAfterMint = await issuerWallet.getTokenBalance(
      await issuerWallet.getIdentityPublicKey()
    );
    expect(issuerBalanceAfterMint.balance).toEqual(tokenAmount);

    const userWalletPublicKey = await userWallet.getIdentityPublicKey();

    await issuerWallet.transferIssuerTokens(tokenAmount, userWalletPublicKey);

    const issuerBalanceAfterTransfer = await issuerWallet.getTokenBalance(
      await issuerWallet.getIdentityPublicKey()
    );
    expect(issuerBalanceAfterTransfer.balance).toEqual(0n);

    const tokenPublicKeyHex = await issuerWallet.getIdentityPublicKey();
    const userBalanceAfterTransfer = await userWallet.getTokenBalance(
      tokenPublicKeyHex
    );
    expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

    await userWallet.transferTokens(
      tokenPublicKeyHex,
      tokenAmount,
      tokenPublicKeyHex
    );

    const userBalanceAfterTransferBack = await userWallet.getTokenBalance(
      tokenPublicKeyHex
    );
    expect(userBalanceAfterTransferBack.balance).toEqual(0n);

    const issuerTokenBalance = await issuerWallet.getTokenBalance(
      tokenPublicKeyHex
    );
    expect(issuerTokenBalance.balance).toEqual(tokenAmount);

    await issuerWallet.burnIssuerTokens(tokenAmount);

    const issuerTokenBalanceAfterBurn = await issuerWallet.getTokenBalance(
      tokenPublicKeyHex
    );
    expect(issuerTokenBalanceAfterBurn.balance).toEqual(0n);
  });
});
