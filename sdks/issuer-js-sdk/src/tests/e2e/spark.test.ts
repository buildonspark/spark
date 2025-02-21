import { IssuerSparkWallet } from "../../services/spark/wallet.js";
import { Network } from "@buildonspark/spark-js-sdk/utils";
import { secp256k1 } from "@noble/curves/secp256k1";

describe("token integration test", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  it("should issue a single token", async () => {
    const tokenAmount: bigint = 1000n;

    const wallet = new IssuerSparkWallet(Network.REGTEST);
    const mnemonic = await wallet.generateMnemonic();
    await wallet.createSparkWallet(mnemonic);

    const tokenPublicKey = await wallet.getSigner().generatePublicKey()

    await wallet.mintTokens(
      tokenPublicKey,
      tokenAmount
    );
  }, 100000);

  it("should issue a single token and transfer it", async () => {
    const tokenAmount: bigint = 1000n;

    const wallet = new IssuerSparkWallet(Network.REGTEST);
    const mnemonic = await wallet.generateMnemonic();
    await wallet.createSparkWallet(mnemonic);

    const targetWalletPrivateKey = secp256k1.utils.randomPrivateKey();
    const targetWalletPubKey = secp256k1.getPublicKey(targetWalletPrivateKey);

    const tokenPublicKey = await wallet.getSigner().generatePublicKey();

    await wallet.mintTokens(tokenPublicKey, tokenAmount);
    await wallet.transferTokens(
      tokenPublicKey,
      tokenAmount,
      targetWalletPubKey
    );
  });

  it("should consolidate token leaves", async () => {
    const tokenAmount: bigint = 1000n;
    const wallet = new IssuerSparkWallet(Network.REGTEST);
    const mnemonic = await wallet.generateMnemonic();
    await wallet.createSparkWallet(mnemonic);

    const tokenPublicKey = await wallet.getSigner().generatePublicKey();
    await wallet.mintTokens(tokenPublicKey, tokenAmount);

    await wallet.consolidateTokenLeaves(tokenPublicKey);
  });

  it("should freeze tokens", async () => {
    const tokenAmount: bigint = 1000n;
    const issuerWallet = new IssuerSparkWallet(Network.REGTEST);
    const issuerMnemonic = await issuerWallet.generateMnemonic();
    await issuerWallet.createSparkWallet(issuerMnemonic);

    const tokenPublicKey = await issuerWallet.getSigner().generatePublicKey();
    await issuerWallet.mintTokens(tokenPublicKey, tokenAmount);

    const userWallet = new IssuerSparkWallet(Network.REGTEST);
    const userMnemonic = await issuerWallet.generateMnemonic();
    await userWallet.createSparkWallet(userMnemonic);

    const userWalletPublicKey = await userWallet.getSigner().getIdentityPublicKey();
    issuerWallet.transferTokens(tokenPublicKey, tokenAmount, userWalletPublicKey);

    await issuerWallet.freezeTokens(
      userWalletPublicKey,
      tokenPublicKey
    );

    await issuerWallet.unfreezeTokens(
      userWalletPublicKey,
      tokenPublicKey
    );
  });

  it("should burn tokens", async () => {
    const tokenAmount: bigint = 1000n;
    const issuerWallet = new IssuerSparkWallet(Network.REGTEST);
    const issuerMnemonic = await issuerWallet.generateMnemonic();
    await issuerWallet.createSparkWallet(issuerMnemonic);

    const tokenPublicKey = await issuerWallet.getSigner().generatePublicKey();
    await issuerWallet.mintTokens(tokenPublicKey, tokenAmount);

    await issuerWallet.burnTokens(tokenPublicKey, tokenAmount);
  });
});
