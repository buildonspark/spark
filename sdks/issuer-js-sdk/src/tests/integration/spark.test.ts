import { IssuerSparkWallet } from "../../services/spark/wallet.js";
import { Network } from "@buildonspark/spark-js-sdk/utils";
import { wordlist } from "@scure/bip39/wordlists/english";
import { generateMnemonic } from "@scure/bip39";
import { secp256k1 } from "@noble/curves/secp256k1";
import { hexToBytes, bytesToHex } from "@noble/curves/abstract/utils";
import { SparkWallet } from "@buildonspark/spark-js-sdk";

describe("token integration test", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  it("should issue a single token", async () => {
    const tokenAmount: bigint = 1000n;

    const wallet = new IssuerSparkWallet(Network.LOCAL);
    const mnemonic = generateMnemonic(wordlist);
    await wallet.initWalletFromMnemonic(mnemonic);

    await wallet.mintIssuerTokens(tokenAmount);
  });

  it("should issue a single token and transfer it", async () => {
    const tokenAmount: bigint = 1000n;

    const wallet = new IssuerSparkWallet(Network.LOCAL);
    const mnemonic = generateMnemonic(wordlist);
    await wallet.initWalletFromMnemonic(mnemonic);

    const destinationWallet = new SparkWallet(Network.LOCAL);
    const destinationMnemonic = generateMnemonic(wordlist);
    await destinationWallet.initWalletFromMnemonic(destinationMnemonic);

    await wallet.mintIssuerTokens(tokenAmount);
    await wallet.transferIssuerTokens(
      tokenAmount,
      await destinationWallet.getIdentityPublicKey()
    );
  });

  it("should consolidate token leaves", async () => {
    const tokenAmount: bigint = 1000n;
    const wallet = new IssuerSparkWallet(Network.LOCAL);
    const mnemonic = generateMnemonic(wordlist);
    await wallet.initWalletFromMnemonic(mnemonic);

    await wallet.mintIssuerTokens(tokenAmount);
    await wallet.consolidateIssuerTokenLeaves();
  });

  it("should freeze tokens", async () => {
    const tokenAmount: bigint = 1000n;
    const issuerWallet = new IssuerSparkWallet(Network.LOCAL);
    const issuerMnemonic = generateMnemonic(wordlist);
    await issuerWallet.initWalletFromMnemonic(issuerMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);

    const userWallet = new IssuerSparkWallet(Network.LOCAL);
    const userMnemonic = generateMnemonic(wordlist);
    await userWallet.initWalletFromMnemonic(userMnemonic);
    const userWalletPublicKey = await userWallet
      .getIdentityPublicKey();

    await issuerWallet.transferIssuerTokens(
      tokenAmount,
      userWalletPublicKey
    );

    // Freeze tokens and validate the return value
    const freezeResult = await issuerWallet.freezeIssuerTokens(
      userWalletPublicKey
    );
    expect(freezeResult.impactedLeafIds.length).toBe(1);
    expect(freezeResult.impactedTokenAmount).toBe(1000n);

    // Unfreeze tokens and validate the return value
    const unfreezeResult = await issuerWallet.unfreezeIssuerTokens(
      userWalletPublicKey
    );
    expect(unfreezeResult.impactedLeafIds.length).toBe(1);
    expect(unfreezeResult.impactedTokenAmount).toBe(1000n);
  });

  it("should burn tokens", async () => {
    const tokenAmount: bigint = 1000n;
    const issuerWallet = new IssuerSparkWallet(Network.LOCAL);
    const issuerMnemonic = generateMnemonic(wordlist)
    await issuerWallet.initWalletFromMnemonic(issuerMnemonic);
    await issuerWallet.mintIssuerTokens(tokenAmount);

    await issuerWallet.burnIssuerTokens(tokenAmount);
  });
});
