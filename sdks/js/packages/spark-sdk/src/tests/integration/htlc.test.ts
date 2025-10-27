import { describe, expect, it } from "@jest/globals";
import { bytesToHex } from "@noble/curves/utils";

import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { waitForClaim } from "../utils/utils.js";

describe("HTLC create and claim tests", () => {
  it("should create and claim a HTLC", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const { wallet: aliceWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });
    const { wallet: bobWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });
    const depositResp = await aliceWallet.getSingleUseDepositAddress();
    const signedTx = await faucet.sendToAddress(depositResp, 1_000n);
    await aliceWallet.claimDeposit(signedTx.id);
    await new Promise((resolve) => setTimeout(resolve, 1000));
    let aliceBalance = await aliceWallet.getBalance();
    expect(aliceBalance.balance).toBe(1_000n);
    const bobSparkAddress = await bobWallet.getSparkAddress();
    const htlc = await aliceWallet.createHTLC({
      receiverSparkAddress: bobSparkAddress,
      amountSats: 1000,
      expiryTime: new Date(Date.now() + 5 * 60 * 1000),
    });
    const transferID = htlc.id;
    const preimage = await aliceWallet.getHTLCPreimage(transferID);
    const preimageHex = bytesToHex(preimage);
    aliceBalance = await aliceWallet.getBalance();
    expect(aliceBalance.balance).toBe(0n);
    let bobBalance = await bobWallet.getBalance();
    expect(bobBalance.balance).toBe(0n);
    await bobWallet.claimHTLC(preimageHex);
    await waitForClaim({ wallet: bobWallet });
    aliceBalance = await aliceWallet.getBalance();
    expect(aliceBalance.balance).toBe(0n);
    bobBalance = await bobWallet.getBalance();
    expect(bobBalance.balance).toBe(1_000n);
  }, 60000);
  it("should fail claiming HTLC if preimage is incorrect", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const { wallet: aliceWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });
    const { wallet: bobWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });
    const depositResp = await aliceWallet.getSingleUseDepositAddress();
    const signedTx = await faucet.sendToAddress(depositResp, 1_000n);
    await aliceWallet.claimDeposit(signedTx.id);
    let aliceBalance = await aliceWallet.getBalance();
    expect(aliceBalance.balance).toBe(1_000n);
    const bobSparkAddress = await bobWallet.getSparkAddress();
    await aliceWallet.createHTLC({
      receiverSparkAddress: bobSparkAddress,
      amountSats: 1000,
      expiryTime: new Date(Date.now() + 5 * 60 * 1000),
    });

    aliceBalance = await aliceWallet.getBalance();
    expect(aliceBalance.balance).toBe(0n);
    let bobBalance = await bobWallet.getBalance();
    expect(bobBalance.balance).toBe(0n);
    await expect(bobWallet.claimHTLC("test2")).rejects.toThrow();
  }, 60000);

  it("should revert HTLC transfer if no preimage is provided before expiry time", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const { wallet: aliceWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });
    const { wallet: bobWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });
    const depositResp = await aliceWallet.getSingleUseDepositAddress();
    const signedTx = await faucet.sendToAddress(depositResp, 1_000n);
    await aliceWallet.claimDeposit(signedTx.id);
    await new Promise((resolve) => setTimeout(resolve, 1000));
    let aliceBalance = await aliceWallet.getBalance();
    expect(aliceBalance.balance).toBe(1_000n);
    const bobSparkAddress = await bobWallet.getSparkAddress();
    await aliceWallet.createHTLC({
      receiverSparkAddress: bobSparkAddress,
      amountSats: 1000,
      expiryTime: new Date(Date.now() + 1 * 60 * 1000),
    });
    aliceBalance = await aliceWallet.getBalance();
    expect(aliceBalance.balance).toBe(0n);
    await new Promise((resolve) => setTimeout(resolve, 80000));
    aliceBalance = await aliceWallet.getBalance();
    expect(aliceBalance.balance).toBe(1000n);
    const bobBalance = await bobWallet.getBalance();
    expect(bobBalance.balance).toBe(0n);
  }, 120000);
});
