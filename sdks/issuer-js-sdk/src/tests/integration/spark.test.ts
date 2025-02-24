import { IssuerSparkWallet } from "../../services/spark/wallet.js";
import { Network } from "@buildonspark/spark-js-sdk/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { hexToBytes, bytesToHex } from "@noble/curves/abstract/utils";
import { SparkWallet } from "@buildonspark/spark-js-sdk";

describe("token integration test", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  it("should issue a single token", async () => {
    const tokenAmount: bigint = 1000n;

    const wallet = new IssuerSparkWallet(Network.LOCAL);
    const mnemonic = await wallet.generateMnemonic();
    await wallet.createSparkWallet(mnemonic);

    await wallet.mintIssuerTokens(tokenAmount);
  });

  it("should issue a single token and transfer it", async () => {
    const tokenAmount: bigint = 1000n;

    const wallet = new IssuerSparkWallet(Network.LOCAL);
    const mnemonic = await wallet.generateMnemonic();
    await wallet.createSparkWallet(mnemonic);

    const destinationWallet = new SparkWallet(Network.LOCAL);
    const destinationMnemonic = await destinationWallet.generateMnemonic();
    await destinationWallet.createSparkWallet(destinationMnemonic);

    await wallet.mintIssuerTokens(tokenAmount);
    await wallet.transferIssuerTokens(
      tokenAmount,
      bytesToHex(await destinationWallet.getSigner().getIdentityPublicKey())
    );
  });

  it("should consolidate token leaves", async () => {
    const tokenAmount: bigint = 1000n;
    const wallet = new IssuerSparkWallet(Network.LOCAL);
    const mnemonic = await wallet.generateMnemonic();
    await wallet.createSparkWallet(mnemonic);

    await wallet.mintIssuerTokens(tokenAmount);
    await wallet.consolidateIssuerTokenLeaves();
  });

  it("should freeze tokens", async () => {
    const tokenAmount: bigint = 1000n;
    const issuerWallet = new IssuerSparkWallet(Network.LOCAL);
    const issuerMnemonic = await issuerWallet.generateMnemonic();
    await issuerWallet.createSparkWallet(issuerMnemonic);

    await issuerWallet.mintIssuerTokens(tokenAmount);

    const userWallet = new IssuerSparkWallet(Network.LOCAL);
    const userMnemonic = await userWallet.generateMnemonic();
    await userWallet.createSparkWallet(userMnemonic);
    const userWalletPublicKey = await userWallet
      .getSigner()
      .getIdentityPublicKey();

    await issuerWallet.transferIssuerTokens(
      tokenAmount,
      bytesToHex(userWalletPublicKey)
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
    const issuerMnemonic = await issuerWallet.generateMnemonic();
    await issuerWallet.createSparkWallet(issuerMnemonic);
    await issuerWallet.mintIssuerTokens(tokenAmount);

    await issuerWallet.burnIssuerTokens(tokenAmount);
  });
});
